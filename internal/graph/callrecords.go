package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	// defaultBaseURL is the v1.0 (NOT beta) Graph endpoint. The crawler
	// pins to v1.0 because beta schema drift would silently break the
	// quality verdict comparison against the PowerShell reference.
	defaultBaseURL = "https://graph.microsoft.com/v1.0"

	// defaultHTTPTimeout is the per-request ceiling. Graph callRecord
	// detail responses can be hundreds of KB and the API is famously
	// slow on cold paths, so we err on the generous side.
	defaultHTTPTimeout = 60 * time.Second

	// graphTimeFormat matches the format the PowerShell reference sends
	// in $filter — second precision, trailing Z. Some Graph clusters
	// reject sub-second precision in $filter values.
	graphTimeFormat = "2006-01-02T15:04:05Z"

	// maxRetryAfterSeconds clamps Retry-After values so a misbehaving
	// upstream cannot wedge the crawler. Real Graph rate-limit windows
	// are seconds-to-minutes; anything larger we treat as an error.
	maxRetryAfterSeconds = 120

	// defaultBackoff is used when a 5xx is retried but no Retry-After
	// header is present.
	defaultBackoff = 2 * time.Second
)

// Client is the public Graph client used by the crawler. Construct one with
// New and reuse it across many calls — it is safe for concurrent use.
type Client struct {
	httpc *http.Client
	tok   tokenProvider
	base  string
	log   *slog.Logger

	// retryAfterCap, when non-zero, caps the sleep duration honored from a
	// Retry-After response header. This field is unexported and intended only
	// for tests that need to avoid sleeping real wall-clock seconds.
	retryAfterCap time.Duration
}

// New builds a Client. log must be non-nil; if you don't care about logging,
// pass slog.New(slog.DiscardHandler) or similar.
func New(ts *TokenSource, log *slog.Logger) *Client {
	return newWithProvider(ts, log)
}

// newWithProvider is the test seam: it accepts the narrower tokenProvider
// interface so callrecords_test.go can inject a stub without standing up
// real MSAL.
func newWithProvider(tok tokenProvider, log *slog.Logger) *Client {
	if log == nil {
		log = slog.Default()
	}
	return &Client{
		httpc: &http.Client{Timeout: defaultHTTPTimeout},
		tok:   tok,
		base:  defaultBaseURL,
		log:   log,
	}
}

// listResponse is the wire shape of a paginated callRecords list page. We
// decode only the bits we use — Graph adds @odata.context and friends we
// can ignore.
type listResponse struct {
	Value    []CallRecordRef `json:"value"`
	NextLink string          `json:"@odata.nextLink"`
}

// ListCallRecordsInRange returns the minimal CallRecordRef projection for
// every callRecord whose startDateTime falls in [start, end). It walks
// @odata.nextLink until exhaustion. The crawler turns each ref into a full
// detail via GetCallRecord — splitting the two phases keeps memory usage
// proportional to the page size, not the whole window.
func (c *Client) ListCallRecordsInRange(ctx context.Context, start, end time.Time) ([]CallRecordRef, error) {
	filter := fmt.Sprintf(
		"startDateTime ge %s and startDateTime lt %s",
		start.UTC().Format(graphTimeFormat),
		end.UTC().Format(graphTimeFormat),
	)
	q := url.Values{}
	q.Set("$filter", filter)
	next := c.base + "/communications/callRecords?" + q.Encode()

	var all []CallRecordRef
	page := 0
	for next != "" {
		page++
		var resp listResponse
		if err := c.do(ctx, http.MethodGet, next, &resp); err != nil {
			return nil, fmt.Errorf("graph.list page %d: %w", page, err)
		}
		all = append(all, resp.Value...)
		c.log.DebugContext(ctx, "graph.list page",
			slog.Int("page", page),
			slog.Int("items", len(resp.Value)),
			slog.Bool("hasNext", resp.NextLink != ""),
		)
		next = resp.NextLink
	}
	c.log.DebugContext(ctx, "graph.list done",
		slog.Int("pages", page),
		slog.Int("total", len(all)),
	)
	return all, nil
}

// participantsResponse is the wire shape of /participants_v2.
type participantsResponse struct {
	Value []ParticipantV2 `json:"value"`
}

// GetCallRecord fetches the full callRecord with sessions/segments expanded,
// then issues a SECOND request to participants_v2 and merges the result.
// Combining both into a single $expand returns 400 BadRequest from Graph —
// see CallQualityUtils.ps1 lines 105-109 for the original discovery.
//
// 404 from the primary call is mapped to ErrCallNotFound so the crawler can
// skip the record cleanly. A 404 from participants_v2 is treated as
// best-effort and silently ignored — group meetings rely on it but most p2p
// records work fine off caller/callee identity alone.
func (c *Client) GetCallRecord(ctx context.Context, id string) (*CallRecord, error) {
	if id == "" {
		return nil, fmt.Errorf("graph.get: empty id")
	}
	encID := url.PathEscape(id)
	detailURL := c.base + "/communications/callRecords/" + encID +
		"?$expand=sessions($expand=segments)"

	var rec CallRecord
	if err := c.do(ctx, http.MethodGet, detailURL, &rec); err != nil {
		return nil, fmt.Errorf("graph.get %s: %w", id, err)
	}

	partsURL := c.base + "/communications/callRecords/" + encID + "/participants_v2"
	var parts participantsResponse
	if err := c.do(ctx, http.MethodGet, partsURL, &parts); err != nil {
		// Best-effort: log and proceed. Quality has a session-based
		// fallback for UPN extraction.
		c.log.WarnContext(ctx, "graph.get participants_v2 failed",
			slog.String("callId", id),
			slog.Any("err", err),
		)
	} else {
		rec.ParticipantsV2 = parts.Value
	}

	return &rec, nil
}

// do issues a single HTTP request, attaches a fresh bearer token, decodes
// the JSON response into out, and handles 404/429/5xx uniformly.
//
// Retry policy (intentionally narrow — the crawler is the place for
// long-running orchestration, not the transport layer):
//   - 429: honor Retry-After (clamped), retry exactly once, then give up
//     with ErrRateLimited.
//   - 5xx: sleep defaultBackoff, retry exactly once.
//   - 404: short-circuit to ErrCallNotFound.
//   - All other non-2xx: wrap into a descriptive error and return.
func (c *Client) do(ctx context.Context, method, urlStr string, out any) error {
	const maxAttempts = 2
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		retry, err := c.doOnce(ctx, method, urlStr, out)
		if err == nil {
			return nil
		}
		lastErr = err
		if retry == 0 || attempt == maxAttempts {
			return err
		}
		// Apply test-only cap so unit tests do not sleep real seconds.
		if c.retryAfterCap > 0 && retry > c.retryAfterCap {
			retry = c.retryAfterCap
		}
		// Sleep, but bail out if the context is cancelled while waiting.
		t := time.NewTimer(retry)
		select {
		case <-ctx.Done():
			t.Stop()
			return fmt.Errorf("graph.do: %w", ctx.Err())
		case <-t.C:
		}
	}
	return lastErr
}

// doOnce performs one HTTP attempt. It returns a non-zero retry duration
// when the caller should sleep and try again. err and retry can both be
// non-zero (the error from this attempt is reported even when we want a
// retry, so the do() loop can return it if attempts run out).
func (c *Client) doOnce(ctx context.Context, method, urlStr string, out any) (time.Duration, error) {
	token, err := c.tok.Token(ctx)
	if err != nil {
		return 0, err
	}

	req, err := http.NewRequestWithContext(ctx, method, urlStr, nil)
	if err != nil {
		return 0, fmt.Errorf("graph.do: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpc.Do(req)
	if err != nil {
		return 0, fmt.Errorf("graph.do: transport: %w", err)
	}
	defer resp.Body.Close()

	c.log.DebugContext(ctx, "graph.http",
		slog.String("method", method),
		slog.String("url", urlStr),
		slog.Int("status", resp.StatusCode),
	)

	switch {
	case resp.StatusCode == http.StatusOK:
		if out == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			return 0, nil
		}
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			// Drain the remainder so HTTP/1.1 keep-alive can reuse the
			// connection even though we are returning an error.
			_, _ = io.Copy(io.Discard, resp.Body)
			return 0, fmt.Errorf("graph.do: decode: %w", err)
		}
		return 0, nil

	case resp.StatusCode == http.StatusNotFound:
		return 0, ErrCallNotFound

	case resp.StatusCode == http.StatusTooManyRequests:
		wait := parseRetryAfter(resp.Header.Get("Retry-After"))
		return wait, fmt.Errorf("graph.do %s: %w (retry-after=%s)",
			urlStr, ErrRateLimited, wait)

	case resp.StatusCode >= 500 && resp.StatusCode <= 599:
		body, _ := io.ReadAll(resp.Body)
		return defaultBackoff, fmt.Errorf("graph.do %s: server %d: %s",
			urlStr, resp.StatusCode, truncate(body, 256))

	default:
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("graph.do %s: status %d: %s",
			urlStr, resp.StatusCode, truncate(body, 256))
	}
}

// parseRetryAfter understands both the integer-seconds and HTTP-date forms
// of the Retry-After header, clamps the result to maxRetryAfterSeconds, and
// returns defaultBackoff if the header is missing or unparseable.
func parseRetryAfter(h string) time.Duration {
	if h == "" {
		return defaultBackoff
	}
	if secs, err := strconv.Atoi(h); err == nil {
		if secs < 0 {
			return defaultBackoff
		}
		if secs > maxRetryAfterSeconds {
			secs = maxRetryAfterSeconds
		}
		return time.Duration(secs) * time.Second
	}
	if ts, err := http.ParseTime(h); err == nil {
		d := time.Until(ts)
		if d <= 0 {
			return defaultBackoff
		}
		if d > time.Duration(maxRetryAfterSeconds)*time.Second {
			d = time.Duration(maxRetryAfterSeconds) * time.Second
		}
		return d
	}
	return defaultBackoff
}

// truncate keeps log lines bounded when Graph spits a multi-KB error body
// at us.
func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}

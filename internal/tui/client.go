package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"teams_con/internal/store"
)

// defaultHTTPTimeout is the per-request ceiling for every call to the API
// service. The TUI renders on a human timescale — 30s is generous enough
// for slow mongo aggregations and tight enough that a hung service does
// not freeze the UI indefinitely.
const defaultHTTPTimeout = 30 * time.Second

// HealthDTO mirrors api.Health on the wire. It is duplicated here (rather
// than imported) to keep internal/tui a leaf package with no dependency on
// internal/api — only internal/store is shared for its DTO types.
type HealthDTO struct {
	LastCrawlAt    *time.Time `json:"lastCrawlAt,omitempty"`
	LastCrawlError string     `json:"lastCrawlError,omitempty"`
	MongoOk        bool       `json:"mongoOk"`
}

// ListCallsParams mirrors api.ListCallsParams. All fields are optional —
// zero/empty values are omitted from the query string.
type ListCallsParams struct {
	From    *time.Time
	To      *time.Time
	Verdict string
	Upn     string
	Limit   int
	Offset  int
}

// callDetailDTO matches api.CallDetail on the wire so GetCall can decode
// the response directly. It is unexported because callers get a
// (*store.Call, []store.StreamRow) tuple back — this struct is a pure
// decode target.
type callDetailDTO struct {
	Call    store.Call        `json:"call"`
	Streams []store.StreamRow `json:"streams"`
}

// errorDTO matches the flat {"error": "..."} shape returned by the API for
// every non-2xx response (see api/handlers.go writeError).
type errorDTO struct {
	Error string `json:"error"`
}

// UserHealthReportDTO mirrors api.UserHealthReport on the wire. Duplicated
// rather than imported to keep internal/tui a leaf package (see comment on
// HealthDTO). Sub-struct fields track api.user_health.go.
type UserHealthReportDTO struct {
	Upn        string             `json:"upn"`
	WindowFrom time.Time          `json:"windowFrom"`
	WindowTo   time.Time          `json:"windowTo"`
	TotalCalls int                `json:"totalCalls"`
	Truncated  bool               `json:"truncated,omitempty"`
	ByVerdict  VerdictCountsDTO   `json:"byVerdict"`
	Subnets    []SubnetUsageDTO   `json:"subnets"`
	Devices    []DeviceUsageDTO   `json:"captureDevices"`
	Clients    []ClientUsageDTO   `json:"clients"`
	Platforms  []PlatformUsageDTO `json:"platforms"`
	AvgMetrics AvgMetricsDTO      `json:"avgMetrics"`
	Pattern    string             `json:"pattern"`
	// Card mirrors api.UserHealthReport.Card. Populated server-side
	// from the usercards collection when an annotation exists for the
	// target user; nil otherwise (the field is omitempty so the wire
	// shape stays compact).
	Card *UserCardDTO `json:"card,omitempty"`
	// PeerBaseline (Phase 3) compares the user against a cohort who
	// shared their subnets in the window. Nil when the server could not
	// compute one (no calls, no subnets, insufficient peers path still
	// populates it with Assessment=insufficient_peers).
	PeerBaseline *PeerBaselineDTO `json:"peerBaseline,omitempty"`
	// TopIssues (Phase 5) mirrors api.UserHealthReport.TopIssues: the
	// Teams diagnostic tags most frequently reported across this user's
	// calls in the window. Nil when the server has no issue data — the
	// portrait view skips the section in that case.
	TopIssues []IssueCountDTO `json:"topIssues,omitempty"`
}

// IssueCountDTO mirrors api.IssueCount on the wire. Kept as a separate
// type so internal/tui does not import internal/api (leaf-package rule).
type IssueCountDTO struct {
	Issue     string `json:"issue"`
	CallCount int    `json:"callCount"`
}

// PeerBaselineDTO mirrors api.PeerBaseline on the wire. Duplicated (not
// imported) to keep internal/tui a leaf package — see HealthDTO rationale.
type PeerBaselineDTO struct {
	CohortSize      int      `json:"cohortSize"`
	CohortCalls     int      `json:"cohortCalls"`
	CohortBadRatio  float64  `json:"cohortBadRatio"`
	TargetBadRatio  float64  `json:"targetBadRatio"`
	Delta           float64  `json:"delta"`
	Assessment      string   `json:"assessment"`
	SubnetsCompared []string `json:"subnetsCompared,omitempty"`
}

// UserCardDTO mirrors api.UserCard / store.UserCard on the wire. It is
// duplicated rather than imported to keep internal/tui a leaf package
// with no dependency on internal/api. Empty Tags/slice serialises as
// absent thanks to omitempty, matching the server shape.
type UserCardDTO struct {
	Upn         string    `json:"upn"`
	DisplayName string    `json:"displayName,omitempty"`
	Location    string    `json:"location,omitempty"`
	Tags        []string  `json:"tags,omitempty"`
	Notes       string    `json:"notes,omitempty"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type VerdictCountsDTO struct {
	Good int `json:"good"`
	Poor int `json:"poor"`
	Bad  int `json:"bad"`
}

type SubnetUsageDTO struct {
	Subnet string `json:"subnet"`
	// Name/Office/Kind are populated server-side from the subnets
	// collection when the raw subnet matches a configured CIDR. They
	// stay empty for unknown blocks. Mirrors api.SubnetUsage.
	Name      string `json:"name,omitempty"`
	Office    string `json:"office,omitempty"`
	Kind      string `json:"kind,omitempty"`
	CallCount int    `json:"callCount"`
	ConnType  string `json:"connType,omitempty"`
}

type DeviceUsageDTO struct {
	Device            string  `json:"device"`
	CallCount         int     `json:"callCount"`
	BadCallCount      int     `json:"badCallCount"`
	AvgConcealedPct   float64 `json:"avgConcealedPct"`
	WorstConcealedPct float64 `json:"worstConcealedPct"`
}

type ClientUsageDTO struct {
	UserAgent string `json:"userAgent"`
	CallCount int    `json:"callCount"`
}

type PlatformUsageDTO struct {
	Platform  string `json:"platform"`
	CallCount int    `json:"callCount"`
}

type AvgMetricsDTO struct {
	JitterSendMs *float64 `json:"jitterSendMs,omitempty"`
	JitterRecvMs *float64 `json:"jitterRecvMs,omitempty"`
	LossSendPct  *float64 `json:"lossSendPct,omitempty"`
	LossRecvPct  *float64 `json:"lossRecvPct,omitempty"`
	RttSendMs    *float64 `json:"rttSendMs,omitempty"`
	RttRecvMs    *float64 `json:"rttRecvMs,omitempty"`
}

// FlakyMicDTO mirrors api.FlakyMic on the wire.
type FlakyMicDTO struct {
	User              string           `json:"user"`
	CaptureDevice     string           `json:"captureDevice"`
	Incidents         int              `json:"incidents"`
	DistinctCalls     int              `json:"distinctCalls"`
	WorstConcealedPct float64          `json:"worstConcealedPct"`
	AvgConcealedPct   float64          `json:"avgConcealedPct"`
	Severity          string           `json:"severity"`
	FirstSeen         time.Time        `json:"firstSeen"`
	LastSeen          time.Time        `json:"lastSeen"`
	Samples           []FlakyMicIncDTO `json:"samples"`
}

type FlakyMicIncDTO struct {
	CallId       string    `json:"callId"`
	StartedAt    time.Time `json:"startedAt"`
	ConcealedPct float64   `json:"concealedPct"`
	Verdict      string    `json:"verdict"`
}

// FlakyMicsParams is the client-side mirror of api.FindFlakyMicParams.
type FlakyMicsParams struct {
	From            *time.Time
	To              *time.Time
	MinConcealedPct float64
	MinIncidents    int
	Limit           int
}

// Client is a thin HTTP wrapper around the teams_con api service. It owns
// a single *http.Client with a bounded timeout and emits already-parsed
// store DTOs so the tab models never touch net/http.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient returns a Client pointing at baseURL (e.g. "http://localhost:8080").
// The trailing slash, if any, is stripped so request URLs can be joined
// with a leading "/" without producing double slashes.
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: defaultHTTPTimeout},
	}
}

// Health fetches /health and returns the parsed DTO. A non-2xx response
// is mapped to a Go error with the server's {"error"} body message when
// present; a failing ping inside the server still surfaces as MongoOk=false
// in a 200 response (the server never returns 5xx for ping failures).
func (c *Client) Health(ctx context.Context) (*HealthDTO, error) {
	var out HealthDTO
	if err := c.getJSON(ctx, "/health", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListCalls fetches /calls with the supplied filters. An empty or nil
// filter field is omitted from the query string so the server applies its
// own default (defaultListLimit etc.).
func (c *Client) ListCalls(ctx context.Context, p ListCallsParams) ([]store.Call, error) {
	q := url.Values{}
	if p.From != nil {
		q.Set("from", p.From.UTC().Format(time.RFC3339))
	}
	if p.To != nil {
		q.Set("to", p.To.UTC().Format(time.RFC3339))
	}
	if p.Verdict != "" {
		q.Set("verdict", p.Verdict)
	}
	if p.Upn != "" {
		q.Set("upn", p.Upn)
	}
	if p.Limit > 0 {
		q.Set("limit", strconv.Itoa(p.Limit))
	}
	if p.Offset > 0 {
		q.Set("offset", strconv.Itoa(p.Offset))
	}

	var out []store.Call
	if err := c.getJSON(ctx, "/calls", q, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetCall fetches /calls/{id} and splits the response into the call and
// its stream rows. A 404 is returned verbatim so callers can distinguish
// "bad id" from a transport failure.
func (c *Client) GetCall(ctx context.Context, id string) (*store.Call, []store.StreamRow, error) {
	if id == "" {
		return nil, nil, fmt.Errorf("tui: get call: empty id")
	}
	var out callDetailDTO
	if err := c.getJSON(ctx, "/calls/"+url.PathEscape(id), nil, &out); err != nil {
		return nil, nil, err
	}
	call := out.Call
	return &call, out.Streams, nil
}

// ListUsers fetches /users with an optional time window. Both bounds are
// optional; passing nil for both returns the all-time stats.
func (c *Client) ListUsers(ctx context.Context, from, to *time.Time) ([]store.UserStat, error) {
	q := url.Values{}
	if from != nil {
		q.Set("from", from.UTC().Format(time.RFC3339))
	}
	if to != nil {
		q.Set("to", to.UTC().Format(time.RFC3339))
	}

	var out []store.UserStat
	if err := c.getJSON(ctx, "/users", q, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetUserHealth fetches /users/{upn}/health. Bounds the window to [from, to)
// when non-nil; otherwise the server applies its own 7-day default.
func (c *Client) GetUserHealth(ctx context.Context, upn string, from, to *time.Time) (*UserHealthReportDTO, error) {
	if upn == "" {
		return nil, fmt.Errorf("tui: get user health: empty upn")
	}
	q := url.Values{}
	if from != nil {
		q.Set("from", from.UTC().Format(time.RFC3339))
	}
	if to != nil {
		q.Set("to", to.UTC().Format(time.RFC3339))
	}
	var out UserHealthReportDTO
	if err := c.getJSON(ctx, "/users/"+url.PathEscape(upn)+"/health", q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListFlakyMics fetches /mics/flaky with optional filters. Zero/nil fields
// are dropped so the server applies its own defaults (7-day window,
// concealed>=5%, >=3 incidents).
func (c *Client) ListFlakyMics(ctx context.Context, p FlakyMicsParams) ([]FlakyMicDTO, error) {
	q := url.Values{}
	if p.From != nil {
		q.Set("from", p.From.UTC().Format(time.RFC3339))
	}
	if p.To != nil {
		q.Set("to", p.To.UTC().Format(time.RFC3339))
	}
	if p.MinConcealedPct > 0 {
		q.Set("min_concealed_pct", strconv.FormatFloat(p.MinConcealedPct, 'f', -1, 64))
	}
	if p.MinIncidents > 0 {
		q.Set("min_incidents", strconv.Itoa(p.MinIncidents))
	}
	if p.Limit > 0 {
		q.Set("limit", strconv.Itoa(p.Limit))
	}
	var out []FlakyMicDTO
	if err := c.getJSON(ctx, "/mics/flaky", q, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// getJSON is the single HTTP code path used by every public method. It
// builds the full URL, issues a GET, and either decodes the 200 body into
// out or parses the errorDTO body into a Go error.
func (c *Client) getJSON(ctx context.Context, path string, q url.Values, out any) error {
	full := c.baseURL + path
	if len(q) > 0 {
		full += "?" + q.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		return fmt.Errorf("tui: build request %s: %w", path, err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("tui: do request %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decodeError(resp)
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("tui: decode response %s: %w", path, err)
	}
	return nil
}

// decodeError reads the non-2xx body and returns a formatted error. If the
// body is a valid errorDTO we surface its message; otherwise we return a
// generic status-code error with a short body snippet for debugging.
func decodeError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var edto errorDTO
	if err := json.Unmarshal(body, &edto); err == nil && edto.Error != "" {
		return fmt.Errorf("tui: api %d: %s", resp.StatusCode, edto.Error)
	}
	snippet := strings.TrimSpace(string(body))
	if snippet == "" {
		return fmt.Errorf("tui: api %d: (empty body)", resp.StatusCode)
	}
	return fmt.Errorf("tui: api %d: %s", resp.StatusCode, snippet)
}

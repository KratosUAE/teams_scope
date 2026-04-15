package api

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"teams_con/internal/store"
)

// Defaults and bounds for FindFlakyMicrophones. Kept here (not shared with
// ListCalls) because the LLM-facing tool has its own token-budget sensitive
// ceilings distinct from the HTTP API.
const (
	flakyDefaultWindow     = 7 * 24 * time.Hour
	flakyDefaultMinPct     = 5.0
	flakyDefaultMinIncid   = 3
	flakyDefaultLimit      = 20
	flakyMaxLimit          = 100
	flakySamplesPerDevice  = 5
	flakyBadWorstThreshold = 15.0
	flakyBadAvgThreshold   = 10.0
)

// FindFlakyMicParams is the HTTP-free input to FindFlakyMicrophones. Nil
// window bounds fall back to [now-7d, now]. Zero/negative thresholds fall
// back to the flakyDefault* constants.
type FindFlakyMicParams struct {
	From            *time.Time
	To              *time.Time
	MinConcealedPct float64
	MinIncidents    int
	Limit           int
}

// FlakyMic is a single (user, captureDevice) finding: a microphone that
// repeatedly exceeded the concealedPct threshold across multiple send-audio
// streams in the selected window. Severity is Bad when the worst or mean
// concealed percentage cross the hard-coded thresholds, else Poor.
type FlakyMic struct {
	User              string        `json:"user"`
	CaptureDevice     string        `json:"captureDevice"`
	Incidents         int           `json:"incidents"`
	DistinctCalls     int           `json:"distinctCalls"`
	WorstConcealedPct float64       `json:"worstConcealedPct"`
	AvgConcealedPct   float64       `json:"avgConcealedPct"`
	Severity          string        `json:"severity"`
	FirstSeen         time.Time     `json:"firstSeen"`
	LastSeen          time.Time     `json:"lastSeen"`
	Samples           []FlakyMicInc `json:"samples"`
}

// FlakyMicInc is one worst-case sample incident attached to a FlakyMic for
// drill-down. Samples are sorted desc by ConcealedPct and capped at
// flakySamplesPerDevice to keep the JSON payload compact.
type FlakyMicInc struct {
	CallId       string    `json:"callId"`
	StartedAt    time.Time `json:"startedAt"`
	ConcealedPct float64   `json:"concealedPct"`
	Verdict      string    `json:"verdict"`
}

// FindFlakyMicrophones scans send-audio streams in the supplied window,
// groups by (user, captureDevice), and surfaces devices with at least
// MinIncidents incidents where concealedPct >= MinConcealedPct. The result
// is sorted by severity desc, then incidents desc, then worst concealed
// desc, and clipped to Limit rows. An empty window returns ([]FlakyMic{},
// nil) — never a non-nil error for "no data".
func (s *Service) FindFlakyMicrophones(ctx context.Context, p FindFlakyMicParams) ([]FlakyMic, error) {
	now := time.Now().UTC()
	from := now.Add(-flakyDefaultWindow)
	to := now
	if p.From != nil {
		from = *p.From
	}
	if p.To != nil {
		to = *p.To
	}
	if !from.Before(to) {
		return nil, fmt.Errorf("%w: from must be before to", ErrBadRequest)
	}

	minConcealed := p.MinConcealedPct
	if minConcealed <= 0 {
		minConcealed = flakyDefaultMinPct
	}
	minIncidents := p.MinIncidents
	if minIncidents < 1 {
		minIncidents = flakyDefaultMinIncid
	}
	limit := p.Limit
	if limit < 1 {
		limit = flakyDefaultLimit
	}
	if limit > flakyMaxLimit {
		limit = flakyMaxLimit
	}

	metas, err := s.calls.ListMetaInWindow(ctx, &from, &to)
	if err != nil {
		return nil, fmt.Errorf("api: flaky mics: list calls: %w", err)
	}
	if len(metas) == 0 {
		return []FlakyMic{}, nil
	}

	ids := make([]string, len(metas))
	metaByID := make(map[string]store.CallMeta, len(metas))
	for i, m := range metas {
		ids[i] = m.CallId
		metaByID[m.CallId] = m
	}

	rows, err := s.streams.ListFlakyAudioRaw(ctx, ids, minConcealed)
	if err != nil {
		return nil, fmt.Errorf("api: flaky mics: list streams: %w", err)
	}

	return groupFlakyMics(rows, metaByID, minIncidents, limit), nil
}

// groupFlakyMics is the pure-Go heuristic: audio-label filter, group by
// (user, device), severity, sort, truncate. Split out so tests can drive
// the algorithm without touching the Service or Mongo.
func groupFlakyMics(
	rows []store.StreamRow,
	metaByID map[string]store.CallMeta,
	minIncidents int,
	limit int,
) []FlakyMic {
	type key struct {
		user   string
		device string
	}
	type bucket struct {
		incidents []FlakyMicInc
		worst     float64
		sum       float64
		distinct  map[string]struct{}
		firstSeen time.Time
		lastSeen  time.Time
	}

	groups := make(map[key]*bucket)
	for _, row := range rows {
		if !isAudioLabel(row.StreamLabel) {
			continue
		}
		if !isRealUpn(row.User) {
			continue
		}
		if row.CaptureDevice == "" {
			continue
		}
		if row.ConcealedPct == nil {
			continue
		}
		conc := *row.ConcealedPct

		startedAt := row.SegmentStart
		if m, ok := metaByID[row.CallId]; ok && !m.StartTimeUtc.IsZero() {
			startedAt = m.StartTimeUtc
		}
		verdict := row.Verdict
		if m, ok := metaByID[row.CallId]; ok && m.Verdict != "" {
			verdict = m.Verdict
		}

		k := key{user: row.User, device: row.CaptureDevice}
		b, ok := groups[k]
		if !ok {
			b = &bucket{
				distinct:  make(map[string]struct{}),
				firstSeen: startedAt,
				lastSeen:  startedAt,
			}
			groups[k] = b
		}
		b.incidents = append(b.incidents, FlakyMicInc{
			CallId:       row.CallId,
			StartedAt:    startedAt,
			ConcealedPct: round1(conc),
			Verdict:      verdict,
		})
		if conc > b.worst {
			b.worst = conc
		}
		b.sum += conc
		b.distinct[row.CallId] = struct{}{}
		if !startedAt.IsZero() && startedAt.Before(b.firstSeen) {
			b.firstSeen = startedAt
		}
		if startedAt.After(b.lastSeen) {
			b.lastSeen = startedAt
		}
	}

	out := make([]FlakyMic, 0, len(groups))
	for k, b := range groups {
		if len(b.incidents) < minIncidents {
			continue
		}
		avg := b.sum / float64(len(b.incidents))
		severity := verdictPoor
		if b.worst >= flakyBadWorstThreshold || avg >= flakyBadAvgThreshold {
			severity = verdictBad
		}
		sort.SliceStable(b.incidents, func(i, j int) bool {
			return b.incidents[i].ConcealedPct > b.incidents[j].ConcealedPct
		})
		samples := b.incidents
		if len(samples) > flakySamplesPerDevice {
			samples = samples[:flakySamplesPerDevice]
		}
		out = append(out, FlakyMic{
			User:              k.user,
			CaptureDevice:     k.device,
			Incidents:         len(b.incidents),
			DistinctCalls:     len(b.distinct),
			WorstConcealedPct: round1(b.worst),
			AvgConcealedPct:   round1(avg),
			Severity:          severity,
			FirstSeen:         b.firstSeen,
			LastSeen:          b.lastSeen,
			Samples:           samples,
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := flakyRank(out[i].Severity), flakyRank(out[j].Severity)
		if ri != rj {
			return ri > rj
		}
		if out[i].Incidents != out[j].Incidents {
			return out[i].Incidents > out[j].Incidents
		}
		if out[i].WorstConcealedPct != out[j].WorstConcealedPct {
			return out[i].WorstConcealedPct > out[j].WorstConcealedPct
		}
		// Stable final tiebreak on (user, device) so test output is
		// deterministic across map iteration orderings.
		if out[i].User != out[j].User {
			return out[i].User < out[j].User
		}
		return out[i].CaptureDevice < out[j].CaptureDevice
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// isAudioLabel mirrors internal/mcp/cascades.go normalizeLabel=="audio"
// without cross-package import. Internal packages don't see each other.
func isAudioLabel(label string) bool {
	if label == "" {
		return false
	}
	return strings.Contains(strings.ToLower(label), "audio")
}

// isRealUpn filters out empty user and the canonical server/unknown
// placeholder tokens that populate non-attributable stream legs.
func isRealUpn(u string) bool {
	if u == "" {
		return false
	}
	if strings.HasPrefix(u, "<") && strings.HasSuffix(u, ">") {
		return false
	}
	return true
}

// flakyRank maps verdict strings to a numeric severity ordering so sort.Slice
// can compare them. Unknown values rank below Poor so they never float to
// the top of the result.
func flakyRank(v string) int {
	switch v {
	case verdictBad:
		return 2
	case verdictPoor:
		return 1
	case verdictGood:
		return 0
	default:
		return -1
	}
}

// round1 rounds x to one decimal place. Keeps JSON compact and avoids the
// noise of 14 digits of float representation bleeding into LLM context.
func round1(x float64) float64 {
	return math.Round(x*10) / 10
}

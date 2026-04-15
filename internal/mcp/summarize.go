package mcp

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"teams_con/internal/api"
	"teams_con/internal/store"
)

// verdictRank orders streams/calls Bad > Poor > Good > anything else for
// severity-based sorting. Unknown verdicts sort last so malformed data
// never shadows a real Bad stream in a top-N listing.
func verdictRank(v string) int {
	switch v {
	case "Bad":
		return 3
	case "Poor":
		return 2
	case "Good":
		return 1
	default:
		return 0
	}
}

// summarizeCalls renders a one-line summary of a slice of calls for the
// text block preceding the JSON payload. Empty slices collapse to a
// stable "no calls" string so LLMs reliably recognise the empty state.
func summarizeCalls(rows []store.Call) string {
	if len(rows) == 0 {
		return "no calls"
	}
	var bad, poor, good int
	var earliest, latest time.Time
	for i, c := range rows {
		switch c.Verdict {
		case "Bad":
			bad++
		case "Poor":
			poor++
		case "Good":
			good++
		}
		if i == 0 || c.StartTimeUtc.Before(earliest) {
			earliest = c.StartTimeUtc
		}
		if i == 0 || c.StartTimeUtc.After(latest) {
			latest = c.StartTimeUtc
		}
	}
	return fmt.Sprintf(
		"%d calls (%d Bad, %d Poor, %d Good) earliest %s latest %s",
		len(rows), bad, poor, good,
		earliest.UTC().Format(time.RFC3339),
		latest.UTC().Format(time.RFC3339),
	)
}

// summarizeUserCalls wraps summarizeCalls with a "for upn" prefix.
func summarizeUserCalls(upn string, rows []store.Call) string {
	return fmt.Sprintf("for %s: %s", upn, summarizeCalls(rows))
}

// summarizeUsers renders the top-3 users by bad-call count so the text
// block highlights the most affected users without the LLM having to
// scan the full JSON array.
func summarizeUsers(rows []store.UserStat) string {
	if len(rows) == 0 {
		return "no users"
	}
	// Sort a copy by BadCount desc so we do not mutate the caller's slice.
	sorted := make([]store.UserStat, len(rows))
	copy(sorted, rows)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].BadCount > sorted[j].BadCount
	})
	top := sorted
	if len(top) > 3 {
		top = top[:3]
	}
	parts := make([]string, 0, len(top))
	for _, u := range top {
		parts = append(parts, fmt.Sprintf("%s %d/%d", u.Upn, u.BadCount, u.CallCount))
	}
	return fmt.Sprintf("%d users; top by bad: %s", len(rows), strings.Join(parts, ", "))
}

// summarizeHealth renders the health payload as a single line suitable
// for the text block. Zero time is reported as "never".
func summarizeHealth(h *api.Health) string {
	if h == nil {
		return "health=unknown"
	}
	mongo := "ok"
	if !h.MongoOk {
		mongo = "DOWN"
	}
	last := "never"
	if h.LastCrawlAt != nil && !h.LastCrawlAt.IsZero() {
		last = h.LastCrawlAt.UTC().Format(time.RFC3339)
	}
	if h.LastCrawlError != "" {
		return fmt.Sprintf("mongo=%s, last_crawl=%s (err: %s)", mongo, last, h.LastCrawlError)
	}
	return fmt.Sprintf("mongo=%s, last_crawl=%s", mongo, last)
}

// fmtFloat formats an optional float metric as "<name>=<val>" or "" if
// the pointer is nil. Used to build compact per-stream metric strings.
func fmtFloat(name string, v *float64, unit string) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%s=%.1f%s", name, *v, unit)
}

// streamMetricsLine builds the "loss=X% jit=Yms rtt=Zms" suffix for a
// single stream, skipping nil metrics and joining with spaces.
func streamMetricsLine(s store.StreamRow) string {
	var parts []string
	if x := fmtFloat("loss", s.AvgLossPct, "%"); x != "" {
		parts = append(parts, x)
	}
	if x := fmtFloat("jit", s.AvgJitterMs, "ms"); x != "" {
		parts = append(parts, x)
	}
	if x := fmtFloat("rtt", s.AvgRttMs, "ms"); x != "" {
		parts = append(parts, x)
	}
	return strings.Join(parts, " ")
}

// rootCauseHint applies a simple threshold heuristic to the worst Bad
// streams to name the dominant metric. The thresholds mirror the rule
// of thumb used by the quality pipeline (loss > 5%, jitter > 50ms,
// rtt > 1000ms). Mixed or sub-threshold = "mixed".
func rootCauseHint(streams []store.StreamRow) string {
	var hasBadLoss, hasBadJitter, hasBadRtt bool
	for _, s := range streams {
		if s.Verdict != "Bad" {
			continue
		}
		if s.AvgLossPct != nil && *s.AvgLossPct > 5 {
			hasBadLoss = true
		}
		if s.AvgJitterMs != nil && *s.AvgJitterMs > 50 {
			hasBadJitter = true
		}
		if s.AvgRttMs != nil && *s.AvgRttMs > 1000 {
			hasBadRtt = true
		}
	}
	switch {
	case hasBadLoss:
		return "high packet loss"
	case hasBadJitter:
		return "high jitter (network congestion or wifi)"
	case hasBadRtt:
		return "high RTT (latency / distance)"
	default:
		return "mixed"
	}
}

// summarizeCallDetail renders a ~10-line natural-language summary of a
// call's quality, used both as the text block for get_call and as the
// sole output of summarize_call.
func summarizeCallDetail(d *api.CallDetail) string {
	if d == nil {
		return "no call"
	}
	c := d.Call

	var b strings.Builder
	duration := time.Duration(c.DurationSec) * time.Second

	fmt.Fprintf(&b, "Call %s  %s  %s  organizer=%s  participants=%d  verdict=%s\n",
		c.CallId, c.CallType, duration, c.Organizer, c.ParticipantCount, c.Verdict)

	var bad, poor, good int
	for _, s := range d.Streams {
		switch s.Verdict {
		case "Bad":
			bad++
		case "Poor":
			poor++
		case "Good":
			good++
		}
	}
	fmt.Fprintf(&b, "streams: %d (%d Bad / %d Poor / %d Good)\n",
		len(d.Streams), bad, poor, good)

	if c.WorstUser != "" {
		// Find the worst user's stream to surface its metrics.
		var worst *store.StreamRow
		for i := range d.Streams {
			s := &d.Streams[i]
			if s.User == c.WorstUser && s.Verdict == "Bad" {
				worst = s
				break
			}
		}
		if worst != nil {
			fmt.Fprintf(&b, "worst: %s [%s] %s  %s\n",
				c.WorstUser, c.WorstDirection, c.WorstStreamLabel,
				streamMetricsLine(*worst))
		} else {
			fmt.Fprintf(&b, "worst: %s [%s] %s\n",
				c.WorstUser, c.WorstDirection, c.WorstStreamLabel)
		}
	}

	// Top 3-5 worst (Bad or Poor) streams.
	var problem []store.StreamRow
	for _, s := range d.Streams {
		if s.Verdict == "Bad" || s.Verdict == "Poor" {
			problem = append(problem, s)
		}
	}
	sort.SliceStable(problem, func(i, j int) bool {
		ri := verdictRank(problem[i].Verdict)
		rj := verdictRank(problem[j].Verdict)
		if ri != rj {
			return ri > rj
		}
		li := float64(0)
		lj := float64(0)
		if problem[i].AvgLossPct != nil {
			li = *problem[i].AvgLossPct
		}
		if problem[j].AvgLossPct != nil {
			lj = *problem[j].AvgLossPct
		}
		return li > lj
	})
	if len(problem) > 5 {
		problem = problem[:5]
	}
	for _, s := range problem {
		fmt.Fprintf(&b, "  %s [%s] %s: %s\n",
			s.User, s.Direction, s.StreamLabel, streamMetricsLine(s))
	}

	fmt.Fprintf(&b, "likely: %s", rootCauseHint(d.Streams))
	return b.String()
}

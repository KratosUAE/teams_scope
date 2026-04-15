package mcp

import (
	"fmt"
	"strings"

	"teams_con/internal/api"
)

// summarizeFlakyMics produces a short natural-language header for the
// find_flaky_microphones tool response. The header is stuffed into the
// first text block so LLM clients can answer "any flaky mics this week?"
// straight from the summary without parsing the JSON payload.
func summarizeFlakyMics(mics []api.FlakyMic) string {
	if len(mics) == 0 {
		return "no flaky microphones found in window"
	}
	var bad, poor int
	for _, m := range mics {
		switch m.Severity {
		case "Bad":
			bad++
		case "Poor":
			poor++
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d flaky microphone%s (%d Bad, %d Poor)",
		len(mics), pluralS(len(mics)), bad, poor)
	top := mics[0]
	fmt.Fprintf(&b, "; worst: %s [%s] %d incidents across %d calls, worst=%.1f%% avg=%.1f%%",
		top.User,
		top.CaptureDevice,
		top.Incidents,
		top.DistinctCalls,
		top.WorstConcealedPct,
		top.AvgConcealedPct,
	)
	return b.String()
}

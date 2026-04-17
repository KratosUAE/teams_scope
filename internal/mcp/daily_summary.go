package mcp

import (
	"context"
	"fmt"
	"time"

	mcpsdk "github.com/mark3labs/mcp-go/mcp"

	"teams_con/internal/api"
	"teams_con/internal/store"
)

// defaultDailySummaryDays is the lookback window when the caller omits "from".
const defaultDailySummaryDays = 7

func (s *Server) handleDailySummary(ctx context.Context, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	now := time.Now().UTC()

	from, badFrom := parseTimeParam(req, "from")
	if badFrom != nil {
		return badFrom, nil
	}
	to, badTo := parseTimeParam(req, "to")
	if badTo != nil {
		return badTo, nil
	}

	// Apply defaults: last 7 days when omitted.
	var fromT, toT time.Time
	if to != nil {
		toT = *to
	} else {
		toT = now
	}
	if from != nil {
		fromT = *from
	} else {
		fromT = toT.AddDate(0, 0, -defaultDailySummaryDays)
	}

	rows, err := s.svc.DailySummary(ctx, api.DailySummaryParams{From: fromT, To: toT})
	if err != nil {
		return s.mapServiceErr(err), nil
	}

	return textAndJSON(summarizeDailySummary(rows, fromT, toT), rows), nil
}

// summarizeDailySummary builds a compact one-line text summary for the LLM.
func summarizeDailySummary(rows []store.DaySummary, from, to time.Time) string {
	days := len(rows)
	var totalCalls, totalBad, totalOver90 int
	var peak float64
	for _, r := range rows {
		totalCalls += r.Calls
		totalBad += r.Bad
		totalOver90 += r.Over90Pct
		if r.PeakLossMaxPct > peak {
			peak = r.PeakLossMaxPct
		}
	}
	return fmt.Sprintf("%d days, %s to %s: %d calls, %d bad, %d streams >90%% lossMax, peak %.1f%%",
		days, from.Format("2006-01-02"), to.Format("2006-01-02"),
		totalCalls, totalBad, totalOver90, peak)
}

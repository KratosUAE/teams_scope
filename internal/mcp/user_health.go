package mcp

import (
	"fmt"
	"strings"

	"teams_con/internal/api"
)

// summarizeUserHealth produces the first text block for user_health_report.
// It answers "is this user OK?" in one line so LLM clients can respond
// without parsing the full JSON aggregate.
func summarizeUserHealth(r *api.UserHealthReport) string {
	if r == nil {
		return "user_health_report: nil report"
	}
	if r.TotalCalls == 0 {
		return fmt.Sprintf("%s: no calls in window [%s .. %s)",
			r.Upn,
			r.WindowFrom.Format("2006-01-02"),
			r.WindowTo.Format("2006-01-02"))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %d call%s (%d Good, %d Poor, %d Bad); pattern=%s",
		r.Upn,
		r.TotalCalls,
		pluralS(r.TotalCalls),
		r.ByVerdict.Good, r.ByVerdict.Poor, r.ByVerdict.Bad,
		r.Pattern,
	)
	if len(r.Devices) > 0 {
		top := r.Devices[0]
		fmt.Fprintf(&b, "; top device: %s (%d calls, avg concealed=%.1f%%)",
			top.Device, top.CallCount, top.AvgConcealedPct)
	}
	if len(r.Subnets) > 0 {
		top := r.Subnets[0]
		if top.ConnType != "" {
			fmt.Fprintf(&b, "; top subnet: %s (%s, %d calls)",
				top.Subnet, top.ConnType, top.CallCount)
		} else {
			fmt.Fprintf(&b, "; top subnet: %s (%d calls)", top.Subnet, top.CallCount)
		}
	}
	return b.String()
}

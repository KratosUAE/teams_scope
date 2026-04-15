package mcp

import (
	"fmt"
	"strings"

	"teams_con/internal/api"
)

// summarizeHotspots produces the one-line text header stuffed into the
// first content block of the find_bad_network_hotspots tool response. The
// summary answers "anything on fire?" without having to parse the JSON
// payload — LLM clients (and curl-in-a-loop operators) can read it
// directly. The format mirrors summarizeFlakyMics: count + worst-offender
// one-liner.
func summarizeHotspots(rows []api.Hotspot) string {
	if len(rows) == 0 {
		return "no network hotspots matched filters"
	}
	worst := rows[0]
	label := worst.SubnetName
	if label == "" {
		label = worst.Subnet
	}
	if label == "" {
		label = worst.RelayIp
	}
	if worst.Subnet != "" && worst.RelayIp != "" && worst.SubnetName == "" {
		label = worst.Subnet + " via " + worst.RelayIp
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d hotspot%s; worst: %s %.0f%% (%d/%d calls, %d users)",
		len(rows), pluralS(len(rows)),
		label,
		worst.BadRatio*100,
		worst.BadCalls, worst.TotalCalls,
		worst.DistinctUsers,
	)
	return b.String()
}

package mcp

import (
	"fmt"
	"sort"
	"strings"

	"teams_con/internal/store"
)

// summarizeSubnets produces the one-line text header for the list_subnets
// tool response. Format mirrors summarizeFlakyMics: a count plus a small
// inline breakdown of the offices so the LLM can answer "how many
// subnets" / "what offices" without parsing the JSON payload.
//
// Example output:
//
//	"10 subnets configured; offices: Dubai(3), RU(2), home(5)"
func summarizeSubnets(entries []store.SubnetEntry) string {
	if len(entries) == 0 {
		return "no subnets configured"
	}

	type officeCount struct {
		name string
		n    int
	}
	counts := map[string]int{}
	for _, e := range entries {
		key := e.Office
		if key == "" {
			key = "(none)"
		}
		counts[key]++
	}
	pairs := make([]officeCount, 0, len(counts))
	for k, v := range counts {
		pairs = append(pairs, officeCount{name: k, n: v})
	}
	// Sort by count desc, then name asc, so the dominant office is first
	// and the order is deterministic across map iteration runs.
	sort.SliceStable(pairs, func(i, j int) bool {
		if pairs[i].n != pairs[j].n {
			return pairs[i].n > pairs[j].n
		}
		return pairs[i].name < pairs[j].name
	})

	var b strings.Builder
	fmt.Fprintf(&b, "%d subnets configured", len(entries))
	if len(pairs) > 0 {
		b.WriteString("; offices: ")
		parts := make([]string, len(pairs))
		for i, p := range pairs {
			parts[i] = fmt.Sprintf("%s(%d)", p.name, p.n)
		}
		b.WriteString(strings.Join(parts, ", "))
	}
	return b.String()
}

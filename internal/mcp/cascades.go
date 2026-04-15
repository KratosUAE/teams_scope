package mcp

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"teams_con/internal/store"
)

// Cascade describes one suspected "shared source" pattern inside a call:
// a single participant's SEND stream was degraded in a time window, and
// multiple other participants' RECV streams of the same media type were
// degraded in the overlapping window. The visual matrix would show this
// as a vertical red column spanning several user rows.
//
// The output is deliberately flat and small — LLM clients consume it
// directly without another round-trip.
type Cascade struct {
	Sender        string            `json:"sender"`
	StreamLabel   string            `json:"streamLabel,omitempty"` // audio|video|data|scr
	SenderVerdict string            `json:"senderVerdict"`         // Bad|Poor
	WindowStart   time.Time         `json:"windowStart"`
	WindowEnd     time.Time         `json:"windowEnd"`
	SenderMetrics cascadeMetric     `json:"senderMetrics"`
	AffectedCount int               `json:"affectedCount"`
	AffectedRecv  []cascadeAffected `json:"affectedRecv"`
	Severity      string            `json:"severity"` // worst verdict across sender + affected
}

type cascadeMetric struct {
	AvgJitterMs *float64 `json:"avgJitterMs,omitempty"`
	AvgLossPct  *float64 `json:"avgLossPct,omitempty"`
	AvgRttMs    *float64 `json:"avgRttMs,omitempty"`
}

type cascadeAffected struct {
	User        string   `json:"user"`
	Verdict     string   `json:"verdict"`
	AvgJitterMs *float64 `json:"avgJitterMs,omitempty"`
	AvgLossPct  *float64 `json:"avgLossPct,omitempty"`
	AvgRttMs    *float64 `json:"avgRttMs,omitempty"`
}

// findCascades scans a call's streams for cascade patterns. The heuristic:
//
//  1. Filter to Bad/Poor streams only (Good never cascades).
//  2. For each SEND (upload) stream of a real user, look for RECV
//     (download) streams of the SAME stream label from OTHER real users
//     whose [segmentStart, segmentEnd] window overlaps.
//  3. If the count of matching recv streams is >= minAffected, flag it
//     as a suspected cascade.
//
// "Real user" means User != "" and not the canonical server tokens
// ("<server/unknown>", "<unknown>"). Server-side legs cannot be
// attributed and just add noise.
//
// minAffected is the cascade threshold. 3 is the default used by the MCP
// tool — it filters out p2p artefacts while still catching 4-person
// meetings where one person tanks the whole call.
func findCascades(streams []store.StreamRow, minAffected int) []Cascade {
	if minAffected < 1 {
		minAffected = 3
	}
	sends := filterProblematic(streams, "send", "upload")
	recvs := filterProblematic(streams, "recv", "download")
	if len(sends) == 0 || len(recvs) == 0 {
		return nil
	}

	var out []Cascade
	for _, snd := range sends {
		if !isRealUser(snd.User) {
			continue
		}
		label := normalizeLabel(snd.StreamLabel)

		var affected []cascadeAffected
		for _, rcv := range recvs {
			if !isRealUser(rcv.User) || rcv.User == snd.User {
				continue
			}
			if normalizeLabel(rcv.StreamLabel) != label {
				continue
			}
			if !windowsOverlap(snd.SegmentStart, snd.SegmentEnd, rcv.SegmentStart, rcv.SegmentEnd) {
				continue
			}
			affected = append(affected, cascadeAffected{
				User:        rcv.User,
				Verdict:     rcv.Verdict,
				AvgJitterMs: round1(rcv.AvgJitterMs),
				AvgLossPct:  round1(rcv.AvgLossPct),
				AvgRttMs:    round1(rcv.AvgRttMs),
			})
		}
		if len(affected) < minAffected {
			continue
		}
		// Dedupe affected users (one user may have several overlapping
		// recv streams — we only care that they were hit, not how many
		// times). Keep the worst verdict per user.
		affected = dedupeAffected(affected)
		if len(affected) < minAffected {
			continue
		}
		// Severity = worst verdict across sender AND affected receivers.
		// A sender flagged Poor whose downstream victims are all Bad is
		// still a Bad-severity event — sort + LLM display should reflect
		// the worst card at the table, not just the sender side.
		severity := snd.Verdict
		for _, a := range affected {
			if verdictRank(a.Verdict) > verdictRank(severity) {
				severity = a.Verdict
			}
		}
		out = append(out, Cascade{
			Sender:        snd.User,
			StreamLabel:   label,
			SenderVerdict: snd.Verdict,
			WindowStart:   snd.SegmentStart,
			WindowEnd:     snd.SegmentEnd,
			SenderMetrics: cascadeMetric{
				AvgJitterMs: round1(snd.AvgJitterMs),
				AvgLossPct:  round1(snd.AvgLossPct),
				AvgRttMs:    round1(snd.AvgRttMs),
			},
			AffectedCount: len(affected),
			AffectedRecv:  affected,
			Severity:      severity,
		})
	}

	// Sort: most severe, then largest blast radius, then oldest first.
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := verdictRank(out[i].Severity), verdictRank(out[j].Severity)
		if ri != rj {
			return ri > rj
		}
		if out[i].AffectedCount != out[j].AffectedCount {
			return out[i].AffectedCount > out[j].AffectedCount
		}
		return out[i].WindowStart.Before(out[j].WindowStart)
	})
	return out
}

// filterProblematic returns Poor+Bad streams matching the direction
// vocabulary (two names per direction for the quality/stream divergence).
func filterProblematic(streams []store.StreamRow, dirA, dirB string) []store.StreamRow {
	out := make([]store.StreamRow, 0, len(streams))
	for _, s := range streams {
		if s.Verdict != "Bad" && s.Verdict != "Poor" {
			continue
		}
		if s.Direction != dirA && s.Direction != dirB {
			continue
		}
		out = append(out, s)
	}
	return out
}

// isRealUser filters out the server/unknown placeholder tokens that
// populate non-attributable stream legs.
func isRealUser(u string) bool {
	if u == "" {
		return false
	}
	if strings.HasPrefix(u, "<") && strings.HasSuffix(u, ">") {
		return false
	}
	return true
}

// normalizeLabel folds the various raw stream labels into the short alias
// set used throughout the MCP layer (audio / video / data / scr).
func normalizeLabel(label string) string {
	return formatStreamLabelShort(label)
}

// formatStreamLabelShort is duplicated from internal/tui/tab_calls.go on
// purpose — internal packages don't cross-import. Keep the two in sync.
func formatStreamLabelShort(label string) string {
	short := strings.ToLower(label)
	switch {
	case short == "":
		return "-"
	case strings.Contains(short, "screen") || strings.Contains(short, "vbss"):
		return "scr"
	case strings.Contains(short, "video"):
		return "video"
	case strings.Contains(short, "audio"):
		return "audio"
	case strings.Contains(short, "data"):
		return "data"
	}
	return short
}

// windowsOverlap returns true when the two half-open intervals share at
// least one instant. Zero-valued endpoints are treated as "missing" and
// count as overlapping (better to over-report than miss).
func windowsOverlap(aStart, aEnd, bStart, bEnd time.Time) bool {
	if aStart.IsZero() || aEnd.IsZero() || bStart.IsZero() || bEnd.IsZero() {
		return true
	}
	return aStart.Before(bEnd) && bStart.Before(aEnd)
}

// dedupeAffected collapses multiple entries for the same user down to
// one, keeping the worst verdict and the largest metric readings.
func dedupeAffected(rows []cascadeAffected) []cascadeAffected {
	idx := make(map[string]int, len(rows))
	var out []cascadeAffected
	for _, r := range rows {
		if i, ok := idx[r.User]; ok {
			cur := out[i]
			if verdictRank(r.Verdict) > verdictRank(cur.Verdict) {
				cur.Verdict = r.Verdict
			}
			if maxPtr(r.AvgJitterMs, cur.AvgJitterMs) != nil {
				cur.AvgJitterMs = maxPtr(r.AvgJitterMs, cur.AvgJitterMs)
			}
			if maxPtr(r.AvgLossPct, cur.AvgLossPct) != nil {
				cur.AvgLossPct = maxPtr(r.AvgLossPct, cur.AvgLossPct)
			}
			if maxPtr(r.AvgRttMs, cur.AvgRttMs) != nil {
				cur.AvgRttMs = maxPtr(r.AvgRttMs, cur.AvgRttMs)
			}
			out[i] = cur
			continue
		}
		idx[r.User] = len(out)
		out = append(out, r)
	}
	return out
}

func maxPtr(a, b *float64) *float64 {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case *a >= *b:
		return a
	default:
		return b
	}
}

// summarizeCascades produces a short natural-language header for the
// find_cascades tool response.
func summarizeCascades(cs []Cascade) string {
	if len(cs) == 0 {
		return "no cascade patterns detected"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d cascade%s detected", len(cs), pluralS(len(cs)))
	top := cs[0]
	fmt.Fprintf(&b, "; top suspect: %s [%s] %s affected %d users",
		top.Sender, top.StreamLabel, top.SenderVerdict, top.AffectedCount)
	return b.String()
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

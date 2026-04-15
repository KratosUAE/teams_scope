package api

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"teams_con/internal/store"
)

// Defaults for BuildUserHealthReport. Kept separate from the generic list
// defaults because this is an LLM-facing aggregate with its own sensible
// top-N ceilings.
const (
	userHealthDefaultWindow = 7 * 24 * time.Hour
	userHealthTopN          = 10
	// userHealthTopIssues caps TopIssues slice length. Kept separate from
	// userHealthTopN so future tuning of the issues aggregation (Phase 5)
	// does not inadvertently change subnet/device top-N behaviour.
	userHealthTopIssues = 10

	// Pattern classification thresholds. Hand-tuned on a week of live data;
	// tune later if false positives surface.
	patternHealthyBadRatio      = 0.20
	patternChronicMicAvgConceal = 10.0
	patternChronicMicShareOfBad = 0.50
	patternWifiBadRatio         = 0.40
	patternWiredCleanRatio      = 0.15
	patternWiredMinCalls        = 2
	patternInsufficientMinCalls = 3

	// network_path (labelled "remote_path" for historical reasons) fires
	// when any of these averages cross their threshold on Bad/Poor-verdict
	// rows only. RTT catches the classic "Dubai → Amsterdam SFU is slow"
	// case; jitter catches bufferbloat / congestion / wifi reordering;
	// loss catches a clogged uplink. Hand-picked so normal VoIP
	// (jitter < 20, loss < 1%) does not trip the detector.
	patternNetRttMs    = 150.0
	patternNetJitterMs = 40.0
	patternNetLossPct  = 2.0
	patternNetMinRows  = 3

	// wifi_suspected fires when the user has no wired baseline to prove
	// the wifi-only hypothesis, but wifi dominates their connectivity and
	// their bad/poor ratio is high enough that the wifi is the most
	// plausible culprit. Looser than wifi_only_issue, so it runs after.
	patternWifiSuspectedShare    = 0.80 // share of (wifi+wired) calls that are wifi
	patternWifiSuspectedBadRatio = 0.50 // share of total calls that are bad/poor
)

// Problem-pattern classification labels returned in UserHealthReport.Pattern.
// They are documented in the MCP tool description so LLM clients can key
// off them without re-reading the aggregates.
const (
	PatternInsufficientData = "insufficient_data"
	PatternHealthy          = "healthy"
	PatternChronicMic       = "chronic_mic"
	PatternWifiOnlyIssue    = "wifi_only_issue"
	PatternWifiSuspected    = "wifi_suspected"
	PatternRemotePath       = "remote_path"
	PatternMixed            = "mixed"
)

// UserHealthParams is the HTTP-free input to BuildUserHealthReport. Only
// Upn is required; From/To default to [now-7d, now).
type UserHealthParams struct {
	Upn  string
	From *time.Time
	To   *time.Time
}

// UserHealthReport is the aggregate dossier returned to the MCP tool. See
// the package-level plan for field semantics. All Top-N slices are capped
// at userHealthTopN to keep the JSON payload compact for LLM context.
type UserHealthReport struct {
	Upn        string    `json:"upn"`
	WindowFrom time.Time `json:"windowFrom"`
	WindowTo   time.Time `json:"windowTo"`
	TotalCalls int       `json:"totalCalls"`
	// Truncated is true when the underlying ListUserCalls hit the
	// maxListLimit ceiling, meaning TotalCalls and all aggregates below
	// describe only the most recent `maxListLimit` calls, not the full
	// window. LLM clients should surface this when present.
	Truncated  bool            `json:"truncated,omitempty"`
	ByVerdict  VerdictCounts   `json:"byVerdict"`
	Subnets    []SubnetUsage   `json:"subnets"`
	Devices    []DeviceUsage   `json:"captureDevices"`
	Clients    []ClientUsage   `json:"clients"`
	Platforms  []PlatformUsage `json:"platforms"`
	AvgMetrics AvgMetrics      `json:"avgMetrics"`
	Pattern    string          `json:"pattern"`
	// Card carries the operator-maintained annotation (notes, tags,
	// location hint) when one exists for this user. Nil when the user
	// has no card — "no card" is a normal state, not an error, and
	// GetUserCard returns (nil, nil) for that case. See
	// Service.GetUserCard GoDoc for the semantics rationale.
	Card *store.UserCard `json:"card,omitempty"`
	// PeerBaseline (Phase 3) compares the target user's bad/poor ratio
	// against a cohort of other users who shared at least one of the
	// target's subnets in the same window. Nil when the target has no
	// calls or the peer query fails — a soft failure logs and skips the
	// field rather than poisoning the whole report.
	PeerBaseline *PeerBaseline `json:"peerBaseline,omitempty"`
	// TopIssues (Phase 5) lists the Teams diagnostic tags (from
	// StreamRow.Issues) most frequently mentioned on this user's calls in
	// the window, counted by distinct callId — a single call that mentions
	// `cpuInsufficient` on three stream rows contributes 1, not 3. Sorted
	// by CallCount desc with a stable alphabetical tie-break, capped at
	// userHealthTopIssues. Nil (omitted on the wire) when no row in the
	// window populated its Issues field.
	TopIssues []IssueCount `json:"topIssues,omitempty"`
}

// IssueCount is one row of UserHealthReport.TopIssues: a Teams diagnostic
// tag (e.g. "cpuInsufficient") and the number of distinct calls that
// reported it in the window.
type IssueCount struct {
	Issue     string `json:"issue"`
	CallCount int    `json:"callCount"`
}

type VerdictCounts struct {
	Good int `json:"good"`
	Poor int `json:"poor"`
	Bad  int `json:"bad"`
}

type SubnetUsage struct {
	Subnet string `json:"subnet"`
	// Name/Office/Kind are populated from the SubnetResolver when the
	// raw Subnet IP/CIDR matches a configured entry in the `subnets`
	// collection. They stay empty for unknown blocks so the rendering
	// layer can fall back to the raw CIDR.
	Name      string `json:"name,omitempty"`
	Office    string `json:"office,omitempty"`
	Kind      string `json:"kind,omitempty"`
	CallCount int    `json:"callCount"`
	ConnType  string `json:"connType,omitempty"`
}

type DeviceUsage struct {
	Device            string  `json:"device"`
	CallCount         int     `json:"callCount"`
	BadCallCount      int     `json:"badCallCount"` // calls where the parent verdict was Poor or Bad
	AvgConcealedPct   float64 `json:"avgConcealedPct"`
	WorstConcealedPct float64 `json:"worstConcealedPct"`
}

type ClientUsage struct {
	UserAgent string `json:"userAgent"`
	CallCount int    `json:"callCount"`
}

type PlatformUsage struct {
	Platform  string `json:"platform"`
	CallCount int    `json:"callCount"`
}

// AvgMetrics holds per-direction averages over all streams in the window.
// Nil pointers mean "no streams contributed a non-nil sample for this
// direction + metric" — the zero value is reserved for real-zero readings.
type AvgMetrics struct {
	JitterSendMs *float64 `json:"jitterSendMs,omitempty"`
	JitterRecvMs *float64 `json:"jitterRecvMs,omitempty"`
	LossSendPct  *float64 `json:"lossSendPct,omitempty"`
	LossRecvPct  *float64 `json:"lossRecvPct,omitempty"`
	RttSendMs    *float64 `json:"rttSendMs,omitempty"`
	RttRecvMs    *float64 `json:"rttRecvMs,omitempty"`
}

// BuildUserHealthReport fetches the user's calls in the window, pulls every
// stream row attributed to them through a single $in query, and folds the
// rows into a compact aggregate with a coarse problem-pattern label. An
// empty window is returned as a non-nil report with TotalCalls=0 and
// Pattern=insufficient_data — never a non-nil error for "no data".
func (s *Service) BuildUserHealthReport(ctx context.Context, p UserHealthParams) (*UserHealthReport, error) {
	if p.Upn == "" {
		return nil, fmt.Errorf("%w: empty upn", ErrBadRequest)
	}
	now := time.Now().UTC()
	from := now.Add(-userHealthDefaultWindow)
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

	calls, err := s.ListUserCalls(ctx, p.Upn, &from, &to, maxListLimit, 0)
	if err != nil {
		return nil, err
	}

	report := &UserHealthReport{
		Upn:        p.Upn,
		WindowFrom: from,
		WindowTo:   to,
		TotalCalls: len(calls),
		// ListUserCalls clamps at maxListLimit. When we hit that exact
		// number the aggregates below describe the most recent slice, not
		// the full window — surface that to the caller.
		Truncated: len(calls) == maxListLimit,
	}

	if len(calls) == 0 {
		report.Pattern = PatternInsufficientData
		return report, nil
	}

	ids := make([]string, len(calls))
	verdictByCall := make(map[string]string, len(calls))
	for i, c := range calls {
		ids[i] = c.CallId
		verdictByCall[c.CallId] = c.Verdict
		switch c.Verdict {
		case verdictGood:
			report.ByVerdict.Good++
		case verdictPoor:
			report.ByVerdict.Poor++
		case verdictBad:
			report.ByVerdict.Bad++
		}
	}

	rows, err := s.streams.ListByUserInCalls(ctx, p.Upn, ids)
	if err != nil {
		return nil, fmt.Errorf("api: user health: list streams: %w", err)
	}

	aggregateUserHealth(report, rows, verdictByCall)
	// Phase 1 enrichment: attach friendly Name/Office/Kind to each
	// SubnetUsage entry via the resolver. Done after aggregateUserHealth
	// so the helper itself stays pure and unit-testable.
	s.decorateSubnets(ctx, report.Subnets)
	// Phase 2: attach the operator-maintained user card when one exists.
	// GetUserCard returns (nil, nil) for missing by design, so we only
	// need to log on a real error and continue — a Mongo hiccup on the
	// card lookup must NOT fail the rest of the report.
	if card, cerr := s.GetUserCard(ctx, p.Upn); cerr != nil {
		s.log.Warn("api: user health: get card",
			slog.String("upn", p.Upn),
			slog.String("err", cerr.Error()),
		)
	} else if card != nil {
		report.Card = card
	}
	// Phase 3: compute the peer baseline BEFORE classifyUserPattern so
	// the classifier can override its user-level diagnosis with the new
	// org_wide_issue label when peers share the pain. A Mongo hiccup here
	// is logged and skipped — the rest of the report must still ship.
	if baseline, berr := s.ComputePeerBaseline(ctx, p.Upn, &from, &to); berr != nil {
		s.log.Warn("api: user health: peer baseline",
			slog.String("upn", p.Upn),
			slog.String("err", berr.Error()),
		)
	} else if baseline != nil {
		report.PeerBaseline = baseline
	}
	report.Pattern = classifyUserPattern(report, rows, verdictByCall)
	return report, nil
}

// Aggregation bucket types used by aggregateUserHealth + top* helpers.
// Declared at package scope so helper-function signatures can name them.
type subnetAgg struct {
	calls     map[string]struct{}
	connTypes map[string]int
}

type deviceAgg struct {
	calls    map[string]struct{} // distinct callIds seen with this device
	badCalls map[string]struct{} // subset of calls whose parent verdict was Poor/Bad
	sum      float64
	samples  int
	worst    float64
}

type strCountAgg struct {
	calls map[string]struct{}
}

// aggregateUserHealth folds rows into the Subnets/Devices/Clients/
// Platforms/AvgMetrics fields of report. Counts are always distinct-callId
// — the same (subnet, device, UA) can appear on many segments of one call
// without inflating the usage counter.
func aggregateUserHealth(report *UserHealthReport, rows []store.StreamRow, verdictByCall map[string]string) {
	subnets := make(map[string]*subnetAgg)
	devices := make(map[string]*deviceAgg)
	clients := make(map[string]*strCountAgg)
	platforms := make(map[string]*strCountAgg)
	// issueCalls: issue-token -> set of distinct callIds mentioning it.
	// Nested set rather than a plain counter so a call with multiple
	// stream rows naming the same tag contributes exactly 1 to CallCount.
	issueCalls := map[string]map[string]struct{}{}

	var (
		jitSendSum, jitSendN   float64
		jitRecvSum, jitRecvN   float64
		lossSendSum, lossSendN float64
		lossRecvSum, lossRecvN float64
		rttSendSum, rttSendN   float64
		rttRecvSum, rttRecvN   float64
	)

	for _, r := range rows {
		if r.Subnet != "" {
			b, ok := subnets[r.Subnet]
			if !ok {
				b = &subnetAgg{calls: map[string]struct{}{}, connTypes: map[string]int{}}
				subnets[r.Subnet] = b
			}
			b.calls[r.CallId] = struct{}{}
			if r.ConnType != "" {
				b.connTypes[r.ConnType]++
			}
		}

		if r.CaptureDevice != "" && isAudioLabel(r.StreamLabel) && isSendDirection(r.Direction) {
			b, ok := devices[r.CaptureDevice]
			if !ok {
				b = &deviceAgg{
					calls:    map[string]struct{}{},
					badCalls: map[string]struct{}{},
				}
				devices[r.CaptureDevice] = b
			}
			b.calls[r.CallId] = struct{}{}
			switch verdictByCall[r.CallId] {
			case verdictBad, verdictPoor:
				b.badCalls[r.CallId] = struct{}{}
			}
			if r.ConcealedPct != nil {
				conc := *r.ConcealedPct
				b.sum += conc
				b.samples++
				if conc > b.worst {
					b.worst = conc
				}
			}
		}

		if r.UserAgent != "" {
			b, ok := clients[r.UserAgent]
			if !ok {
				b = &strCountAgg{calls: map[string]struct{}{}}
				clients[r.UserAgent] = b
			}
			b.calls[r.CallId] = struct{}{}
		}

		if r.Platform != "" {
			b, ok := platforms[r.Platform]
			if !ok {
				b = &strCountAgg{calls: map[string]struct{}{}}
				platforms[r.Platform] = b
			}
			b.calls[r.CallId] = struct{}{}
		}

		// Phase 5: issue-tag aggregation. StreamRow.Issues is typically
		// ';'-delimited but the crawler has historically emitted both
		// ',' separators and single-tag strings, so splitIssues handles
		// all three. Empty/whitespace tokens are dropped upstream.
		// splitIssues("") returns nil, so range over it is a no-op.
		for _, tok := range splitIssues(r.Issues) {
			set, ok := issueCalls[tok]
			if !ok {
				set = map[string]struct{}{}
				issueCalls[tok] = set
			}
			set[r.CallId] = struct{}{}
		}

		send := isSendDirection(r.Direction)
		if r.AvgJitterMs != nil {
			if send {
				jitSendSum += *r.AvgJitterMs
				jitSendN++
			} else {
				jitRecvSum += *r.AvgJitterMs
				jitRecvN++
			}
		}
		if r.AvgLossPct != nil {
			if send {
				lossSendSum += *r.AvgLossPct
				lossSendN++
			} else {
				lossRecvSum += *r.AvgLossPct
				lossRecvN++
			}
		}
		if r.AvgRttMs != nil {
			if send {
				rttSendSum += *r.AvgRttMs
				rttSendN++
			} else {
				rttRecvSum += *r.AvgRttMs
				rttRecvN++
			}
		}
	}

	report.Subnets = topSubnets(subnets)
	report.Devices = topDevices(devices)
	report.Clients = topClients(clients)
	report.Platforms = topPlatforms(platforms)
	report.TopIssues = topIssues(issueCalls)

	report.AvgMetrics = AvgMetrics{
		JitterSendMs: avgPtr(jitSendSum, jitSendN),
		JitterRecvMs: avgPtr(jitRecvSum, jitRecvN),
		LossSendPct:  avgPtr(lossSendSum, lossSendN),
		LossRecvPct:  avgPtr(lossRecvSum, lossRecvN),
		RttSendMs:    avgPtr(rttSendSum, rttSendN),
		RttRecvMs:    avgPtr(rttRecvSum, rttRecvN),
	}
}

func topSubnets(m map[string]*subnetAgg) []SubnetUsage {
	out := make([]SubnetUsage, 0, len(m))
	for subnet, b := range m {
		dominant := ""
		best := 0
		for ct, n := range b.connTypes {
			if n > best {
				best = n
				dominant = ct
			}
		}
		out = append(out, SubnetUsage{
			Subnet:    subnet,
			CallCount: len(b.calls),
			ConnType:  dominant,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CallCount != out[j].CallCount {
			return out[i].CallCount > out[j].CallCount
		}
		return out[i].Subnet < out[j].Subnet
	})
	if len(out) > userHealthTopN {
		out = out[:userHealthTopN]
	}
	return out
}

func topDevices(m map[string]*deviceAgg) []DeviceUsage {
	out := make([]DeviceUsage, 0, len(m))
	for device, b := range m {
		var avg float64
		if b.samples > 0 {
			avg = b.sum / float64(b.samples)
		}
		out = append(out, DeviceUsage{
			Device:            device,
			CallCount:         len(b.calls),
			BadCallCount:      len(b.badCalls),
			AvgConcealedPct:   round1(avg),
			WorstConcealedPct: round1(b.worst),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CallCount != out[j].CallCount {
			return out[i].CallCount > out[j].CallCount
		}
		return out[i].Device < out[j].Device
	})
	if len(out) > userHealthTopN {
		out = out[:userHealthTopN]
	}
	return out
}

func topClients(m map[string]*strCountAgg) []ClientUsage {
	out := make([]ClientUsage, 0, len(m))
	for ua, b := range m {
		out = append(out, ClientUsage{UserAgent: ua, CallCount: len(b.calls)})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CallCount != out[j].CallCount {
			return out[i].CallCount > out[j].CallCount
		}
		return out[i].UserAgent < out[j].UserAgent
	})
	if len(out) > userHealthTopN {
		out = out[:userHealthTopN]
	}
	return out
}

func topPlatforms(m map[string]*strCountAgg) []PlatformUsage {
	out := make([]PlatformUsage, 0, len(m))
	for p, b := range m {
		out = append(out, PlatformUsage{Platform: p, CallCount: len(b.calls)})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CallCount != out[j].CallCount {
			return out[i].CallCount > out[j].CallCount
		}
		return out[i].Platform < out[j].Platform
	})
	if len(out) > userHealthTopN {
		out = out[:userHealthTopN]
	}
	return out
}

// classifyUserPattern runs the coarse problem-pattern heuristic described
// in the plan. First match wins. The function is pure so tests can drive
// it without the Service.
func classifyUserPattern(r *UserHealthReport, rows []store.StreamRow, verdictByCall map[string]string) string {
	if r.TotalCalls < patternInsufficientMinCalls {
		return PatternInsufficientData
	}
	badPoor := r.ByVerdict.Bad + r.ByVerdict.Poor
	bpRatio := float64(badPoor) / float64(r.TotalCalls)
	if bpRatio < patternHealthyBadRatio {
		return PatternHealthy
	}

	// Phase 3 insertion: org_wide_issue. When the peer cohort is on-par
	// with the target AND the target's bad/poor ratio is materially high,
	// the user-level diagnosis ("your mic is broken", "your wifi sucks")
	// is misleading — everyone on their subnets is suffering the same.
	// Surface the org-wide signal and bail BEFORE chronic_mic / wifi /
	// remote_path so the operator's first instinct is to look at network
	// health, not the user. Ordered per design-peer-baseline.md.
	if r.PeerBaseline != nil &&
		r.PeerBaseline.Assessment == PeerAssessmentOnPar &&
		bpRatio >= patternPeerHighBad {
		return PatternOrgWideIssue
	}

	// chronic_mic: a single capture device averaging concealedPct >= 10 and
	// responsible for at least half of the bad/poor calls. Uses BadCallCount
	// (bad/poor calls where this device was active), NOT CallCount — a
	// healthy headset used on 20 good + 3 unrelated bad calls would
	// otherwise false-positive because CallCount dwarfs badPoor.
	for _, d := range r.Devices {
		if d.AvgConcealedPct >= patternChronicMicAvgConceal &&
			badPoor > 0 &&
			float64(d.BadCallCount) >= patternChronicMicShareOfBad*float64(badPoor) {
			return PatternChronicMic
		}
	}

	// Build wifi/wired call sets from raw rows + parent-call verdicts.
	// A call counts as "wifi" if any of its attributed streams reported
	// a wifi connType, and "wired" analogously. A single call with a
	// wifi→wired handoff would land in both sets; treat that as noise.
	wifiCalls := map[string]struct{}{}
	wiredCalls := map[string]struct{}{}
	for _, row := range rows {
		ct := strings.ToLower(row.ConnType)
		if strings.Contains(ct, "wifi") {
			wifiCalls[row.CallId] = struct{}{}
		} else if strings.Contains(ct, "wired") || strings.Contains(ct, "ethernet") {
			wiredCalls[row.CallId] = struct{}{}
		}
	}

	// wifi_only_issue: strong evidence — wifi is bad AND a wired baseline
	// (>= 2 calls) is clean. Requires both sides of the experiment to run.
	wifiBad := badPoorCountIn(wifiCalls, verdictByCall)
	wiredBad := badPoorCountIn(wiredCalls, verdictByCall)
	if len(wifiCalls) > 0 && len(wiredCalls) >= patternWiredMinCalls {
		wifiRatio := float64(wifiBad) / float64(len(wifiCalls))
		wiredRatio := float64(wiredBad) / float64(len(wiredCalls))
		if wifiRatio > patternWifiBadRatio && wiredRatio < patternWiredCleanRatio {
			return PatternWifiOnlyIssue
		}
	}

	// wifi_suspected: weaker form of wifi_only_issue for the common case
	// where the user has NO wired baseline (100% wifi laptop). If wifi
	// dominates their connectivity and their bad ratio is high, blaming
	// wifi is the most actionable first hypothesis — the operator's next
	// step is "try wired and re-test".
	totalConn := len(wifiCalls) + len(wiredCalls)
	if totalConn > 0 {
		wifiShare := float64(len(wifiCalls)) / float64(totalConn)
		if wifiShare >= patternWifiSuspectedShare && bpRatio >= patternWifiSuspectedBadRatio {
			return PatternWifiSuspected
		}
	}

	// remote_path (network-side problems in general): sustained high
	// RTT OR jitter OR loss on bad/poor call streams. We cannot use
	// r.AvgMetrics because that mixes in good calls — a user with 10
	// clean + 3 degraded calls would dilute below every threshold.
	// Recompute the direction-free averages across bad/poor rows only.
	// The label remains "remote_path" for stability of the MCP contract;
	// the semantics now cover RTT (physics), jitter (congestion), and
	// loss (clogged link) equivalently.
	if badNetworkSignal(rows, verdictByCall) {
		return PatternRemotePath
	}

	return PatternMixed
}

// badNetworkSignal returns true when the mean RTT, jitter, or loss across
// Bad/Poor-verdict stream rows crosses its threshold. A minimum sample
// count guards against single-outlier flips. Directions are merged: we
// care that SOMETHING network-side is consistently bad, not which
// direction — operator drilling down can query the raw aggregates.
func badNetworkSignal(rows []store.StreamRow, verdictByCall map[string]string) bool {
	var rttSum, jitSum, lossSum float64
	var rttN, jitN, lossN int
	for _, row := range rows {
		switch verdictByCall[row.CallId] {
		case verdictBad, verdictPoor:
		default:
			continue
		}
		if row.AvgRttMs != nil {
			rttSum += *row.AvgRttMs
			rttN++
		}
		if row.AvgJitterMs != nil {
			jitSum += *row.AvgJitterMs
			jitN++
		}
		if row.AvgLossPct != nil {
			lossSum += *row.AvgLossPct
			lossN++
		}
	}
	if rttN >= patternNetMinRows && rttSum/float64(rttN) >= patternNetRttMs {
		return true
	}
	if jitN >= patternNetMinRows && jitSum/float64(jitN) >= patternNetJitterMs {
		return true
	}
	if lossN >= patternNetMinRows && lossSum/float64(lossN) >= patternNetLossPct {
		return true
	}
	return false
}

// badPoorCountIn returns the number of calls in `set` whose verdict is
// Bad or Poor according to verdictByCall.
func badPoorCountIn(set map[string]struct{}, verdictByCall map[string]string) int {
	n := 0
	for id := range set {
		switch verdictByCall[id] {
		case verdictBad, verdictPoor:
			n++
		}
	}
	return n
}

// isSendDirection folds the two send-side aliases ("send", "upload") used
// in our stream rows into one bool. Receive side is everything else,
// which matches how findCascades splits traffic.
func isSendDirection(d string) bool {
	return d == "send" || d == "upload"
}

// splitIssues splits a raw StreamRow.Issues string into normalised tokens.
// Teams diagnostic tag fields are commonly ';'-delimited, occasionally
// ','-delimited, and sometimes a single undelimited tag — we accept all
// three. Empty and whitespace-only tokens are dropped. No case folding:
// the tags are already camelCase identifiers and operators key off them
// verbatim.
func splitIssues(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return r == ';' || r == ','
	})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// topIssues flattens the issue->callIds map into the sorted, capped
// IssueCount slice surfaced on UserHealthReport.TopIssues. Returns a nil
// slice (not an empty one) when the map is empty so the wire payload
// stays clean under the `omitempty` JSON tag.
func topIssues(m map[string]map[string]struct{}) []IssueCount {
	if len(m) == 0 {
		return nil
	}
	out := make([]IssueCount, 0, len(m))
	for tok, set := range m {
		out = append(out, IssueCount{Issue: tok, CallCount: len(set)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CallCount != out[j].CallCount {
			return out[i].CallCount > out[j].CallCount
		}
		return out[i].Issue < out[j].Issue
	})
	if len(out) > userHealthTopIssues {
		out = out[:userHealthTopIssues]
	}
	return out
}

// avgPtr returns a pointer to sum/n rounded to one decimal, or nil when
// there were no samples. Nil distinguishes "no data" from "real zero" on
// the wire.
func avgPtr(sum, n float64) *float64 {
	if n == 0 {
		return nil
	}
	v := round1(sum / n)
	return &v
}

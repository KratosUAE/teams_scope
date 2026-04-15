package quality

import (
	"math"
	"strconv"

	"teams_con/internal/graph"
)

// Verdict is the qualitative bucket assigned to a stream or call. The three
// string values match the PowerShell reference literally (title case).
type Verdict string

const (
	// VerdictGood means no metric crossed a Poor threshold.
	VerdictGood Verdict = "Good"
	// VerdictPoor means at least one metric crossed its Poor threshold but
	// none crossed a Bad threshold.
	VerdictPoor Verdict = "Poor"
	// VerdictBad means at least one metric crossed its Bad threshold.
	VerdictBad Verdict = "Bad"
)

// RankVerdict maps a verdict to an integer for worst-of-call comparison.
// Bad (2) > Poor (1) > Good (0). Unknown strings rank as -1 so they never
// escalate a valid verdict. PS reference: Get-CallVerdict line 222
// (`$rank = @{ 'Good'=0; 'Poor'=1; 'Bad'=2 }`).
func RankVerdict(v Verdict) int {
	switch v {
	case VerdictGood:
		return 0
	case VerdictPoor:
		return 1
	case VerdictBad:
		return 2
	default:
		return -1
	}
}

// worseOf returns whichever of a or b has the higher rank. On a tie, a wins
// (first occurrence — matches PS `-gt` strict comparison semantics).
func worseOf(a, b Verdict) Verdict {
	if RankVerdict(b) > RankVerdict(a) {
		return b
	}
	return a
}

// StreamMetrics carries the numeric values pulled out of a MediaStream after
// ISO 8601 conversion. All pointer fields are nil when the corresponding
// Graph field was absent — this mirrors the PowerShell convention of
// distinguishing "missing" from "zero". Consumers must nil-check before
// dereferencing.
type StreamMetrics struct {
	AvgJitterMs    *float64 // milliseconds, rounded to 2 decimals (PS parity)
	MaxJitterMs    *float64
	AvgLossPct     *float64 // fractional loss (0..1), not percent
	MaxLossPct     *float64
	AvgRttMs       *float64
	MaxRttMs       *float64
	MosDegradation *float64
	ConcealedPct   *float64 // fractional (0..1), not percent
	PacketCount    int64
}

// StreamEvaluation is the per-stream evaluation result — the return shape of
// the PowerShell Test-StreamQuality function.
//
// Reasons are stream-local only ("jitter=45ms"). Call-level decoration with
// the "{upn}[{dir}]: " prefix happens in row.go when building a CallRow.
type StreamEvaluation struct {
	Verdict Verdict
	Reasons []string
	Metrics StreamMetrics
}

// EvaluateStream is the Go port of Test-StreamQuality (PowerShell
// CallQualityUtils.ps1 lines 139-207). It inspects one MediaStream and
// returns the verdict, reason list, and extracted metrics.
//
// The isVideo flag mirrors the PS -IsVideo switch: when true the audio-only
// checks (mosDegrad, concealed) are skipped (PS lines 183, 187). Jitter, RTT,
// and packet loss are evaluated for every stream regardless of modality.
//
// Reason string formats are chosen to match the PowerShell output byte for
// byte after rounding:
//
//	loss     -> "loss=<round1>%"       (value = loss*100 rounded to 1 dp)
//	jitter   -> "jitter=<ms>ms"        (ms rounded to 2 dp by Iso conversion)
//	rtt      -> "rtt=<ms>ms"           (same)
//	mosDeg   -> "mosDeg=<round2>"      (value rounded to 2 dp)
//	concealed-> "concealed=<round1>%"  (value = concealed*100 rounded to 1 dp)
//
// Note on "mosDeg": the canonical Go reason label is "mosDeg" (per task
// spec). The PowerShell script historically used "mosDegrad"; downstream
// Go consumers should rely on the literal used here.
func EvaluateStream(stream *graph.MediaStream, isVideo bool) StreamEvaluation {
	var eval StreamEvaluation
	if stream == nil {
		eval.Verdict = VerdictGood
		return eval
	}

	// --- Convert ISO 8601 metrics to milliseconds (rounded to 2 dp). -----
	if ms, ok := isoToMs(stream.AverageJitter); ok {
		eval.Metrics.AvgJitterMs = floatPtr(ms)
	}
	if ms, ok := isoToMs(stream.MaxJitter); ok {
		eval.Metrics.MaxJitterMs = floatPtr(ms)
	}
	if ms, ok := isoToMs(stream.AverageRoundTripTime); ok {
		eval.Metrics.AvgRttMs = floatPtr(ms)
	}
	if ms, ok := isoToMs(stream.MaxRoundTripTime); ok {
		eval.Metrics.MaxRttMs = floatPtr(ms)
	}
	// Numeric pointer fields are copied verbatim — Graph sends them as plain
	// JSON numbers so no conversion is needed.
	if stream.AveragePacketLossRate != nil {
		v := *stream.AveragePacketLossRate
		eval.Metrics.AvgLossPct = &v
	}
	if stream.MaxPacketLossRate != nil {
		v := *stream.MaxPacketLossRate
		eval.Metrics.MaxLossPct = &v
	}
	if stream.AverageAudioDegradation != nil {
		v := *stream.AverageAudioDegradation
		eval.Metrics.MosDegradation = &v
	}
	if stream.AverageRatioOfConcealedSamples != nil {
		v := *stream.AverageRatioOfConcealedSamples
		eval.Metrics.ConcealedPct = &v
	}
	eval.Metrics.PacketCount = stream.PacketUtilization

	// --- Threshold evaluation (same order as PS 171-190). ----------------
	eval.Verdict = VerdictGood

	// Packet loss (fractional).
	if eval.Metrics.AvgLossPct != nil {
		loss := *eval.Metrics.AvgLossPct
		switch {
		case loss > PacketLossBad:
			eval.Reasons = append(eval.Reasons, "loss="+round1Pct(loss)+"%")
			eval.Verdict = worseOf(eval.Verdict, VerdictBad)
		case loss > PacketLossPoor:
			eval.Reasons = append(eval.Reasons, "loss="+round1Pct(loss)+"%")
			eval.Verdict = worseOf(eval.Verdict, VerdictPoor)
		}
	}

	// Jitter (ms).
	if eval.Metrics.AvgJitterMs != nil {
		j := *eval.Metrics.AvgJitterMs
		switch {
		case j > JitterBadMs:
			eval.Reasons = append(eval.Reasons, "jitter="+formatMs(j)+"ms")
			eval.Verdict = worseOf(eval.Verdict, VerdictBad)
		case j > JitterPoorMs:
			eval.Reasons = append(eval.Reasons, "jitter="+formatMs(j)+"ms")
			eval.Verdict = worseOf(eval.Verdict, VerdictPoor)
		}
	}

	// Round-trip time (ms).
	if eval.Metrics.AvgRttMs != nil {
		r := *eval.Metrics.AvgRttMs
		switch {
		case r > RTTBadMs:
			eval.Reasons = append(eval.Reasons, "rtt="+formatMs(r)+"ms")
			eval.Verdict = worseOf(eval.Verdict, VerdictBad)
		case r > RTTPoorMs:
			eval.Reasons = append(eval.Reasons, "rtt="+formatMs(r)+"ms")
			eval.Verdict = worseOf(eval.Verdict, VerdictPoor)
		}
	}

	// Audio MOS degradation — skipped for video streams (PS 183).
	if !isVideo && eval.Metrics.MosDegradation != nil {
		d := *eval.Metrics.MosDegradation
		switch {
		case d > MosDegradationBad:
			eval.Reasons = append(eval.Reasons, "mosDeg="+round2(d))
			eval.Verdict = worseOf(eval.Verdict, VerdictBad)
		case d > MosDegradationPoor:
			eval.Reasons = append(eval.Reasons, "mosDeg="+round2(d))
			eval.Verdict = worseOf(eval.Verdict, VerdictPoor)
		}
	}

	// Concealed samples ratio — skipped for video streams (PS 187).
	if !isVideo && eval.Metrics.ConcealedPct != nil {
		c := *eval.Metrics.ConcealedPct
		switch {
		case c > ConcealedSamplesBadPct:
			eval.Reasons = append(eval.Reasons, "concealed="+round1Pct(c)+"%")
			eval.Verdict = worseOf(eval.Verdict, VerdictBad)
		case c > ConcealedSamplesPoorPct:
			eval.Reasons = append(eval.Reasons, "concealed="+round1Pct(c)+"%")
			eval.Verdict = worseOf(eval.Verdict, VerdictPoor)
		}
	}

	return eval
}

// isoToMs converts an ISO 8601 duration string to milliseconds rounded to 2
// decimals. The rounding matches PowerShell's
// `[math]::Round($ts.TotalMilliseconds, 2)` (CallQualityUtils.ps1 line 48).
// Returns (0, false) for empty or unparseable input.
func isoToMs(s string) (float64, bool) {
	d, ok := graph.ParseISO8601Duration(s)
	if !ok {
		return 0, false
	}
	ms := float64(d) / float64(1_000_000) // ns -> ms
	return math.Round(ms*100) / 100, true
}

// round1Pct takes a fraction in [0..1], multiplies by 100 and rounds to 1 dp,
// then formats without a trailing ".0" (matching PS `[math]::Round(x*100,1)`
// which prints integers without a decimal point). Example: 0.065 -> "6.5",
// 0.10 -> "10".
func round1Pct(frac float64) string {
	rounded := math.Round(frac*100*10) / 10
	return strconv.FormatFloat(rounded, 'f', -1, 64)
}

// round2 rounds to 2 decimals and formats without a trailing zero.
func round2(v float64) string {
	rounded := math.Round(v*100) / 100
	return strconv.FormatFloat(rounded, 'f', -1, 64)
}

// formatMs formats a millisecond value produced by isoToMs (already rounded
// to 2 dp) without a trailing zero. PowerShell string-interpolation for a
// double follows the same "minimal representation" rule.
func formatMs(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// floatPtr is a tiny helper to turn a float literal into a *float64 without
// leaking a named temporary into every call site.
func floatPtr(v float64) *float64 { return &v }

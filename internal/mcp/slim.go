package mcp

import (
	"math"
	"time"

	"teams_con/internal/store"
)

// slimStream is a token-budget-friendly projection of store.StreamRow used
// by get_call and find_cascades responses. It rounds floats to one
// decimal and drops *pure noise* fields — packetCount, ipAddress (subnet
// covers network blame and is less PII), productFamily (almost always
// "teams"), renderDevice (speakers are rarely the root cause),
// reflexiveIp/networkName/wifiBand/wifiSignal/linkMbps (too nichey for
// first-pass triage), callId (already on parent), and issues (redundant
// with verdict + metrics).
//
// Kept because they carry real triage signal:
//   - userAgent: "outdated Teams client" root cause
//   - relayIp / relayPort: direct media vs TURN relay (NAT / extra hops)
//   - captureDevice: microphone model — Top-problem-devices pattern from
//     the PS reference
//   - mosDegradation / concealedPct: audio-specific quality metrics
//   - max{Jitter,Loss,Rtt}: spike detection vs averages
//
// omitempty everywhere — a nil-metric field or empty string simply
// disappears from the wire payload instead of adding
// `"avgLossPct":0,"platform":""` noise.
type slimStream struct {
	User           string     `json:"user,omitempty"`
	Direction      string     `json:"dir,omitempty"`     // recv|send|? (kept short)
	Verdict        string     `json:"verdict,omitempty"`
	StreamLabel    string     `json:"label,omitempty"`   // audio|video|data|scr|...
	AvgJitterMs    *float64   `json:"jitMs,omitempty"`
	MaxJitterMs    *float64   `json:"jitMaxMs,omitempty"`
	AvgLossPct     *float64   `json:"lossPct,omitempty"`
	MaxLossPct     *float64   `json:"lossMaxPct,omitempty"`
	AvgRttMs       *float64   `json:"rttMs,omitempty"`
	MaxRttMs       *float64   `json:"rttMaxMs,omitempty"`
	MosDegradation *float64   `json:"mosDeg,omitempty"`
	ConcealedPct   *float64   `json:"concealedPct,omitempty"`
	ConnType       string     `json:"conn,omitempty"`
	Platform       string     `json:"platform,omitempty"`
	UserAgent      string     `json:"userAgent,omitempty"`
	Subnet         string     `json:"subnet,omitempty"`
	RelayIp        string     `json:"relayIp,omitempty"`
	RelayPort      int        `json:"relayPort,omitempty"`
	CaptureDevice  string     `json:"capture,omitempty"`
	SegmentStart   *time.Time `json:"from,omitempty"`
	SegmentEnd     *time.Time `json:"to,omitempty"`
}

// slimCall is the compact CallDetail payload: a thin Call header plus
// slimStream[] instead of the full store.StreamRow dump.
type slimCall struct {
	Call    store.Call   `json:"call"`
	Streams []slimStream `json:"streams"`
}

// toSlimStreams compacts a slice of store.StreamRow values.
func toSlimStreams(rows []store.StreamRow) []slimStream {
	out := make([]slimStream, 0, len(rows))
	for _, r := range rows {
		out = append(out, slimStream{
			User:           r.User,
			Direction:      r.Direction,
			Verdict:        r.Verdict,
			StreamLabel:    r.StreamLabel,
			AvgJitterMs:    round1(r.AvgJitterMs),
			MaxJitterMs:    round1(r.MaxJitterMs),
			AvgLossPct:     round1(r.AvgLossPct),
			MaxLossPct:     round1(r.MaxLossPct),
			AvgRttMs:       round1(r.AvgRttMs),
			MaxRttMs:       round1(r.MaxRttMs),
			MosDegradation: round1(r.MosDegradation),
			ConcealedPct:   round1(r.ConcealedPct),
			ConnType:       r.ConnType,
			Platform:       r.Platform,
			UserAgent:      r.UserAgent,
			Subnet:         r.Subnet,
			RelayIp:        r.RelayIp,
			RelayPort:      r.RelayPort,
			CaptureDevice:  r.CaptureDevice,
			SegmentStart:   nilIfZero(r.SegmentStart),
			SegmentEnd:     nilIfZero(r.SegmentEnd),
		})
	}
	return out
}

// round1 rounds a nullable float to one decimal place. nil-in / nil-out.
func round1(v *float64) *float64 {
	if v == nil {
		return nil
	}
	r := math.Round(*v*10) / 10
	return &r
}

// nilIfZero converts a zero-valued time.Time into nil so omitempty kicks
// in and the field is dropped from the JSON payload entirely.
func nilIfZero(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

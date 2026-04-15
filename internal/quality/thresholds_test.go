package quality

import "testing"

// TestThresholdConstants pins the numeric values to the PowerShell reference.
// If any of these fail, the Go port has drifted from CallQualityUtils.ps1
// lines 21-32 and downstream verdicts will no longer match the PS script.
func TestThresholdConstants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		got  float64
		want float64
	}{
		{"PacketLossPoor", PacketLossPoor, 0.05},
		{"PacketLossBad", PacketLossBad, 0.10},
		{"JitterPoorMs", JitterPoorMs, 30},
		{"JitterBadMs", JitterBadMs, 50},
		{"RTTPoorMs", RTTPoorMs, 500},
		{"RTTBadMs", RTTBadMs, 1000},
		{"MosDegradationPoor", MosDegradationPoor, 1.0},
		{"MosDegradationBad", MosDegradationBad, 1.5},
		{"ConcealedSamplesPoorPct", ConcealedSamplesPoorPct, 0.07},
		{"ConcealedSamplesBadPct", ConcealedSamplesBadPct, 0.15},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

// TestThresholdBoundaries exercises every "just-below / just-above" edge
// for every metric using EvaluateStream. The PS script uses strict `-gt`
// comparisons, so a value equal to a threshold must land in the lower
// bucket.
func TestThresholdBoundaries(t *testing.T) {
	t.Parallel()

	frac := func(v float64) *float64 { return &v }

	cases := []struct {
		name    string
		stream  mediaStreamInit
		isVideo bool
		want    Verdict
	}{
		// Packet loss.
		{"loss just at Poor", mediaStreamInit{loss: frac(0.05)}, false, VerdictGood},
		{"loss just above Poor", mediaStreamInit{loss: frac(0.0501)}, false, VerdictPoor},
		{"loss just at Bad", mediaStreamInit{loss: frac(0.10)}, false, VerdictPoor},
		{"loss just above Bad", mediaStreamInit{loss: frac(0.1001)}, false, VerdictBad},

		// Jitter — encoded as ISO 8601 seconds.
		{"jitter just at Poor", mediaStreamInit{jitter: "PT0.030S"}, false, VerdictGood},
		{"jitter just above Poor", mediaStreamInit{jitter: "PT0.031S"}, false, VerdictPoor},
		{"jitter just at Bad", mediaStreamInit{jitter: "PT0.050S"}, false, VerdictPoor},
		{"jitter just above Bad", mediaStreamInit{jitter: "PT0.051S"}, false, VerdictBad},

		// RTT.
		{"rtt just at Poor", mediaStreamInit{rtt: "PT0.500S"}, false, VerdictGood},
		{"rtt just above Poor", mediaStreamInit{rtt: "PT0.501S"}, false, VerdictPoor},
		{"rtt just at Bad", mediaStreamInit{rtt: "PT1.000S"}, false, VerdictPoor},
		{"rtt just above Bad", mediaStreamInit{rtt: "PT1.001S"}, false, VerdictBad},

		// MOS degradation — audio only.
		{"mosDeg just at Poor", mediaStreamInit{mos: frac(1.0)}, false, VerdictGood},
		{"mosDeg just above Poor", mediaStreamInit{mos: frac(1.01)}, false, VerdictPoor},
		{"mosDeg just at Bad", mediaStreamInit{mos: frac(1.5)}, false, VerdictPoor},
		{"mosDeg just above Bad", mediaStreamInit{mos: frac(1.51)}, false, VerdictBad},
		// Video stream ignores mosDeg entirely.
		{"mosDeg ignored when video", mediaStreamInit{mos: frac(2.0)}, true, VerdictGood},

		// Concealed samples — audio only.
		{"concealed just at Poor", mediaStreamInit{concealed: frac(0.07)}, false, VerdictGood},
		{"concealed just above Poor", mediaStreamInit{concealed: frac(0.0701)}, false, VerdictPoor},
		{"concealed just at Bad", mediaStreamInit{concealed: frac(0.15)}, false, VerdictPoor},
		{"concealed just above Bad", mediaStreamInit{concealed: frac(0.1501)}, false, VerdictBad},
		{"concealed ignored when video", mediaStreamInit{concealed: frac(0.9)}, true, VerdictGood},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := EvaluateStream(c.stream.build(), c.isVideo).Verdict
			if got != c.want {
				t.Errorf("verdict = %q, want %q", got, c.want)
			}
		})
	}
}

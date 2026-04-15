package quality

import (
	"reflect"
	"testing"
)

func TestRankVerdict(t *testing.T) {
	t.Parallel()
	if RankVerdict(VerdictBad) <= RankVerdict(VerdictPoor) ||
		RankVerdict(VerdictPoor) <= RankVerdict(VerdictGood) {
		t.Fatalf("rank ordering wrong: Good=%d Poor=%d Bad=%d",
			RankVerdict(VerdictGood), RankVerdict(VerdictPoor), RankVerdict(VerdictBad))
	}
}

func TestWorseOf(t *testing.T) {
	t.Parallel()
	cases := []struct {
		a, b, want Verdict
	}{
		{VerdictGood, VerdictGood, VerdictGood},
		{VerdictGood, VerdictPoor, VerdictPoor},
		{VerdictPoor, VerdictGood, VerdictPoor},
		{VerdictPoor, VerdictBad, VerdictBad},
		{VerdictBad, VerdictPoor, VerdictBad},
		{VerdictBad, VerdictBad, VerdictBad},
	}
	for _, c := range cases {
		if got := worseOf(c.a, c.b); got != c.want {
			t.Errorf("worseOf(%q,%q) = %q, want %q", c.a, c.b, got, c.want)
		}
	}
}

func TestEvaluateStream_NilAndEmpty(t *testing.T) {
	t.Parallel()

	got := EvaluateStream(nil, false)
	if got.Verdict != VerdictGood || len(got.Reasons) != 0 {
		t.Errorf("nil stream should be Good/[], got %+v", got)
	}

	got = EvaluateStream(mediaStreamInit{}.build(), false)
	if got.Verdict != VerdictGood || len(got.Reasons) != 0 {
		t.Errorf("empty stream should be Good/[], got %+v", got)
	}
}

func TestEvaluateStream_ReasonFormats(t *testing.T) {
	t.Parallel()

	frac := func(v float64) *float64 { return &v }

	// Loss 6.5% -> Poor.
	got := EvaluateStream(mediaStreamInit{loss: frac(0.065)}.build(), false)
	if got.Verdict != VerdictPoor {
		t.Errorf("loss 6.5%%: verdict=%q want Poor", got.Verdict)
	}
	if !reflect.DeepEqual(got.Reasons, []string{"loss=6.5%"}) {
		t.Errorf("loss 6.5%%: reasons=%v want [loss=6.5%%]", got.Reasons)
	}

	// Jitter 45ms -> Poor (PT0.045S -> 45ms).
	got = EvaluateStream(mediaStreamInit{jitter: "PT0.045S"}.build(), false)
	if got.Verdict != VerdictPoor || !reflect.DeepEqual(got.Reasons, []string{"jitter=45ms"}) {
		t.Errorf("jitter 45ms: got %+v", got)
	}

	// RTT 620ms -> Poor.
	got = EvaluateStream(mediaStreamInit{rtt: "PT0.620S"}.build(), false)
	if got.Verdict != VerdictPoor || !reflect.DeepEqual(got.Reasons, []string{"rtt=620ms"}) {
		t.Errorf("rtt 620ms: got %+v", got)
	}

	// MOS 1.2 -> Poor; audio only.
	got = EvaluateStream(mediaStreamInit{mos: frac(1.2)}.build(), false)
	if got.Verdict != VerdictPoor || !reflect.DeepEqual(got.Reasons, []string{"mosDeg=1.2"}) {
		t.Errorf("mos 1.2: got %+v", got)
	}

	// Concealed 12.3% -> Poor.
	got = EvaluateStream(mediaStreamInit{concealed: frac(0.123)}.build(), false)
	if got.Verdict != VerdictPoor || !reflect.DeepEqual(got.Reasons, []string{"concealed=12.3%"}) {
		t.Errorf("concealed 12.3%%: got %+v", got)
	}
}

func TestEvaluateStream_Combinations(t *testing.T) {
	t.Parallel()
	frac := func(v float64) *float64 { return &v }

	// Poor loss + Bad jitter → overall Bad; reasons in canonical order
	// (loss before jitter, matching PS check sequence).
	got := EvaluateStream(mediaStreamInit{
		loss:   frac(0.06),
		jitter: "PT0.055S",
	}.build(), false)
	if got.Verdict != VerdictBad {
		t.Errorf("combo: verdict=%q want Bad", got.Verdict)
	}
	want := []string{"loss=6%", "jitter=55ms"}
	if !reflect.DeepEqual(got.Reasons, want) {
		t.Errorf("combo: reasons=%v want %v", got.Reasons, want)
	}
}

func TestEvaluateStream_VideoIgnoresAudioChecks(t *testing.T) {
	t.Parallel()
	frac := func(v float64) *float64 { return &v }

	got := EvaluateStream(mediaStreamInit{
		mos:       frac(3.0),
		concealed: frac(0.9),
	}.build(), true)
	if got.Verdict != VerdictGood || len(got.Reasons) != 0 {
		t.Errorf("video stream with bad audio metrics should be Good, got %+v", got)
	}
}

func TestEvaluateStream_MetricsPopulated(t *testing.T) {
	t.Parallel()
	frac := func(v float64) *float64 { return &v }

	got := EvaluateStream(mediaStreamInit{
		jitter:    "PT0.016S",
		rtt:       "PT0.100S",
		loss:      frac(0.01),
		mos:       frac(0.5),
		concealed: frac(0.02),
		packets:   1234,
	}.build(), false)

	if got.Verdict != VerdictGood {
		t.Errorf("clean stream should be Good, got %q", got.Verdict)
	}
	if got.Metrics.AvgJitterMs == nil || *got.Metrics.AvgJitterMs != 16 {
		t.Errorf("jitter metric = %v, want 16", got.Metrics.AvgJitterMs)
	}
	if got.Metrics.AvgRttMs == nil || *got.Metrics.AvgRttMs != 100 {
		t.Errorf("rtt metric = %v, want 100", got.Metrics.AvgRttMs)
	}
	if got.Metrics.PacketCount != 1234 {
		t.Errorf("packet count = %d, want 1234", got.Metrics.PacketCount)
	}
}

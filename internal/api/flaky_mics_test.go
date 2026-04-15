package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"teams_con/internal/store"
)

// ptrF makes float pointer literals a one-liner for test rows.
func ptrF(f float64) *float64 { return &f }

// fixedStart is the canonical call start time used across tests so the
// enrichment path (StartedAt from CallMeta) can be asserted without fiddly
// clock handling.
var fixedStart = time.Date(2026, 4, 10, 9, 0, 0, 0, time.UTC)

// streamOf is a compact test helper for a send-audio StreamRow with a
// concealed percentage. All other fields get sensible defaults so tests
// can focus on the fields the heuristic actually reads.
func streamOf(user, device, callID string, concealed float64) store.StreamRow {
	return store.StreamRow{
		CallId:        callID,
		User:          user,
		Direction:     "send",
		Verdict:       "Poor",
		StreamLabel:   "audio/send",
		CaptureDevice: device,
		ConcealedPct:  ptrF(concealed),
		SegmentStart:  fixedStart,
	}
}

func TestFlakyMics_Grouping_MinIncidents(t *testing.T) {
	metaByID := map[string]store.CallMeta{
		"c1": {CallId: "c1", StartTimeUtc: fixedStart, Verdict: "Bad"},
		"c2": {CallId: "c2", StartTimeUtc: fixedStart, Verdict: "Bad"},
		"c3": {CallId: "c3", StartTimeUtc: fixedStart, Verdict: "Poor"},
	}
	rows := []store.StreamRow{
		streamOf("alice@corp.com", "HeadsetPro", "c1", 12.0),
		streamOf("alice@corp.com", "HeadsetPro", "c2", 8.0),
		streamOf("alice@corp.com", "HeadsetPro", "c3", 6.0),
		// Below threshold of 3 incidents:
		streamOf("bob@corp.com", "BuiltIn", "c1", 7.0),
		streamOf("bob@corp.com", "BuiltIn", "c2", 9.0),
	}

	out := groupFlakyMics(rows, metaByID, 3, 20)
	if len(out) != 1 {
		t.Fatalf("want 1 surfaced device, got %d", len(out))
	}
	if out[0].User != "alice@corp.com" || out[0].CaptureDevice != "HeadsetPro" {
		t.Errorf("unexpected top device: %+v", out[0])
	}
	if out[0].Incidents != 3 || out[0].DistinctCalls != 3 {
		t.Errorf("incidents=%d distinct=%d", out[0].Incidents, out[0].DistinctCalls)
	}
}

func TestFlakyMics_Severity_BadOnWorst(t *testing.T) {
	rows := []store.StreamRow{
		streamOf("u", "D", "c1", 16.0), // worst exceeds 15 → Bad
		streamOf("u", "D", "c2", 5.0),
		streamOf("u", "D", "c3", 5.0),
	}
	out := groupFlakyMics(rows, nil, 3, 20)
	if len(out) != 1 || out[0].Severity != verdictBad {
		t.Errorf("want Bad severity, got %+v", out)
	}
}

func TestFlakyMics_Severity_BadOnAvg(t *testing.T) {
	rows := []store.StreamRow{
		streamOf("u", "D", "c1", 11.0),
		streamOf("u", "D", "c2", 10.0), // avg=10 → Bad
		streamOf("u", "D", "c3", 9.0),
	}
	out := groupFlakyMics(rows, nil, 3, 20)
	if len(out) != 1 || out[0].Severity != verdictBad {
		t.Errorf("want Bad severity, got %+v", out)
	}
}

func TestFlakyMics_Severity_PoorBelowBothThresholds(t *testing.T) {
	rows := []store.StreamRow{
		streamOf("u", "D", "c1", 8.0),
		streamOf("u", "D", "c2", 8.0),
		streamOf("u", "D", "c3", 8.0),
	}
	out := groupFlakyMics(rows, nil, 3, 20)
	if len(out) != 1 || out[0].Severity != verdictPoor {
		t.Errorf("want Poor severity, got %+v", out)
	}
}

func TestFlakyMics_NonAudioDropped(t *testing.T) {
	// streamLabel without "audio" substring must be ignored entirely.
	rows := []store.StreamRow{
		{
			CallId: "c1", User: "u", Direction: "send", CaptureDevice: "D",
			StreamLabel: "video/send", ConcealedPct: ptrF(50.0),
		},
		{
			CallId: "c2", User: "u", Direction: "send", CaptureDevice: "D",
			StreamLabel: "video/send", ConcealedPct: ptrF(50.0),
		},
		{
			CallId: "c3", User: "u", Direction: "send", CaptureDevice: "D",
			StreamLabel: "video/send", ConcealedPct: ptrF(50.0),
		},
	}
	out := groupFlakyMics(rows, nil, 3, 20)
	if len(out) != 0 {
		t.Errorf("video streams must not surface, got %+v", out)
	}
}

func TestFlakyMics_UnknownUserDropped(t *testing.T) {
	rows := []store.StreamRow{
		streamOf("<unknown>", "D", "c1", 20.0),
		streamOf("<server>", "D", "c2", 20.0),
		streamOf("", "D", "c3", 20.0),
	}
	out := groupFlakyMics(rows, nil, 1, 20)
	if len(out) != 0 {
		t.Errorf("placeholder users must be dropped, got %+v", out)
	}
}

func TestFlakyMics_EmptyCaptureDeviceDropped(t *testing.T) {
	rows := []store.StreamRow{
		streamOf("u", "", "c1", 20.0),
		streamOf("u", "", "c2", 20.0),
		streamOf("u", "", "c3", 20.0),
	}
	out := groupFlakyMics(rows, nil, 1, 20)
	if len(out) != 0 {
		t.Errorf("empty device must be dropped, got %+v", out)
	}
}

func TestFlakyMics_SamplesTruncatedToFive(t *testing.T) {
	var rows []store.StreamRow
	for i := 0; i < 8; i++ {
		rows = append(rows, streamOf("u", "D", "c1", float64(i+1)))
	}
	out := groupFlakyMics(rows, nil, 3, 20)
	if len(out) != 1 {
		t.Fatalf("want 1 device, got %d", len(out))
	}
	if len(out[0].Samples) != flakySamplesPerDevice {
		t.Errorf("samples len = %d, want %d", len(out[0].Samples), flakySamplesPerDevice)
	}
	// Samples must be sorted desc by ConcealedPct.
	for i := 1; i < len(out[0].Samples); i++ {
		if out[0].Samples[i-1].ConcealedPct < out[0].Samples[i].ConcealedPct {
			t.Errorf("samples not sorted desc: %+v", out[0].Samples)
		}
	}
	if out[0].Incidents != 8 {
		t.Errorf("incidents counter should reflect full set, got %d", out[0].Incidents)
	}
}

func TestFlakyMics_TwoDevicesSameUser(t *testing.T) {
	rows := []store.StreamRow{
		streamOf("u", "HeadsetPro", "c1", 5.0),
		streamOf("u", "HeadsetPro", "c2", 5.0),
		streamOf("u", "HeadsetPro", "c3", 5.0),
		streamOf("u", "BuiltIn", "c4", 5.0),
		streamOf("u", "BuiltIn", "c5", 5.0),
		streamOf("u", "BuiltIn", "c6", 5.0),
	}
	out := groupFlakyMics(rows, nil, 3, 20)
	if len(out) != 2 {
		t.Fatalf("want 2 devices, got %d", len(out))
	}
}

func TestFlakyMics_LimitClamping(t *testing.T) {
	var rows []store.StreamRow
	// Build 5 devices, all past min_incidents.
	for i := 0; i < 5; i++ {
		user := string(rune('a' + i))
		for j := 0; j < 3; j++ {
			rows = append(rows, streamOf(user+"@x", "D", user+string(rune('0'+j)), 5.0))
		}
	}
	out := groupFlakyMics(rows, nil, 3, 2)
	if len(out) != 2 {
		t.Errorf("limit not applied: %d", len(out))
	}
}

func TestFlakyMics_MetaEnrichmentForStartedAt(t *testing.T) {
	callStart := time.Date(2026, 4, 11, 15, 30, 0, 0, time.UTC)
	metaByID := map[string]store.CallMeta{
		"c1": {CallId: "c1", StartTimeUtc: callStart, Verdict: "Bad"},
	}
	// Row SegmentStart differs from call StartTime — enrichment must win.
	row := streamOf("u", "D", "c1", 10.0)
	row.SegmentStart = fixedStart // different from callStart
	rows := []store.StreamRow{row, row, row}

	out := groupFlakyMics(rows, metaByID, 3, 20)
	if len(out) != 1 {
		t.Fatalf("want 1 device, got %d", len(out))
	}
	if !out[0].Samples[0].StartedAt.Equal(callStart) {
		t.Errorf("StartedAt = %v, want %v", out[0].Samples[0].StartedAt, callStart)
	}
	if out[0].Samples[0].Verdict != "Bad" {
		t.Errorf("Verdict = %q, want Bad", out[0].Samples[0].Verdict)
	}
}

func TestFindFlakyMicrophones_EmptyWindow(t *testing.T) {
	calls := &fakeCalls{metaResult: nil}
	svc := newTestService(calls, nil, nil, nil, nil)
	out, err := svc.FindFlakyMicrophones(context.Background(), FindFlakyMicParams{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if out == nil {
		t.Fatal("want non-nil empty slice")
	}
	if len(out) != 0 {
		t.Errorf("want empty result, got %d", len(out))
	}
}

func TestFindFlakyMicrophones_FromAfterToIsBadRequest(t *testing.T) {
	svc := newTestService(nil, nil, nil, nil, nil)
	to := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	from := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)
	_, err := svc.FindFlakyMicrophones(context.Background(), FindFlakyMicParams{
		From: &from,
		To:   &to,
	})
	if !errors.Is(err, ErrBadRequest) {
		t.Errorf("want ErrBadRequest, got %v", err)
	}
}

func TestFindFlakyMicrophones_EndToEnd(t *testing.T) {
	calls := &fakeCalls{
		metaResult: []store.CallMeta{
			{CallId: "c1", StartTimeUtc: fixedStart, Verdict: "Bad"},
			{CallId: "c2", StartTimeUtc: fixedStart, Verdict: "Poor"},
			{CallId: "c3", StartTimeUtc: fixedStart, Verdict: "Bad"},
		},
	}
	streams := &fakeStreams{
		flakyRows: []store.StreamRow{
			streamOf("alice@corp.com", "HeadsetPro", "c1", 18.0),
			streamOf("alice@corp.com", "HeadsetPro", "c2", 12.0),
			streamOf("alice@corp.com", "HeadsetPro", "c3", 9.0),
		},
	}
	svc := newTestService(calls, streams, nil, nil, nil)

	out, err := svc.FindFlakyMicrophones(context.Background(), FindFlakyMicParams{
		MinConcealedPct: 5,
		MinIncidents:    3,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(out) != 1 || out[0].Severity != verdictBad {
		t.Fatalf("want 1 Bad finding, got %+v", out)
	}
	if out[0].Incidents != 3 || out[0].DistinctCalls != 3 {
		t.Errorf("unexpected counts: %+v", out[0])
	}
	// WorstConcealed must be rounded to 1 decimal.
	if out[0].WorstConcealedPct != 18.0 {
		t.Errorf("worst = %v, want 18.0", out[0].WorstConcealedPct)
	}
}

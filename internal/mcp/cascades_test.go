package mcp

import (
	"testing"
	"time"

	"teams_con/internal/store"
)

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func f(x float64) *float64 { return &x }

// TestFindCascades_Basic builds a canonical cascade:
// alice has a Bad send-data stream in a window, and three other users
// have Bad recv-data streams in overlapping windows. Expect one cascade
// with alice as the suspected sender and three affected users.
func TestFindCascades_Basic(t *testing.T) {
	streams := []store.StreamRow{
		// Alice — bad upload data (the suspected source).
		{
			User: "alice@x.com", Direction: "send", Verdict: "Bad", StreamLabel: "data",
			AvgJitterMs: f(120), AvgLossPct: f(0.5),
			SegmentStart: ts("2026-04-13T10:00:00Z"), SegmentEnd: ts("2026-04-13T10:05:00Z"),
		},
		// Bob — bad download data, overlaps alice's window.
		{
			User: "bob@x.com", Direction: "recv", Verdict: "Bad", StreamLabel: "data",
			AvgJitterMs: f(118),
			SegmentStart: ts("2026-04-13T09:59:00Z"), SegmentEnd: ts("2026-04-13T10:06:00Z"),
		},
		// Carol — bad download data, overlaps.
		{
			User: "carol@x.com", Direction: "recv", Verdict: "Bad", StreamLabel: "data",
			AvgJitterMs: f(122),
			SegmentStart: ts("2026-04-13T10:01:00Z"), SegmentEnd: ts("2026-04-13T10:04:00Z"),
		},
		// Dave — poor download data, overlaps.
		{
			User: "dave@x.com", Direction: "recv", Verdict: "Poor", StreamLabel: "data",
			AvgJitterMs: f(95),
			SegmentStart: ts("2026-04-13T10:02:00Z"), SegmentEnd: ts("2026-04-13T10:07:00Z"),
		},
		// Eve — good stream, should not appear.
		{
			User: "eve@x.com", Direction: "recv", Verdict: "Good", StreamLabel: "data",
			AvgJitterMs: f(4),
			SegmentStart: ts("2026-04-13T10:00:00Z"), SegmentEnd: ts("2026-04-13T10:05:00Z"),
		},
	}
	cs := findCascades(streams, 3)
	if len(cs) != 1 {
		t.Fatalf("cascade count = %d, want 1", len(cs))
	}
	c := cs[0]
	if c.Sender != "alice@x.com" {
		t.Errorf("sender = %q, want alice@x.com", c.Sender)
	}
	if c.StreamLabel != "data" {
		t.Errorf("label = %q, want data", c.StreamLabel)
	}
	if c.AffectedCount != 3 {
		t.Errorf("affected count = %d, want 3", c.AffectedCount)
	}
}

// TestFindCascades_BelowThreshold — only two affected, threshold 3, no cascade.
func TestFindCascades_BelowThreshold(t *testing.T) {
	streams := []store.StreamRow{
		{User: "a@x.com", Direction: "send", Verdict: "Bad", StreamLabel: "audio",
			AvgJitterMs: f(100),
			SegmentStart: ts("2026-04-13T10:00:00Z"), SegmentEnd: ts("2026-04-13T10:05:00Z")},
		{User: "b@x.com", Direction: "recv", Verdict: "Bad", StreamLabel: "audio",
			AvgJitterMs: f(99),
			SegmentStart: ts("2026-04-13T10:00:00Z"), SegmentEnd: ts("2026-04-13T10:05:00Z")},
		{User: "c@x.com", Direction: "recv", Verdict: "Bad", StreamLabel: "audio",
			AvgJitterMs: f(101),
			SegmentStart: ts("2026-04-13T10:00:00Z"), SegmentEnd: ts("2026-04-13T10:05:00Z")},
	}
	if cs := findCascades(streams, 3); len(cs) != 0 {
		t.Errorf("expected 0 cascades with threshold 3, got %d", len(cs))
	}
}

// TestFindCascades_LabelMismatch — send is data, recv is audio. No match.
func TestFindCascades_LabelMismatch(t *testing.T) {
	streams := []store.StreamRow{
		{User: "a@x.com", Direction: "send", Verdict: "Bad", StreamLabel: "data",
			SegmentStart: ts("2026-04-13T10:00:00Z"), SegmentEnd: ts("2026-04-13T10:05:00Z")},
		{User: "b@x.com", Direction: "recv", Verdict: "Bad", StreamLabel: "audio",
			SegmentStart: ts("2026-04-13T10:00:00Z"), SegmentEnd: ts("2026-04-13T10:05:00Z")},
		{User: "c@x.com", Direction: "recv", Verdict: "Bad", StreamLabel: "audio",
			SegmentStart: ts("2026-04-13T10:00:00Z"), SegmentEnd: ts("2026-04-13T10:05:00Z")},
		{User: "d@x.com", Direction: "recv", Verdict: "Bad", StreamLabel: "audio",
			SegmentStart: ts("2026-04-13T10:00:00Z"), SegmentEnd: ts("2026-04-13T10:05:00Z")},
	}
	if cs := findCascades(streams, 3); len(cs) != 0 {
		t.Errorf("expected 0 cascades with label mismatch, got %d", len(cs))
	}
}

// TestFindCascades_SkipsServerLegs — <server/unknown> must not appear.
func TestFindCascades_SkipsServerLegs(t *testing.T) {
	streams := []store.StreamRow{
		{User: "<server/unknown>", Direction: "send", Verdict: "Bad", StreamLabel: "data",
			SegmentStart: ts("2026-04-13T10:00:00Z"), SegmentEnd: ts("2026-04-13T10:05:00Z")},
		{User: "b@x.com", Direction: "recv", Verdict: "Bad", StreamLabel: "data",
			SegmentStart: ts("2026-04-13T10:00:00Z"), SegmentEnd: ts("2026-04-13T10:05:00Z")},
		{User: "c@x.com", Direction: "recv", Verdict: "Bad", StreamLabel: "data",
			SegmentStart: ts("2026-04-13T10:00:00Z"), SegmentEnd: ts("2026-04-13T10:05:00Z")},
		{User: "d@x.com", Direction: "recv", Verdict: "Bad", StreamLabel: "data",
			SegmentStart: ts("2026-04-13T10:00:00Z"), SegmentEnd: ts("2026-04-13T10:05:00Z")},
	}
	if cs := findCascades(streams, 3); len(cs) != 0 {
		t.Errorf("expected 0 cascades (server-leg sender), got %d", len(cs))
	}
}

// TestFindCascades_DedupeAffected — one recv user has multiple overlapping
// streams; they should count once.
func TestFindCascades_DedupeAffected(t *testing.T) {
	streams := []store.StreamRow{
		{User: "a@x.com", Direction: "send", Verdict: "Bad", StreamLabel: "data",
			SegmentStart: ts("2026-04-13T10:00:00Z"), SegmentEnd: ts("2026-04-13T10:05:00Z")},
		// bob has TWO bad recv streams in the window (different segments).
		{User: "bob@x.com", Direction: "recv", Verdict: "Bad", StreamLabel: "data",
			AvgJitterMs: f(100),
			SegmentStart: ts("2026-04-13T10:00:00Z"), SegmentEnd: ts("2026-04-13T10:02:00Z")},
		{User: "bob@x.com", Direction: "recv", Verdict: "Bad", StreamLabel: "data",
			AvgJitterMs: f(110),
			SegmentStart: ts("2026-04-13T10:02:00Z"), SegmentEnd: ts("2026-04-13T10:05:00Z")},
		{User: "carol@x.com", Direction: "recv", Verdict: "Bad", StreamLabel: "data",
			SegmentStart: ts("2026-04-13T10:00:00Z"), SegmentEnd: ts("2026-04-13T10:05:00Z")},
	}
	// With 2 deduped affected users (bob counted once) and threshold 3,
	// expect no cascade.
	if cs := findCascades(streams, 3); len(cs) != 0 {
		t.Errorf("dedupe failed: expected 0 with threshold 3, got %d", len(cs))
	}
	// With threshold 2, expect exactly one cascade with two affected users.
	cs := findCascades(streams, 2)
	if len(cs) != 1 {
		t.Fatalf("cascade count = %d, want 1", len(cs))
	}
	if cs[0].AffectedCount != 2 {
		t.Errorf("affected = %d, want 2 (bob deduped)", cs[0].AffectedCount)
	}
}

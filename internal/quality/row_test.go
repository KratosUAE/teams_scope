package quality

import (
	"strings"
	"testing"
	"time"

	"teams_con/internal/graph"
)

// buildSyntheticCallRecord assembles a CallRecord with two sessions and three
// streams of mixed verdicts, for use by ToCallRow / ToStreamRows tests.
//
// Session A (alice@corp <-> server):
//   - audio callerToCallee stream: clean           -> Good
//   - audio callerToCallee stream: loss 8%         -> Poor (attributed to
//     alice as recv, since callee is server w/o UPN -> fallback to caller
//     recv direction in PS logic... wait: in callerToCallee, callee is the
//     receiver. Here the callee is the server (no UPN). Fallback: caller
//     gets direction=send. So alice[send]: loss=8%.)
//
// Session B (bob@corp <-> server):
//   - audio calleeToCaller stream: jitter 120ms    -> Bad
//     In calleeToCaller, caller is the receiver. Caller here is "bob"?
//     We'll make caller=server, callee=bob. Then in calleeToCaller the
//     caller is server (no UPN), fallback to callee with send. So
//     bob[send]: jitter=120ms.
//
// Worst-of-call = Bad (session B jitter).
func buildSyntheticCallRecord() *graph.CallRecord {
	frac := func(v float64) *float64 { return &v }
	start := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)
	end := start.Add(5 * time.Minute)

	return &graph.CallRecord{
		ID:            "call-123",
		Type:          "groupCall",
		StartDateTime: start,
		EndDateTime:   end,
		Modalities:    []string{"audio"},
		OrganizerV2: &graph.ParticipantV2{
			Identity: &graph.Identity{User: &graph.User{UserPrincipalName: "alice@corp.com"}},
		},
		ParticipantsV2: []graph.ParticipantV2{
			{Identity: &graph.Identity{User: &graph.User{UserPrincipalName: "alice@corp.com"}}},
			{Identity: &graph.Identity{User: &graph.User{UserPrincipalName: "bob@corp.com"}}},
			{Identity: &graph.Identity{User: &graph.User{UserPrincipalName: "alice@corp.com"}}}, // dup
		},
		Sessions: []graph.Session{
			{
				Caller: newUserEndpoint("alice@corp.com", "Windows"),
				Callee: newServerEndpoint(),
				Segments: []graph.Segment{{
					StartDateTime: start,
					EndDateTime:   end,
					Media: []graph.Media{{
						Label: "main-audio",
						CallerNetwork: &graph.NetworkInfo{
							ConnectionType: "wired",
							Subnet:         "10.0.0.0/24",
							LinkSpeed:      1_000_000_000,
						},
						CallerDevice: &graph.DeviceInfo{CaptureDeviceName: "Mic-A", RenderDeviceName: "Spk-A"},
						Streams: []graph.MediaStream{
							{
								StreamDirection:       "callerToCallee",
								AveragePacketLossRate: frac(0.001),
							},
							{
								StreamDirection:       "callerToCallee",
								AveragePacketLossRate: frac(0.08),
							},
						},
					}},
				}},
			},
			{
				Caller: newServerEndpoint(),
				Callee: newUserEndpoint("bob@corp.com", "Mac"),
				Segments: []graph.Segment{{
					StartDateTime: start,
					EndDateTime:   end,
					Media: []graph.Media{{
						Label: "main-audio",
						CalleeNetwork: &graph.NetworkInfo{
							ConnectionType: "wifi",
							Subnet:         "192.168.1.0/24",
						},
						CalleeDevice: &graph.DeviceInfo{CaptureDeviceName: "Mic-B"},
						Streams: []graph.MediaStream{{
							StreamDirection: "calleeToCaller",
							AverageJitter:   "PT0.120S",
						}},
					}},
				}},
			},
		},
	}
}

func TestToCallRow_WorstOfCall(t *testing.T) {
	t.Parallel()
	rec := buildSyntheticCallRecord()
	row := ToCallRow(rec, false)

	if row.CallId != "call-123" {
		t.Errorf("CallId = %q", row.CallId)
	}
	if row.Verdict != VerdictBad {
		t.Errorf("Verdict = %q, want Bad", row.Verdict)
	}
	if row.DurationSec != 300 {
		t.Errorf("DurationSec = %d, want 300", row.DurationSec)
	}
	if row.WorstUser != "bob@corp.com" {
		t.Errorf("WorstUser = %q, want bob@corp.com", row.WorstUser)
	}
	if row.WorstDirection != "send" {
		t.Errorf("WorstDirection = %q, want send", row.WorstDirection)
	}
	if !strings.Contains(row.WorstStream, "jitter=120ms") {
		t.Errorf("WorstStream = %q, want to contain jitter=120ms", row.WorstStream)
	}
	if row.WorstSubnet != "192.168.1.0/24" {
		t.Errorf("WorstSubnet = %q", row.WorstSubnet)
	}
	if row.WorstPlatform != "Mac" {
		t.Errorf("WorstPlatform = %q", row.WorstPlatform)
	}
	if row.WorstCaptureDevice != "Mic-B" {
		t.Errorf("WorstCaptureDevice = %q", row.WorstCaptureDevice)
	}
}

func TestToCallRow_ReasonsFormattedAndDeduped(t *testing.T) {
	t.Parallel()
	row := ToCallRow(buildSyntheticCallRecord(), false)

	// Expected: Poor loss on session A + Bad jitter on session B. Session A
	// has a clean stream (no reason). No duplicates because the strings
	// differ (different upn and different metric).
	want := map[string]bool{
		"alice@corp.com[send]: loss=8%":  false,
		"bob@corp.com[send]: jitter=120ms": false,
	}
	for _, r := range row.Reasons {
		if _, ok := want[r]; !ok {
			t.Errorf("unexpected reason: %q", r)
			continue
		}
		want[r] = true
	}
	for k, seen := range want {
		if !seen {
			t.Errorf("missing reason: %q (got %v)", k, row.Reasons)
		}
	}
}

func TestToCallRow_ParticipantsDeduped(t *testing.T) {
	t.Parallel()
	row := ToCallRow(buildSyntheticCallRecord(), false)
	if len(row.Participants) != 2 {
		t.Errorf("ParticipantCount via slice len = %d, want 2 (%v)", len(row.Participants), row.Participants)
	}
	if row.ParticipantCount != 2 {
		t.Errorf("ParticipantCount = %d, want 2", row.ParticipantCount)
	}
	if row.Organizer != "alice@corp.com" {
		t.Errorf("Organizer = %q", row.Organizer)
	}
}

func TestToCallRow_WorstBeatsPoor(t *testing.T) {
	t.Parallel()
	// Sanity: if we flip session B's jitter to a Good value, the overall
	// verdict must collapse to Poor (from session A's loss=8%) and the
	// worst snapshot must point at alice.
	rec := buildSyntheticCallRecord()
	rec.Sessions[1].Segments[0].Media[0].Streams[0].AverageJitter = "PT0.010S"

	row := ToCallRow(rec, false)
	if row.Verdict != VerdictPoor {
		t.Errorf("Verdict = %q, want Poor", row.Verdict)
	}
	if row.WorstUser != "alice@corp.com" {
		t.Errorf("WorstUser = %q, want alice@corp.com", row.WorstUser)
	}
}

func TestToStreamRows_Shape(t *testing.T) {
	t.Parallel()
	rows := ToStreamRows(buildSyntheticCallRecord(), false)
	if len(rows) != 3 {
		t.Fatalf("len(rows) = %d, want 3", len(rows))
	}
	for _, r := range rows {
		if r.CallId != "call-123" {
			t.Errorf("CallId = %q", r.CallId)
		}
	}

	// Session A stream 1: clean -> Good, direction=upload (caller->callee,
	// callee is server, fallback to caller send vocab=upload).
	s0 := rows[0]
	if s0.Verdict != VerdictGood || s0.User != "alice@corp.com" || s0.Direction != "upload" {
		t.Errorf("stream[0] = %+v", s0)
	}

	// Session A stream 2: loss 8% -> Poor, AvgLossPct = 8.00 (rounded to 2).
	s1 := rows[1]
	if s1.Verdict != VerdictPoor {
		t.Errorf("stream[1] verdict=%q", s1.Verdict)
	}
	if s1.AvgLossPct == nil || *s1.AvgLossPct != 8 {
		t.Errorf("stream[1] AvgLossPct = %v, want 8", s1.AvgLossPct)
	}
	if s1.LinkMbps == nil || *s1.LinkMbps != 1000 {
		t.Errorf("stream[1] LinkMbps = %v, want 1000", s1.LinkMbps)
	}

	// Session B: jitter 120ms -> Bad, direction=upload, user=bob.
	s2 := rows[2]
	if s2.Verdict != VerdictBad || s2.User != "bob@corp.com" || s2.Direction != "upload" {
		t.Errorf("stream[2] = %+v", s2)
	}
	if s2.AvgJitterMs == nil || *s2.AvgJitterMs != 120 {
		t.Errorf("stream[2] jitter = %v", s2.AvgJitterMs)
	}
	if s2.Issues != "jitter=120ms" {
		t.Errorf("stream[2] Issues = %q", s2.Issues)
	}
}

func TestToCallRow_NilSafe(t *testing.T) {
	t.Parallel()
	row := ToCallRow(nil, false)
	if row.Verdict != VerdictGood {
		t.Errorf("nil rec: verdict = %q", row.Verdict)
	}
	rows := ToStreamRows(nil, false)
	if len(rows) != 0 {
		t.Errorf("nil rec: stream rows = %v", rows)
	}
}

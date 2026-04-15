package api

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"teams_con/internal/store"
)

func streamRow(callID, user, direction, subnet, connType, device, platform, ua string) store.StreamRow {
	return store.StreamRow{
		CallId:        callID,
		User:          user,
		Direction:     direction,
		StreamLabel:   "audio/send",
		Subnet:        subnet,
		ConnType:      connType,
		CaptureDevice: device,
		Platform:      platform,
		UserAgent:     ua,
	}
}

func TestBuildUserHealthReport_EmptyUpnRejected(t *testing.T) {
	svc := newTestService(nil, nil, nil, nil, nil)
	_, err := svc.BuildUserHealthReport(context.Background(), UserHealthParams{})
	if !errors.Is(err, ErrBadRequest) {
		t.Fatalf("want ErrBadRequest, got %v", err)
	}
}

func TestBuildUserHealthReport_InvertedWindow(t *testing.T) {
	svc := newTestService(nil, nil, nil, nil, nil)
	to := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	from := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)
	_, err := svc.BuildUserHealthReport(context.Background(), UserHealthParams{
		Upn: "alice", From: &from, To: &to,
	})
	if !errors.Is(err, ErrBadRequest) {
		t.Fatalf("want ErrBadRequest, got %v", err)
	}
}

func TestBuildUserHealthReport_NoCalls_InsufficientData(t *testing.T) {
	svc := newTestService(&fakeCalls{}, nil, nil, nil, nil)
	r, err := svc.BuildUserHealthReport(context.Background(), UserHealthParams{Upn: "alice"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if r.TotalCalls != 0 || r.Pattern != PatternInsufficientData {
		t.Errorf("got %+v", r)
	}
}

func TestBuildUserHealthReport_HealthyPattern(t *testing.T) {
	// 6 calls, 1 poor → 16.7% bad ratio, below 20% threshold.
	calls := &fakeCalls{listResult: []store.Call{
		{CallId: "c1", Verdict: verdictGood, Participants: []string{"alice"}},
		{CallId: "c2", Verdict: verdictGood, Participants: []string{"alice"}},
		{CallId: "c3", Verdict: verdictGood, Participants: []string{"alice"}},
		{CallId: "c4", Verdict: verdictGood, Participants: []string{"alice"}},
		{CallId: "c5", Verdict: verdictGood, Participants: []string{"alice"}},
		{CallId: "c6", Verdict: verdictPoor, Participants: []string{"alice"}},
	}}
	streams := &fakeStreams{}
	svc := newTestService(calls, streams, nil, nil, nil)
	r, err := svc.BuildUserHealthReport(context.Background(), UserHealthParams{Upn: "alice"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if r.Pattern != PatternHealthy {
		t.Errorf("pattern = %q, want healthy", r.Pattern)
	}
	if r.ByVerdict.Good != 5 || r.ByVerdict.Poor != 1 {
		t.Errorf("verdict counts = %+v", r.ByVerdict)
	}
}

func TestBuildUserHealthReport_ChronicMicNotTriggeredByHealthyHeavyUse(t *testing.T) {
	// Regression guard: 20 Good + 3 Bad calls, all on the same device,
	// must NOT classify as chronic_mic. The share is computed against
	// BadCallCount, not CallCount.
	var cs []store.Call
	for i := 0; i < 20; i++ {
		cs = append(cs, store.Call{
			CallId: fmt.Sprintf("g%d", i), Verdict: verdictGood,
			Participants: []string{"alice"},
		})
	}
	for i := 0; i < 3; i++ {
		cs = append(cs, store.Call{
			CallId: fmt.Sprintf("b%d", i), Verdict: verdictBad,
			Participants: []string{"alice"},
		})
	}
	pct := func(v float64) *float64 { return &v }
	// Device HeadsetPro appears on every call; concealed is high on bad
	// calls but those bad calls are a small fraction of total use. The
	// high AvgConcealedPct (≥10) used to be enough to trigger chronic_mic
	// under the old buggy share check; it must not now.
	var rows []store.StreamRow
	for i := 0; i < 20; i++ {
		r := streamRow(fmt.Sprintf("g%d", i), "alice", "send",
			"10.0.0.0/24", "wired", "HeadsetPro", "Windows", "Teams/1.6")
		r.ConcealedPct = pct(1.0)
		rows = append(rows, r)
	}
	// On 3 Bad calls: concealed elevated but UNRELATED to device — could
	// be anything. Here we set 20% to stress the avg.
	for i := 0; i < 3; i++ {
		r := streamRow(fmt.Sprintf("b%d", i), "alice", "send",
			"10.0.0.0/24", "wired", "HeadsetPro", "Windows", "Teams/1.6")
		r.ConcealedPct = pct(20.0)
		rows = append(rows, r)
	}
	svc := newTestService(&fakeCalls{listResult: cs}, &fakeStreams{userRows: rows}, nil, nil, nil)
	r, err := svc.BuildUserHealthReport(context.Background(), UserHealthParams{Upn: "alice"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if r.Pattern == PatternChronicMic {
		t.Errorf("device used on 20 good + 3 bad calls must not classify as chronic_mic, got %+v", r)
	}
	// Sanity: the device report still reflects real usage.
	if len(r.Devices) != 1 || r.Devices[0].CallCount != 23 || r.Devices[0].BadCallCount != 3 {
		t.Errorf("device counts wrong: %+v", r.Devices)
	}
}

func TestBuildUserHealthReport_ChronicMicPattern(t *testing.T) {
	// 4 calls, 3 Bad/Poor — all on HeadsetPro with high concealed.
	calls := &fakeCalls{listResult: []store.Call{
		{CallId: "c1", Verdict: verdictBad, Participants: []string{"alice"}},
		{CallId: "c2", Verdict: verdictBad, Participants: []string{"alice"}},
		{CallId: "c3", Verdict: verdictPoor, Participants: []string{"alice"}},
		{CallId: "c4", Verdict: verdictGood, Participants: []string{"alice"}},
	}}
	pct := func(v float64) *float64 { return &v }
	mkRow := func(cid string, c float64) store.StreamRow {
		r := streamRow(cid, "alice", "send", "10.0.0.0/24", "wired", "HeadsetPro", "Windows", "Teams/1.6")
		r.ConcealedPct = pct(c)
		return r
	}
	streams := &fakeStreams{userRows: []store.StreamRow{
		mkRow("c1", 18.0),
		mkRow("c2", 15.0),
		mkRow("c3", 12.0),
		mkRow("c4", 1.0),
	}}
	svc := newTestService(calls, streams, nil, nil, nil)
	r, err := svc.BuildUserHealthReport(context.Background(), UserHealthParams{Upn: "alice"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if r.Pattern != PatternChronicMic {
		t.Errorf("pattern = %q, want chronic_mic", r.Pattern)
	}
	if len(r.Devices) != 1 || r.Devices[0].CallCount != 4 {
		t.Errorf("devices = %+v", r.Devices)
	}
}

func TestBuildUserHealthReport_WifiOnlyIssuePattern(t *testing.T) {
	// 4 wifi calls, 3 Bad. 3 wired calls, all Good. No mic issues.
	calls := &fakeCalls{listResult: []store.Call{
		{CallId: "w1", Verdict: verdictBad, Participants: []string{"alice"}},
		{CallId: "w2", Verdict: verdictBad, Participants: []string{"alice"}},
		{CallId: "w3", Verdict: verdictBad, Participants: []string{"alice"}},
		{CallId: "w4", Verdict: verdictGood, Participants: []string{"alice"}},
		{CallId: "e1", Verdict: verdictGood, Participants: []string{"alice"}},
		{CallId: "e2", Verdict: verdictGood, Participants: []string{"alice"}},
		{CallId: "e3", Verdict: verdictGood, Participants: []string{"alice"}},
	}}
	rows := []store.StreamRow{
		streamRow("w1", "alice", "send", "10.0.0.0/24", "wifi", "", "Windows", "Teams/1.6"),
		streamRow("w2", "alice", "send", "10.0.0.0/24", "wifi", "", "Windows", "Teams/1.6"),
		streamRow("w3", "alice", "send", "10.0.0.0/24", "wifi", "", "Windows", "Teams/1.6"),
		streamRow("w4", "alice", "send", "10.0.0.0/24", "wifi", "", "Windows", "Teams/1.6"),
		streamRow("e1", "alice", "send", "10.0.1.0/24", "wired", "", "Windows", "Teams/1.6"),
		streamRow("e2", "alice", "send", "10.0.1.0/24", "wired", "", "Windows", "Teams/1.6"),
		streamRow("e3", "alice", "send", "10.0.1.0/24", "wired", "", "Windows", "Teams/1.6"),
	}
	streams := &fakeStreams{userRows: rows}
	svc := newTestService(calls, streams, nil, nil, nil)
	r, err := svc.BuildUserHealthReport(context.Background(), UserHealthParams{Upn: "alice"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if r.Pattern != PatternWifiOnlyIssue {
		t.Errorf("pattern = %q, want wifi_only_issue", r.Pattern)
	}
}

func TestBuildUserHealthReport_RemotePathFromJitter(t *testing.T) {
	// Regression: 56ms recv jitter on bad calls with normal RTT should
	// still be flagged as remote_path (formerly missed because the
	// classifier only looked at RTT).
	calls := &fakeCalls{listResult: []store.Call{
		{CallId: "c1", Verdict: verdictBad, Participants: []string{"alice"}},
		{CallId: "c2", Verdict: verdictBad, Participants: []string{"alice"}},
		{CallId: "c3", Verdict: verdictPoor, Participants: []string{"alice"}},
	}}
	ptr := func(v float64) *float64 { return &v }
	mkRow := func(cid string, jit float64) store.StreamRow {
		row := streamRow(cid, "alice", "recv", "10.0.0.0/24", "wired", "", "Windows", "Teams/1.6")
		row.AvgJitterMs = ptr(jit)
		row.AvgRttMs = ptr(90.0) // below RTT threshold
		return row
	}
	streams := &fakeStreams{userRows: []store.StreamRow{
		mkRow("c1", 58.0),
		mkRow("c2", 55.0),
		mkRow("c3", 52.0),
	}}
	svc := newTestService(calls, streams, nil, nil, nil)
	r, err := svc.BuildUserHealthReport(context.Background(), UserHealthParams{Upn: "alice"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if r.Pattern != PatternRemotePath {
		t.Errorf("pattern = %q, want remote_path (via jitter)", r.Pattern)
	}
}

func TestBuildUserHealthReport_WifiSuspectedPattern(t *testing.T) {
	// 100% wifi, no wired baseline, 5/5 Bad — should classify as
	// wifi_suspected (cannot be wifi_only_issue without a wired baseline).
	var cs []store.Call
	for i := 0; i < 5; i++ {
		cs = append(cs, store.Call{
			CallId: fmt.Sprintf("c%d", i), Verdict: verdictBad,
			Participants: []string{"bob"},
		})
	}
	var rows []store.StreamRow
	for i := 0; i < 5; i++ {
		rows = append(rows, streamRow(fmt.Sprintf("c%d", i),
			"bob", "send", "192.168.1.0/24", "wifi", "", "macOS", "Teams/1.6"))
	}
	svc := newTestService(&fakeCalls{listResult: cs},
		&fakeStreams{userRows: rows}, nil, nil, nil)
	r, err := svc.BuildUserHealthReport(context.Background(), UserHealthParams{Upn: "bob"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if r.Pattern != PatternWifiSuspected {
		t.Errorf("pattern = %q, want wifi_suspected", r.Pattern)
	}
}

func TestBuildUserHealthReport_RemotePathPattern(t *testing.T) {
	calls := &fakeCalls{listResult: []store.Call{
		{CallId: "c1", Verdict: verdictPoor, Participants: []string{"alice"}},
		{CallId: "c2", Verdict: verdictPoor, Participants: []string{"alice"}},
		{CallId: "c3", Verdict: verdictPoor, Participants: []string{"alice"}},
	}}
	rtt := func(v float64) *float64 { return &v }
	mkRow := func(cid string, r float64) store.StreamRow {
		row := streamRow(cid, "alice", "recv", "10.0.0.0/24", "wired", "", "Windows", "Teams/1.6")
		row.AvgRttMs = rtt(r)
		return row
	}
	streams := &fakeStreams{userRows: []store.StreamRow{
		mkRow("c1", 180.0),
		mkRow("c2", 170.0),
		mkRow("c3", 160.0),
	}}
	svc := newTestService(calls, streams, nil, nil, nil)
	r, err := svc.BuildUserHealthReport(context.Background(), UserHealthParams{Upn: "alice"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if r.Pattern != PatternRemotePath {
		t.Errorf("pattern = %q, want remote_path", r.Pattern)
	}
	if r.AvgMetrics.RttRecvMs == nil || *r.AvgMetrics.RttRecvMs < 150 {
		t.Errorf("avg rtt recv not surfacing: %+v", r.AvgMetrics.RttRecvMs)
	}
}

func TestBuildUserHealthReport_DistinctCallCounting(t *testing.T) {
	// Three streams for one call must yield CallCount=1, not 3.
	calls := &fakeCalls{listResult: []store.Call{
		{CallId: "c1", Verdict: verdictGood, Participants: []string{"alice"}},
	}}
	row := streamRow("c1", "alice", "send", "10.0.0.0/24", "wifi", "Built-In", "Windows", "Teams/1.6")
	streams := &fakeStreams{userRows: []store.StreamRow{row, row, row}}
	svc := newTestService(calls, streams, nil, nil, nil)
	r, err := svc.BuildUserHealthReport(context.Background(), UserHealthParams{Upn: "alice"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(r.Subnets) != 1 || r.Subnets[0].CallCount != 1 {
		t.Errorf("subnet count not distinct: %+v", r.Subnets)
	}
	if len(r.Devices) != 1 || r.Devices[0].CallCount != 1 {
		t.Errorf("device count not distinct: %+v", r.Devices)
	}
	if len(r.Clients) != 1 || r.Clients[0].CallCount != 1 {
		t.Errorf("client count not distinct: %+v", r.Clients)
	}
	if len(r.Platforms) != 1 || r.Platforms[0].CallCount != 1 {
		t.Errorf("platform count not distinct: %+v", r.Platforms)
	}
}

func TestBuildUserHealthReport_TopNTruncation(t *testing.T) {
	// 15 distinct subnets → top 10 retained.
	const n = 15
	var callRows []store.Call
	var streamRows []store.StreamRow
	for i := 0; i < n; i++ {
		cid := string(rune('a' + i))
		callRows = append(callRows, store.Call{
			CallId: cid, Verdict: verdictGood, Participants: []string{"alice"},
		})
		streamRows = append(streamRows,
			streamRow(cid, "alice", "send",
				"10.0."+string(rune('0'+i%10))+".0/24", "wifi", "", "Windows", "Teams/1.6"),
		)
	}
	svc := newTestService(&fakeCalls{listResult: callRows},
		&fakeStreams{userRows: streamRows}, nil, nil, nil)
	r, err := svc.BuildUserHealthReport(context.Background(), UserHealthParams{Upn: "alice"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(r.Subnets) > userHealthTopN {
		t.Errorf("subnets not truncated: %d > %d", len(r.Subnets), userHealthTopN)
	}
}

func TestBuildUserHealthReport_StreamsErrorPropagates(t *testing.T) {
	calls := &fakeCalls{listResult: []store.Call{
		{CallId: "c1", Verdict: verdictGood, Participants: []string{"alice"}},
	}}
	streams := &fakeStreams{userErr: errors.New("boom")}
	svc := newTestService(calls, streams, nil, nil, nil)
	_, err := svc.BuildUserHealthReport(context.Background(), UserHealthParams{Upn: "alice"})
	if err == nil {
		t.Fatal("want error, got nil")
	}
}

// Phase 1: SubnetUsage entries should be decorated with Name/Office/Kind
// when the resolver matches a configured subnet, and left empty otherwise.
func TestBuildUserHealthReport_SubnetEnrichment(t *testing.T) {
	calls := &fakeCalls{listResult: []store.Call{
		{CallId: "c1", Verdict: verdictGood, Participants: []string{"alice"}},
		{CallId: "c2", Verdict: verdictGood, Participants: []string{"alice"}},
		{CallId: "c3", Verdict: verdictGood, Participants: []string{"alice"}},
	}}
	rows := []store.StreamRow{
		// Two calls on a known office block — should resolve.
		streamRow("c1", "alice", "send", "10.16.1.5", "wired", "HeadsetPro", "Windows", "Teams/1.6"),
		streamRow("c2", "alice", "send", "10.16.1.6", "wired", "HeadsetPro", "Windows", "Teams/1.6"),
		// One call on an unknown subnet — should stay raw.
		streamRow("c3", "alice", "send", "203.0.113.4", "wifi", "HeadsetPro", "Windows", "Teams/1.6"),
	}
	subs := newFakeSubnets(store.SubnetEntry{
		Cidr:   "10.16.0.0/16",
		Name:   "Xpanceo Dubai HQ",
		Office: "Dubai",
		Kind:   "wired",
	})
	svc := newTestServiceFull(calls, &fakeStreams{userRows: rows}, nil, nil, subs, nil, nil)

	r, err := svc.BuildUserHealthReport(context.Background(), UserHealthParams{Upn: "alice"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}

	var matched, unmatched *SubnetUsage
	for i := range r.Subnets {
		switch r.Subnets[i].Subnet {
		case "10.16.1.5", "10.16.1.6":
			matched = &r.Subnets[i]
		case "203.0.113.4":
			unmatched = &r.Subnets[i]
		}
	}
	if matched == nil {
		t.Fatalf("expected a matched subnet entry, got %+v", r.Subnets)
	}
	if matched.Name != "Xpanceo Dubai HQ" || matched.Office != "Dubai" || matched.Kind != "wired" {
		t.Errorf("matched entry not enriched: %+v", matched)
	}
	if unmatched == nil {
		t.Fatalf("expected an unmatched subnet entry, got %+v", r.Subnets)
	}
	if unmatched.Name != "" || unmatched.Office != "" || unmatched.Kind != "" {
		t.Errorf("unmatched entry should stay bare, got %+v", unmatched)
	}
}

// Phase 2: when a user card exists, BuildUserHealthReport attaches it to
// report.Card. The classifier already ran so this is a pure decoration
// step — the pattern output must not change.
func TestBuildUserHealthReport_AttachesCardWhenPresent(t *testing.T) {
	calls := &fakeCalls{listResult: []store.Call{
		{CallId: "c1", Verdict: verdictGood, Participants: []string{"alice"}},
		{CallId: "c2", Verdict: verdictGood, Participants: []string{"alice"}},
		{CallId: "c3", Verdict: verdictGood, Participants: []string{"alice"}},
	}}
	rows := []store.StreamRow{
		streamRow("c1", "alice", "send", "10.0.0.0", "wired", "HeadsetPro", "Windows", "Teams/1.6"),
	}
	cards := newFakeUserCards(store.UserCard{
		Upn:      "alice",
		Location: "Dubai HQ",
		Tags:     []string{"vip"},
		Notes:    "escalated 2026-04-10",
	})
	svc := newTestServiceFull(calls, &fakeStreams{userRows: rows}, nil, nil, nil, cards, nil)

	r, err := svc.BuildUserHealthReport(context.Background(), UserHealthParams{Upn: "alice"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if r.Card == nil {
		t.Fatalf("want Card, got nil")
	}
	if r.Card.Location != "Dubai HQ" || len(r.Card.Tags) != 1 || r.Card.Tags[0] != "vip" {
		t.Errorf("card mismatch: %+v", r.Card)
	}
}

// Absent card is a normal state, not an error — report.Card stays nil
// and the rest of the report is unchanged.
func TestBuildUserHealthReport_CardAbsentIsNotError(t *testing.T) {
	calls := &fakeCalls{listResult: []store.Call{
		{CallId: "c1", Verdict: verdictGood, Participants: []string{"alice"}},
		{CallId: "c2", Verdict: verdictGood, Participants: []string{"alice"}},
		{CallId: "c3", Verdict: verdictGood, Participants: []string{"alice"}},
	}}
	rows := []store.StreamRow{
		streamRow("c1", "alice", "send", "10.0.0.0", "wired", "HeadsetPro", "Windows", "Teams/1.6"),
	}
	svc := newTestServiceFull(calls, &fakeStreams{userRows: rows}, nil, nil, nil, nil, nil)

	r, err := svc.BuildUserHealthReport(context.Background(), UserHealthParams{Upn: "alice"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if r.Card != nil {
		t.Errorf("want nil card, got %+v", r.Card)
	}
}

// A Mongo error on the card lookup must NOT fail the whole report. The
// report is still returned with Card=nil; the error path is log-only.
func TestBuildUserHealthReport_CardFetchErrorSoftFails(t *testing.T) {
	calls := &fakeCalls{listResult: []store.Call{
		{CallId: "c1", Verdict: verdictGood, Participants: []string{"alice"}},
		{CallId: "c2", Verdict: verdictGood, Participants: []string{"alice"}},
		{CallId: "c3", Verdict: verdictGood, Participants: []string{"alice"}},
	}}
	rows := []store.StreamRow{
		streamRow("c1", "alice", "send", "10.0.0.0", "wired", "HeadsetPro", "Windows", "Teams/1.6"),
	}
	cards := newFakeUserCards()
	cards.getErr = fmt.Errorf("mongo boom")
	svc := newTestServiceFull(calls, &fakeStreams{userRows: rows}, nil, nil, nil, cards, nil)

	r, err := svc.BuildUserHealthReport(context.Background(), UserHealthParams{Upn: "alice"})
	if err != nil {
		t.Fatalf("soft-fail violated: got %v", err)
	}
	if r == nil {
		t.Fatal("want non-nil report")
	}
	if r.Card != nil {
		t.Errorf("want nil card on error, got %+v", r.Card)
	}
	if r.TotalCalls != 3 {
		t.Errorf("totalCalls = %d, want 3", r.TotalCalls)
	}
}

// Phase 3: the peer-baseline integration into the classifier.

// orgWideFixture builds a Service where the target user has 10 calls, 5 of
// them bad (50% bad ratio), all on subnet 10.0.0.0, plus a 5-peer cohort
// that also sits at ~50% bad — i.e. the entire office is on fire.
func buildOrgWideFixture(targetBadRatio float64) *Service {
	total := 10
	targetBad := int(float64(total) * targetBadRatio)
	// Target calls.
	tCalls := make([]store.Call, 0, total)
	for i := 0; i < total; i++ {
		v := verdictGood
		if i < targetBad {
			v = verdictBad
		}
		tCalls = append(tCalls, store.Call{CallId: "tc" + strconv.Itoa(i), Verdict: v, Participants: []string{"alice"}})
	}
	// Cohort: 5 peers × 10 calls, 50% bad. Same subnet as target.
	cohortMetas, cohortRows := makeCohort("p", 5, 10, 5, "10.0.0.0")
	// Target streams so rawSubnetSet is non-empty.
	tRows := make([]store.StreamRow, 0, total)
	for i := 0; i < total; i++ {
		tRows = append(tRows, store.StreamRow{
			CallId: "tc" + strconv.Itoa(i),
			User:   "alice",
			Subnet: "10.0.0.0",
		})
	}
	calls := &fakeCalls{
		listResult: tCalls,
		metaResult: cohortMetas,
	}
	streams := &fakeStreams{
		userRows: tRows,
		peerRows: cohortRows,
	}
	return newTestService(calls, streams, nil, nil, nil)
}

func TestBuildUserHealthReport_OrgWideIssuePattern(t *testing.T) {
	// target bad ratio 50% and cohort bad ratio 50% → delta 0 → on-par,
	// and 50% >= patternPeerHighBad (40%) → org_wide_issue wins.
	svc := buildOrgWideFixture(0.5)
	r, err := svc.BuildUserHealthReport(context.Background(), UserHealthParams{Upn: "alice"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if r.PeerBaseline == nil {
		t.Fatal("PeerBaseline must be attached")
	}
	if r.PeerBaseline.Assessment != PeerAssessmentOnPar {
		t.Errorf("assessment = %q, want on-par", r.PeerBaseline.Assessment)
	}
	if r.Pattern != PatternOrgWideIssue {
		t.Errorf("pattern = %q, want org_wide_issue", r.Pattern)
	}
}

func TestBuildUserHealthReport_OrgWideNotFiredBelowThreshold(t *testing.T) {
	// Target and cohort both at 25% bad — on-par BUT below
	// patternPeerHighBad (40%). Pattern must fall through to one of the
	// downstream labels (mixed is the default sink for this fixture
	// because there are no device/wifi/remote signals).
	total := 20
	targetBad := 5 // 25%
	tCalls := make([]store.Call, 0, total)
	for i := 0; i < total; i++ {
		v := verdictGood
		if i < targetBad {
			v = verdictBad
		}
		tCalls = append(tCalls, store.Call{CallId: "tc" + strconv.Itoa(i), Verdict: v, Participants: []string{"alice"}})
	}
	// Cohort 5 peers × 20 calls, 5 bad each = 25% bad.
	cohortMetas, cohortRows := makeCohort("p", 5, 20, 5, "10.0.0.0")
	tRows := make([]store.StreamRow, 0, total)
	for i := 0; i < total; i++ {
		tRows = append(tRows, store.StreamRow{CallId: "tc" + strconv.Itoa(i), User: "alice", Subnet: "10.0.0.0"})
	}
	calls := &fakeCalls{listResult: tCalls, metaResult: cohortMetas}
	streams := &fakeStreams{userRows: tRows, peerRows: cohortRows}
	svc := newTestService(calls, streams, nil, nil, nil)

	r, err := svc.BuildUserHealthReport(context.Background(), UserHealthParams{Upn: "alice"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if r.PeerBaseline == nil || r.PeerBaseline.Assessment != PeerAssessmentOnPar {
		t.Fatalf("precondition: want on-par baseline, got %+v", r.PeerBaseline)
	}
	if r.Pattern == PatternOrgWideIssue {
		t.Errorf("pattern = org_wide_issue, want fall-through (bad ratio below gate)")
	}
}

func TestBuildUserHealthReport_PeerBaselineAbsentDoesNotAffectClassifier(t *testing.T) {
	// Target with no subnet evidence → PeerBaseline attached with
	// insufficient assessment, NOT org_wide_issue. Existing patterns
	// must still trigger normally.
	calls := &fakeCalls{listResult: []store.Call{
		{CallId: "c1", Verdict: verdictGood, Participants: []string{"alice"}},
		{CallId: "c2", Verdict: verdictGood, Participants: []string{"alice"}},
		{CallId: "c3", Verdict: verdictGood, Participants: []string{"alice"}},
		{CallId: "c4", Verdict: verdictGood, Participants: []string{"alice"}},
		{CallId: "c5", Verdict: verdictGood, Participants: []string{"alice"}},
	}}
	// No streams → no subnets → insufficient path.
	svc := newTestService(calls, &fakeStreams{}, nil, nil, nil)
	r, err := svc.BuildUserHealthReport(context.Background(), UserHealthParams{Upn: "alice"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if r.Pattern != PatternHealthy {
		t.Errorf("pattern = %q, want healthy", r.Pattern)
	}
}

// M-7 / M-8: org_wide_issue must win over chronic_mic and remote_path when
// both sets of conditions fire simultaneously. Table-driven so each priority
// override is an explicit, independently named case.
func TestBuildUserHealthReport_OrgWideWinsOverOtherPatterns(t *testing.T) {
	// orgWideCohort builds the peer-cohort side (5 peers, 50% bad on the
	// target subnet) shared by all sub-cases. It returns metaResult and
	// peerRows suitable for fakeStreams/fakeCalls injection.
	buildCohort := func() ([]store.CallMeta, []store.StreamRow) {
		return makeCohort("peer", 5, 10, 5, "10.0.0.0")
	}

	pct := func(v float64) *float64 { return &v }

	tests := []struct {
		name      string
		buildSvc  func() *Service
	}{
		{
			// M-7: chronic_mic conditions fire (AvgConcealedPct≥10, device
			// present on ≥50% of bad calls) but org_wide_issue wins because
			// the cohort is on-par AND target bpRatio≥40%.
			// Cohort is 5 peers × 10 calls, 5 bad each = 50% bad ratio.
			// Target: 5 bad out of 10 total = 50% → delta 0 → on-par.
			name: "org_wide_wins_over_chronic_mic",
			buildSvc: func() *Service {
				// 5 bad/poor calls out of 10 total = 50% ≥ 40% gate.
				// Matches the cohort bad ratio exactly so delta=0 (on-par).
				var tCalls []store.Call
				for i := 0; i < 10; i++ {
					v := verdictGood
					if i < 5 {
						v = verdictBad
					}
					tCalls = append(tCalls, store.Call{
						CallId: "tc" + strconv.Itoa(i), Verdict: v,
						Participants: []string{"alice"},
					})
				}
				cohortMetas, cohortRows := buildCohort()
				// Build streams: all bad calls have elevated ConcealedPct on
				// HeadsetPro so chronic_mic conditions are satisfied.
				var tRows []store.StreamRow
				for i := 0; i < 10; i++ {
					row := store.StreamRow{
						CallId:        "tc" + strconv.Itoa(i),
						User:          "alice",
						Subnet:        "10.0.0.0",
						CaptureDevice: "HeadsetPro",
					}
					if i < 5 {
						// High concealment on bad calls — chronic_mic would
						// fire if there were no org_wide signal.
						row.ConcealedPct = pct(15.0)
					} else {
						row.ConcealedPct = pct(1.0)
					}
					tRows = append(tRows, row)
				}
				return newTestService(
					&fakeCalls{listResult: tCalls, metaResult: cohortMetas},
					&fakeStreams{userRows: tRows, peerRows: cohortRows},
					nil, nil, nil,
				)
			},
		},
		{
			// M-8: remote_path conditions fire (AvgRttMs≥150 on recv rows)
			// but org_wide_issue wins because the cohort is on-par AND target
			// bpRatio≥40%.
			name: "org_wide_wins_over_remote_path",
			buildSvc: func() *Service {
				// 5 bad calls out of 10 total = 50% ≥ 40% gate.
				var tCalls []store.Call
				for i := 0; i < 10; i++ {
					v := verdictGood
					if i < 5 {
						v = verdictBad
					}
					tCalls = append(tCalls, store.Call{
						CallId: "tc" + strconv.Itoa(i), Verdict: v,
						Participants: []string{"alice"},
					})
				}
				cohortMetas, cohortRows := buildCohort()
				rtt := pct(180.0)
				var tRows []store.StreamRow
				for i := 0; i < 5; i++ {
					// High RTT on bad calls — remote_path would fire without
					// the org_wide signal.
					tRows = append(tRows, store.StreamRow{
						CallId:    "tc" + strconv.Itoa(i),
						User:      "alice",
						Subnet:    "10.0.0.0",
						Direction: "recv",
						AvgRttMs:  rtt,
					})
				}
				for i := 5; i < 10; i++ {
					tRows = append(tRows, store.StreamRow{
						CallId: "tc" + strconv.Itoa(i),
						User:   "alice",
						Subnet: "10.0.0.0",
					})
				}
				return newTestService(
					&fakeCalls{listResult: tCalls, metaResult: cohortMetas},
					&fakeStreams{userRows: tRows, peerRows: cohortRows},
					nil, nil, nil,
				)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := tt.buildSvc()
			r, err := svc.BuildUserHealthReport(context.Background(), UserHealthParams{Upn: "alice"})
			if err != nil {
				t.Fatalf("unexpected: %v", err)
			}
			if r.PeerBaseline == nil {
				t.Fatal("PeerBaseline must be attached")
			}
			if r.PeerBaseline.Assessment != PeerAssessmentOnPar {
				t.Errorf("precondition: assessment = %q, want on-par", r.PeerBaseline.Assessment)
			}
			if r.Pattern != PatternOrgWideIssue {
				t.Errorf("pattern = %q, want org_wide_issue", r.Pattern)
			}
		})
	}
}

// M-6: a failure inside ComputePeerBaseline must not fail BuildUserHealthReport.
// The report is returned with PeerBaseline==nil and the classifier falls
// through to a non-org_wide_issue pattern (healthy in this fixture).
func TestBuildUserHealthReport_PeerBaselineErrorSoftFails(t *testing.T) {
	calls := &fakeCalls{listResult: []store.Call{
		{CallId: "c1", Verdict: verdictGood, Participants: []string{"alice"}},
		{CallId: "c2", Verdict: verdictGood, Participants: []string{"alice"}},
		{CallId: "c3", Verdict: verdictGood, Participants: []string{"alice"}},
	}}
	rows := []store.StreamRow{
		streamRow("c1", "alice", "send", "10.0.0.0", "wired", "HeadsetPro", "Windows", "Teams/1.6"),
	}
	streams := &fakeStreams{
		userRows: rows,
		// peerErr causes ComputePeerBaseline to return an error; the service
		// must absorb it and return the partial report.
		peerErr: fmt.Errorf("mongo peer boom"),
	}
	svc := newTestService(calls, streams, nil, nil, nil)

	r, err := svc.BuildUserHealthReport(context.Background(), UserHealthParams{Upn: "alice"})
	if err != nil {
		t.Fatalf("soft-fail violated: BuildUserHealthReport returned error: %v", err)
	}
	if r == nil {
		t.Fatal("want non-nil report")
	}
	if r.PeerBaseline != nil {
		t.Errorf("want nil PeerBaseline on peer error, got %+v", r.PeerBaseline)
	}
	if r.Pattern == PatternOrgWideIssue {
		t.Errorf("pattern must not be org_wide_issue when PeerBaseline is nil")
	}
}

// Phase 5: issue aggregation helpers and end-to-end attachment tests.

func TestSplitIssues(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"single undelimited", "cpuInsufficient", []string{"cpuInsufficient"}},
		{"semi delimited", "a;b;c", []string{"a", "b", "c"}},
		{"comma delimited", "a,b,c", []string{"a", "b", "c"}},
		{"mixed with whitespace", "a ; b , c", []string{"a", "b", "c"}},
		{"whitespace-only dropped", "a;; ; b", []string{"a", "b"}},
		{"trailing delimiter", "cpuInsufficient;", []string{"cpuInsufficient"}},
		{"leading delimiter", ";cpuInsufficient,networkSend", []string{"cpuInsufficient", "networkSend"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := splitIssues(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("len=%d want=%d (got=%#v)", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("[%d]=%q want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestTopIssues_OrderingAndCap(t *testing.T) {
	// 12 distinct issues, varying counts. Verify desc-by-count with
	// alphabetical tie-break, capped to userHealthTopIssues (10).
	m := map[string]map[string]struct{}{}
	add := func(issue string, n int) {
		set := map[string]struct{}{}
		for i := 0; i < n; i++ {
			set[fmt.Sprintf("c%d", i)] = struct{}{}
		}
		m[issue] = set
	}
	add("alpha", 5)
	add("bravo", 5) // ties with alpha → alphabetical order
	add("charlie", 9)
	add("delta", 3)
	add("echo", 3)
	add("foxtrot", 3)
	add("golf", 2)
	add("hotel", 2)
	add("india", 1)
	add("juliet", 1)
	add("kilo", 1)    // 11th most — one of these two drops after cap
	add("lima", 1)    // 12th

	out := topIssues(m)
	if len(out) != userHealthTopIssues {
		t.Fatalf("len=%d want=%d", len(out), userHealthTopIssues)
	}
	if out[0].Issue != "charlie" || out[0].CallCount != 9 {
		t.Errorf("top[0]=%+v want charlie/9", out[0])
	}
	// alpha < bravo alphabetically, both count=5
	if out[1].Issue != "alpha" || out[2].Issue != "bravo" {
		t.Errorf("tie-break wrong: [1]=%s [2]=%s", out[1].Issue, out[2].Issue)
	}
	// Monotonic non-increasing counts.
	for i := 1; i < len(out); i++ {
		if out[i].CallCount > out[i-1].CallCount {
			t.Errorf("not sorted desc at %d: %+v", i, out)
		}
	}

	// Lock in WHICH items survive the cap. The count=1 tier has 4 issues:
	// india, juliet, kilo, lima. Sorted alphabetically, the top 2 survive
	// (india, juliet) and the last 2 (kilo, lima) are dropped.
	got := map[string]int{}
	for _, ic := range out {
		got[ic.Issue] = ic.CallCount
	}
	for _, mustHave := range []string{"india", "juliet"} {
		if _, ok := got[mustHave]; !ok {
			t.Errorf("expected %q to survive the cap, but it was dropped", mustHave)
		}
	}
	for _, mustDrop := range []string{"kilo", "lima"} {
		if _, ok := got[mustDrop]; ok {
			t.Errorf("expected %q to be dropped by cap, but it survived", mustDrop)
		}
	}
}

func TestTopIssues_EmptyReturnsNil(t *testing.T) {
	if got := topIssues(nil); got != nil {
		t.Errorf("want nil, got %#v", got)
	}
	if got := topIssues(map[string]map[string]struct{}{}); got != nil {
		t.Errorf("want nil, got %#v", got)
	}
}

func TestBuildUserHealthReport_TopIssuesAggregated(t *testing.T) {
	// Two calls. c1 has three stream rows all mentioning cpuInsufficient
	// (via ';' delimiter with networkSendQualityEventRatio). c2 mentions
	// only cpuInsufficient once. Expected:
	//   cpuInsufficient -> 2 (distinct calls)
	//   networkSendQualityEventRatio -> 1
	calls := &fakeCalls{listResult: []store.Call{
		{CallId: "c1", Verdict: verdictGood, Participants: []string{"alice"}},
		{CallId: "c2", Verdict: verdictGood, Participants: []string{"alice"}},
		{CallId: "c3", Verdict: verdictGood, Participants: []string{"alice"}},
	}}
	mk := func(cid, issues string) store.StreamRow {
		r := streamRow(cid, "alice", "send", "10.0.0.0", "wired", "Built-In", "Windows", "Teams/1.6")
		r.Issues = issues
		return r
	}
	rows := []store.StreamRow{
		mk("c1", "cpuInsufficient;networkSendQualityEventRatio"),
		mk("c1", "cpuInsufficient"), // same call, same tag — distinct-call dedup
		mk("c1", " cpuInsufficient , foo "),
		mk("c2", "cpuInsufficient"),
		mk("c3", ""), // empty — must not contribute
	}
	svc := newTestService(calls, &fakeStreams{userRows: rows}, nil, nil, nil)
	r, err := svc.BuildUserHealthReport(context.Background(), UserHealthParams{Upn: "alice"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(r.TopIssues) == 0 {
		t.Fatalf("TopIssues empty: %+v", r)
	}
	byName := map[string]int{}
	for _, ic := range r.TopIssues {
		byName[ic.Issue] = ic.CallCount
	}
	if byName["cpuInsufficient"] != 2 {
		t.Errorf("cpuInsufficient count = %d, want 2", byName["cpuInsufficient"])
	}
	if byName["networkSendQualityEventRatio"] != 1 {
		t.Errorf("networkSendQualityEventRatio count = %d, want 1", byName["networkSendQualityEventRatio"])
	}
	if byName["foo"] != 1 {
		t.Errorf("foo count = %d, want 1", byName["foo"])
	}
	// First entry must be the most frequent one.
	if r.TopIssues[0].Issue != "cpuInsufficient" {
		t.Errorf("top issue = %s, want cpuInsufficient", r.TopIssues[0].Issue)
	}
}

func TestBuildUserHealthReport_TopIssuesNilWhenNoIssues(t *testing.T) {
	// When no row reports an issue, the field stays nil so the wire
	// payload omits it entirely under `omitempty`.
	calls := &fakeCalls{listResult: []store.Call{
		{CallId: "c1", Verdict: verdictGood, Participants: []string{"alice"}},
	}}
	rows := []store.StreamRow{
		streamRow("c1", "alice", "send", "10.0.0.0", "wired", "Built-In", "Windows", "Teams/1.6"),
	}
	svc := newTestService(calls, &fakeStreams{userRows: rows}, nil, nil, nil)
	r, err := svc.BuildUserHealthReport(context.Background(), UserHealthParams{Upn: "alice"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if r.TopIssues != nil {
		t.Errorf("TopIssues = %+v, want nil", r.TopIssues)
	}
}

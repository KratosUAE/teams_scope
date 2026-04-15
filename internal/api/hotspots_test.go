package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"teams_con/internal/store"
)

func f64(v float64) *float64 { return &v }

// mkHotspotRow is a tiny helper that constructs a StreamRow with exactly the
// fields FindNetworkHotspots actually reads, so the table-driven tests below
// stay terse and readable.
func mkHotspotRow(callID, user, subnet, relay string, rtt, jit, loss *float64) store.StreamRow {
	return store.StreamRow{
		CallId:      callID,
		User:        user,
		Subnet:      subnet,
		RelayIp:     relay,
		AvgRttMs:    rtt,
		AvgJitterMs: jit,
		AvgLossPct:  loss,
	}
}

func TestFindNetworkHotspots_GroupBySubnet_HappyPath(t *testing.T) {
	now := time.Now().UTC()
	metas := []store.CallMeta{
		{CallId: "c1", Verdict: "Bad", StartTimeUtc: now},
		{CallId: "c2", Verdict: "Bad", StartTimeUtc: now},
		{CallId: "c3", Verdict: "Poor", StartTimeUtc: now},
		{CallId: "c4", Verdict: "Bad", StartTimeUtc: now},
		{CallId: "c5", Verdict: "Bad", StartTimeUtc: now},
		{CallId: "c6", Verdict: "Good", StartTimeUtc: now},
	}
	rows := []store.StreamRow{
		// Bucket "10.0.0.0/24": 5 distinct calls, 5 bad → 100% bad.
		mkHotspotRow("c1", "alice@x.com", "10.0.0.0/24", "", f64(120), f64(10), f64(2)),
		mkHotspotRow("c2", "alice@x.com", "10.0.0.0/24", "", f64(120), f64(10), f64(2)),
		mkHotspotRow("c3", "bob@x.com", "10.0.0.0/24", "", f64(80), nil, nil),
		mkHotspotRow("c4", "carol@x.com", "10.0.0.0/24", "", nil, nil, nil),
		mkHotspotRow("c5", "dave@x.com", "10.0.0.0/24", "", nil, nil, nil),
		// Bucket "192.168.0.0/24": 1 call → should be dropped by min_calls.
		mkHotspotRow("c6", "eve@x.com", "192.168.0.0/24", "", nil, nil, nil),
	}
	calls := &fakeCalls{metaResult: metas}
	streams := &fakeStreams{windowRows: rows}
	svc := newTestService(calls, streams, nil, nil, nil)

	out, err := svc.FindNetworkHotspots(context.Background(), HotspotsParams{
		MinCalls:    3,
		MinBadRatio: 0.5,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1 (min_calls must drop the 1-call bucket): %+v", len(out), out)
	}
	h := out[0]
	if h.Subnet != "10.0.0.0/24" {
		t.Errorf("subnet = %q", h.Subnet)
	}
	if h.TotalCalls != 5 || h.BadCalls != 5 {
		t.Errorf("counts = %d/%d, want 5/5", h.BadCalls, h.TotalCalls)
	}
	if h.DistinctUsers != 4 {
		t.Errorf("distinctUsers = %d, want 4", h.DistinctUsers)
	}
	if h.AvgRttMs == nil || *h.AvgRttMs != 106.7 {
		// (120 + 120 + 80) / 3 = 106.6667 → round1 → 106.7
		t.Errorf("avgRtt = %v, want 106.7", h.AvgRttMs)
	}
}

func TestFindNetworkHotspots_GroupByRelay(t *testing.T) {
	now := time.Now().UTC()
	metas := []store.CallMeta{
		{CallId: "c1", Verdict: "Bad", StartTimeUtc: now},
		{CallId: "c2", Verdict: "Bad", StartTimeUtc: now},
		{CallId: "c3", Verdict: "Bad", StartTimeUtc: now},
	}
	rows := []store.StreamRow{
		// Relay groups independent of subnet.
		mkHotspotRow("c1", "alice@x.com", "10.0.0.0/24", "52.1.2.3", nil, nil, nil),
		mkHotspotRow("c2", "bob@x.com", "192.168.1.0/24", "52.1.2.3", nil, nil, nil),
		mkHotspotRow("c3", "carol@x.com", "10.99.0.0/24", "52.1.2.3", nil, nil, nil),
	}
	svc := newTestService(&fakeCalls{metaResult: metas}, &fakeStreams{windowRows: rows}, nil, nil, nil)

	out, err := svc.FindNetworkHotspots(context.Background(), HotspotsParams{
		GroupBy:     hotspotGroupRelay,
		MinCalls:    2,
		MinBadRatio: 0.5,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1: %+v", len(out), out)
	}
	if out[0].RelayIp != "52.1.2.3" {
		t.Errorf("relayIp = %q", out[0].RelayIp)
	}
	// relay-grouped rows must have empty Subnet / SubnetName.
	if out[0].Subnet != "" || out[0].SubnetName != "" {
		t.Errorf("relay bucket leaked subnet fields: %+v", out[0])
	}
	if out[0].TotalCalls != 3 || out[0].BadCalls != 3 {
		t.Errorf("counts = %d/%d", out[0].BadCalls, out[0].TotalCalls)
	}
}

func TestFindNetworkHotspots_GroupBySubnetRelay_RequiresBothFields(t *testing.T) {
	now := time.Now().UTC()
	metas := []store.CallMeta{
		{CallId: "c1", Verdict: "Bad", StartTimeUtc: now},
		{CallId: "c2", Verdict: "Bad", StartTimeUtc: now},
		{CallId: "c3", Verdict: "Bad", StartTimeUtc: now},
		{CallId: "c4", Verdict: "Bad", StartTimeUtc: now},
	}
	rows := []store.StreamRow{
		mkHotspotRow("c1", "a@x.com", "10.0.0.0/24", "52.1.2.3", nil, nil, nil),
		mkHotspotRow("c2", "b@x.com", "10.0.0.0/24", "52.1.2.3", nil, nil, nil),
		// Missing relay → skipped by subnet+relay grouping.
		mkHotspotRow("c3", "c@x.com", "10.0.0.0/24", "", nil, nil, nil),
		// Missing subnet → skipped.
		mkHotspotRow("c4", "d@x.com", "", "52.9.9.9", nil, nil, nil),
	}
	svc := newTestService(&fakeCalls{metaResult: metas}, &fakeStreams{windowRows: rows}, nil, nil, nil)

	out, err := svc.FindNetworkHotspots(context.Background(), HotspotsParams{
		GroupBy:     hotspotGroupSubnetRelay,
		MinCalls:    2,
		MinBadRatio: 0.5,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1: %+v", len(out), out)
	}
	if out[0].Subnet != "10.0.0.0/24" || out[0].RelayIp != "52.1.2.3" {
		t.Errorf("bucket = %q/%q", out[0].Subnet, out[0].RelayIp)
	}
}

func TestFindNetworkHotspots_MinBadRatioFloor(t *testing.T) {
	now := time.Now().UTC()
	metas := []store.CallMeta{
		{CallId: "c1", Verdict: "Good", StartTimeUtc: now},
		{CallId: "c2", Verdict: "Good", StartTimeUtc: now},
		{CallId: "c3", Verdict: "Good", StartTimeUtc: now},
		{CallId: "c4", Verdict: "Good", StartTimeUtc: now},
		{CallId: "c5", Verdict: "Bad", StartTimeUtc: now},
	}
	rows := []store.StreamRow{
		mkHotspotRow("c1", "a@x.com", "10.0.0.0/24", "", nil, nil, nil),
		mkHotspotRow("c2", "a@x.com", "10.0.0.0/24", "", nil, nil, nil),
		mkHotspotRow("c3", "a@x.com", "10.0.0.0/24", "", nil, nil, nil),
		mkHotspotRow("c4", "a@x.com", "10.0.0.0/24", "", nil, nil, nil),
		mkHotspotRow("c5", "a@x.com", "10.0.0.0/24", "", nil, nil, nil),
	}
	svc := newTestService(&fakeCalls{metaResult: metas}, &fakeStreams{windowRows: rows}, nil, nil, nil)
	out, err := svc.FindNetworkHotspots(context.Background(), HotspotsParams{
		MinCalls:    3,
		MinBadRatio: 0.5, // 20% bad → below floor → nothing returned.
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("want empty, got %+v", out)
	}
}

func TestFindNetworkHotspots_SampleUsersTopBadByPersonalRatio(t *testing.T) {
	now := time.Now().UTC()
	// Bucket is 10 calls total; 8 bad. Alice has 4/4 bad (100%), bob 2/3
	// (67%), carol 2/3 (67%), dave 0/1 (0%, excluded). Tie between
	// bob/carol is broken by higher bad count (both 2) then UPN asc.
	metas := []store.CallMeta{
		{CallId: "c1", Verdict: "Bad", StartTimeUtc: now},
		{CallId: "c2", Verdict: "Bad", StartTimeUtc: now},
		{CallId: "c3", Verdict: "Bad", StartTimeUtc: now},
		{CallId: "c4", Verdict: "Bad", StartTimeUtc: now},
		{CallId: "c5", Verdict: "Bad", StartTimeUtc: now},
		{CallId: "c6", Verdict: "Bad", StartTimeUtc: now},
		{CallId: "c7", Verdict: "Bad", StartTimeUtc: now},
		{CallId: "c8", Verdict: "Bad", StartTimeUtc: now},
		{CallId: "c9", Verdict: "Good", StartTimeUtc: now},
		{CallId: "c10", Verdict: "Good", StartTimeUtc: now},
	}
	const sn = "10.0.0.0/24"
	rows := []store.StreamRow{
		mkHotspotRow("c1", "alice@x.com", sn, "", nil, nil, nil),
		mkHotspotRow("c2", "alice@x.com", sn, "", nil, nil, nil),
		mkHotspotRow("c3", "alice@x.com", sn, "", nil, nil, nil),
		mkHotspotRow("c4", "alice@x.com", sn, "", nil, nil, nil),
		mkHotspotRow("c5", "bob@x.com", sn, "", nil, nil, nil),
		mkHotspotRow("c6", "bob@x.com", sn, "", nil, nil, nil),
		mkHotspotRow("c9", "bob@x.com", sn, "", nil, nil, nil),
		mkHotspotRow("c7", "carol@x.com", sn, "", nil, nil, nil),
		mkHotspotRow("c8", "carol@x.com", sn, "", nil, nil, nil),
		mkHotspotRow("c10", "carol@x.com", sn, "", nil, nil, nil),
		mkHotspotRow("c9", "dave@x.com", sn, "", nil, nil, nil),
	}
	svc := newTestService(&fakeCalls{metaResult: metas}, &fakeStreams{windowRows: rows}, nil, nil, nil)
	out, err := svc.FindNetworkHotspots(context.Background(), HotspotsParams{MinCalls: 5, MinBadRatio: 0.5})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("want 1 bucket, got %d", len(out))
	}
	got := out[0].SampleUsers
	want := []string{"alice@x.com", "bob@x.com", "carol@x.com"}
	if len(got) != len(want) {
		t.Fatalf("sampleUsers = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sampleUsers[%d] = %q, want %q (full=%v)", i, got[i], want[i], got)
		}
	}
	// Dave had 0 bad calls → must NOT appear.
	for _, u := range got {
		if u == "dave@x.com" {
			t.Error("dave leaked into sample users despite zero bad calls")
		}
	}
}

func TestFindNetworkHotspots_SampleUsersCappedAtFive(t *testing.T) {
	now := time.Now().UTC()
	metas := make([]store.CallMeta, 0, 10)
	rows := make([]store.StreamRow, 0, 10)
	for i, upn := range []string{"u1@x", "u2@x", "u3@x", "u4@x", "u5@x", "u6@x", "u7@x"} {
		callID := "c" + string(rune('a'+i))
		metas = append(metas, store.CallMeta{CallId: callID, Verdict: "Bad", StartTimeUtc: now})
		rows = append(rows, mkHotspotRow(callID, upn, "10.0.0.0/24", "", nil, nil, nil))
	}
	svc := newTestService(&fakeCalls{metaResult: metas}, &fakeStreams{windowRows: rows}, nil, nil, nil)
	out, err := svc.FindNetworkHotspots(context.Background(), HotspotsParams{MinCalls: 3, MinBadRatio: 0.5})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("want 1 bucket, got %d", len(out))
	}
	if len(out[0].SampleUsers) != hotspotSampleUsers {
		t.Errorf("sampleUsers len = %d, want %d", len(out[0].SampleUsers), hotspotSampleUsers)
	}
}

func TestFindNetworkHotspots_UnknownGroupByRejected(t *testing.T) {
	svc := newTestService(nil, nil, nil, nil, nil)
	_, err := svc.FindNetworkHotspots(context.Background(), HotspotsParams{GroupBy: "subnet_relay"})
	if !errors.Is(err, ErrBadRequest) {
		t.Fatalf("err = %v, want ErrBadRequest", err)
	}
}

func TestFindNetworkHotspots_EmptyWindowReturnsEmptySlice(t *testing.T) {
	svc := newTestService(&fakeCalls{metaResult: nil}, &fakeStreams{}, nil, nil, nil)
	out, err := svc.FindNetworkHotspots(context.Background(), HotspotsParams{})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out == nil {
		t.Fatal("want empty slice, got nil")
	}
	if len(out) != 0 {
		t.Errorf("want empty, got %+v", out)
	}
}

func TestFindNetworkHotspots_NoRttRowsLeavesAvgNil(t *testing.T) {
	now := time.Now().UTC()
	metas := []store.CallMeta{
		{CallId: "c1", Verdict: "Bad", StartTimeUtc: now},
		{CallId: "c2", Verdict: "Bad", StartTimeUtc: now},
		{CallId: "c3", Verdict: "Bad", StartTimeUtc: now},
	}
	rows := []store.StreamRow{
		mkHotspotRow("c1", "a@x.com", "10.0.0.0/24", "", nil, nil, f64(3)),
		mkHotspotRow("c2", "a@x.com", "10.0.0.0/24", "", nil, nil, f64(4)),
		mkHotspotRow("c3", "a@x.com", "10.0.0.0/24", "", nil, nil, f64(5)),
	}
	svc := newTestService(&fakeCalls{metaResult: metas}, &fakeStreams{windowRows: rows}, nil, nil, nil)
	out, err := svc.FindNetworkHotspots(context.Background(), HotspotsParams{MinCalls: 2, MinBadRatio: 0.5})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("len = %d", len(out))
	}
	if out[0].AvgRttMs != nil {
		t.Errorf("avgRtt = %v, want nil", out[0].AvgRttMs)
	}
	if out[0].AvgLossPct == nil || *out[0].AvgLossPct != 4.0 {
		t.Errorf("avgLoss = %v, want 4.0", out[0].AvgLossPct)
	}
}

func TestFindNetworkHotspots_SubnetResolverPopulatesName(t *testing.T) {
	now := time.Now().UTC()
	metas := []store.CallMeta{
		{CallId: "c1", Verdict: "Bad", StartTimeUtc: now},
		{CallId: "c2", Verdict: "Bad", StartTimeUtc: now},
		{CallId: "c3", Verdict: "Bad", StartTimeUtc: now},
	}
	rows := []store.StreamRow{
		mkHotspotRow("c1", "a@x.com", "10.16.1.42", "", nil, nil, nil),
		mkHotspotRow("c2", "b@x.com", "10.16.2.99", "", nil, nil, nil),
		mkHotspotRow("c3", "c@x.com", "10.16.3.11", "", nil, nil, nil),
	}
	subs := newFakeSubnets(store.SubnetEntry{
		Cidr: "10.16.0.0/16",
		Name: "Xpanceo Dubai HQ",
	})
	// Normally all three rows would hit distinct buckets (subnet exact
	// strings differ), so verify the post-group enrichment path populates
	// Name on at least one resolved row.
	svc := newTestServiceFull(
		&fakeCalls{metaResult: metas},
		&fakeStreams{windowRows: rows},
		nil, nil, subs, nil, nil,
	)
	out, err := svc.FindNetworkHotspots(context.Background(), HotspotsParams{MinCalls: 1, MinBadRatio: 0.5})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("want at least one bucket")
	}
	for _, h := range out {
		if h.SubnetName != "Xpanceo Dubai HQ" {
			t.Errorf("subnetName = %q, want %q (subnet=%s)", h.SubnetName, "Xpanceo Dubai HQ", h.Subnet)
		}
	}
}

func TestFindNetworkHotspots_RowsWithEmptyFieldKeyAreSkipped(t *testing.T) {
	now := time.Now().UTC()
	metas := []store.CallMeta{
		{CallId: "c1", Verdict: "Bad", StartTimeUtc: now},
		{CallId: "c2", Verdict: "Bad", StartTimeUtc: now},
	}
	rows := []store.StreamRow{
		// GroupBy=subnet and subnet empty → row skipped, no panic.
		mkHotspotRow("c1", "a@x.com", "", "52.1.2.3", nil, nil, nil),
		mkHotspotRow("c2", "b@x.com", "", "52.1.2.3", nil, nil, nil),
	}
	svc := newTestService(&fakeCalls{metaResult: metas}, &fakeStreams{windowRows: rows}, nil, nil, nil)
	out, err := svc.FindNetworkHotspots(context.Background(), HotspotsParams{
		GroupBy:     hotspotGroupSubnet,
		MinCalls:    1,
		MinBadRatio: 0.5,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("want empty (all rows skipped), got %+v", out)
	}
}

func TestFindNetworkHotspots_MinBadRatioAboveOneRejected(t *testing.T) {
	svc := newTestService(nil, nil, nil, nil, nil)
	_, err := svc.FindNetworkHotspots(context.Background(), HotspotsParams{MinBadRatio: 1.5})
	if !errors.Is(err, ErrBadRequest) {
		t.Fatalf("want ErrBadRequest, got %v", err)
	}
}

func TestFindNetworkHotspots_CallIDsForwardedToStreams(t *testing.T) {
	metas := []store.CallMeta{
		{CallId: "x1", Verdict: "Bad"},
		{CallId: "x2", Verdict: "Poor"},
	}
	var gotCallIDs []string
	streams := &fakeStreams{
		windowFn: func(callIDs []string) ([]store.StreamRow, error) {
			gotCallIDs = append([]string(nil), callIDs...)
			return nil, nil
		},
	}
	svc := newTestServiceFull(
		&fakeCalls{metaResult: metas},
		streams,
		nil, nil, nil, nil, nil,
	)
	_, _ = svc.FindNetworkHotspots(context.Background(), HotspotsParams{})
	if len(gotCallIDs) != 2 {
		t.Fatalf("forwarded %d callIDs, want 2", len(gotCallIDs))
	}
	want := map[string]bool{"x1": true, "x2": true}
	for _, id := range gotCallIDs {
		if !want[id] {
			t.Errorf("unexpected callID %q forwarded", id)
		}
	}
}

func TestFindNetworkHotspots_StoreErrorPropagates(t *testing.T) {
	boom := errors.New("mongo down")
	svc := newTestService(&fakeCalls{metaResult: []store.CallMeta{{CallId: "c1", Verdict: "Bad"}}}, &fakeStreams{windowErr: boom}, nil, nil, nil)
	_, err := svc.FindNetworkHotspots(context.Background(), HotspotsParams{MinCalls: 1, MinBadRatio: 0.1})
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("err = %v, want wrap of %v", err, boom)
	}
}

func TestFindNetworkHotspots_BadTimeWindow(t *testing.T) {
	svc := newTestService(nil, nil, nil, nil, nil)
	t1 := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)
	t2 := t1
	_, err := svc.FindNetworkHotspots(context.Background(), HotspotsParams{From: &t1, To: &t2})
	if !errors.Is(err, ErrBadRequest) {
		t.Fatalf("err = %v, want ErrBadRequest", err)
	}
}

func TestFindNetworkHotspots_SortAndLimit(t *testing.T) {
	now := time.Now().UTC()
	// Build three buckets with distinct bad ratios so we can verify sort.
	// Each subnet gets the calls it needs.
	metas := []store.CallMeta{}
	rows := []store.StreamRow{}
	seed := []struct {
		subnet string
		bad    int
		good   int
	}{
		{"10.0.1.0/24", 3, 1}, // 75%
		{"10.0.2.0/24", 4, 0}, // 100%
		{"10.0.3.0/24", 2, 2}, // 50%
	}
	cid := 0
	for _, s := range seed {
		for i := 0; i < s.bad; i++ {
			cid++
			id := "c" + string(rune('a'+cid))
			metas = append(metas, store.CallMeta{CallId: id, Verdict: "Bad", StartTimeUtc: now})
			rows = append(rows, mkHotspotRow(id, "u@x.com", s.subnet, "", nil, nil, nil))
		}
		for i := 0; i < s.good; i++ {
			cid++
			id := "c" + string(rune('a'+cid))
			metas = append(metas, store.CallMeta{CallId: id, Verdict: "Good", StartTimeUtc: now})
			rows = append(rows, mkHotspotRow(id, "u@x.com", s.subnet, "", nil, nil, nil))
		}
	}
	svc := newTestService(&fakeCalls{metaResult: metas}, &fakeStreams{windowRows: rows}, nil, nil, nil)
	out, err := svc.FindNetworkHotspots(context.Background(), HotspotsParams{MinCalls: 2, MinBadRatio: 0.4, Limit: 2})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("want limit=2, got %d: %+v", len(out), out)
	}
	if out[0].Subnet != "10.0.2.0/24" {
		t.Errorf("first bucket subnet = %q, want 10.0.2.0/24 (highest bad ratio)", out[0].Subnet)
	}
	if out[1].Subnet != "10.0.1.0/24" {
		t.Errorf("second bucket subnet = %q, want 10.0.1.0/24", out[1].Subnet)
	}
}

package api

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strconv"
	"testing"
	"time"

	"teams_con/internal/store"
)

// Phase 3 — peer baseline tests. These exercise ComputePeerBaseline end
// to end through the Service fakes, plus classifyPeerDelta as a pure unit.

func TestClassifyPeerDelta_Buckets(t *testing.T) {
	tests := []struct {
		name  string
		delta float64
		want  string
	}{
		{"much better", -10, PeerAssessmentBetter},
		{"just better", -5.1, PeerAssessmentBetter},
		{"on par negative edge", -5, PeerAssessmentOnPar},
		{"exactly zero", 0, PeerAssessmentOnPar},
		{"on par positive edge", 5, PeerAssessmentOnPar},
		{"just below rounding edge", 4.99, PeerAssessmentOnPar},
		{"worse lower edge", 5.1, PeerAssessmentWorse},
		{"worse upper edge", 15, PeerAssessmentWorse},
		{"much worse", 15.1, PeerAssessmentMuchWorse},
		{"very much worse", 40, PeerAssessmentMuchWorse},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyPeerDelta(tt.delta); got != tt.want {
				t.Errorf("classifyPeerDelta(%v) = %q, want %q", tt.delta, got, tt.want)
			}
		})
	}
}

func TestComputePeerBaseline_EmptyUpn(t *testing.T) {
	svc := newTestService(nil, nil, nil, nil, nil)
	_, err := svc.ComputePeerBaseline(context.Background(), "", nil, nil)
	if !errors.Is(err, ErrBadRequest) {
		t.Fatalf("err = %v, want ErrBadRequest", err)
	}
}

func TestComputePeerBaseline_NoCalls(t *testing.T) {
	calls := &fakeCalls{listResult: nil}
	svc := newTestService(calls, &fakeStreams{}, nil, nil, nil)
	got, err := svc.ComputePeerBaseline(context.Background(), "alice@corp.com", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("empty target → want nil baseline, got %+v", got)
	}
}

func TestComputePeerBaseline_NoSubnetsOnTarget(t *testing.T) {
	calls := &fakeCalls{
		listResult: []store.Call{{CallId: "c1", Verdict: "Good"}},
	}
	streams := &fakeStreams{
		userRows: []store.StreamRow{{CallId: "c1", User: "alice@corp.com"}}, // empty Subnet
	}
	svc := newTestService(calls, streams, nil, nil, nil)
	got, err := svc.ComputePeerBaseline(context.Background(), "alice@corp.com", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || got.Assessment != PeerAssessmentInsufficient {
		t.Fatalf("want insufficient assessment, got %+v", got)
	}
}

// peerFixture builds a fakeCalls + fakeStreams combo describing one target
// user and an arbitrary cohort of peers. Target calls all claim subnet
// "10.0.0.0"; the ListInWindowBySubnets response is assembled by repeating
// each peer's (callId, verdict, subnet) row.
type peerFixture struct {
	target       string
	targetCalls  []store.Call   // full verdict list for the target
	cohortMetas  []store.CallMeta // cohort calls with verdicts
	peerStreams  []store.StreamRow // rows ListInWindowBySubnets returns
	targetSubnet string
}

func buildPeerService(t *testing.T, fx peerFixture) *Service {
	t.Helper()
	subnet := fx.targetSubnet
	if subnet == "" {
		subnet = "10.0.0.0"
	}
	calls := &fakeCalls{
		listResult: fx.targetCalls,
		metaResult: fx.cohortMetas,
	}
	// Target's streams carry the target subnet so ComputePeerBaseline
	// finds a non-empty rawSubnetSet.
	targetRows := make([]store.StreamRow, 0, len(fx.targetCalls))
	for _, c := range fx.targetCalls {
		targetRows = append(targetRows, store.StreamRow{
			CallId: c.CallId,
			User:   fx.target,
			Subnet: subnet,
		})
	}
	streams := &fakeStreams{
		userRows: targetRows,
		peerRows: fx.peerStreams,
	}
	return newTestService(calls, streams, nil, nil, nil)
}

// makeCohort builds peerStreams + cohortMetas such that `n` distinct users
// each make `callsPerUser` calls, with `badPerUser` of those rated Bad.
func makeCohort(prefix string, n, callsPerUser, badPerUser int, subnet string) ([]store.CallMeta, []store.StreamRow) {
	metas := make([]store.CallMeta, 0, n*callsPerUser)
	rows := make([]store.StreamRow, 0, n*callsPerUser)
	for u := 0; u < n; u++ {
		user := prefix + "-u" + strconv.Itoa(u) + "@corp.com"
		for c := 0; c < callsPerUser; c++ {
			cid := prefix + "-c" + strconv.Itoa(u) + "-" + strconv.Itoa(c)
			verdict := "Good"
			if c < badPerUser {
				verdict = "Bad"
			}
			metas = append(metas, store.CallMeta{CallId: cid, Verdict: verdict})
			rows = append(rows, store.StreamRow{CallId: cid, User: user, Subnet: subnet})
		}
	}
	return metas, rows
}


func targetCalls(n, bad int) []store.Call {
	out := make([]store.Call, 0, n)
	for i := 0; i < n; i++ {
		v := "Good"
		if i < bad {
			v = "Bad"
		}
		out = append(out, store.Call{CallId: "t-c" + strconv.Itoa(i), Verdict: v})
	}
	return out
}

func TestComputePeerBaseline_InsufficientPeers(t *testing.T) {
	// Only 2 distinct peers → below the min cohort, assessment flips to
	// insufficient regardless of their ratio.
	metas, rows := makeCohort("p", 2, 4, 1, "10.0.0.0")
	svc := buildPeerService(t, peerFixture{
		target:      "alice@corp.com",
		targetCalls: targetCalls(10, 4),
		cohortMetas: metas,
		peerStreams: rows,
	})
	got, err := svc.ComputePeerBaseline(context.Background(), "alice@corp.com", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("want non-nil baseline with insufficient assessment")
	}
	if got.Assessment != PeerAssessmentInsufficient {
		t.Errorf("assessment = %q, want insufficient", got.Assessment)
	}
	if got.CohortSize != 2 {
		t.Errorf("CohortSize = %d, want 2", got.CohortSize)
	}
	if got.CohortCalls != 8 {
		t.Errorf("CohortCalls = %d, want 8", got.CohortCalls)
	}
	if got.TargetBadRatio != 0.4 {
		t.Errorf("TargetBadRatio = %v, want 0.4", got.TargetBadRatio)
	}
}

func TestComputePeerBaseline_Assessments(t *testing.T) {
	// Cohort: 5 peers × 10 calls each = 50 cohort calls, 10% bad.
	tests := []struct {
		name           string
		targetTotal    int
		targetBad      int
		wantAssessment string
	}{
		// target 0/10 = 0% vs cohort 10% → delta -10pp → better
		{"better", 10, 0, PeerAssessmentBetter},
		// target 1/10 = 10% vs cohort 10% → delta 0pp → on-par
		{"on-par", 10, 1, PeerAssessmentOnPar},
		// target 2/10 = 20% vs cohort 10% → delta +10pp → worse
		{"worse", 10, 2, PeerAssessmentWorse},
		// target 4/10 = 40% vs cohort 10% → delta +30pp → much-worse
		{"much-worse", 10, 4, PeerAssessmentMuchWorse},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metas, rows := makeCohort("p", 5, 10, 1, "10.0.0.0") // 10% bad
			svc := buildPeerService(t, peerFixture{
				target:      "alice@corp.com",
				targetCalls: targetCalls(tt.targetTotal, tt.targetBad),
				cohortMetas: metas,
				peerStreams: rows,
			})
			got, err := svc.ComputePeerBaseline(context.Background(), "alice@corp.com", nil, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got == nil {
				t.Fatal("baseline nil")
			}
			if got.Assessment != tt.wantAssessment {
				t.Errorf("assessment = %q, want %q (delta=%v)", got.Assessment, tt.wantAssessment, got.Delta)
			}
			if got.CohortSize != 5 {
				t.Errorf("CohortSize = %d, want 5", got.CohortSize)
			}
			if got.CohortCalls != 50 {
				t.Errorf("CohortCalls = %d, want 50", got.CohortCalls)
			}
		})
	}
}

func TestComputePeerBaseline_ExcludesTargetFromCohort(t *testing.T) {
	// Peer fixture with 3 real peers + the target re-appearing (simulates
	// the Mongo query returning the target's own rows because their subnet
	// matches). The target must not inflate cohortSize/cohortCalls.
	metas, rows := makeCohort("p", 3, 5, 0, "10.0.0.0")
	// Inject target rows — same subnet, different verdicts.
	metas = append(metas, store.CallMeta{CallId: "t-c0", Verdict: "Bad"})
	rows = append(rows, store.StreamRow{
		CallId: "t-c0", User: "alice@corp.com", Subnet: "10.0.0.0",
	})
	svc := buildPeerService(t, peerFixture{
		target:      "alice@corp.com",
		targetCalls: targetCalls(5, 0),
		cohortMetas: metas,
		peerStreams: rows,
	})
	got, err := svc.ComputePeerBaseline(context.Background(), "alice@corp.com", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.CohortSize != 3 {
		t.Errorf("CohortSize = %d, want 3 (target excluded)", got.CohortSize)
	}
	if got.CohortCalls != 15 {
		t.Errorf("CohortCalls = %d, want 15", got.CohortCalls)
	}
}

func TestComputePeerBaseline_SubnetsComparedSorted(t *testing.T) {
	// Target touches two subnets; SubnetsCompared must come out sorted
	// for deterministic wire output.
	calls := &fakeCalls{
		listResult: []store.Call{{CallId: "c1", Verdict: "Good"}, {CallId: "c2", Verdict: "Good"}},
		metaResult: nil, // no cohort
	}
	streams := &fakeStreams{
		userRows: []store.StreamRow{
			{CallId: "c1", User: "alice@corp.com", Subnet: "192.168.0.0"},
			{CallId: "c2", User: "alice@corp.com", Subnet: "10.0.0.0"},
		},
	}
	svc := newTestService(calls, streams, nil, nil, nil)
	got, err := svc.ComputePeerBaseline(context.Background(), "alice@corp.com", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"10.0.0.0", "192.168.0.0"}
	if !reflect.DeepEqual(got.SubnetsCompared, want) {
		t.Errorf("SubnetsCompared = %v, want %v", got.SubnetsCompared, want)
	}
}

func TestComputePeerBaseline_ForwardsCallIDsToStore(t *testing.T) {
	// Verify the two-phase wiring: Service must pass ListMetaInWindow's
	// callIds + target's rawSubnets into ListInWindowBySubnets.
	calls := &fakeCalls{
		listResult: []store.Call{{CallId: "tc1", Verdict: "Good"}},
		metaResult: []store.CallMeta{
			{CallId: "win-a", Verdict: "Good"},
			{CallId: "win-b", Verdict: "Bad"},
		},
	}
	var gotCallIDs, gotSubnets []string
	streams := &fakeStreams{
		userRows: []store.StreamRow{{CallId: "tc1", User: "alice@corp.com", Subnet: "10.0.0.0"}},
		peerFn: func(cids, sns []string) ([]store.StreamRow, error) {
			gotCallIDs = cids
			gotSubnets = sns
			return nil, nil
		},
	}
	svc := newTestService(calls, streams, nil, nil, nil)
	_, err := svc.ComputePeerBaseline(context.Background(), "alice@corp.com", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sort.Strings(gotCallIDs)
	if !reflect.DeepEqual(gotCallIDs, []string{"win-a", "win-b"}) {
		t.Errorf("store got callIDs = %v, want [win-a win-b]", gotCallIDs)
	}
	if !reflect.DeepEqual(gotSubnets, []string{"10.0.0.0"}) {
		t.Errorf("store got subnets = %v, want [10.0.0.0]", gotSubnets)
	}
}

func TestComputePeerBaseline_WindowValidation(t *testing.T) {
	svc := newTestService(nil, nil, nil, nil, nil)
	now := time.Now().UTC()
	earlier := now.Add(-1 * time.Hour)
	_, err := svc.ComputePeerBaseline(context.Background(), "alice@corp.com", &now, &earlier)
	if !errors.Is(err, ErrBadRequest) {
		t.Fatalf("err = %v, want ErrBadRequest", err)
	}
}

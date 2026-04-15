package api

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// Phase 3 — peer baseline.
//
// ComputePeerBaseline answers the operator's first question when a user's
// health report looks bad: "is it just them, or is everyone on the same
// network having a bad day?". The cohort is every OTHER user who shared
// at least one of the target's subnets inside the same time window; the
// function compares the target's bad/poor ratio against the cohort's and
// emits a coarse assessment the LLM/TUI can display in one line.
//
// The implementation follows the existing two-phase Mongo pattern
// (ListMetaInWindow → $in callIds) used by FindFlakyMicrophones so no
// unbounded streams-collection scan ever reaches the database.

// Peer-baseline thresholds. Hand-picked to match the spec; tune later if
// operator feedback says the buckets sit in the wrong place.
const (
	// Delta is in percentage points (target bad ratio minus cohort bad
	// ratio, *100). A 10% vs 5% split yields delta=+5pp.
	peerDeltaBetter   = -5.0
	peerDeltaOnParAbs = 5.0
	peerDeltaWorseMax = 15.0

	// peerMinCohort is the minimum number of DISTINCT peer users required
	// for a meaningful comparison; below this the assessment collapses to
	// "insufficient_peers" regardless of delta.
	peerMinCohort = 3

	// patternPeerHighBad gates the org_wide_issue classifier branch:
	// "peers share the pain" is only interesting when the target's own
	// bad/poor ratio is materially high in absolute terms.
	patternPeerHighBad = 0.40
)

// Public peer-baseline assessment strings. Exported so handlers, TUI, and
// tests can reference them without duplicating magic strings.
const (
	PeerAssessmentBetter       = "better"
	PeerAssessmentOnPar        = "on-par"
	PeerAssessmentWorse        = "worse"
	PeerAssessmentMuchWorse    = "much-worse"
	PeerAssessmentInsufficient = "insufficient_peers"

	// PatternOrgWideIssue is the Phase 3 classifier label inserted between
	// `healthy` and `chronic_mic` — "your network is burning, not your
	// mic". Returned from classifyUserPattern only when PeerBaseline is
	// non-nil, its assessment is on-par, and the bad/poor ratio clears
	// patternPeerHighBad.
	PatternOrgWideIssue = "org_wide_issue"
)

// PeerBaseline is the aggregate emitted by ComputePeerBaseline and
// attached to UserHealthReport.PeerBaseline. All numeric fields are
// populated even on the insufficient_peers path so the operator can see
// whatever cohort we did find. Delta is pre-rounded to 1dp.
type PeerBaseline struct {
	CohortSize      int      `json:"cohortSize"`
	CohortCalls     int      `json:"cohortCalls"`
	CohortBadRatio  float64  `json:"cohortBadRatio"`
	TargetBadRatio  float64  `json:"targetBadRatio"`
	Delta           float64  `json:"delta"`
	Assessment      string   `json:"assessment"`
	SubnetsCompared []string `json:"subnetsCompared"`
}

// ComputePeerBaseline builds a peer comparison for upn inside [from, to).
// A nil from/to defaults to the last userHealthDefaultWindow. Errors are
// reserved for real failures (bad input, Mongo boom) — an empty cohort
// returns a non-nil baseline with Assessment=insufficient_peers.
//
// The method is intentionally independent of BuildUserHealthReport so it
// can also be called standalone from a future MCP tool or CLI probe. The
// current call site is inside BuildUserHealthReport, BEFORE
// classifyUserPattern, so the classifier can consult the baseline.
func (s *Service) ComputePeerBaseline(ctx context.Context, upn string, from, to *time.Time) (*PeerBaseline, error) {
	if upn == "" {
		return nil, fmt.Errorf("%w: empty upn", ErrBadRequest)
	}
	now := time.Now().UTC()
	fromT := now.Add(-userHealthDefaultWindow)
	toT := now
	if from != nil {
		fromT = *from
	}
	if to != nil {
		toT = *to
	}
	if !fromT.Before(toT) {
		return nil, fmt.Errorf("%w: from must be before to", ErrBadRequest)
	}

	// Phase 1: target's calls and verdict map.
	targetCalls, err := s.ListUserCalls(ctx, upn, &fromT, &toT, maxListLimit, 0)
	if err != nil {
		return nil, err
	}
	if len(targetCalls) == 0 {
		// Nothing to compare against. Caller (BuildUserHealthReport)
		// treats nil as "no baseline attached" and skips the field.
		return nil, nil
	}

	targetCallIDs := make([]string, 0, len(targetCalls))
	targetVerdictByCall := make(map[string]string, len(targetCalls))
	var targetBad, targetTotal int
	for _, c := range targetCalls {
		targetCallIDs = append(targetCallIDs, c.CallId)
		targetVerdictByCall[c.CallId] = c.Verdict
		targetTotal++
		switch c.Verdict {
		case verdictBad, verdictPoor:
			targetBad++
		}
	}

	// Phase 2: target's stream rows → distinct subnets the target used.
	targetRows, err := s.streams.ListByUserInCalls(ctx, upn, targetCallIDs)
	if err != nil {
		return nil, fmt.Errorf("api: peer baseline: list target streams: %w", err)
	}
	rawSubnetSet := make(map[string]struct{})
	for _, r := range targetRows {
		if r.Subnet != "" {
			rawSubnetSet[r.Subnet] = struct{}{}
		}
	}
	if len(rawSubnetSet) == 0 {
		// Target has no subnet evidence in the window — we cannot define
		// a cohort. Treat as insufficient peers so the wire shape is
		// consistent rather than returning (nil, nil) which would look
		// identical to "no calls".
		return &PeerBaseline{
			TargetBadRatio:  ratio(targetBad, targetTotal),
			Assessment:      PeerAssessmentInsufficient,
			SubnetsCompared: []string{},
		}, nil
	}

	rawSubnets := make([]string, 0, len(rawSubnetSet))
	for sn := range rawSubnetSet {
		rawSubnets = append(rawSubnets, sn)
	}
	sort.Strings(rawSubnets)

	// Phase 3: the window's cohort callIds come from ListMetaInWindow,
	// matching the find_flaky_microphones two-phase pattern. This keeps
	// the streams query bounded by the calls-side time index.
	metas, err := s.calls.ListMetaInWindow(ctx, &fromT, &toT)
	if err != nil {
		return nil, fmt.Errorf("api: peer baseline: list calls in window: %w", err)
	}
	cohortCallIDs := make([]string, 0, len(metas))
	verdictByCall := make(map[string]string, len(metas))
	for _, m := range metas {
		cohortCallIDs = append(cohortCallIDs, m.CallId)
		verdictByCall[m.CallId] = m.Verdict
	}

	// Phase 4: peer stream rows in the window whose subnet is one the
	// target used. User filter happens in Go so we can skip the target
	// without an extra $ne clause.
	peerRows, err := s.streams.ListInWindowBySubnets(ctx, cohortCallIDs, rawSubnets)
	if err != nil {
		return nil, fmt.Errorf("api: peer baseline: list peer streams: %w", err)
	}

	// Phase 5: tally distinct (user, callId) pairs per verdict. A call
	// with 5 streams under the same user counts once.
	type pairKey struct{ user, callID string }
	seenPair := make(map[pairKey]struct{})
	cohortUsers := make(map[string]struct{})
	var cohortBad, cohortTotal int
	for _, r := range peerRows {
		if r.User == "" || r.User == upn {
			continue
		}
		k := pairKey{user: r.User, callID: r.CallId}
		if _, ok := seenPair[k]; ok {
			continue
		}
		seenPair[k] = struct{}{}
		cohortUsers[r.User] = struct{}{}
		cohortTotal++
		switch verdictByCall[r.CallId] {
		case verdictBad, verdictPoor:
			cohortBad++
		}
	}

	baseline := &PeerBaseline{
		CohortSize:      len(cohortUsers),
		CohortCalls:     cohortTotal,
		CohortBadRatio:  ratio(cohortBad, cohortTotal),
		TargetBadRatio:  ratio(targetBad, targetTotal),
		SubnetsCompared: rawSubnets,
	}

	if baseline.CohortSize < peerMinCohort {
		baseline.Assessment = PeerAssessmentInsufficient
		baseline.Delta = round1((baseline.TargetBadRatio - baseline.CohortBadRatio) * 100)
		return baseline, nil
	}

	delta := round1((baseline.TargetBadRatio - baseline.CohortBadRatio) * 100)
	baseline.Delta = delta
	baseline.Assessment = classifyPeerDelta(delta)
	return baseline, nil
}

// classifyPeerDelta maps a percentage-point delta to the coarse assessment
// bucket. Kept pure for table-driven tests.
func classifyPeerDelta(delta float64) string {
	switch {
	case delta < peerDeltaBetter:
		return PeerAssessmentBetter
	// delta >= -peerDeltaOnParAbs is always true here because case-1 already
	// consumed everything below -peerDeltaBetter (i.e. < -5.0).
	case delta <= peerDeltaOnParAbs:
		return PeerAssessmentOnPar
	case delta <= peerDeltaWorseMax:
		return PeerAssessmentWorse
	default:
		return PeerAssessmentMuchWorse
	}
}

// ratio returns num/den or 0 when den == 0, avoiding NaN on empty inputs.
func ratio(num, den int) float64 {
	if den == 0 {
		return 0
	}
	return float64(num) / float64(den)
}

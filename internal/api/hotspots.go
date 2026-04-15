package api

import (
	"context"
	"fmt"
	"sort"
	"time"

	"teams_con/internal/store"
)

// Phase 4 — bad network hotspots.
//
// FindNetworkHotspots complements find_flaky_microphones (per-device) and
// user_health_report (per-user) with a cross-cutting query: which
// (subnet, relayIp) pairs concentrate bad calls across an arbitrary time
// window? This is how the operator catches office-wide wifi rot and broken
// TURN relays that per-user tooling cannot surface.
//
// The implementation follows the same two-phase Mongo shape used by the
// flaky-mics and peer-baseline paths so the streams query never walks an
// unbounded window: first ListMetaInWindow → call verdict map, then
// ListInWindow(callIds) → projected stream rows, then a pure-Go
// aggregation. The SubnetResolver instance built in Phase 1 is reused for
// populating the friendly SubnetName — we never instantiate a second one.

// Hotspot is a single (bucket key → aggregated counts) finding emitted by
// FindNetworkHotspots. Subnet / RelayIp are populated according to the
// GroupBy mode the caller asked for; SubnetName is populated (when
// available) whenever Subnet is present so the LLM / operator can read
// "Xpanceo Dubai HQ wired" instead of the raw CIDR.
type Hotspot struct {
	Subnet        string   `json:"subnet,omitempty"`
	SubnetName    string   `json:"subnetName,omitempty"`
	RelayIp       string   `json:"relayIp,omitempty"`
	TotalCalls    int      `json:"totalCalls"`
	BadCalls      int      `json:"badCalls"`
	BadRatio      float64  `json:"badRatio"`
	DistinctUsers int      `json:"distinctUsers"`
	AvgRttMs      *float64 `json:"avgRttMs,omitempty"`
	AvgJitterMs   *float64 `json:"avgJitterMs,omitempty"`
	AvgLossPct    *float64 `json:"avgLossPct,omitempty"`
	SampleUsers   []string `json:"sampleUsers,omitempty"`
}

// HotspotsParams is the HTTP-free input to FindNetworkHotspots. Nil window
// bounds default to [now-7d, now]. Zero/negative thresholds are replaced by
// the hotspot* defaults. GroupBy must be one of the hotspotGroup* constants;
// anything else is rejected as ErrBadRequest.
type HotspotsParams struct {
	From        *time.Time
	To          *time.Time
	MinCalls    int
	MinBadRatio float64
	GroupBy     string
	Limit       int
}

// Defaults and bounds. Kept in one place so handlers / MCP tools never
// hard-code them and tests can reference the same constants.
const (
	hotspotDefaultWindow   = 7 * 24 * time.Hour
	hotspotDefaultMinCalls = 5
	hotspotDefaultMinBadR  = 0.30
	hotspotDefaultLimit    = 20
	hotspotMaxLimit        = 100
	hotspotSampleUsers     = 5

	// hotspotGroupSubnet groups purely by the stream row's subnet field.
	// hotspotGroupRelay groups purely by relayIp. hotspotGroupSubnetRelay
	// groups by the composite (subnet|relayIp) pair. Literal strings
	// (including the '+') are part of the wire contract — see
	// design-hotspots.md §Gotchas.
	hotspotGroupSubnet      = "subnet"
	hotspotGroupRelay       = "relay"
	hotspotGroupSubnetRelay = "subnet+relay"
)

// FindNetworkHotspots runs the Phase 4 aggregation. Empty windows and
// "nothing matched the thresholds" are NOT errors: the method returns an
// empty (non-nil) slice so the HTTP/MCP layer emits [] instead of null.
// Real failures (store boom, bad input) propagate wrapped sentinel errors.
func (s *Service) FindNetworkHotspots(ctx context.Context, p HotspotsParams) ([]Hotspot, error) {
	now := time.Now().UTC()
	from := now.Add(-hotspotDefaultWindow)
	to := now
	if p.From != nil {
		from = *p.From
	}
	if p.To != nil {
		to = *p.To
	}
	if !from.Before(to) {
		return nil, fmt.Errorf("%w: from must be before to", ErrBadRequest)
	}

	groupBy := p.GroupBy
	if groupBy == "" {
		groupBy = hotspotGroupSubnet
	}
	switch groupBy {
	case hotspotGroupSubnet, hotspotGroupRelay, hotspotGroupSubnetRelay:
	default:
		return nil, fmt.Errorf("%w: unknown group_by %q", ErrBadRequest, p.GroupBy)
	}

	minCalls := p.MinCalls
	if minCalls < 1 {
		minCalls = hotspotDefaultMinCalls
	}
	minBadRatio := p.MinBadRatio
	if minBadRatio <= 0 {
		minBadRatio = hotspotDefaultMinBadR
	}
	if minBadRatio > 1.0 {
		return nil, fmt.Errorf("%w: min_bad_ratio %.2f exceeds 1.0", ErrBadRequest, p.MinBadRatio)
	}
	limit := p.Limit
	if limit < 1 {
		limit = hotspotDefaultLimit
	}
	if limit > hotspotMaxLimit {
		limit = hotspotMaxLimit
	}

	metas, err := s.calls.ListMetaInWindow(ctx, &from, &to)
	if err != nil {
		return nil, fmt.Errorf("api: hotspots: list calls: %w", err)
	}
	if len(metas) == 0 {
		return []Hotspot{}, nil
	}
	callIDs := make([]string, len(metas))
	verdictByCall := make(map[string]string, len(metas))
	for i, m := range metas {
		callIDs[i] = m.CallId
		verdictByCall[m.CallId] = m.Verdict
	}

	rows, err := s.streams.ListInWindow(ctx, callIDs)
	if err != nil {
		return nil, fmt.Errorf("api: hotspots: list streams: %w", err)
	}

	out := groupHotspots(rows, verdictByCall, groupBy, minCalls, minBadRatio, limit)
	if len(out) == 0 {
		return []Hotspot{}, nil
	}

	// Resolver enrichment. Done AFTER grouping so we only resolve each
	// distinct bucket once instead of once per stream row. Skipped for
	// GroupBy=relay because the row has no subnet to resolve.
	if groupBy != hotspotGroupRelay && s.subnetResolver != nil {
		for i := range out {
			if out[i].Subnet == "" {
				continue
			}
			if e := s.subnetResolver.Resolve(ctx, out[i].Subnet); e != nil {
				out[i].SubnetName = e.Name
			}
		}
	}
	return out, nil
}

// hotspotBucket is the in-progress aggregation state for one (subnet|relay)
// key. Kept unexported because only groupHotspots uses it.
type hotspotBucket struct {
	subnet  string
	relayIp string

	// distinct callId set — BadCalls counts Bad|Poor parent verdicts on
	// DISTINCT callIds, not stream rows (one call with 5 stream rows
	// counts once).
	calls    map[string]struct{}
	badCalls map[string]struct{}

	users map[string]*hotspotUserAgg

	rttSum, rttN       float64
	jitSum, jitN       float64
	lossSum, lossN     float64
}

// hotspotUserAgg tracks per-user totals inside a bucket so SampleUsers can
// be ranked by the user's PERSONAL bad ratio within the bucket (not their
// global bad ratio). This matches design-hotspots.md §Gotchas.
type hotspotUserAgg struct {
	calls    map[string]struct{}
	badCalls map[string]struct{}
}

func (u *hotspotUserAgg) ratio() float64 {
	if len(u.calls) == 0 {
		return 0
	}
	return float64(len(u.badCalls)) / float64(len(u.calls))
}

// groupHotspots is the pure-Go aggregation. Split out so tests can drive
// it without constructing a Service or touching Mongo.
func groupHotspots(
	rows []store.StreamRow,
	verdictByCall map[string]string,
	groupBy string,
	minCalls int,
	minBadRatio float64,
	limit int,
) []Hotspot {
	type bucketKey struct {
		subnet  string
		relayIp string
	}

	buckets := make(map[bucketKey]*hotspotBucket)
	for _, row := range rows {
		var key bucketKey
		switch groupBy {
		case hotspotGroupSubnet:
			if row.Subnet == "" {
				continue
			}
			key.subnet = row.Subnet
		case hotspotGroupRelay:
			if row.RelayIp == "" {
				continue
			}
			key.relayIp = row.RelayIp
		case hotspotGroupSubnetRelay:
			if row.Subnet == "" || row.RelayIp == "" {
				continue
			}
			key.subnet = row.Subnet
			key.relayIp = row.RelayIp
		default:
			continue
		}

		b, ok := buckets[key]
		if !ok {
			b = &hotspotBucket{
				subnet:   key.subnet,
				relayIp:  key.relayIp,
				calls:    make(map[string]struct{}),
				badCalls: make(map[string]struct{}),
				users:    make(map[string]*hotspotUserAgg),
			}
			buckets[key] = b
		}

		b.calls[row.CallId] = struct{}{}
		isBad := false
		switch verdictByCall[row.CallId] {
		case verdictBad, verdictPoor:
			isBad = true
		}
		if isBad {
			b.badCalls[row.CallId] = struct{}{}
		}

		if isRealUpn(row.User) {
			u, ok := b.users[row.User]
			if !ok {
				u = &hotspotUserAgg{
					calls:    make(map[string]struct{}),
					badCalls: make(map[string]struct{}),
				}
				b.users[row.User] = u
			}
			u.calls[row.CallId] = struct{}{}
			if isBad {
				u.badCalls[row.CallId] = struct{}{}
			}
		}

		if row.AvgRttMs != nil {
			b.rttSum += *row.AvgRttMs
			b.rttN++
		}
		if row.AvgJitterMs != nil {
			b.jitSum += *row.AvgJitterMs
			b.jitN++
		}
		if row.AvgLossPct != nil {
			b.lossSum += *row.AvgLossPct
			b.lossN++
		}
	}

	out := make([]Hotspot, 0, len(buckets))
	for _, b := range buckets {
		total := len(b.calls)
		if total < minCalls {
			continue
		}
		bad := len(b.badCalls)
		ratio := 0.0
		if total > 0 {
			ratio = float64(bad) / float64(total)
		}
		if ratio < minBadRatio {
			continue
		}

		h := Hotspot{
			Subnet:        b.subnet,
			RelayIp:       b.relayIp,
			TotalCalls:    total,
			BadCalls:      bad,
			BadRatio:      ratio,
			DistinctUsers: len(b.users),
			AvgRttMs:      avgPtr(b.rttSum, b.rttN),
			AvgJitterMs:   avgPtr(b.jitSum, b.jitN),
			AvgLossPct:    avgPtr(b.lossSum, b.lossN),
			SampleUsers:   topBadUsers(b.users, hotspotSampleUsers),
		}
		out = append(out, h)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].BadRatio != out[j].BadRatio {
			return out[i].BadRatio > out[j].BadRatio
		}
		if out[i].BadCalls != out[j].BadCalls {
			return out[i].BadCalls > out[j].BadCalls
		}
		// Deterministic final tiebreak on the bucket key so test output is
		// stable across map-iteration ordering.
		ki := out[i].Subnet + "|" + out[i].RelayIp
		kj := out[j].Subnet + "|" + out[j].RelayIp
		return ki < kj
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// topBadUsers returns up to n user UPNs sorted desc by their personal bad
// ratio inside the bucket, with (bad count, UPN) as the tiebreaker. Users
// with zero bad calls are excluded — the list exists to give the operator
// "who to talk to first", not a dump of everyone on the subnet.
func topBadUsers(users map[string]*hotspotUserAgg, n int) []string {
	if len(users) == 0 || n <= 0 {
		return nil
	}
	type entry struct {
		upn   string
		ratio float64
		bad   int
	}
	entries := make([]entry, 0, len(users))
	for upn, u := range users {
		if len(u.badCalls) == 0 {
			continue
		}
		entries = append(entries, entry{upn: upn, ratio: u.ratio(), bad: len(u.badCalls)})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].ratio != entries[j].ratio {
			return entries[i].ratio > entries[j].ratio
		}
		if entries[i].bad != entries[j].bad {
			return entries[i].bad > entries[j].bad
		}
		return entries[i].upn < entries[j].upn
	})
	if len(entries) > n {
		entries = entries[:n]
	}
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.upn
	}
	return out
}

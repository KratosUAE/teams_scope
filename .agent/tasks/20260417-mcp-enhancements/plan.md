# MCP Tools Enhancement Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add three context-saving MCP tools: daily_quality_summary, min_participants filter on list_calls, and relay geo-enrichment with ipinfo.io cache.

**Architecture:** Three independent features sharing the existing MCP tool → api.Service → store.Repo pattern. Feature 2 is a one-line filter addition. Feature 1 adds a new Mongo aggregate. Feature 3 adds a new collection + HTTP resolver + slimStream field. No crawler changes.

**Tech Stack:** Go 1.23+, MongoDB (go.mongodb.org/mongo-driver), mcp-go SDK (github.com/mark3labs/mcp-go), ipinfo.io REST API.

**Spec:** `.agent/tasks/20260417-mcp-enhancements/spec.md`

---

### Task 1: `min_participants` filter on `list_calls`

Smallest change — one new parameter, one filter clause. Ship first.

**Files:**
- Modify: `internal/store/calls.go` — add MinParticipants to CallListParams + buildCallFilter
- Modify: `internal/store/calls_test.go` — test the filter
- Modify: `internal/api/service.go` — add MinParticipants to ListCallsParams, pass through
- Modify: `internal/mcp/tools.go` — add parameter to list_calls registration + handler

- [ ] **Step 1: Add MinParticipants to store.CallListParams**

In `internal/store/calls.go`, add field to `CallListParams`:

```go
type CallListParams struct {
	From            *time.Time
	To              *time.Time
	Verdict         *string
	Upn             *string
	MinParticipants int // 0 = disabled
	Limit           int
	Offset          int
}
```

- [ ] **Step 2: Add filter clause to buildCallFilter**

In `internal/store/calls.go`, inside `buildCallFilter`, after the Upn block add:

```go
if p.MinParticipants > 0 {
	filter = append(filter, bson.E{Key: "participantCount", Value: bson.D{{Key: "$gte", Value: p.MinParticipants}}})
}
```

- [ ] **Step 3: Write test for min_participants filter**

In `internal/store/calls_test.go`, add test verifying the filter is applied. Follow existing test patterns in that file (check `buildCallFilter` output or do integration test with fake data depending on what's there).

- [ ] **Step 4: Add MinParticipants to api.ListCallsParams and pass through**

In `internal/api/service.go`, add `MinParticipants int` to `ListCallsParams` struct and copy it to `store.CallListParams` in the `ListCalls` method.

- [ ] **Step 5: Add MCP parameter and wire to handler**

In `internal/mcp/tools.go`:

In `registerTools()`, add to the `list_calls` tool registration:
```go
mcpsdk.WithNumber("min_participants", mcpsdk.Description("Only return calls with at least this many participants (default 0 = disabled)")),
```

In `handleListCalls`, extract and pass:
```go
p.MinParticipants = req.GetInt("min_participants", 0)
```

- [ ] **Step 6: Run tests and build**

```bash
go test ./internal/store/ ./internal/api/ ./internal/mcp/ -count=1 -timeout 30s
go build ./...
go vet ./...
```

- [ ] **Step 7: Commit**

```bash
git add internal/store/calls.go internal/store/calls_test.go internal/api/service.go internal/mcp/tools.go
git commit -m "feat(mcp): add min_participants filter to list_calls tool"
```

---

### Task 2: `daily_quality_summary` — store layer

New aggregate queries on calls and streams collections.

**Files:**
- Create: `internal/store/daily_summary.go` — DailySummaryRepo with aggregate methods
- Create: `internal/store/daily_summary_test.go` — tests
- Modify: `internal/store/client.go` — add DailySummary repo to Client

- [ ] **Step 1: Define DaySummary type**

Create `internal/store/daily_summary.go`:

```go
package store

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// DaySummary is the per-day quality aggregate returned by DailySummaryRepo.
type DaySummary struct {
	Date            string  `bson:"date"            json:"date"`            // "2026-04-16"
	Calls           int     `bson:"calls"           json:"calls"`
	Good            int     `bson:"good"            json:"good"`
	Poor            int     `bson:"poor"            json:"poor"`
	Bad             int     `bson:"bad"             json:"bad"`
	GroupCalls      int     `bson:"groupCalls"      json:"groupCalls"`
	P2PCalls        int     `bson:"p2pCalls"        json:"p2pCalls"`
	StreamsWithLoss int     `bson:"streamsWithLoss" json:"streamsWithLoss"`
	Over30Pct       int     `bson:"over30pct"       json:"streamsOver30pct"`
	Over60Pct       int     `bson:"over60pct"       json:"streamsOver60pct"`
	Over90Pct       int     `bson:"over90pct"       json:"streamsOver90pct"`
	PeakLossMaxPct  float64 `bson:"peakLossMaxPct"  json:"peakLossMaxPct"`
}
```

- [ ] **Step 2: Implement DailySummaryRepo with call aggregate**

In same file, add repo struct and call-side aggregate:

```go
type DailySummaryRepo struct {
	calls   *mongo.Collection
	streams *mongo.Collection
}

func newDailySummaryRepo(db *mongo.Database) *DailySummaryRepo {
	return &DailySummaryRepo{
		calls:   db.Collection("calls"),
		streams: db.Collection("streams"),
	}
}

// callDaySummary is the intermediate shape from the calls aggregate.
type callDaySummary struct {
	Date       string `bson:"_id"`
	Calls      int    `bson:"calls"`
	Good       int    `bson:"good"`
	Poor       int    `bson:"poor"`
	Bad        int    `bson:"bad"`
	GroupCalls int    `bson:"groupCalls"`
	P2PCalls   int    `bson:"p2pCalls"`
}

func (r *DailySummaryRepo) aggregateCalls(ctx context.Context, from, to time.Time) (map[string]callDaySummary, error) {
	pipeline := bson.A{
		bson.D{{Key: "$match", Value: bson.D{
			{Key: "startTimeUtc", Value: bson.D{
				{Key: "$gte", Value: from},
				{Key: "$lt", Value: to},
			}},
		}}},
		bson.D{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: bson.D{{Key: "$dateToString", Value: bson.D{
				{Key: "format", Value: "%Y-%m-%d"},
				{Key: "date", Value: "$startTimeUtc"},
			}}}},
			{Key: "calls", Value: bson.D{{Key: "$sum", Value: 1}}},
			{Key: "good", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$cond", Value: bson.A{
				bson.D{{Key: "$eq", Value: bson.A{"$verdict", "Good"}}}, 1, 0,
			}}}}}},
			{Key: "poor", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$cond", Value: bson.A{
				bson.D{{Key: "$eq", Value: bson.A{"$verdict", "Poor"}}}, 1, 0,
			}}}}}},
			{Key: "bad", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$cond", Value: bson.A{
				bson.D{{Key: "$eq", Value: bson.A{"$verdict", "Bad"}}}, 1, 0,
			}}}}}},
			{Key: "groupCalls", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$cond", Value: bson.A{
				bson.D{{Key: "$eq", Value: bson.A{"$callType", "groupCall"}}}, 1, 0,
			}}}}}},
			{Key: "p2pCalls", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$cond", Value: bson.A{
				bson.D{{Key: "$eq", Value: bson.A{"$callType", "peerToPeer"}}}, 1, 0,
			}}}}}},
		}}},
		bson.D{{Key: "$sort", Value: bson.D{{Key: "_id", Value: 1}}}},
	}

	cursor, err := r.calls.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	out := make(map[string]callDaySummary)
	for cursor.Next(ctx) {
		var row callDaySummary
		if err := cursor.Decode(&row); err != nil {
			return nil, err
		}
		out[row.Date] = row
	}
	return out, cursor.Err()
}
```

- [ ] **Step 3: Add stream-side aggregate**

In same file:

```go
// streamDaySummary is the intermediate shape from the streams aggregate.
type streamDaySummary struct {
	Date            string  `bson:"_id"`
	StreamsWithLoss int     `bson:"streamsWithLoss"`
	Over30Pct       int     `bson:"over30pct"`
	Over60Pct       int     `bson:"over60pct"`
	Over90Pct       int     `bson:"over90pct"`
	PeakLossMaxPct  float64 `bson:"peakLossMaxPct"`
}

func (r *DailySummaryRepo) aggregateStreams(ctx context.Context, from, to time.Time) (map[string]streamDaySummary, error) {
	pipeline := bson.A{
		bson.D{{Key: "$match", Value: bson.D{
			{Key: "segmentStart", Value: bson.D{
				{Key: "$gte", Value: from},
				{Key: "$lt", Value: to},
			}},
			{Key: "maxLossPct", Value: bson.D{{Key: "$gt", Value: 0}}},
		}}},
		bson.D{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: bson.D{{Key: "$dateToString", Value: bson.D{
				{Key: "format", Value: "%Y-%m-%d"},
				{Key: "date", Value: "$segmentStart"},
			}}}},
			{Key: "streamsWithLoss", Value: bson.D{{Key: "$sum", Value: 1}}},
			{Key: "over30pct", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$cond", Value: bson.A{
				bson.D{{Key: "$gt", Value: bson.A{"$maxLossPct", 30}}}, 1, 0,
			}}}}}},
			{Key: "over60pct", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$cond", Value: bson.A{
				bson.D{{Key: "$gt", Value: bson.A{"$maxLossPct", 60}}}, 1, 0,
			}}}}}},
			{Key: "over90pct", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$cond", Value: bson.A{
				bson.D{{Key: "$gt", Value: bson.A{"$maxLossPct", 90}}}, 1, 0,
			}}}}}},
			{Key: "peakLossMaxPct", Value: bson.D{{Key: "$max", Value: "$maxLossPct"}}},
		}}},
		bson.D{{Key: "$sort", Value: bson.D{{Key: "_id", Value: 1}}}},
	}

	cursor, err := r.streams.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	out := make(map[string]streamDaySummary)
	for cursor.Next(ctx) {
		var row streamDaySummary
		if err := cursor.Decode(&row); err != nil {
			return nil, err
		}
		out[row.Date] = row
	}
	return out, cursor.Err()
}
```

- [ ] **Step 4: Add public Summary method that merges both**

```go
// Summary returns per-day quality data for [from, to). Days with no
// calls are included with zero values so the consumer gets a contiguous
// series.
func (r *DailySummaryRepo) Summary(ctx context.Context, from, to time.Time) ([]DaySummary, error) {
	callMap, err := r.aggregateCalls(ctx, from, to)
	if err != nil {
		return nil, fmt.Errorf("daily summary calls: %w", err)
	}
	streamMap, err := r.aggregateStreams(ctx, from, to)
	if err != nil {
		return nil, fmt.Errorf("daily summary streams: %w", err)
	}

	// Build contiguous day series.
	var out []DaySummary
	for d := from; d.Before(to); d = d.AddDate(0, 0, 1) {
		key := d.Format("2006-01-02")
		ds := DaySummary{Date: key}
		if c, ok := callMap[key]; ok {
			ds.Calls = c.Calls
			ds.Good = c.Good
			ds.Poor = c.Poor
			ds.Bad = c.Bad
			ds.GroupCalls = c.GroupCalls
			ds.P2PCalls = c.P2PCalls
		}
		if s, ok := streamMap[key]; ok {
			ds.StreamsWithLoss = s.StreamsWithLoss
			ds.Over30Pct = s.Over30Pct
			ds.Over60Pct = s.Over60Pct
			ds.Over90Pct = s.Over90Pct
			ds.PeakLossMaxPct = math.Round(s.PeakLossMaxPct*10) / 10
		}
		out = append(out, ds)
	}
	return out, nil
}
```

Add `"fmt"` and `"math"` to imports.

- [ ] **Step 5: Wire DailySummaryRepo into store.Client**

In `internal/store/client.go`, add field to `Client` struct:

```go
DailySummary *DailySummaryRepo
```

In the `New` constructor, after other repo init:

```go
DailySummary: newDailySummaryRepo(db),
```

- [ ] **Step 6: Run build**

```bash
go build ./internal/store/...
```

- [ ] **Step 7: Commit store layer**

```bash
git add internal/store/daily_summary.go internal/store/client.go
git commit -m "feat(store): add DailySummaryRepo with per-day quality aggregation"
```

---

### Task 3: `daily_quality_summary` — service + MCP tool

Wire the store aggregate into the MCP tool surface.

**Files:**
- Modify: `internal/api/service.go` — add DailySummary method + params
- Create: `internal/mcp/daily_summary.go` — handler + summary formatter
- Modify: `internal/mcp/tools.go` — register tool

- [ ] **Step 1: Add service method**

In `internal/api/service.go`, add params struct and method:

```go
type DailySummaryParams struct {
	From time.Time
	To   time.Time
}

func (s *Service) DailySummary(ctx context.Context, p DailySummaryParams) ([]store.DaySummary, error) {
	// Clamp window to max 30 days.
	maxWindow := 30 * 24 * time.Hour
	if p.To.Sub(p.From) > maxWindow {
		p.From = p.To.Add(-maxWindow)
	}
	return s.st.DailySummary.Summary(ctx, p.From, p.To)
}
```

Ensure `s.st` has access to DailySummary repo (check how other repos are accessed — likely `s.st.DailySummary` or through a method).

- [ ] **Step 2: Create MCP handler file**

Create `internal/mcp/daily_summary.go`:

```go
package mcp

import (
	"context"
	"fmt"
	"time"

	"teams_con/internal/api"
	"teams_con/internal/store"

	mcpsdk "github.com/mark3labs/mcp-go/mcp"
)

func (s *Server) handleDailySummary(ctx context.Context, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	now := time.Now().UTC()
	defaultFrom := now.AddDate(0, 0, -7)

	from, _ := parseTimeParam(req, "from")
	to, _ := parseTimeParam(req, "to")
	if from.IsZero() {
		from = defaultFrom
	}
	if to.IsZero() {
		to = now
	}

	rows, err := s.svc.DailySummary(ctx, api.DailySummaryParams{From: from, To: to})
	if err != nil {
		return s.mapServiceErr(err), nil
	}

	return textAndJSON(summarizeDailySummary(rows, from, to), rows), nil
}

func summarizeDailySummary(rows []store.DaySummary, from, to time.Time) string {
	days := len(rows)
	var totalCalls, totalBad, totalOver90 int
	var peak float64
	for _, r := range rows {
		totalCalls += r.Calls
		totalBad += r.Bad
		totalOver90 += r.Over90Pct
		if r.PeakLossMaxPct > peak {
			peak = r.PeakLossMaxPct
		}
	}
	return fmt.Sprintf("%d days, %s to %s: %d calls, %d bad, %d streams >90%% lossMax, peak %.1f%%",
		days, from.Format("2006-01-02"), to.Format("2006-01-02"),
		totalCalls, totalBad, totalOver90, peak)
}
```

- [ ] **Step 3: Register the tool**

In `internal/mcp/tools.go`, in `registerTools()`, add:

```go
s.m.AddTool(
	mcpsdk.NewTool("daily_quality_summary",
		mcpsdk.WithDescription("Per-day quality trend: call counts by verdict, lossMax distribution (streams >30%/>60%/>90%), peak lossMax. Default last 7 days, max 30 days."),
		mcpsdk.WithString("from", mcpsdk.Description("RFC3339 lower bound, optional (default now-7d)")),
		mcpsdk.WithString("to", mcpsdk.Description("RFC3339 upper bound, optional (default now)")),
	),
	s.handleDailySummary,
)
```

- [ ] **Step 4: Run tests and build**

```bash
go build ./...
go vet ./...
go test ./internal/mcp/ -count=1 -timeout 30s
```

- [ ] **Step 5: Commit**

```bash
git add internal/api/service.go internal/mcp/daily_summary.go internal/mcp/tools.go
git commit -m "feat(mcp): add daily_quality_summary tool"
```

---

### Task 4: Relay geo — store layer

New collection + repo for relay IP geolocation cache.

**Files:**
- Create: `internal/store/relay_geo.go` — RelayGeo type + RelayGeoRepo
- Modify: `internal/store/types.go` — add RelayGeo struct
- Modify: `internal/store/client.go` — add RelayGeo repo to Client

- [ ] **Step 1: Add RelayGeo type**

In `internal/store/types.go`, add:

```go
// RelayGeo caches the geolocation of a Microsoft Transport Relay IP.
// Keyed by IP string. Resolved via ipinfo.io, cached indefinitely
// (relay IPs are static datacenter addresses).
type RelayGeo struct {
	IP         string    `bson:"_id"         json:"ip"`
	City       string    `bson:"city"        json:"city"`
	Country    string    `bson:"country"     json:"country"`
	ResolvedAt time.Time `bson:"resolvedAt"  json:"resolvedAt"`
}
```

- [ ] **Step 2: Create RelayGeoRepo**

Create `internal/store/relay_geo.go`:

```go
package store

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// RelayGeoRepo manages the relay_geo collection — a cache of relay IP
// to city/country mappings resolved via ipinfo.io.
type RelayGeoRepo struct {
	coll *mongo.Collection
}

func newRelayGeoRepo(db *mongo.Database) *RelayGeoRepo {
	return &RelayGeoRepo{coll: db.Collection("relay_geo")}
}

// Get returns the cached geo for a single relay IP, or nil if not cached.
func (r *RelayGeoRepo) Get(ctx context.Context, ip string) (*RelayGeo, error) {
	var geo RelayGeo
	err := r.coll.FindOne(ctx, bson.D{{Key: "_id", Value: ip}}).Decode(&geo)
	if err == mongo.ErrNoDocuments {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &geo, nil
}

// GetMany returns cached geos for multiple IPs. Missing IPs are absent
// from the returned map (not an error).
func (r *RelayGeoRepo) GetMany(ctx context.Context, ips []string) (map[string]RelayGeo, error) {
	if len(ips) == 0 {
		return nil, nil
	}
	cursor, err := r.coll.Find(ctx, bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: ips}}}})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	out := make(map[string]RelayGeo, len(ips))
	for cursor.Next(ctx) {
		var geo RelayGeo
		if err := cursor.Decode(&geo); err != nil {
			return nil, err
		}
		out[geo.IP] = geo
	}
	return out, cursor.Err()
}

// Upsert inserts or replaces a geo entry. ResolvedAt is set to now.
func (r *RelayGeoRepo) Upsert(ctx context.Context, geo RelayGeo) error {
	geo.ResolvedAt = time.Now().UTC()
	opts := options.Replace().SetUpsert(true)
	_, err := r.coll.ReplaceOne(ctx, bson.D{{Key: "_id", Value: geo.IP}}, geo, opts)
	return err
}
```

- [ ] **Step 3: Wire into store.Client**

In `internal/store/client.go`, add field:

```go
RelayGeo *RelayGeoRepo
```

In constructor:

```go
RelayGeo: newRelayGeoRepo(db),
```

- [ ] **Step 4: Run build**

```bash
go build ./internal/store/...
```

- [ ] **Step 5: Commit**

```bash
git add internal/store/types.go internal/store/relay_geo.go internal/store/client.go
git commit -m "feat(store): add RelayGeoRepo for relay IP geolocation cache"
```

---

### Task 5: Relay geo — resolver

HTTP resolver that checks Mongo cache first, falls back to ipinfo.io.

**Files:**
- Create: `internal/geo/resolver.go` — Resolver struct with Resolve/ResolveMany
- Create: `internal/geo/resolver_test.go` — tests with fake HTTP + fake store

- [ ] **Step 1: Create resolver**

Create `internal/geo/resolver.go`:

```go
package geo

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"teams_con/internal/store"
)

// Resolver resolves relay IPs to city/country, caching results in Mongo.
type Resolver struct {
	repo   *store.RelayGeoRepo
	httpc  *http.Client
	log    *slog.Logger
}

// New builds a Resolver. The HTTP client has a 3-second timeout to avoid
// blocking call detail responses on slow ipinfo lookups.
func New(repo *store.RelayGeoRepo, log *slog.Logger) *Resolver {
	if log == nil {
		log = slog.Default()
	}
	return &Resolver{
		repo:  repo,
		httpc: &http.Client{Timeout: 3 * time.Second},
		log:   log,
	}
}

// ipInfoResponse is the subset of ipinfo.io JSON we care about.
type ipInfoResponse struct {
	City    string `json:"city"`
	Country string `json:"country"`
}

// Resolve returns "City, CC" for a single IP. On any error returns "".
func (r *Resolver) Resolve(ctx context.Context, ip string) string {
	if ip == "" {
		return ""
	}
	// Check cache.
	cached, err := r.repo.Get(ctx, ip)
	if err != nil {
		r.log.Warn("geo: cache get failed", "ip", ip, "err", err)
	}
	if cached != nil {
		return formatGeo(cached.City, cached.Country)
	}
	// Fetch from ipinfo.io.
	city, country, err := r.fetchIPInfo(ctx, ip)
	if err != nil {
		r.log.Warn("geo: ipinfo fetch failed", "ip", ip, "err", err)
		return ""
	}
	// Cache result.
	if err := r.repo.Upsert(ctx, store.RelayGeo{IP: ip, City: city, Country: country}); err != nil {
		r.log.Warn("geo: cache upsert failed", "ip", ip, "err", err)
	}
	return formatGeo(city, country)
}

// ResolveMany resolves multiple IPs, returning a map[ip]"City, CC".
// Batch-reads the cache first, then resolves misses individually.
func (r *Resolver) ResolveMany(ctx context.Context, ips []string) map[string]string {
	if len(ips) == 0 {
		return nil
	}
	// Dedupe.
	unique := make(map[string]struct{}, len(ips))
	for _, ip := range ips {
		if ip != "" {
			unique[ip] = struct{}{}
		}
	}
	keys := make([]string, 0, len(unique))
	for ip := range unique {
		keys = append(keys, ip)
	}

	// Batch cache lookup.
	cached, err := r.repo.GetMany(ctx, keys)
	if err != nil {
		r.log.Warn("geo: batch cache get failed", "err", err)
		cached = make(map[string]store.RelayGeo)
	}

	out := make(map[string]string, len(keys))
	for _, ip := range keys {
		if g, ok := cached[ip]; ok {
			out[ip] = formatGeo(g.City, g.Country)
			continue
		}
		// Cache miss — resolve individually.
		out[ip] = r.Resolve(ctx, ip)
	}
	return out
}

func (r *Resolver) fetchIPInfo(ctx context.Context, ip string) (city, country string, err error) {
	url := fmt.Sprintf("https://ipinfo.io/%s/json", ip)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", err
	}
	resp, err := r.httpc.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("ipinfo: status %d", resp.StatusCode)
	}
	var info ipInfoResponse
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return "", "", err
	}
	return info.City, info.Country, nil
}

func formatGeo(city, country string) string {
	if city == "" && country == "" {
		return ""
	}
	if city == "" {
		return country
	}
	if country == "" {
		return city
	}
	return city + ", " + country
}
```

- [ ] **Step 2: Write tests**

Create `internal/geo/resolver_test.go`:

```go
package geo

import (
	"testing"
)

func TestFormatGeo(t *testing.T) {
	tests := []struct {
		city, country, want string
	}{
		{"Dubai", "AE", "Dubai, AE"},
		{"", "AE", "AE"},
		{"Dubai", "", "Dubai"},
		{"", "", ""},
	}
	for _, tt := range tests {
		got := formatGeo(tt.city, tt.country)
		if got != tt.want {
			t.Errorf("formatGeo(%q, %q) = %q, want %q", tt.city, tt.country, got, tt.want)
		}
	}
}
```

- [ ] **Step 3: Run tests and build**

```bash
go test ./internal/geo/ -count=1 -timeout 30s
go build ./internal/geo/...
```

- [ ] **Step 4: Commit**

```bash
git add internal/geo/resolver.go internal/geo/resolver_test.go
git commit -m "feat(geo): add relay IP geo resolver with ipinfo.io + Mongo cache"
```

---

### Task 6: Relay geo — MCP integration

Wire resolver into slim stream output and MCP server initialization.

**Files:**
- Modify: `internal/mcp/slim.go` — add RelayGeo field to slimStream, update toSlimStreams signature
- Modify: `internal/mcp/server.go` — add geo.Resolver to Server struct
- Modify: `internal/mcp/tools.go` — pass geo map in get_call and summarize_call handlers
- Modify: `cmd/mcp.go` — initialize geo.Resolver

- [ ] **Step 1: Add RelayGeo to slimStream**

In `internal/mcp/slim.go`, add field to `slimStream`:

```go
RelayGeo      string     `json:"relayGeo,omitempty"`
```

Add after `RelayPort`.

- [ ] **Step 2: Update toSlimStreams to accept geo map**

Change signature and add geo enrichment:

```go
func toSlimStreams(rows []store.StreamRow, geoMap map[string]string) []slimStream {
	out := make([]slimStream, 0, len(rows))
	for _, r := range rows {
		s := slimStream{
			// ... all existing fields unchanged ...
		}
		if geoMap != nil && r.RelayIp != "" {
			s.RelayGeo = geoMap[r.RelayIp]
		}
		out = append(out, s)
	}
	return out
}
```

- [ ] **Step 3: Add geo.Resolver to Server**

In `internal/mcp/server.go`, add field to `Server` struct:

```go
geo *geo.Resolver  // nil-safe: if nil, geo enrichment is skipped
```

Update `NewServer` to accept resolver:

```go
func NewServer(svc *api.Service, geoResolver *geo.Resolver, log *slog.Logger) *Server {
	// ... existing code ...
	s := &Server{svc: svc, geo: geoResolver, log: log, m: m}
	// ...
}
```

- [ ] **Step 4: Add helper to collect relay IPs and resolve**

In `internal/mcp/slim.go`, add:

```go
func (s *Server) resolveRelayGeo(ctx context.Context, rows []store.StreamRow) map[string]string {
	if s.geo == nil {
		return nil
	}
	ips := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.RelayIp != "" {
			ips = append(ips, r.RelayIp)
		}
	}
	return s.geo.ResolveMany(ctx, ips)
}
```

- [ ] **Step 5: Wire geo into get_call and summarize_call handlers**

In `internal/mcp/tools.go`, find `handleGetCall` — where it calls `toSlimStreams(streams)`, change to:

```go
geoMap := s.resolveRelayGeo(ctx, streams)
slim := toSlimStreams(streams, geoMap)
```

Same for `handleSummarizeCall` if it uses `toSlimStreams`.

Find all other callers of `toSlimStreams` and update to pass `nil` for geo map (cascades, etc.):

```go
toSlimStreams(rows, nil)
```

- [ ] **Step 6: Update cmd/mcp.go**

In `cmd/mcp.go`, after `svc := api.NewService(st, log)`:

```go
import "teams_con/internal/geo"

geoResolver := geo.New(st.RelayGeo, log)
srv := mcpsrv.NewServer(svc, geoResolver, log)
```

- [ ] **Step 7: Run build and tests**

```bash
go build ./...
go vet ./...
go test ./... -count=1 -timeout 60s
```

- [ ] **Step 8: Commit**

```bash
git add internal/mcp/slim.go internal/mcp/server.go internal/mcp/tools.go cmd/mcp.go
git commit -m "feat(mcp): add relay geo enrichment to get_call and summarize_call"
```

---

### Task 7: Integration test and final verification

**Files:**
- Modify: `internal/mcp/tools_test.go` — add tests for new/modified tools

- [ ] **Step 1: Run full test suite**

```bash
go test ./... -count=1 -timeout 60s
```

Fix any failures.

- [ ] **Step 2: Run build and vet**

```bash
go build ./...
go vet ./...
```

- [ ] **Step 3: Final commit if any fixes**

```bash
git add -A
git commit -m "test: integration fixes for MCP enhancements"
```

- [ ] **Step 4: Tag release**

```bash
git tag -a v1.2.4 -m "Release v1.2.4"
```

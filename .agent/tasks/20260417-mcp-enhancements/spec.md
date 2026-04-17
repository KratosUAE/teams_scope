# MCP Tools Enhancement: Context-Saving Analytics

## Problem

During call quality analysis sessions, the AI agent repeatedly falls back to raw MongoDB queries (`mongosh`) because the MCP tool surface lacks three capabilities:

1. **No per-day trend data** — analyzing lossMax patterns over time requires hand-crafted aggregation pipelines (20+ lines of mongosh), polluting context with raw JSON.
2. **No participant count filter** — finding large conferences requires fetching all calls and scanning manually.
3. **No relay geolocation** — every call analysis triggers 3-5 `curl ipinfo.io` calls to identify relay locations (Dubai vs Amsterdam vs Sweden).

Each workaround costs 500-2000 tokens of context per invocation. Over a typical analysis session this adds up to 10-15k wasted tokens.

## Solution

Three additions to the MCP tool surface, all read-only, no crawler changes.

---

### Feature 1: `daily_quality_summary` tool

**Purpose:** One-call replacement for the per-day aggregation pipeline.

**Parameters:**
| Name | Type | Required | Default | Description |
|------|------|----------|---------|-------------|
| `from` | RFC3339 string | no | now - 7d | Inclusive lower bound |
| `to` | RFC3339 string | no | now | Exclusive upper bound |

Window clamped to max 30 days (Graph retention boundary — data beyond 30d may have gaps).

**Response shape (JSON array, one entry per calendar day UTC):**
```json
[
  {
    "date": "2026-04-16",
    "calls": 36,
    "good": 20,
    "poor": 5,
    "bad": 11,
    "groupCalls": 12,
    "p2pCalls": 24,
    "streamsWithLoss": 98,
    "streamsOver30pct": 38,
    "streamsOver60pct": 30,
    "streamsOver90pct": 16,
    "peakLossMaxPct": 99.5
  }
]
```

**Text header:** `"20 days, 2026-03-28 to 2026-04-16: 450 calls, peak lossMax 99.5%"`

**Implementation:**
- New handler function in `internal/mcp/tools.go` (or new file `daily_summary.go` if tools.go is large).
- Two MongoDB aggregates:
  1. `calls` collection: `$group` by date truncated from `startTimeUtc`, count by verdict and callType.
  2. `streams` collection: `$match` `maxLossPct > 0`, `$group` by date from `segmentStart`, compute `$sum` conditionals for >30/>60/>90 thresholds, `$max` for peak.
- Merge results in Go by date key, fill missing days with zeros.
- Register tool in MCP tool list with schema.

---

### Feature 2: `min_participants` filter on `list_calls`

**Purpose:** Filter calls by minimum participant count without client-side scanning.

**Parameters (addition to existing):**
| Name | Type | Required | Default | Description |
|------|------|----------|---------|-------------|
| `min_participants` | int | no | 0 (disabled) | Only return calls with `participantCount >= N` |

**Implementation:**
- In the existing `list_calls` handler, add `bson.E{Key: "participantCount", Value: bson.D{{Key: "$gte", Value: minParticipants}}}` to the filter when `min_participants > 0`.
- Update tool schema to include the new parameter.
- No store layer changes — filter applied at query level.

---

### Feature 3: Relay geo-enrichment

**Purpose:** Automatically resolve relay IP → city/country so the agent doesn't need to call ipinfo.io.

#### 3a. `relay_geo` Mongo collection

```
{
  _id: "52.112.207.190",     // relay IP as string key
  city: "Dubai",
  country: "AE",
  resolvedAt: ISODate(...)   // for TTL / staleness
}
```

Index: `_id` (default). No TTL index — relay geo is effectively static (MS datacenter IPs don't move).

#### 3b. Store layer

New file `internal/store/relay_geo.go`:
- `type RelayGeo struct` with bson/json tags.
- `type RelayGeoRepo struct` with methods:
  - `Get(ctx, ip string) (*RelayGeo, error)` — single lookup
  - `GetMany(ctx, ips []string) (map[string]RelayGeo, error)` — batch lookup for get_call
  - `Upsert(ctx, geo RelayGeo) error` — cache write

#### 3c. Geo resolver

New file `internal/geo/resolver.go`:
- `type Resolver struct` with `store.RelayGeoRepo` + HTTP client.
- `Resolve(ctx, ip string) (city, country string, err error)`:
  1. Check Mongo cache via `repo.Get(ip)`.
  2. Cache hit → return immediately.
  3. Cache miss → `GET https://ipinfo.io/{ip}/json`, parse `city` + `country`, upsert to Mongo, return.
- `ResolveMany(ctx, ips []string) (map[string]RelayGeo, error)`:
  1. Batch lookup via `repo.GetMany(ips)`.
  2. For each miss → resolve individually (sequential, ipinfo has no batch API).
  3. Return merged map.
- HTTP timeout: 3s per request. On error: return empty geo, don't fail the call.
- Rate awareness: ipinfo free tier = 50k/month. We have ~200-300 unique relay IPs total. No rate limit concern.

#### 3d. MCP integration

- In `toSlimStreams()` (slim.go): accept optional `map[string]RelayGeo` parameter.
- Add `RelayGeo string` field to `slimStream`: `json:"relayGeo,omitempty"` — format `"Dubai, AE"`.
- In `get_call` and `summarize_call` handlers: collect unique relay IPs from streams → `resolver.ResolveMany()` → pass map to `toSlimStreams()`.
- `list_calls` does NOT resolve geo (no stream data in listing, would be wasteful).

#### 3e. Initialization

- Add `RelayGeo` repo to `store.Client`.
- Add `geo.Resolver` to MCP server struct, initialized in `cmd/serve.go`.
- `EnsureIndexes` — no new indexes needed (_id is auto-indexed).

---

## What does NOT change

- Crawler code — untouched.
- Existing MCP tool parameters and response formats — backward compatible.
- TUI — no changes (TUI reads from store directly, not MCP).
- `calls` and `streams` collections — no schema changes.
- PowerShell reference scripts — untouched.

## Testing

- `daily_quality_summary`: unit test with fake store returning known data, verify aggregation math and day-filling.
- `min_participants`: unit test that filter is applied correctly, boundary test (0 = disabled).
- `relay_geo`: unit test resolver with fake HTTP + fake store, verify cache-hit path, cache-miss path, HTTP-error-returns-empty path.
- Integration: MCP tool schema validation (tool registers, parameters parse, response shape matches).

## Token budget impact (estimated)

| Operation | Before (tokens) | After (tokens) | Saving |
|-----------|-----------------|----------------|--------|
| 20-day trend analysis | ~2000 (mongosh + raw JSON) | ~400 (one tool call) | 80% |
| Find large conferences | ~800 (list 50 + scan) | ~200 (list with filter) | 75% |
| Relay geo per call | ~500 (3-5 curl + parse) | ~0 (inline in response) | 99% |
| Typical analysis session | ~15000 wasted | ~2000 | ~85% |

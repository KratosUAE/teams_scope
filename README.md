# teams_con

**Microsoft Teams call quality monitor** — crawler + HTTP API + bubbletea TUI + MCP server, backed by MongoDB. Bypasses the 30-day Graph retention window and gives you an interactive investigation surface for every Teams call in your tenant.

Written in Go 1.25, ships as a single binary with four cobra subcommands and a three-service Docker Compose stack.

---

## What it does

Microsoft Graph's `callRecords` API exposes per-stream call-quality metrics (jitter, packet loss, RTT, MOS degradation, concealed samples), but:

1. **Graph only keeps 30 days of history.** Quarterly or annual trend analysis is impossible with the raw API.
2. **The API is slow.** Fetching every call in a week takes minutes; fetching a month takes tens of minutes. Each interactive query becomes a coffee break.
3. **There is no interactive UI.** You get JSON. You write PowerShell. You write more PowerShell.

This project solves those problems by:

- **Crawler** — tails `callRecords` every N minutes and upserts them into MongoDB. Immutable-once-completed, so the crawler only pays Graph's API cost once per call.
- **HTTP API** — thin read-only REST layer over the stored history.
- **TUI** — interactive four-tab terminal UI (calls / users with per-user portrait / drill-down with stats and matrix views / flaky microphones dashboard).
- **MCP server** — exposes the read-only API as nine LLM tools so you can chat with Claude about your call data in natural language while watching the TUI.

The quality verdicts (Good / Poor / Bad) are a 1:1 port of an internal production PowerShell tool we side-by-side tested against during development — every threshold and every worst-of-call attribution matches the PowerShell output byte-for-byte (modulo rounding). Concrete threshold values are documented below; the verdict logic is the single source of truth.

---

## Architecture

```
Microsoft Graph (v1.0)
       │
       ▼
 ┌───────────┐     ┌─────────┐     ┌───────────┐     ┌──────────┐
 │  crawler  │────▶│ MongoDB │◀────│    api    │◀────│   tui    │
 └───────────┘     └─────────┘     │  (HTTP)   │     │(bubbletea)│
   docker svc       docker svc     └───────────┘     └──────────┘
                                         ▲              local bin
                                         │
                                         │  shared read-only Service
                                         │
                                   ┌───────────┐
                                   │    mcp    │◀── Claude Code/Desktop
                                   │  (stdio)  │    (spawns as subprocess
                                   └───────────┘     via .mcp.json)
                                      local bin
```

All four components live in a single Go module and share `internal/store` + `internal/api.Service`. The Service layer is deliberately `net/http`-free so it can be reused by both the HTTP handlers and the MCP tool handlers without duplication.

---

## Prerequisites

- **Go 1.25+** for building the host binary (the Docker image builds Go inside a multi-stage container, so you only need Go locally if you want the TUI on your host).
- **Docker + Docker Compose** for the backend stack.
- **Microsoft 365 tenant** with admin rights to register an Entra ID application.
- **Claude Code or Claude Desktop** (optional) if you want to use the MCP server for conversational triage.

---

## What it looks like

### TUI — tab 1 Calls

Sortable, filterable table of every call the crawler has ever fetched. Verdict cells are colour-coded; hotkey letters in the help line match the column header they sort.

```
 1 Calls   2 Users   3 Drill   4 Flaky mics

 Time           Dur      Verdict  N   Organizer             Worst       WorstUser            WorstStream
 ──────────────────────────────────────────────────────────────────────────────────────────────────────────────
 Apr 13 09:05   1h03m    Bad      8   alice@corp.com        ↑ data      bob@corp.com         loss 8.2% · jit 160ms · rtt 1210ms
 Apr 13 08:58   0m45s    Good     2   carol@corp.com        -           -                    -
 Apr 13 08:42   23m16s   Poor     4   dave@corp.com         ↓ audio     dave@corp.com        jit 71ms · rtt 83ms
 Apr 13 08:30   18m02s   Bad     14   evan@corp.com         ↑ data      frank@corp.com       jit 125ms · rtt 120ms
 Apr 13 07:54   1h01m    Good     6   grace@corp.com        -           -                    -
 Apr 12 17:12   42m31s   Bad      3   bob@corp.com          ↑ audio     bob@corp.com         loss 6.8% · jit 52ms · rtt 88ms
 ...
 ↑/↓ move  enter drill  r refresh  c clear filter  ·  sort:  t time  d dur  v verdict  n count  o organizer  w worst

  last crawl: 09:12:34  ·  mongo: ok   [1/2/3/4 tabs · tab/shift+tab cycle · r refresh · q quit]
```

Press `enter` to drill into the highlighted row. Press `v` to sort by verdict (Bad first), `n` by participant count, `t` by time — direction toggles on repeat.

### TUI — tab 2 Users

Per-user stats over the whole window. Sorted alphabetically by default; press `b` to sort by Bad count descending to find chronic offenders. `enter` on a row jumps to tab 1 with that UPN pre-filtered. **`shift+P`** opens the per-user portrait sub-view described below.

```
 1 Calls   2 Users   3 Drill   4 Flaky mics

 UPN                              Calls    Good    Poor    Bad
 ──────────────────────────────────────────────────────────────
 alice@corp.com                   12       10      1       1
 bob@corp.com                      7        1      1       5
 carol@corp.com                   14       14      0       0
 dave@corp.com                     9        5      3       1
 evan@corp.com                    18       14      2       2
 frank@corp.com                    6        3      0       3
 grace@corp.com                   11       11      0       0
 ...
 ↑/↓ move  enter → filter Calls by UPN  P portrait  r refresh  ·  sort:  u upn  n total  g good  p poor  b bad
```

### TUI — tab 2 Users, portrait sub-view

Press `shift+P` on a row to flip the tab into a per-user dossier: window, verdict breakdown, coloured problem-pattern label, top capture devices (red when concealed is chronically high), subnets with dominant connType, platforms, Teams client versions, and direction-split avg jitter/loss/rtt. Press `b` or `backspace` to return to the list. Re-opening the same UPN is instant (cached report).

```
 Portrait: bob@corp.com
 window: 2026-04-07 09:00 .. 2026-04-14 09:00   calls: 9   good=0  poor=0  bad=9   pattern: wifi_suspected

 Capture devices
   Built-In input                 calls=9  bad=9  avg concealed=0.5%  worst=1.2%

 Subnets
   192.168.111.0        wifi      calls=9

 Platforms
   macOS             calls=9

 Clients
   ms-teams-desktop/25.10.0.12345                                   calls=9

 Avg metrics
   send: jitter=18.4ms  loss=0.1%  rtt=72.1ms
   recv: jitter=248.4ms  loss=0.3%  rtt=74.3ms

 b back  r refresh
```

The `pattern` field is the classifier's coarse summary. Possible values, first-match-wins:

| Pattern | Meaning | Operator's next step |
|---|---|---|
| `insufficient_data` | fewer than 3 calls in window | widen the window |
| `healthy` | bad+poor < 20% of total | nothing — move on |
| `chronic_mic` | one capture device dominates the bad/poor calls AND averages ≥ 10% concealed | replace the headset / disable the built-in mic |
| `wifi_only_issue` | wifi is bad AND a wired baseline (≥ 2 wired calls) is clean | push the user to always use wired |
| `wifi_suspected` | no wired baseline, but wifi ≥ 80% of connectivity AND bad ratio ≥ 50% | try wired and re-test |
| `remote_path` | sustained high RTT (≥ 150ms) OR jitter (≥ 40ms) OR loss (≥ 2%) on bad/poor streams | investigate network path (ISP, VPN, SFU region) |
| `mixed` | no single dominant cause | drill manually |

The classifier is a heuristic — LLM clients (and operators) should always cross-check with the raw aggregates shown alongside. When the underlying `ListUserCalls` hits its 500-call cap the report sets `truncated: true` so you know the aggregates describe a recent slice, not the full window.

### TUI — tab 3 Drill, matrix view

This is the killer visualisation. Rows are participants, two lines per user (↑ upstream, ↓ downstream). Columns are time buckets across the call duration. Cells are colour-coded: `█` red = Bad, `▓` amber = Poor, `░` grey = Good, blank = participant not connected in that bucket. Zebra-striped every other user.

When one participant's upstream is bad and every other participant's downstream is bad in the **same time buckets**, you see a vertical red column spanning multiple rows — the source of the cascade is at the top of the column. Neither the production PowerShell tool we ported from nor the Teams Admin Center shows this.

```
 1 Calls   2 Users   3 Drill   4 Flaky mics

 Call abc12345-...  groupCall · audio,video,screen · alice@corp.com · 8 ppl
 Started Apr 13 09:05 UTC  ·  Duration 1h03m  ·  Verdict BAD
 Streams: 13 Bad · 4 Poor · 43 Good · 60 total      view: matrix   filter: all   channel: data

                                 09:05         09:12         09:20         09:27         09:35         09:42         09:50         09:57
 ▶ bob@corp.com                ↑ ████████████████████████████████████████████████████████████████████████████████████████░░░░░░
   mobile/android              ↓ ░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░
   alice@corp.com              ↑ ░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░
   wired/windows               ↓ ████████████████████████████████████████████████████████████████████████████████████████░░░░░░
   carol@corp.com              ↑ ░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░
   wifi/macos                  ↓ ████████████████████████████████████████████████████████████████████████████████████████░░░░░░
   dave@corp.com               ↑ ░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░
   wifi/windows                ↓ ████████████████████████████████████████████████████████████████████████████████████████░░░░░░
   evan@corp.com               ↑ ░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░
   wired/windows               ↓ ████████████████████████████████████████████████████████████████████████████████████████░░░░░░
   frank@corp.com              ↑ ░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░
   wired/mac                   ↓ ████████████████████████████████████████████████████████████████████████████████████████░░░░░░
 ↑/↓ select user  v stats  f filter  c channel  b back  r refresh

  last crawl: 09:12:34  ·  mongo: ok
```

The `bob` row has Bad upload (`█` on the ↑ line) straight across the call. Every other participant's download line is Bad (`█` on the ↓ line) in exactly the same window. `bob`'s mobile connection is the source — everyone else's download was degraded because their receiving endpoint was pulling broken packets from his upload. The cascade is visible at a glance without reading a single metric value.

Press `v` to switch to the stats-table view (sortable streams), `c` to cycle the channel filter (`all → audio → data → video → screen`), `b` to return to tab 1.

### TUI — tab 4 Flaky mics

A standalone dashboard that autoloads the flaky-microphone findings of the last 7 days (`concealedPct ≥ 5%`, `incidents ≥ 2`). Each row is a `(user, captureDevice)` pair ranked by severity, incident count, and worst-case concealed percentage. Press `enter` on a row to jump straight to that user's portrait — one keystroke from "who's flaky" to the full dossier.

```
 1 Calls   2 Users   3 Drill   4 Flaky mics

 User                            Device                Incidents  Calls  Worst%  Avg%  Severity
 ─────────────────────────────────────────────────────────────────────────────────────────────
 hannah@corp.com                 Built-In input                2      2    43.2  29.8  Bad
 ivan@corp.com                   Built-In input                2      2    22.2  15.6  Bad
 julia@corp.com                  Built-In input                2      2    21.9  14.4  Bad
 kevin@corp.com                  vid:132428|pid:3572           2      2    17.7  11.0  Bad
 liam@corp.com                   Default input device          2      2    14.3   9.1  Poor
 mia@corp.com                    Built-In input                2      2     9.3   7.1  Poor
 ↑/↓ move  enter → portrait  r refresh  ·  window: last 7 days, concealed ≥ 5%, incidents ≥ 2
```

Severity is `Bad` when either worst concealed ≥ 15% or average ≥ 10%, otherwise `Poor`. The 7-day window and incident threshold are hard-coded for the dashboard — the same data is available via MCP (`find_flaky_microphones`) with tunable parameters if you need a tighter or looser filter.

### Conversational triage with MCP

With the MCP server attached to Claude Code or Claude Desktop, you can stay on the TUI and have a side-by-side dialog about the data. Claude calls the `mcp__teams_con__*` tools in the background and reasons over the results.

Example exchange — user is watching the TUI and asks what went wrong with alice's morning meeting:

```
You: what was wrong with alice's 9am meeting today?

Claude: [calls list_calls upn=alice@corp.com from=today]
        → 3 calls; the 09:05 group call is Bad, 1h03m, 8 participants.

        [calls summarize_call call_id=abc12345-...]
        → Call abc12345  groupCall  1h03m  organizer=alice@corp.com  participants=8  verdict=Bad
          streams: 60 (13 Bad / 4 Poor / 43 Good)
          worst: bob@corp.com [send] data  jit=125.0ms rtt=1210.0ms
            alice@corp.com [download] data: loss=1.3% jit=40.0ms rtt=1210.0ms
            bob@corp.com [upload] data: jit=125.0ms rtt=120.0ms
            carol@corp.com [download] data: jit=64.0ms rtt=77.0ms
            dave@corp.com [download] data: jit=58.0ms rtt=142.0ms
            evan@corp.com [download] data: jit=73.0ms rtt=85.0ms
          likely: high jitter (network congestion or wifi)

        TL;DR: the meeting went badly because of bob@corp.com's upload.
        His data upstream had ~125 ms jitter and 1210 ms RTT for the
        entire call, and every other participant saw bad downloads of
        that same stream. Alice, carol, dave, evan, frank all show up
        with high jitter on their recv side in the same window.

You: how confident are we that bob is the actual source?

Claude: [calls find_cascades call_id=abc12345-...]
        → 2 cascades detected; top suspect: bob@corp.com [data] Bad
          affected 7 users in [09:05:00 → 09:48:12]. Second suspect was
          only 2 affected (weak signal).

        Very confident. find_cascades groups the overlapping time
        windows and confirms bob is the single dominant source — his
        upload window aligns with seven other participants' degraded
        download windows on the same media (data). No other sender in
        the call produces the same blast radius.

You: what's bob's device and network like?

Claude: [calls get_call call_id=abc12345-... include_good=false max_streams=10]
        → bob@corp.com stream sample:
          { "user": "bob@corp.com",
            "dir": "upload",
            "verdict": "Bad",
            "label": "data",
            "jitMs": 125, "jitMaxMs": 125,
            "rttMs": 120,
            "conn": "mobile",
            "platform": "android",
            "userAgent": "ms-teams-mobile/1.6.0.27180",
            "subnet": "10.197.18.0",
            "relayIp": "52.112.207.128",
            "relayPort": 3481,
            "from": "2026-04-13T09:05:00Z",
            "to": "2026-04-13T10:08:12Z" }

        Bob joined from his Android phone on a 4G mobile network
        (conn=mobile, platform=android). His media went through a
        Teams TURN relay (relayIp 52.112.* is a Microsoft relay),
        which means direct peer-to-peer was blocked — probably by a
        cellular carrier NAT. The mobile link couldn't sustain the
        screen-share bitrate, hence the 125 ms jitter and 1.2 s RTT.

        Recommendations:
          1. Tell bob to rejoin from wifi or desktop Teams next time.
          2. If mobile is unavoidable, disable his outgoing video/
             screen share on weak networks.
          3. For meetings where bob is the presenter, have a fallback
             presenter ready.
```

Behind the scenes Claude made four MCP calls: `list_calls`, `summarize_call`, `find_cascades`, `get_call`. Each returns a short text summary plus a structured JSON block — Claude reads the summary for quick answers and parses the JSON for specific follow-ups (device, network, IP, user agent). You never leave the chat.

This is exactly the workflow the project was built for: **visual triage in the TUI on one screen, conversational root-cause analysis with Claude on the other**, both over the same stored dataset.

---

## Getting Graph credentials (Entra ID app registration)

The `callRecords` API requires **application permissions** — there is no delegated-user flow. You need an Entra ID app registration with `CallRecords.Read.All` granted at the tenant level.

### Step 1 — Register the app

1. Sign in to [https://entra.microsoft.com](https://entra.microsoft.com) as a Global Administrator (or Cloud App Admin).
2. Go to **Identity → Applications → App registrations → + New registration**.
3. Fill in:
   - **Name:** `teams_con` (anything works, this is just a label).
   - **Supported account types:** *Accounts in this organizational directory only (Single tenant)*.
   - **Redirect URI:** leave blank. `teams_con` uses the client-credentials flow with no user interaction.
4. Click **Register**.

On the app's **Overview** page, copy:
- **Application (client) ID** → this is your `ClientId`.
- **Directory (tenant) ID** → this is your `TenantId`.

### Step 2 — Create a client secret

1. In the app's left nav, open **Certificates & secrets → Client secrets → + New client secret**.
2. Description: `teams_con crawler`. Expiration: 12 or 24 months (rotation is your responsibility — Entra does not rotate these automatically).
3. Click **Add**.
4. **Copy the `Value` field immediately.** It is only visible right after creation — reload the page and you will never see it again. This is your `ClientSecret`.

> If you miss the value, just delete the secret and make a new one. Nothing on Azure's side depends on the particular secret string.

### Step 3 — Grant the API permission

1. In the app's left nav, open **API permissions → + Add a permission → Microsoft Graph → Application permissions**.
2. Search for `CallRecords.Read.All`.
3. Check it and click **Add permissions**.
4. Back on the API permissions screen, click **Grant admin consent for <your tenant>** (requires Global Admin or Privileged Role Admin). The status for `CallRecords.Read.All` must turn green (**Granted for ...**). Without this step, the API returns `403 Forbidden` on every request.

> **Delegated permissions do not work for `callRecords`.** The Microsoft Graph docs are explicit about this — only application permissions are supported. A "delegated" consent will pass Entra but fail at the API with a 403.

### Step 4 — Write credentials to `.env`

From the project root:

```bash
cp .env.example .env
$EDITOR .env
```

Fill in the three fields:

```properties
# Microsoft Graph app credentials (CallRecords.Read.All application permission)
TenantId=00000000-0000-0000-0000-000000000000
ClientId=11111111-1111-1111-1111-111111111111
ClientSecret=the-secret-value-you-just-copied

# Mongo URI — compose overrides this to mongodb://mongo:27017/teams_con inside containers.
MongoUri=mongodb://localhost:27018/teams_con

# HTTP API server bind — used by `teams_con serve`
ApiAddr=:8080

# HTTP API client URL — used by `teams_con tui`
ApiUrl=http://localhost:8080
```

`.env` is in `.gitignore`. Do not commit it. The crawler container reads it via `env_file: .env` in `docker-compose.yml`.

> The field names are intentionally PascalCase (`TenantId`, not `TENANT_ID`) to stay compatible with the PowerShell tool we ported from, so the same `.env` file can be reused between both.

---

## Quick start

```bash
# 1. Clone and enter
git clone https://github.com/KratosUAE/teams_con.git
cd teams_con

# 2. Fill in Entra credentials (see previous section)
cp .env.example .env
$EDITOR .env

# 3. Start the backend stack
docker compose up -d

# 4. Watch the first crawl tick
docker compose logs -f crawler

# 5. Backfill the last 7 days (one-shot)
docker compose run --rm crawler crawl --backfill=7

# 6. Sanity check the API
curl http://localhost:8080/health | jq
curl 'http://localhost:8080/calls?verdict=Bad&limit=5' | jq

# 7. Build the host TUI binary
./build.sh         # installs to ~/.aux/bin/teams_con

# 8. Open the TUI
~/.aux/bin/teams_con tui
```

---

## Docker Compose layout

| Service | Image / build | Ports | Purpose |
|---|---|---|---|
| `mongo` | `mongo:7` | `127.0.0.1:27018:27017` | Stores call records and streams. Port bound to localhost only so the local TUI and MCP binary can reach it. Host port 27018 avoids conflict with a system-wide `mongod` on 27017. |
| `crawler` | built from `Dockerfile` | — | Runs `teams_con crawl --interval=5m --window=30m`. Tailing daemon that upserts new calls into Mongo. Reads Graph creds from `.env`. |
| `api` | built from `Dockerfile` | `127.0.0.1:8080:8080` | Runs `teams_con serve --addr=:8080`. HTTP REST layer over the store. |

The TUI and MCP server run **on the host**, not in the compose stack — they connect to `localhost:27018` for Mongo (MCP) or `localhost:8080` for HTTP (TUI).

---

## CLI — four subcommands

```
teams_con
├── crawl   # graph → mongo daemon
├── serve   # http rest api
├── tui     # interactive bubbletea terminal UI
└── mcp     # stdio mcp server for LLM clients
```

All subcommands share a `--log-level` persistent flag (`debug | info | warn | error`) and read config from environment variables + an optional `.env` file in the current working directory.

### `teams_con crawl`

```bash
teams_con crawl [--interval 5m] [--window 30m] [--backfill 0]
```

- **`--interval`** — tick period. Default `5m`. Every tick the crawler fetches `callRecords` started in the last `--window` duration, upserts the new ones into Mongo, and records the tick timestamp in the `meta` collection.
- **`--window`** — how far back each tick looks. Default `30m`. Graph has ingestion lag, so a bigger window than interval covers late-arriving records. The crawler idempotently skips calls already stored.
- **`--backfill N`** — one-shot mode. Fetches the last `N` days, then exits. Max `30` (Graph retention ceiling). Use after fresh installs or if you want to replay the last month.

Requires `TenantId`, `ClientId`, `ClientSecret`, `MongoUri` in the environment.

### `teams_con serve`

```bash
teams_con serve [--addr :8080]
```

HTTP REST server over the store. Endpoints:

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/health` | `{lastCrawlAt, lastCrawlError, mongoOk}` |
| `GET` | `/calls?from=&to=&verdict=&upn=&limit=&offset=` | paginated list, most recent first |
| `GET` | `/calls/{id}` | `{call, streams[]}` drill-down |
| `GET` | `/users?from=&to=` | per-user stats sorted by UPN |
| `GET` | `/users/{upn}/calls?from=&to=&limit=&offset=` | one user's calls |
| `GET` | `/users/{upn}/health?from=&to=` | full per-user dossier: verdict counts, subnets, devices, clients, platforms, avg metrics, problem pattern |
| `GET` | `/mics/flaky?from=&to=&min_concealed_pct=&min_incidents=&limit=` | flaky-microphone aggregate over the window |

Requires only `MongoUri`. No Graph credentials — the API is read-only over the stored data.

### `teams_con tui`

```bash
teams_con tui [--api http://localhost:8080]
```

Four-tab bubbletea terminal UI. Requires a running `teams_con serve` (or the `api` compose service).

**Global keys (any tab):**

| Key | Action |
|---|---|
| `1` / `2` / `3` / `4` | switch to tab (Calls / Users / Drill / Flaky mics) |
| `tab` / `shift+tab` | cycle tabs |
| `r` | refresh active tab |
| `q` / `esc` / `ctrl+c` | quit |
| `↑↓` / `j` `k` | row navigation |

**Tab 1 — Calls** (all calls in store, most recent first):

| Key | Action |
|---|---|
| `t` | sort by time |
| `d` | sort by duration |
| `v` | sort by verdict (worst first) |
| `n` | sort by participant count |
| `o` | sort by organizer |
| `w` | sort by worst user |
| `c` | clear UPN filter (set via Users tab) |
| `enter` | drill into selected call (flips to tab 3) |

Columns: Time, Dur, Verdict, N, Organizer, Worst (direction + stream label), WorstUser, WorstStream. Flex columns stretch to terminal width. Sort hotkey letters are colour-matched to their column header.

**Tab 2 — Users** (per-user stats over the whole window):

| Key | Action |
|---|---|
| `u` | sort by UPN (alphabetical) |
| `n` | sort by total call count |
| `g` / `p` / `b` | sort by Good / Poor / Bad count |
| `enter` | open Calls tab filtered to that UPN |
| `shift+P` | open per-user **portrait** sub-view (window, verdicts, devices, subnets, platforms, clients, avg metrics, problem pattern) |

While in portrait mode: `b` / `backspace` returns to the list, `r` refetches. Re-opening the same UPN hits the cached report.

**Tab 3 — Drill** (one call's detail, two view modes):

Press `enter` on a Calls-tab row to open. Initially in **stats view** — a sortable streams table with User, Dir, Label, Verdict, Jit avg/max, Loss, Rtt, Conn, Client, Subnet, Segment. Press `v` to toggle to **matrix view** — a gantt-heatmap of participants (↑/↓) × time buckets, zebra-striped, with conn/platform annotation under each UPN.

| Key | Action |
|---|---|
| `v` | toggle stats ↔ matrix |
| `f` | cycle filter (all → poor+bad → bad → all) |
| `c` | cycle channel (all → audio → data → video → scr → all) |
| `b` / `backspace` | back to Calls tab |
| `r` | refetch |
| Sort (stats only): `s` severity, `u` user, `y` dir, `a` label, `i` jitter, `l` loss, `p` rtt, `m` segment time | |

**The matrix view is the main analytical tool.** When one participant's upstream is bad and many other participants' downstream goes bad in the same time buckets, you see a vertical red column spanning multiple rows — the source of the cascade is at the top of the column. This pattern is not visible in the raw PowerShell output or in Teams Admin Center.

**Tab 4 — Flaky mics** (weekly dashboard of chronically flaky `(user, captureDevice)` pairs):

| Key | Action |
|---|---|
| `enter` | open the portrait of the highlighted user (flips to tab 2 in portrait mode) |
| `r` | refetch |

Autoloads with fixed parameters: last 7 days, `concealedPct ≥ 5%`, `incidents ≥ 2`, up to 50 findings. Columns: User, Device, Incidents, Calls, Worst%, Avg%, Severity. For adjustable thresholds use the MCP tool `find_flaky_microphones`.

### `teams_con mcp`

```bash
teams_con mcp
```

MCP (Model Context Protocol) server over stdio. Spawned as a subprocess by Claude Code or Claude Desktop. Exposes nine tools:

| Tool | Purpose |
|---|---|
| `health` | backend liveness: last crawl, mongo connectivity |
| `list_calls` | time/verdict/upn filter, default limit 20 |
| `get_call` | per-stream detail, token-budget-friendly slim JSON, default drops Good streams |
| `list_users` | per-user stats |
| `list_user_calls` | drill into one person |
| `summarize_call` | compact 10-line natural-language triage report with root-cause heuristic |
| `find_cascades` | structured cascade detection — finds "one bad sender → many bad receivers in the same time window" patterns and returns suspect sources with blast radius |
| `find_flaky_microphones` | cross-call aggregation of send-audio streams by `(user, captureDevice)` over a window; flags devices whose `concealedPct` repeatedly exceeds a threshold — catches chronic hardware issues that cascade detection does not |
| `user_health_report` | one-shot per-user dossier over a window: verdict counts, subnets, devices (with bad-call counts + concealed averages), clients, platforms, avg jitter/loss/rtt by direction, and a coarse problem-pattern classification (`chronic_mic` / `wifi_only_issue` / `wifi_suspected` / `remote_path` / `healthy` / `mixed` / `insufficient_data`). Replaces the `list_users` → `list_user_calls` → `get_call` × N chain |

The Service layer that the HTTP API uses is the same one the MCP tools wrap — zero business logic duplication.

---

## Wiring MCP to Claude Code

A `.mcp.json.example` template lives at the repo root. Copy it to `.mcp.json` (which is gitignored so each developer's absolute path stays out of the public repo):

```bash
cp .mcp.json.example .mcp.json
# edit .mcp.json and replace /ABSOLUTE/PATH/TO/teams_con with the real binary path
```

The template looks like:

```json
{
  "mcpServers": {
    "teams_con": {
      "command": "/ABSOLUTE/PATH/TO/teams_con",
      "args": ["mcp"],
      "env": {
        "MongoUri": "mongodb://localhost:27018/teams_con"
      }
    }
  }
}
```

Adjust the `command` path to wherever `./build.sh` installed your binary. The absolute path is required because Claude Desktop does not inherit your shell `PATH`.

For Claude Desktop, merge the same `mcpServers` block into `~/.config/Claude/claude_desktop_config.json` (Linux), `~/Library/Application Support/Claude/claude_desktop_config.json` (macOS), or `%APPDATA%/Claude/claude_desktop_config.json` (Windows).

After the config is in place:

1. Run `./build.sh` if you haven't.
2. Reconnect MCP in Claude Code: `/mcp` → you should see `teams_con` connected with 9 tools listed as `mcp__teams_con__*`.
3. Start asking questions like `what went wrong with the call at 09:00 today?` or `what's alice's call quality looking like this week?` — Claude will call `list_calls` + `summarize_call` + `find_cascades` + `find_flaky_microphones` + `user_health_report` as needed on your behalf.

Run the TUI (`teams_con tui`) on one screen and chat with Claude on another. You do visual triage, Claude does queries-and-reasoning over the same data.

---

## Building from source

```bash
# Host TUI binary (installs to ~/.aux/bin/teams_con)
./build.sh

# Or manually
go build -o ./teams_con .

# Docker images (via compose)
docker compose build

# Tests
go test ./...
```

---

## Project layout

```
teams_con/
├── cmd/                         # cobra subcommands
│   ├── root.go
│   ├── config.go                # .env + env var loader
│   ├── crawl.go                 # teams_con crawl
│   ├── serve.go                 # teams_con serve
│   ├── tui.go                   # teams_con tui
│   └── mcp.go                   # teams_con mcp
├── internal/
│   ├── graph/                   # Microsoft Graph client (MSAL, pagination, 429 retry)
│   ├── quality/                 # 1:1 port of CallQualityUtils.ps1 (verdicts, worst-of-call, UPN extraction)
│   ├── store/                   # MongoDB layer (calls/streams/users/meta repos)
│   ├── crawler/                 # tick-loop daemon + backfill
│   ├── api/                     # HTTP-free Service + thin handlers (reused by mcp)
│   ├── tui/                     # bubbletea v2 + lipgloss v2
│   └── mcp/                     # MCP tools (server, encoding, summarize, cascades, slim DTO)
├── Dockerfile                   # multi-stage distroless build (~18 MB final image)
├── docker-compose.yml
├── build.sh
├── .mcp.json.example            # Claude Code / Desktop MCP config template (copy to .mcp.json locally)
├── .env.example
└── go.mod                       # single module, Go 1.25
```

---

## Troubleshooting

**`ERROR: Failed to connect to Microsoft Graph`** — credentials wrong or admin consent not granted. Double-check:
- `TenantId`, `ClientId`, `ClientSecret` match the app registration.
- `CallRecords.Read.All` is listed under **Application permissions** (not Delegated) with a green "Granted" status.
- The client secret hasn't expired.

**`403 Forbidden` on every `callRecords` call** — you added the permission but did not click **Grant admin consent**. Do that.

**`0 call records in window`** — crawler is working but your tenant has no completed calls in the last 30 minutes. Try `--backfill=7` to pull a week of history.

**TUI: `no call data`** on tab 3** — you pressed `3` directly without selecting a call. Go to tab 1, pick a call, press `enter`.

**TUI: `terminal too narrow for matrix view`** — drill tab 3 matrix needs >= 62 columns. Widen the terminal or use stats view (`v`).

**MCP: Claude Code says server disconnected** — the binary path in `.mcp.json` is wrong, or the binary crashes on startup because `MongoUri` is missing. Check stderr output in Claude Code's MCP panel. Run `echo '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"s","version":"0"}}}' | ~/.aux/bin/teams_con mcp` manually — if it prints a JSON response on stdout, the server works.

**Mongo port conflict on 27017** — the compose file already binds to host port `27018` to avoid collisions with a system-wide `mongod`. If `27018` is also taken, edit the `ports:` line in `docker-compose.yml`.

**Cascade detector reports multiple suspects for the same call** — this is expected when several users simultaneously have bad uploads (e.g. a shared ISP outage). Any of them could be "the source"; look at their `subnet` / `connType` / `userAgent` to narrow down.

---

## Quality verdict thresholds

Ported verbatim from the production PowerShell tool we validated against. These define the Good / Poor / Bad classification per stream and are the single source of truth for verdict logic.

| Metric | Poor (>) | Bad (>) |
|---|---|---|
| Packet loss (avg) | 5 % | 10 % |
| Jitter (avg) | 30 ms | 50 ms |
| RTT (avg) | 500 ms | 1000 ms |
| MOS degradation | 1.0 | 1.5 |
| Concealed samples % | 7 % | 15 % |

A call's worst-of-call verdict is the maximum severity across all its stream segments, attributed to the stream that hit the threshold first. Direction (`recv` / `send`) is always at the receiving endpoint — Graph reports metrics on the side that observed the packets.

---

## Development workflow

```bash
# Feature branch from main
git checkout main && git pull
git checkout -b feat-something

# Before commit
go build ./...
go vet ./...
go test ./...

# Commit and push
git add -A
git commit -m "feat: description"
git push -u origin feat-something

# Merge to main
git checkout main
git merge feat-something --no-ff
git push
```

See `CLAUDE.md` in the parent directory for the full agent-driven workflow used during development.

---

## License

MIT.

## Acknowledgements

- A pre-existing internal PowerShell tool served as the canonical behavioural spec during development. Every quality verdict and every attribution rule in `teams_con` was validated side-by-side against its output to ensure byte-for-byte parity.
- `github.com/mark3labs/mcp-go` for the MCP SDK.
- `charm.land/bubbletea/v2` and `charm.land/lipgloss/v2` for the TUI stack.
- Architectural inspiration from [`KratosUAE/waf_con`](https://github.com/KratosUAE/waf_con) — the tab-based bubbletea pattern.

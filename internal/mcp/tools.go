package mcp

import (
	"context"
	"fmt"
	"sort"
	"time"

	mcpsdk "github.com/mark3labs/mcp-go/mcp"

	"teams_con/internal/api"
	"teams_con/internal/store"
)

// Default / bound constants for tool parameters. Kept here (not in the
// api package) because they are LLM-facing defaults — the HTTP API uses
// looser bounds suited for dashboards.
const (
	defaultCallListLimit = 20
	maxCallListLimit     = 500
	defaultMaxStreams    = 30
	topProblemStreams    = 5
)

// registerTools wires all six tool definitions into the underlying
// MCPServer. Called from NewServer. Tool names match the plan:
//
//	health           — backend liveness
//	list_calls       — paged call listing
//	get_call         — full per-call detail, trimmed for token budget
//	list_users       — per-user aggregate stats
//	list_user_calls  — drill-down into one user's calls
//	summarize_call   — compact natural-language triage summary
func (s *Server) registerTools() {
	s.m.AddTool(
		mcpsdk.NewTool("health",
			mcpsdk.WithDescription("Check liveness of the teams_con backend: last crawl timestamp, last crawl error, and MongoDB connectivity. Use this first to confirm data is fresh before answering questions about recent calls."),
		),
		s.handleHealth,
	)

	s.m.AddTool(
		mcpsdk.NewTool("list_calls",
			mcpsdk.WithDescription("List calls over an optional time window, filtered by verdict and/or user UPN. Most recent first. Use this to find candidate calls to investigate. Default limit 20."),
			mcpsdk.WithString("from", mcpsdk.Description("RFC3339 inclusive lower bound on startTimeUtc, optional")),
			mcpsdk.WithString("to", mcpsdk.Description("RFC3339 exclusive upper bound on startTimeUtc, optional")),
			mcpsdk.WithString("verdict",
				mcpsdk.Description("Filter by call verdict (optional)"),
				mcpsdk.Enum("Good", "Poor", "Bad"),
			),
			mcpsdk.WithString("upn", mcpsdk.Description("Filter to calls where this user participated, optional")),
			mcpsdk.WithNumber("limit", mcpsdk.Description("Default 20, max 500")),
			mcpsdk.WithNumber("offset", mcpsdk.Description("Default 0")),
		),
		s.handleListCalls,
	)

	s.m.AddTool(
		mcpsdk.NewTool("get_call",
			mcpsdk.WithDescription("Get full detail for a specific call: metadata + per-stream quality metrics. Use after list_calls to investigate one call's streams. By default omits Good streams to save tokens; set include_good=true for full detail."),
			mcpsdk.WithString("call_id", mcpsdk.Description("Required Teams callRecord id"), mcpsdk.Required()),
			mcpsdk.WithBoolean("include_good", mcpsdk.Description("Include Good-verdict streams (default false)")),
			mcpsdk.WithNumber("max_streams", mcpsdk.Description("Truncate to N most-severe streams (default 30)")),
		),
		s.handleGetCall,
	)

	s.m.AddTool(
		mcpsdk.NewTool("list_users",
			mcpsdk.WithDescription("List per-user call statistics (total/good/poor/bad counts) over an optional time window. Use to find chronically affected users."),
			mcpsdk.WithString("from", mcpsdk.Description("RFC3339 lower bound, optional")),
			mcpsdk.WithString("to", mcpsdk.Description("RFC3339 upper bound, optional")),
		),
		s.handleListUsers,
	)

	s.m.AddTool(
		mcpsdk.NewTool("list_user_calls",
			mcpsdk.WithDescription("List a specific user's calls over a time window. Use after list_users to drill into one person."),
			mcpsdk.WithString("upn", mcpsdk.Description("User UPN (required)"), mcpsdk.Required()),
			mcpsdk.WithString("from", mcpsdk.Description("RFC3339 lower bound, optional")),
			mcpsdk.WithString("to", mcpsdk.Description("RFC3339 upper bound, optional")),
			mcpsdk.WithNumber("limit", mcpsdk.Description("Default 20, max 500")),
			mcpsdk.WithNumber("offset", mcpsdk.Description("Default 0")),
		),
		s.handleListUserCalls,
	)

	s.m.AddTool(
		mcpsdk.NewTool("summarize_call",
			mcpsdk.WithDescription("Compact natural-language summary of a call's quality issues: worst user, worst streams, top metric violations, likely root cause (loss vs jitter vs rtt). Cheaper than get_call when you only need 'what was wrong?'."),
			mcpsdk.WithString("call_id", mcpsdk.Description("Required Teams callRecord id"), mcpsdk.Required()),
		),
		s.handleSummarizeCall,
	)

	s.m.AddTool(
		mcpsdk.NewTool("user_health_report",
			mcpsdk.WithDescription("Comprehensive per-user call-quality dossier over a time window: total calls by verdict, subnets used, capture devices with concealedPct averages, Teams client versions, platforms, average jitter/loss/rtt by direction, and a coarse problem-pattern classification. Pattern values: chronic_mic (one device dominates bad calls), wifi_only_issue (wifi is bad AND wired baseline is clean), wifi_suspected (no wired baseline, but wifi dominates and bad ratio is high — try wired), remote_path (sustained high RTT, jitter, or loss on bad/poor-verdict streams), healthy, mixed, insufficient_data. Use this when a user reports a call-quality complaint — one call instead of chaining list_users, list_user_calls, and get_call. Default window is the last 7 days."),
			mcpsdk.WithString("upn", mcpsdk.Description("User UPN (required)"), mcpsdk.Required()),
			mcpsdk.WithString("from", mcpsdk.Description("RFC3339 inclusive lower bound, optional (default now-7d)")),
			mcpsdk.WithString("to", mcpsdk.Description("RFC3339 exclusive upper bound, optional (default now)")),
		),
		s.handleUserHealthReport,
	)

	s.m.AddTool(
		mcpsdk.NewTool("find_flaky_microphones",
			mcpsdk.WithDescription("Find users with chronically flaky microphones over a time window. Aggregates send-audio streams by (user, captureDevice) and flags devices whose concealedPct repeatedly exceeds a threshold across multiple calls. Use to distinguish bad hardware from bad network: cascade patterns imply network, flaky microphones imply device. Default window is the last 7 days."),
			mcpsdk.WithString("from", mcpsdk.Description("RFC3339 inclusive lower bound, optional (default now-7d)")),
			mcpsdk.WithString("to", mcpsdk.Description("RFC3339 exclusive upper bound, optional (default now)")),
			mcpsdk.WithNumber("min_concealed_pct", mcpsdk.Description("Per-stream concealedPct threshold to count as an incident (default 5.0)")),
			mcpsdk.WithNumber("min_incidents", mcpsdk.Description("Minimum incidents required to surface a device (default 3)")),
			mcpsdk.WithNumber("limit", mcpsdk.Description("Max devices returned (default 20, max 100)")),
		),
		s.handleFindFlakyMicrophones,
	)

	s.m.AddTool(
		mcpsdk.NewTool("list_subnets",
			mcpsdk.WithDescription("List all configured subnet labels (CIDR → friendly office/kind). Use to understand the operator's network taxonomy when interpreting user_health_report subnet entries — a row labelled \"Xpanceo Dubai HQ wired\" is meaningfully different from a home wifi block. Read-only."),
		),
		s.handleListSubnets,
	)

	s.m.AddTool(
		mcpsdk.NewTool("get_user_card",
			mcpsdk.WithDescription("Fetch the operator-maintained annotation card for a user: location hint, tags (vip, remote-only, mobile-heavy, escalated...), and free-form notes. Use to surface context when answering questions about a specific user. Returns an empty-card message when no annotation exists. Read-only — card writes happen via CLI or HTTP."),
			mcpsdk.WithString("upn", mcpsdk.Description("User UPN (required)"), mcpsdk.Required()),
		),
		s.handleGetUserCard,
	)

	s.m.AddTool(
		mcpsdk.NewTool("find_bad_network_hotspots",
			mcpsdk.WithDescription("Find (subnet, relay) pairs with concentrated bad calls in a time window. Complements user_health_report: instead of asking 'is this user broken?', asks 'which office wifi or TURN relay is broken?'. Group by subnet (default), relay, or subnet+relay. Returns pairs sorted by bad ratio with distinct-user counts, avg RTT/jitter/loss, and top-5 affected users. Default window last 7d, min 5 calls, min 30% bad ratio."),
			mcpsdk.WithString("from", mcpsdk.Description("RFC3339 lower bound, optional")),
			mcpsdk.WithString("to", mcpsdk.Description("RFC3339 upper bound, optional")),
			mcpsdk.WithNumber("min_calls", mcpsdk.Description("Default 5")),
			mcpsdk.WithNumber("min_bad_ratio", mcpsdk.Description("Default 0.30")),
			mcpsdk.WithString("group_by",
				mcpsdk.Description("Default 'subnet'"),
				mcpsdk.Enum("subnet", "relay", "subnet+relay"),
			),
			mcpsdk.WithNumber("limit", mcpsdk.Description("Default 20, max 100")),
		),
		s.handleFindBadNetworkHotspots,
	)

	s.m.AddTool(
		mcpsdk.NewTool("find_cascades",
			mcpsdk.WithDescription("Detect cascade patterns in a call: one participant's bad send stream that correlates with multiple other participants' bad receive streams in the same time window. Surfaces the likely source of a call-wide issue (e.g. one user's bad wifi uplink tanking everyone else's download audio). Returns suspected cascade events sorted by severity and blast radius."),
			mcpsdk.WithString("call_id", mcpsdk.Description("Required Teams callRecord id"), mcpsdk.Required()),
			mcpsdk.WithNumber("min_affected", mcpsdk.Description("Minimum number of affected receivers required to flag a cascade (default 3)")),
		),
		s.handleFindCascades,
	)
}

// parseTimeParam reads an optional RFC3339 timestamp from the tool
// arguments. An absent/empty value yields (nil, nil, nil). An invalid
// value yields (nil, toolErrorResult, nil) — the caller should return it
// verbatim.
func parseTimeParam(req mcpsdk.CallToolRequest, key string) (*time.Time, *mcpsdk.CallToolResult) {
	raw := req.GetString(key, "")
	if raw == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, mcpsdk.NewToolResultError(fmt.Sprintf("bad %q: %s", key, err.Error()))
	}
	return &t, nil
}

func (s *Server) handleHealth(ctx context.Context, _ mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	h, err := s.svc.Health(ctx)
	if err != nil {
		return s.mapServiceErr(err), nil
	}
	return textAndJSON(summarizeHealth(h), h), nil
}

func (s *Server) handleListCalls(ctx context.Context, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	from, badFrom := parseTimeParam(req, "from")
	if badFrom != nil {
		return badFrom, nil
	}
	to, badTo := parseTimeParam(req, "to")
	if badTo != nil {
		return badTo, nil
	}

	p := api.ListCallsParams{
		From:    from,
		To:      to,
		Verdict: req.GetString("verdict", ""),
		Upn:     req.GetString("upn", ""),
		Limit:   req.GetInt("limit", defaultCallListLimit),
		Offset:  req.GetInt("offset", 0),
	}
	if p.Limit > maxCallListLimit {
		p.Limit = maxCallListLimit
	}

	calls, err := s.svc.ListCalls(ctx, p)
	if err != nil {
		return s.mapServiceErr(err), nil
	}
	return textAndJSON(summarizeCalls(calls), calls), nil
}

func (s *Server) handleGetCall(ctx context.Context, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	callID, err := req.RequireString("call_id")
	if err != nil {
		return mcpsdk.NewToolResultError("bad call_id: " + err.Error()), nil
	}
	includeGood := req.GetBool("include_good", false)
	maxStreams := req.GetInt("max_streams", defaultMaxStreams)
	if maxStreams <= 0 {
		maxStreams = defaultMaxStreams
	}

	detail, err := s.svc.GetCall(ctx, callID)
	if err != nil {
		return s.mapServiceErr(err), nil
	}

	preTotal := len(detail.Streams)
	var preGood int
	for _, s := range detail.Streams {
		if s.Verdict == "Good" {
			preGood++
		}
	}

	// Filter + sort a copy so we do not mutate the Service result.
	filtered := make([]store.StreamRow, 0, preTotal)
	for _, s := range detail.Streams {
		if !includeGood && s.Verdict == "Good" {
			continue
		}
		filtered = append(filtered, s)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		ri := verdictRank(filtered[i].Verdict)
		rj := verdictRank(filtered[j].Verdict)
		if ri != rj {
			return ri > rj
		}
		li := float64(0)
		lj := float64(0)
		if filtered[i].AvgLossPct != nil {
			li = *filtered[i].AvgLossPct
		}
		if filtered[j].AvgLossPct != nil {
			lj = *filtered[j].AvgLossPct
		}
		return li > lj
	})
	truncated := 0
	if len(filtered) > maxStreams {
		truncated = len(filtered) - maxStreams
		filtered = filtered[:maxStreams]
	}
	goodHidden := 0
	if !includeGood {
		goodHidden = preGood
	}

	// Build the response payload with the trimmed + slimmed streams.
	// slimCall drops noise fields and rounds floats so the JSON block
	// stays compact — LLM clients choke on 50KB stream dumps.
	trimmed := &api.CallDetail{
		Call:    detail.Call,
		Streams: filtered,
	}
	slim := slimCall{
		Call:    detail.Call,
		Streams: toSlimStreams(filtered),
	}

	header := fmt.Sprintf("showing %d/%d streams (%d Good hidden, %d truncated)\n",
		len(filtered), preTotal, goodHidden, truncated)
	// summarizeCallDetail still reads the full CallDetail so the text
	// summary retains worst-user / root-cause heuristics.
	summary := header + summarizeCallDetail(trimmed)
	return textAndJSON(summary, slim), nil
}

func (s *Server) handleListUsers(ctx context.Context, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	from, badFrom := parseTimeParam(req, "from")
	if badFrom != nil {
		return badFrom, nil
	}
	to, badTo := parseTimeParam(req, "to")
	if badTo != nil {
		return badTo, nil
	}

	users, err := s.svc.ListUsers(ctx, from, to)
	if err != nil {
		return s.mapServiceErr(err), nil
	}
	return textAndJSON(summarizeUsers(users), users), nil
}

func (s *Server) handleListUserCalls(ctx context.Context, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	upn, err := req.RequireString("upn")
	if err != nil {
		return mcpsdk.NewToolResultError("bad upn: " + err.Error()), nil
	}
	from, badFrom := parseTimeParam(req, "from")
	if badFrom != nil {
		return badFrom, nil
	}
	to, badTo := parseTimeParam(req, "to")
	if badTo != nil {
		return badTo, nil
	}
	limit := req.GetInt("limit", defaultCallListLimit)
	if limit > maxCallListLimit {
		limit = maxCallListLimit
	}
	offset := req.GetInt("offset", 0)

	calls, err := s.svc.ListUserCalls(ctx, upn, from, to, limit, offset)
	if err != nil {
		return s.mapServiceErr(err), nil
	}
	return textAndJSON(summarizeUserCalls(upn, calls), calls), nil
}

func (s *Server) handleSummarizeCall(ctx context.Context, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	callID, err := req.RequireString("call_id")
	if err != nil {
		return mcpsdk.NewToolResultError("bad call_id: " + err.Error()), nil
	}
	detail, err := s.svc.GetCall(ctx, callID)
	if err != nil {
		return s.mapServiceErr(err), nil
	}
	return textOnly(summarizeCallDetail(detail)), nil
}

// Compile-time guard: topProblemStreams must fit inside defaultMaxStreams
// so the summary's "top 5" slice is always a subset of the JSON payload.
const _ = defaultMaxStreams - topProblemStreams // must be >= 0

func (s *Server) handleUserHealthReport(ctx context.Context, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	upn, err := req.RequireString("upn")
	if err != nil {
		return mcpsdk.NewToolResultError("bad upn: " + err.Error()), nil
	}
	from, bad := parseTimeParam(req, "from")
	if bad != nil {
		return bad, nil
	}
	to, bad2 := parseTimeParam(req, "to")
	if bad2 != nil {
		return bad2, nil
	}
	report, err := s.svc.BuildUserHealthReport(ctx, api.UserHealthParams{
		Upn:  upn,
		From: from,
		To:   to,
	})
	if err != nil {
		return s.mapServiceErr(err), nil
	}
	return textAndJSON(summarizeUserHealth(report), report), nil
}

func (s *Server) handleFindFlakyMicrophones(ctx context.Context, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	from, badFrom := parseTimeParam(req, "from")
	if badFrom != nil {
		return badFrom, nil
	}
	to, badTo := parseTimeParam(req, "to")
	if badTo != nil {
		return badTo, nil
	}
	p := api.FindFlakyMicParams{
		From:            from,
		To:              to,
		MinConcealedPct: req.GetFloat("min_concealed_pct", 0),
		MinIncidents:    req.GetInt("min_incidents", 0),
		Limit:           req.GetInt("limit", 0),
	}
	mics, err := s.svc.FindFlakyMicrophones(ctx, p)
	if err != nil {
		return s.mapServiceErr(err), nil
	}
	return textAndJSON(summarizeFlakyMics(mics), mics), nil
}

func (s *Server) handleListSubnets(ctx context.Context, _ mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	out, err := s.svc.ListSubnets(ctx)
	if err != nil {
		return s.mapServiceErr(err), nil
	}
	return textAndJSON(summarizeSubnets(out), out), nil
}

func (s *Server) handleGetUserCard(ctx context.Context, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	upn, err := req.RequireString("upn")
	if err != nil {
		return mcpsdk.NewToolResultError("bad upn: " + err.Error()), nil
	}
	card, err := s.svc.GetUserCard(ctx, upn)
	if err != nil {
		return s.mapServiceErr(err), nil
	}
	// Missing card is a successful result, not an error. Use textOnly so
	// the nil pointer is never marshalled to the literal "null" JSON block,
	// which could cause LLM clients to attempt indexing into null.
	if card == nil {
		return textOnly(summarizeUserCard(nil, upn)), nil
	}
	return textAndJSON(summarizeUserCard(card, upn), card), nil
}

func (s *Server) handleFindBadNetworkHotspots(ctx context.Context, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	from, badFrom := parseTimeParam(req, "from")
	if badFrom != nil {
		return badFrom, nil
	}
	to, badTo := parseTimeParam(req, "to")
	if badTo != nil {
		return badTo, nil
	}
	p := api.HotspotsParams{
		From:        from,
		To:          to,
		MinCalls:    req.GetInt("min_calls", 0),
		MinBadRatio: req.GetFloat("min_bad_ratio", 0),
		GroupBy:     req.GetString("group_by", ""),
		Limit:       req.GetInt("limit", 0),
	}
	out, err := s.svc.FindNetworkHotspots(ctx, p)
	if err != nil {
		return s.mapServiceErr(err), nil
	}
	return textAndJSON(summarizeHotspots(out), out), nil
}

func (s *Server) handleFindCascades(ctx context.Context, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	callID, err := req.RequireString("call_id")
	if err != nil {
		return mcpsdk.NewToolResultError("bad call_id: " + err.Error()), nil
	}
	// Default / clamp lives inside findCascades so the two call sites
	// cannot drift. A missing or negative min_affected is treated as 0
	// here; findCascades substitutes the canonical default.
	minAffected := req.GetInt("min_affected", 0)

	detail, err := s.svc.GetCall(ctx, callID)
	if err != nil {
		return s.mapServiceErr(err), nil
	}
	cascades := findCascades(detail.Streams, minAffected)
	return textAndJSON(summarizeCascades(cascades), cascades), nil
}

// Package api is the HTTP layer on top of internal/store. Business logic
// lives in Service (zero net/http imports), handlers.go is a thin adapter
// around http.ServeMux, and server.go owns lifecycle + graceful shutdown.
//
// The split exists so Service can later be wrapped as an MCP tool surface
// with no rewrite: every Service method takes a typed param struct, returns
// Go types, and uses sentinel errors for the caller to map to transport.
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"teams_con/internal/store"
)

// Service param/result defaults and bounds. Kept in one place so handlers
// never hard-code them and tests can reference the same constants.
const (
	defaultListLimit = 100
	minListLimit     = 1
	maxListLimit     = 500
)

// verdictGood/Poor/Bad are the canonical call verdict strings defined in
// design.md Cross-Layer Contracts. They are never lowercased, never
// localised. Any value outside this set is rejected at the service boundary.
const (
	verdictGood = "Good"
	verdictPoor = "Poor"
	verdictBad  = "Bad"
)

// Sentinel errors that handlers (and a future MCP wrapper) map to transport
// error shapes. Service methods always wrap or return these for 400/404
// mapping; anything else is treated as 500 by the handler layer.
var (
	// ErrBadRequest signals malformed or invalid input (e.g. unknown verdict,
	// unparsable time). Handlers map this to 400.
	ErrBadRequest = errors.New("api: bad request")

	// ErrNotFound signals that a requested resource does not exist. Handlers
	// map this to 404. Returned by GetCall when the underlying store reports
	// store.ErrNotFound.
	ErrNotFound = errors.New("api: not found")
)

// Narrow reader interfaces let service_test.go inject fakes without spinning
// up a real *store.Client. Each interface is the minimum subset of the
// corresponding store repo method set that Service actually consumes.

type callsReader interface {
	List(ctx context.Context, params store.CallListParams) ([]store.Call, error)
	Get(ctx context.Context, id string) (*store.Call, error)
	ListMetaInWindow(ctx context.Context, from, to *time.Time) ([]store.CallMeta, error)
}

type streamsReader interface {
	ListByCall(ctx context.Context, callID string) ([]store.StreamRow, error)
	ListFlakyAudioRaw(ctx context.Context, callIDs []string, minConcealedPct float64) ([]store.StreamRow, error)
	ListByUserInCalls(ctx context.Context, upn string, callIDs []string) ([]store.StreamRow, error)
	// ListInWindowBySubnets powers ComputePeerBaseline (Phase 3). Callers
	// pre-compute callIDs via calls.ListMetaInWindow and pass the target
	// user's distinct subnets; the store returns every matching row and
	// Service filters out the target user in Go.
	ListInWindowBySubnets(ctx context.Context, callIDs, subnets []string) ([]store.StreamRow, error)
	// ListInWindow powers FindNetworkHotspots (Phase 4). Unlike
	// ListInWindowBySubnets it has no subnet filter — the hotspot
	// aggregation groups rows by subnet OR relayIp in Go, so the store
	// query only narrows by the callId window.
	ListInWindow(ctx context.Context, callIDs []string) ([]store.StreamRow, error)
}

type usersReader interface {
	List(ctx context.Context, params store.UserListParams) ([]store.UserStat, error)
}

type metaReader interface {
	GetCrawlerMeta(ctx context.Context) (*store.CrawlerMeta, error)
}

// subnetsListReader is the minimal read-only surface consumed by
// SubnetResolver. Keeping it narrow prevents read-only subsystems from
// accidentally mutating state via type assertion (H3 fix).
type subnetsListReader interface {
	List(ctx context.Context) ([]store.SubnetEntry, error)
}

// subnetsStore is the full CRUD surface consumed by Service write methods
// (UpsertSubnet / DeleteSubnet / GetSubnet / ListSubnets). It embeds
// subnetsListReader so *store.SubnetsRepo satisfies both with one
// declaration, and so the compiler catches any narrowing mistakes.
type subnetsStore interface {
	subnetsListReader
	Get(ctx context.Context, cidr string) (*store.SubnetEntry, error)
	Upsert(ctx context.Context, e store.SubnetEntry) error
	Delete(ctx context.Context, cidr string) error
}

// userCardsStore is the full CRUD surface consumed by Service write methods
// (UpsertUserCard / DeleteUserCard / GetUserCard / ListUserCards). Unlike
// subnetsStore there is no read-only split because no background resolver
// consumes the usercards collection — the only reader is
// BuildUserHealthReport which already runs through Service.
type userCardsStore interface {
	Upsert(ctx context.Context, c store.UserCard) error
	Get(ctx context.Context, upn string) (*store.UserCard, error)
	Delete(ctx context.Context, upn string) error
	List(ctx context.Context) ([]store.UserCard, error)
}

// dailySummaryReader is the minimal surface for per-day quality aggregation.
type dailySummaryReader interface {
	Summary(ctx context.Context, from, to time.Time) ([]store.DaySummary, error)
}

// mongoPinger abstracts the liveness probe used by Health. *store.Client
// satisfies this via its Ping method; tests inject a stub closure.
type mongoPinger interface {
	Ping(ctx context.Context) error
}

// Service is the HTTP-free business layer. Methods take typed params and
// return Go types + sentinel errors so they can be reused unchanged by a
// future MCP wrapper.
type Service struct {
	calls        callsReader
	streams      streamsReader
	users        usersReader
	meta         metaReader
	subnets      subnetsStore
	userCards    userCardsStore
	dailySummary dailySummaryReader
	pinger       mongoPinger
	log          *slog.Logger

	// subnetResolver caches the parsed subnet table for longest-prefix
	// lookups. It is constructed in NewService/newServiceFromDeps and is
	// invalidated by UpsertSubnet/DeleteSubnet so writes show up on the
	// next read without waiting for the TTL.
	subnetResolver *SubnetResolver
}

// NewService wires a Service over the repositories exposed by store.Client.
// This is the production constructor; tests use newServiceFromDeps to inject
// fake readers without a running Mongo.
func NewService(st *store.Client, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	s := &Service{
		calls:        st.Calls,
		streams:      st.Streams,
		users:        st.Users,
		meta:         st.Meta,
		subnets:      st.Subnets,
		userCards:    st.UserCards,
		dailySummary: st.DailySummary,
		pinger:       st,
		log:          log,
	}
	s.subnetResolver = NewSubnetResolver(s.subnets, log)
	return s
}

// newServiceFromDeps is the test seam — callers pass concrete (or fake)
// readers directly. It is unexported because production code has no reason
// to bypass NewService.
func newServiceFromDeps(
	calls callsReader,
	streams streamsReader,
	users usersReader,
	meta metaReader,
	subnets subnetsStore,
	userCards userCardsStore,
	dailySummary dailySummaryReader,
	pinger mongoPinger,
	log *slog.Logger,
) *Service {
	if log == nil {
		log = slog.Default()
	}
	s := &Service{
		calls:        calls,
		streams:      streams,
		users:        users,
		meta:         meta,
		subnets:      subnets,
		userCards:    userCards,
		dailySummary: dailySummary,
		pinger:       pinger,
		log:          log,
	}
	s.subnetResolver = NewSubnetResolver(s.subnets, log)
	return s
}

// ListCallsParams is the HTTP-free input struct for ListCalls. All fields
// are optional; zero/empty values are treated as "no filter".
type ListCallsParams struct {
	From            *time.Time
	To              *time.Time
	Verdict         string // "" | Good | Poor | Bad
	Upn             string
	MinParticipants int // 0 = disabled
	Limit           int
	Offset          int
}

// CallDetail is the GetCall result — a call plus its stream rows.
type CallDetail struct {
	Call    store.Call        `json:"call"`
	Streams []store.StreamRow `json:"streams"`
}

// Health is the /health payload. MongoOk reflects a live Ping; a failing
// ping does NOT turn Health into an error — the endpoint still returns
// 200 with MongoOk=false so operators can see the degraded state.
// GraphOk is intentionally omitted from this iteration (tracked as future
// work in design-api.md).
type Health struct {
	LastCrawlAt    *time.Time `json:"lastCrawlAt,omitempty"`
	LastCrawlError string     `json:"lastCrawlError,omitempty"`
	MongoOk        bool       `json:"mongoOk"`
}

// Health reads the crawler heartbeat from store.Meta and pings Mongo. A
// ping failure is logged and flipped to MongoOk=false; the method still
// returns a non-nil Health so the handler can render the degraded state as
// a regular 200 response.
func (s *Service) Health(ctx context.Context) (*Health, error) {
	h := &Health{}

	meta, err := s.meta.GetCrawlerMeta(ctx)
	if err != nil {
		return nil, fmt.Errorf("api: health meta: %w", err)
	}
	if !meta.LastCrawlAt.IsZero() {
		t := meta.LastCrawlAt
		h.LastCrawlAt = &t
	}
	h.LastCrawlError = meta.LastCrawlError

	if err := s.pinger.Ping(ctx); err != nil {
		s.log.Warn("api: mongo ping failed", slog.String("err", err.Error()))
		h.MongoOk = false
	} else {
		h.MongoOk = true
	}
	return h, nil
}

// ListCalls validates and normalises the supplied params, then delegates to
// store.CallsRepo.List. Invalid input (unknown verdict) is rejected with
// ErrBadRequest. Limit is clamped to [minListLimit, maxListLimit] with a
// default of defaultListLimit when zero or negative.
func (s *Service) ListCalls(ctx context.Context, p ListCallsParams) ([]store.Call, error) {
	if !isValidVerdict(p.Verdict) {
		return nil, fmt.Errorf("%w: unknown verdict %q", ErrBadRequest, p.Verdict)
	}
	if p.Offset < 0 {
		return nil, fmt.Errorf("%w: negative offset", ErrBadRequest)
	}

	limit := clampListLimit(p.Limit)

	storeParams := store.CallListParams{
		From:            p.From,
		To:              p.To,
		MinParticipants: p.MinParticipants,
		Limit:           limit,
		Offset:          p.Offset,
	}
	if p.Verdict != "" {
		v := p.Verdict
		storeParams.Verdict = &v
	}
	if p.Upn != "" {
		u := p.Upn
		storeParams.Upn = &u
	}

	calls, err := s.calls.List(ctx, storeParams)
	if err != nil {
		return nil, fmt.Errorf("api: list calls: %w", err)
	}
	return calls, nil
}

// GetCall returns a call and all of its stream rows. A missing call is
// mapped to ErrNotFound so handlers can emit 404; other store errors are
// wrapped with context.
func (s *Service) GetCall(ctx context.Context, id string) (*CallDetail, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: empty call id", ErrBadRequest)
	}

	call, err := s.calls.Get(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("api: get call: %w", err)
	}

	streams, err := s.streams.ListByCall(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("api: get call streams: %w", err)
	}

	return &CallDetail{Call: *call, Streams: streams}, nil
}

// ListUsers runs the per-user stats aggregation over an optional time window.
func (s *Service) ListUsers(ctx context.Context, from, to *time.Time) ([]store.UserStat, error) {
	stats, err := s.users.List(ctx, store.UserListParams{From: from, To: to})
	if err != nil {
		return nil, fmt.Errorf("api: list users: %w", err)
	}
	return stats, nil
}

// ListUserCalls is a convenience shortcut used by the /users/{upn}/calls
// route: it defers to ListCalls with Upn pre-set.
func (s *Service) ListUserCalls(
	ctx context.Context,
	upn string,
	from, to *time.Time,
	limit, offset int,
) ([]store.Call, error) {
	if upn == "" {
		return nil, fmt.Errorf("%w: empty upn", ErrBadRequest)
	}
	return s.ListCalls(ctx, ListCallsParams{
		From:   from,
		To:     to,
		Upn:    upn,
		Limit:  limit,
		Offset: offset,
	})
}

// isValidVerdict gates the Verdict filter: empty means "any", otherwise must
// match one of the canonical strings. Any other value is a client error.
func isValidVerdict(v string) bool {
	switch v {
	case "", verdictGood, verdictPoor, verdictBad:
		return true
	default:
		return false
	}
}

// clampListLimit mirrors store.clampLimit but is duplicated here because the
// service is the API-side authority for the public contract. Values below
// minListLimit (including zero and negatives) become defaultListLimit;
// values above maxListLimit are capped.
func clampListLimit(n int) int {
	if n < minListLimit {
		return defaultListLimit
	}
	if n > maxListLimit {
		return maxListLimit
	}
	return n
}

// maxDailySummaryWindow is the longest time window allowed for the daily
// quality summary. Longer requests are silently clamped so a careless LLM
// prompt cannot trigger a multi-month aggregation.
const maxDailySummaryWindow = 30 * 24 * time.Hour

// DailySummaryParams is the HTTP-free input for DailySummary. Both fields
// are expected to be set by the caller (MCP handler defaults to last 7d).
type DailySummaryParams struct {
	From time.Time
	To   time.Time
}

// DailySummary returns per-day quality aggregates for [From, To). The window
// is clamped to maxDailySummaryWindow; if the caller asks for more, From is
// silently moved forward.
func (s *Service) DailySummary(ctx context.Context, p DailySummaryParams) ([]store.DaySummary, error) {
	if p.To.Sub(p.From) > maxDailySummaryWindow {
		p.From = p.To.Add(-maxDailySummaryWindow)
	}
	rows, err := s.dailySummary.Summary(ctx, p.From, p.To)
	if err != nil {
		return nil, fmt.Errorf("api: daily summary: %w", err)
	}
	return rows, nil
}

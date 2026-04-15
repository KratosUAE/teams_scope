package store

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// StreamsRepo persists flat per-stream documents in the `streams`
// collection, one document per (session × segment × media × stream). Writes
// go through ReplaceByCall which is the only shape the crawler needs —
// rows are immutable once the call ends, so delete-then-insert by callId
// is safe and idempotent across crashes: a partially-written replace will
// simply be redone on the next crawl tick.
type StreamsRepo struct {
	coll *mongo.Collection
	log  *slog.Logger
}

// ReplaceByCall atomically (from the caller's point of view) swaps all
// streams belonging to callID with the supplied set. We do not use a Mongo
// transaction because:
//
//  1. The target deployment is a standalone mongo, not a replica set.
//  2. Teams stream rows are immutable post-call, so a crashed delete+insert
//     re-run on the next crawl tick converges to the correct state.
//
// When rows is empty the Insert phase is skipped — otherwise InsertMany
// would error out on an empty slice.
func (r *StreamsRepo) ReplaceByCall(ctx context.Context, callID string, rows []StreamRow) error {
	filter := bson.D{{Key: "callId", Value: callID}}
	if _, err := r.coll.DeleteMany(ctx, filter); err != nil {
		return fmt.Errorf("store: delete streams for call %s: %w", callID, err)
	}

	if len(rows) == 0 {
		return nil
	}

	docs := make([]any, len(rows))
	for i := range rows {
		// Defensive: enforce the caller-provided callId on every row so the
		// index and the filter stay in sync even if the caller forgot.
		rows[i].CallId = callID
		docs[i] = rows[i]
	}

	if _, err := r.coll.InsertMany(ctx, docs); err != nil {
		return fmt.Errorf("store: insert streams for call %s: %w", callID, err)
	}
	return nil
}

// HasStreams reports whether at least one stream document exists for callID.
// It uses CountDocuments with a limit of 1 so Mongo short-circuits after the
// first match — equivalent to an indexed existence check.
func (r *StreamsRepo) HasStreams(ctx context.Context, callID string) (bool, error) {
	filter := bson.D{{Key: "callId", Value: callID}}
	n, err := r.coll.CountDocuments(ctx, filter, options.Count().SetLimit(1))
	if err != nil {
		return false, fmt.Errorf("store: has streams for call %s: %w", callID, err)
	}
	return n >= 1, nil
}

// ListFlakyAudioRaw returns every stream row belonging to one of callIDs
// whose direction is send/upload, whose captureDevice is not empty, and
// whose concealedPct meets or exceeds minConcealedPct. It is the raw input
// to the Service-level find_flaky_microphones heuristic: the final
// streamLabel==audio filter and (user, captureDevice) grouping happen in
// the Service layer because StreamLabel normalisation lives there.
//
// An empty callIDs slice returns (nil, nil) without touching Mongo — this
// keeps the Service happy path clean when the calls window is empty.
func (r *StreamsRepo) ListFlakyAudioRaw(ctx context.Context, callIDs []string, minConcealedPct float64) ([]StreamRow, error) {
	if len(callIDs) == 0 {
		return nil, nil
	}

	filter := bson.D{
		{Key: "callId", Value: bson.D{{Key: "$in", Value: callIDs}}},
		{Key: "direction", Value: bson.D{{Key: "$in", Value: []string{"send", "upload"}}}},
		{Key: "captureDevice", Value: bson.D{{Key: "$ne", Value: ""}}},
		{Key: "concealedPct", Value: bson.D{{Key: "$gte", Value: minConcealedPct}}},
	}

	cursor, err := r.coll.Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("store: list flaky audio streams: %w", err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if cerr := cursor.Close(closeCtx); cerr != nil {
			r.log.Warn("store: close flaky audio cursor", slog.String("err", cerr.Error()))
		}
	}()

	out := make([]StreamRow, 0, 64)
	if err := cursor.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("store: decode flaky audio streams: %w", err)
	}
	return out, nil
}

// ListByUserInCalls returns every stream row where user == upn and the
// parent callId is in the supplied set. It powers user-scoped analytics
// (user_health_report) that need one user's stream history across a time
// window without paying for an N-call ListByCall loop. The query uses the
// existing streams.user index plus $in on callId.
//
// An empty callIDs or empty upn returns (nil, nil) without touching Mongo.
func (r *StreamsRepo) ListByUserInCalls(ctx context.Context, upn string, callIDs []string) ([]StreamRow, error) {
	if upn == "" || len(callIDs) == 0 {
		return nil, nil
	}
	filter := bson.D{
		{Key: "user", Value: upn},
		{Key: "callId", Value: bson.D{{Key: "$in", Value: callIDs}}},
	}
	cursor, err := r.coll.Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("store: list streams by user: %w", err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if cerr := cursor.Close(closeCtx); cerr != nil {
			r.log.Warn("store: close user streams cursor", slog.String("err", cerr.Error()))
		}
	}()
	out := make([]StreamRow, 0, 32)
	if err := cursor.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("store: decode user streams: %w", err)
	}
	return out, nil
}

// ListInWindowBySubnets returns every stream row whose parent callId is in
// the supplied set AND whose subnet is in the supplied subnet set. It is
// the Phase 3 peer-baseline query: the Service layer pre-computes callIDs
// from CallsRepo.ListMetaInWindow (so the window filter rides on the
// existing calls.startTimeUtc index) and passes them here together with
// the target user's distinct subnets. Projection is implicit: the full
// StreamRow is decoded because callers need user+subnet+callId to tally
// the cohort, and the hot fields are cheap.
//
// Empty callIDs OR empty subnets returns (nil, nil) without touching
// Mongo — an empty $in would match nothing and waste a round trip.
func (r *StreamsRepo) ListInWindowBySubnets(ctx context.Context, callIDs, subnets []string) ([]StreamRow, error) {
	if len(callIDs) == 0 || len(subnets) == 0 {
		return nil, nil
	}
	filter := bson.D{
		{Key: "callId", Value: bson.D{{Key: "$in", Value: callIDs}}},
		{Key: "subnet", Value: bson.D{{Key: "$in", Value: subnets}}},
	}
	cursor, err := r.coll.Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("store: list streams in window by subnets: %w", err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if cerr := cursor.Close(closeCtx); cerr != nil {
			r.log.Warn("store: close peer-baseline cursor", slog.String("err", cerr.Error()))
		}
	}()
	out := make([]StreamRow, 0, 64)
	if err := cursor.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("store: decode peer-baseline streams: %w", err)
	}
	return out, nil
}

// ListInWindow returns every stream row whose callId is in callIDs,
// projecting only the fields the Phase 4 hotspots aggregation needs:
// callId, user, subnet, relayIp, avgRttMs, avgJitterMs, avgLossPct. No
// user filter, no metric filter — the Service layer applies the
// threshold/group logic in pure Go once the rows are in memory.
//
// The Service layer pre-computes callIDs from ListMetaInWindow (the
// existing two-phase Mongo shape used by FindFlakyMicrophones and
// ComputePeerBaseline) so the streams query rides the existing callId
// index. Empty callIDs returns (nil, nil) without touching Mongo.
func (r *StreamsRepo) ListInWindow(ctx context.Context, callIDs []string) ([]StreamRow, error) {
	if len(callIDs) == 0 {
		return nil, nil
	}
	filter := bson.D{
		{Key: "callId", Value: bson.D{{Key: "$in", Value: callIDs}}},
	}
	opts := options.Find().SetProjection(bson.D{
		{Key: "callId", Value: 1},
		{Key: "user", Value: 1},
		{Key: "subnet", Value: 1},
		{Key: "relayIp", Value: 1},
		{Key: "avgRttMs", Value: 1},
		{Key: "avgJitterMs", Value: 1},
		{Key: "avgLossPct", Value: 1},
	})
	cursor, err := r.coll.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("store: list streams in window: %w", err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if cerr := cursor.Close(closeCtx); cerr != nil {
			r.log.Warn("store: close hotspots cursor", slog.String("err", cerr.Error()))
		}
	}()
	out := make([]StreamRow, 0, 64)
	if err := cursor.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("store: decode hotspots streams: %w", err)
	}
	return out, nil
}

// ListByCall returns every stream row belonging to callID, sorted by
// SegmentStart ascending so the drill-down view can render a timeline.
func (r *StreamsRepo) ListByCall(ctx context.Context, callID string) ([]StreamRow, error) {
	filter := bson.D{{Key: "callId", Value: callID}}
	opts := options.Find().SetSort(bson.D{{Key: "segmentStart", Value: 1}})

	cursor, err := r.coll.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("store: list streams for call %s: %w", callID, err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if cerr := cursor.Close(closeCtx); cerr != nil {
			r.log.Warn("store: close streams cursor", slog.String("err", cerr.Error()))
		}
	}()

	out := make([]StreamRow, 0, 8)
	if err := cursor.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("store: decode streams: %w", err)
	}
	return out, nil
}

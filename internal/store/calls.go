package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Call list pagination bounds. The TUI defaults to 100 rows per page and
// never needs more than 500; clamping here keeps the API honest regardless
// of what a client sends.
const (
	defaultCallListLimit = 100
	maxCallListLimit     = 500
	minCallListLimit     = 1
)

// CallsRepo persists flat per-call documents in the `calls` collection.
// The _id field is the Graph callId string, so upsert-by-id is the natural
// idempotency key for the crawler.
type CallsRepo struct {
	coll *mongo.Collection
	log  *slog.Logger
}

// CallListParams is the filter/pagination struct accepted by List. All
// fields are optional; nil/zero values are skipped. Limit is clamped to
// [minCallListLimit, maxCallListLimit] with a default of defaultCallListLimit
// when <= 0.
type CallListParams struct {
	From            *time.Time
	To              *time.Time
	Verdict         *string
	Upn             *string
	MinParticipants int // 0 = disabled
	Limit           int
	Offset          int
}

// Upsert writes c to the calls collection, creating the document if absent
// and overwriting it if present. FetchedAt is set to time.Now() before the
// write so the freshest fetch timestamp always wins — this is safe because
// Teams call records are immutable once the call ends.
func (r *CallsRepo) Upsert(ctx context.Context, c Call) error {
	c.FetchedAt = time.Now().UTC()

	// ReplaceOne avoids the "Mod on _id not allowed" error that $set triggers
	// when the document already exists, because replacing the full document
	// never touches the immutable _id field.
	filter := bson.D{{Key: "_id", Value: c.CallId}}
	opts := options.Replace().SetUpsert(true)
	if _, err := r.coll.ReplaceOne(ctx, filter, c, opts); err != nil {
		return fmt.Errorf("store: calls upsert: %w", err)
	}
	return nil
}

// Exists is a cheap existence check used by the crawler to skip re-fetch of
// calls it has already persisted. CountDocuments with Limit=1 avoids
// decoding any document body, which is what we want on the hot path.
func (r *CallsRepo) Exists(ctx context.Context, id string) (bool, error) {
	filter := bson.D{{Key: "_id", Value: id}}
	opts := options.Count().SetLimit(1)
	n, err := r.coll.CountDocuments(ctx, filter, opts)
	if err != nil {
		return false, fmt.Errorf("store: exists call %s: %w", id, err)
	}
	return n > 0, nil
}

// GetState returns (exists, streamsProjected) for the given call id in a
// single round-trip. The crawler uses it on every tick to decide whether a
// call has been fully processed (skip) or only partially written (heal).
// Projecting only the bare minimum keeps the hot path cheap; absent
// documents are reported as (false, false, nil) without an error so the
// caller can treat "not found" as a normal branch.
func (r *CallsRepo) GetState(ctx context.Context, id string) (bool, bool, error) {
	type stateProjection struct {
		StreamsProjected bool `bson:"streamsProjected"`
	}
	var s stateProjection
	filter := bson.D{{Key: "_id", Value: id}}
	opts := options.FindOne().SetProjection(bson.D{
		{Key: "streamsProjected", Value: 1},
	})
	err := r.coll.FindOne(ctx, filter, opts).Decode(&s)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return false, false, nil
		}
		return false, false, fmt.Errorf("store: get call state %s: %w", id, err)
	}
	return true, s.StreamsProjected, nil
}

// MarkStreamsProjected sets streamsProjected=true on the call document. The
// crawler calls this after a successful ReplaceByCall — even when the
// stream projection was empty — so the next tick's GetState returns
// (true, true) and the heal branch is bypassed.
func (r *CallsRepo) MarkStreamsProjected(ctx context.Context, id string) error {
	filter := bson.D{{Key: "_id", Value: id}}
	update := bson.D{{Key: "$set", Value: bson.D{{Key: "streamsProjected", Value: true}}}}
	if _, err := r.coll.UpdateOne(ctx, filter, update); err != nil {
		return fmt.Errorf("store: mark streams projected %s: %w", id, err)
	}
	return nil
}

// Get fetches a single call by id. Returns ErrNotFound (wrappable via
// errors.Is) when the document is absent.
func (r *CallsRepo) Get(ctx context.Context, id string) (*Call, error) {
	var c Call
	filter := bson.D{{Key: "_id", Value: id}}
	if err := r.coll.FindOne(ctx, filter).Decode(&c); err != nil {
		return nil, wrapNotFound(err, fmt.Sprintf("get call %s", id))
	}
	return &c, nil
}

// List returns calls matching params, sorted newest-first by startTimeUtc.
// The filter is built by buildCallFilter (a pure helper, unit-tested) so
// this method is a thin wrapper around Find + cursor decode.
func (r *CallsRepo) List(ctx context.Context, params CallListParams) ([]Call, error) {
	filter := buildCallFilter(params)
	limit := clampLimit(params.Limit)

	findOpts := options.Find().
		SetSort(bson.D{{Key: "startTimeUtc", Value: -1}}).
		SetLimit(int64(limit))
	if params.Offset > 0 {
		findOpts.SetSkip(int64(params.Offset))
	}

	cursor, err := r.coll.Find(ctx, filter, findOpts)
	if err != nil {
		return nil, fmt.Errorf("store: list calls: %w", err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if cerr := cursor.Close(closeCtx); cerr != nil {
			r.log.Warn("store: close calls cursor", slog.String("err", cerr.Error()))
		}
	}()

	out := make([]Call, 0, limit)
	if err := cursor.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("store: decode calls: %w", err)
	}
	return out, nil
}

// buildCallFilter is a pure helper that translates CallListParams into a
// bson.D filter. Kept separate from List so it can be unit-tested without a
// running MongoDB instance.
func buildCallFilter(p CallListParams) bson.D {
	filter := bson.D{}

	if p.From != nil || p.To != nil {
		rng := bson.D{}
		if p.From != nil {
			rng = append(rng, bson.E{Key: "$gte", Value: *p.From})
		}
		if p.To != nil {
			rng = append(rng, bson.E{Key: "$lt", Value: *p.To})
		}
		filter = append(filter, bson.E{Key: "startTimeUtc", Value: rng})
	}
	if p.Verdict != nil && *p.Verdict != "" {
		filter = append(filter, bson.E{Key: "verdict", Value: *p.Verdict})
	}
	if p.Upn != nil && *p.Upn != "" {
		filter = append(filter, bson.E{Key: "participants", Value: *p.Upn})
	}
	if p.MinParticipants > 0 {
		filter = append(filter, bson.E{Key: "participantCount", Value: bson.D{{Key: "$gte", Value: p.MinParticipants}}})
	}
	return filter
}

// CallMeta is the light projection returned by ListMetaInWindow. It is a
// subset of Call used by cross-call analytics (find_flaky_microphones) that
// need to correlate stream rows with their parent call's start time and
// verdict without paying the decode cost of the full Call document.
type CallMeta struct {
	CallId       string    `bson:"_id"          json:"callId"`
	StartTimeUtc time.Time `bson:"startTimeUtc" json:"startTimeUtc"`
	Verdict      string    `bson:"verdict"      json:"verdict"`
}

// ListMetaInWindow returns a projection-only view of every call whose
// startTimeUtc falls inside [from, to). Unlike List, it does not clamp the
// result size — analytical queries need every match in the window. The
// projection keeps the decoded documents small so a week-long window with
// thousands of calls fits comfortably in memory.
func (r *CallsRepo) ListMetaInWindow(ctx context.Context, from, to *time.Time) ([]CallMeta, error) {
	filter := bson.D{}
	if from != nil || to != nil {
		rng := bson.D{}
		if from != nil {
			rng = append(rng, bson.E{Key: "$gte", Value: *from})
		}
		if to != nil {
			rng = append(rng, bson.E{Key: "$lt", Value: *to})
		}
		filter = append(filter, bson.E{Key: "startTimeUtc", Value: rng})
	}

	opts := options.Find().SetProjection(bson.D{
		{Key: "_id", Value: 1},
		{Key: "startTimeUtc", Value: 1},
		{Key: "verdict", Value: 1},
	})

	cursor, err := r.coll.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("store: list call meta in window: %w", err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if cerr := cursor.Close(closeCtx); cerr != nil {
			r.log.Warn("store: close call meta cursor", slog.String("err", cerr.Error()))
		}
	}()

	out := make([]CallMeta, 0, 64)
	if err := cursor.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("store: decode call meta: %w", err)
	}
	return out, nil
}

// clampLimit returns n clamped to [minCallListLimit, maxCallListLimit].
// Any value below minCallListLimit (1), including zero and negatives, falls
// back to defaultCallListLimit.
func clampLimit(n int) int {
	if n < minCallListLimit {
		return defaultCallListLimit
	}
	if n > maxCallListLimit {
		return maxCallListLimit
	}
	return n
}

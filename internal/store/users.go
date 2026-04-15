package store

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// UsersRepo produces per-user call statistics by aggregating over the
// `calls` collection. There is no dedicated `users` collection — the TUI
// users tab is always a fresh aggregation so newly-crawled calls show up
// without any background rollup job.
type UsersRepo struct {
	calls *mongo.Collection
	log   *slog.Logger
}

// UserListParams bounds the aggregation time window. Both fields are
// optional: nil From/To means "no lower/upper bound".
type UserListParams struct {
	From *time.Time
	To   *time.Time
}

// List runs the per-user aggregation pipeline. The pipeline is:
//
//  1. $match — optional startTimeUtc range.
//  2. $unwind participants — one row per (call × participant).
//  3. $group by participant — count total + per-verdict buckets.
//  4. $sort _id asc — alphabetical UPN order for the TUI.
//
// Verdict bucketing uses $cond on equality with the canonical "Good" /
// "Poor" / "Bad" strings defined in design.md Cross-Layer Contracts —
// never lowercased, never localised.
func (r *UsersRepo) List(ctx context.Context, params UserListParams) ([]UserStat, error) {
	pipeline := bson.A{}

	if params.From != nil || params.To != nil {
		rng := bson.D{}
		if params.From != nil {
			rng = append(rng, bson.E{Key: "$gte", Value: *params.From})
		}
		if params.To != nil {
			rng = append(rng, bson.E{Key: "$lt", Value: *params.To})
		}
		pipeline = append(pipeline, bson.D{{Key: "$match", Value: bson.D{
			{Key: "startTimeUtc", Value: rng},
		}}})
	}

	pipeline = append(pipeline,
		bson.D{{Key: "$unwind", Value: "$participants"}},
		bson.D{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: "$participants"},
			{Key: "callCount", Value: bson.D{{Key: "$sum", Value: 1}}},
	// "$verdict" here references the call-level field that survives $unwind of
			// "participants" — it is intentionally NOT the unwound element. Do NOT
			// change these to "$participants.verdict" or any other path; the
			// participants array holds plain UPN strings, not objects.
			{Key: "goodCount", Value: bson.D{{Key: "$sum", Value: bson.D{
				{Key: "$cond", Value: bson.A{
					bson.D{{Key: "$eq", Value: bson.A{"$verdict", "Good"}}},
					1, 0,
				}},
			}}}},
			{Key: "poorCount", Value: bson.D{{Key: "$sum", Value: bson.D{
				{Key: "$cond", Value: bson.A{
					bson.D{{Key: "$eq", Value: bson.A{"$verdict", "Poor"}}},
					1, 0,
				}},
			}}}},
			{Key: "badCount", Value: bson.D{{Key: "$sum", Value: bson.D{
				{Key: "$cond", Value: bson.A{
					bson.D{{Key: "$eq", Value: bson.A{"$verdict", "Bad"}}},
					1, 0,
				}},
			}}}},
		}}},
		bson.D{{Key: "$sort", Value: bson.D{{Key: "_id", Value: 1}}}},
	)

	cursor, err := r.calls.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("store: aggregate user stats: %w", err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if cerr := cursor.Close(closeCtx); cerr != nil {
			r.log.Warn("store: close users cursor", slog.String("err", cerr.Error()))
		}
	}()

	out := make([]UserStat, 0, 16)
	if err := cursor.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("store: decode user stats: %w", err)
	}
	return out, nil
}

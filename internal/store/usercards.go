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

// userCardsCursorCloseTimeout bounds the deferred cursor cleanup in List.
const userCardsCursorCloseTimeout = 5 * time.Second

// UserCardsRepo persists per-UPN operator annotations in the `usercards`
// collection. The _id is the UPN so Upsert is idempotent without a
// secondary key. No extra indexes are needed — _id is implicit.
type UserCardsRepo struct {
	coll *mongo.Collection
	log  *slog.Logger
}

// Upsert writes c to the usercards collection, creating it if absent. The
// caller is responsible for stamping UpdatedAt — the Service layer does so
// before calling here.
func (r *UserCardsRepo) Upsert(ctx context.Context, c UserCard) error {
	filter := bson.D{{Key: "_id", Value: c.Upn}}
	opts := options.Replace().SetUpsert(true)
	if _, err := r.coll.ReplaceOne(ctx, filter, c, opts); err != nil {
		return fmt.Errorf("store: usercards upsert %s: %w", c.Upn, err)
	}
	return nil
}

// Get returns a single user card by upn. Missing documents are reported
// via ErrNotFound (errors.Is friendly). The Service layer translates this
// to a (nil, nil) result for GetUserCard by design — "no card" is a
// normal state there, not an error.
func (r *UserCardsRepo) Get(ctx context.Context, upn string) (*UserCard, error) {
	var c UserCard
	filter := bson.D{{Key: "_id", Value: upn}}
	if err := r.coll.FindOne(ctx, filter).Decode(&c); err != nil {
		return nil, wrapNotFound(err, fmt.Sprintf("get usercard %s", upn))
	}
	return &c, nil
}

// Delete removes the user card keyed by upn. Returns ErrNotFound when the
// document does not exist so callers can distinguish "no-op" from
// "deleted".
func (r *UserCardsRepo) Delete(ctx context.Context, upn string) error {
	filter := bson.D{{Key: "_id", Value: upn}}
	res, err := r.coll.DeleteOne(ctx, filter)
	if err != nil {
		return fmt.Errorf("store: usercards delete %s: %w", upn, err)
	}
	if res.DeletedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// List returns every user card sorted by Upn ascending so the TUI and CLI
// render them in a stable, human-friendly order regardless of insertion
// order. Sort key is _id which is already indexed — no extra index needed.
func (r *UserCardsRepo) List(ctx context.Context) ([]UserCard, error) {
	opts := options.Find().SetSort(bson.D{{Key: "_id", Value: 1}})
	cursor, err := r.coll.Find(ctx, bson.D{}, opts)
	if err != nil {
		return nil, fmt.Errorf("store: list usercards: %w", err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), userCardsCursorCloseTimeout)
		defer cancel()
		if cerr := cursor.Close(closeCtx); cerr != nil {
			r.log.Warn("store: close usercards cursor", slog.String("err", cerr.Error()))
		}
	}()

	out := make([]UserCard, 0, 16)
	if err := cursor.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("store: decode usercards: %w", err)
	}
	return out, nil
}

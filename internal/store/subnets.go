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

// subnetsCursorCloseTimeout bounds the deferred cursor cleanup in List.
const subnetsCursorCloseTimeout = 5 * time.Second

// SubnetsRepo persists CIDR-keyed subnet labels in the `subnets` collection.
// The _id is the canonical CIDR string; the resolver in internal/api loads
// the full collection on demand and walks it longest-prefix-first, so the
// repo itself only exposes a flat CRUD surface.
type SubnetsRepo struct {
	coll *mongo.Collection
	log  *slog.Logger
}

// Upsert writes e to the subnets collection, creating it if absent. The
// caller is responsible for canonicalising the CIDR and stamping UpdatedAt
// — the Service layer does both before calling here.
func (r *SubnetsRepo) Upsert(ctx context.Context, e SubnetEntry) error {
	filter := bson.D{{Key: "_id", Value: e.Cidr}}
	opts := options.Replace().SetUpsert(true)
	if _, err := r.coll.ReplaceOne(ctx, filter, e, opts); err != nil {
		return fmt.Errorf("store: subnets upsert %s: %w", e.Cidr, err)
	}
	return nil
}

// Get returns a single subnet entry by canonical CIDR. Missing documents are
// reported via ErrNotFound (errors.Is friendly).
func (r *SubnetsRepo) Get(ctx context.Context, cidr string) (*SubnetEntry, error) {
	var e SubnetEntry
	filter := bson.D{{Key: "_id", Value: cidr}}
	if err := r.coll.FindOne(ctx, filter).Decode(&e); err != nil {
		return nil, wrapNotFound(err, fmt.Sprintf("get subnet %s", cidr))
	}
	return &e, nil
}

// Delete removes the subnet entry keyed by cidr. Returns ErrNotFound when
// the document does not exist so callers can distinguish "no-op" from
// "deleted".
func (r *SubnetsRepo) Delete(ctx context.Context, cidr string) error {
	filter := bson.D{{Key: "_id", Value: cidr}}
	res, err := r.coll.DeleteOne(ctx, filter)
	if err != nil {
		return fmt.Errorf("store: subnets delete %s: %w", cidr, err)
	}
	if res.DeletedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// List returns every subnet entry sorted by Name ascending so the TUI and
// CLI render them in a stable, human-friendly order regardless of insertion
// order.
func (r *SubnetsRepo) List(ctx context.Context) ([]SubnetEntry, error) {
	opts := options.Find().SetSort(bson.D{{Key: "name", Value: 1}})
	cursor, err := r.coll.Find(ctx, bson.D{}, opts)
	if err != nil {
		return nil, fmt.Errorf("store: list subnets: %w", err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), subnetsCursorCloseTimeout)
		defer cancel()
		if cerr := cursor.Close(closeCtx); cerr != nil {
			r.log.Warn("store: close subnets cursor", slog.String("err", cerr.Error()))
		}
	}()

	out := make([]SubnetEntry, 0, 16)
	if err := cursor.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("store: decode subnets: %w", err)
	}
	return out, nil
}

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

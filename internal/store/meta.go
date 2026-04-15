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

// crawlerMetaID is the fixed _id of the singleton meta document.
const crawlerMetaID = "crawler"

// MetaRepo owns the singleton document in the `meta` collection under
// _id="crawler". The api layer reads it for /health and the crawler writes
// to it on every tick.
type MetaRepo struct {
	coll *mongo.Collection
	log  *slog.Logger
}

// SetLastCrawl upserts the meta document with the supplied crawl timestamp.
// crawlErr may be nil, in which case the stored lastCrawlError is cleared.
// This is the crawler's heartbeat — /health surfaces it verbatim.
func (r *MetaRepo) SetLastCrawl(ctx context.Context, at time.Time, crawlErr error) error {
	errStr := ""
	if crawlErr != nil {
		errStr = crawlErr.Error()
	}
	update := bson.D{{Key: "$set", Value: bson.D{
		{Key: "lastCrawlAt", Value: at.UTC()},
		{Key: "lastCrawlError", Value: errStr},
	}}}
	filter := bson.D{{Key: "_id", Value: crawlerMetaID}}
	opts := options.UpdateOne().SetUpsert(true)
	if _, err := r.coll.UpdateOne(ctx, filter, update, opts); err != nil {
		return fmt.Errorf("store: set last crawl: %w", err)
	}
	return nil
}

// SetLastBackfill upserts only the lastBackfillAt field so it is independent
// of the tick heartbeat — a backfill run does not reset lastCrawlAt.
func (r *MetaRepo) SetLastBackfill(ctx context.Context, at time.Time) error {
	update := bson.D{{Key: "$set", Value: bson.D{
		{Key: "lastBackfillAt", Value: at.UTC()},
	}}}
	filter := bson.D{{Key: "_id", Value: crawlerMetaID}}
	opts := options.UpdateOne().SetUpsert(true)
	if _, err := r.coll.UpdateOne(ctx, filter, update, opts); err != nil {
		return fmt.Errorf("store: set last backfill: %w", err)
	}
	return nil
}

// GetCrawlerMeta returns the singleton meta document. A missing document is
// not an error — callers get a zero-value CrawlerMeta instead, which lets
// /health render "never crawled" without special-casing the first run.
func (r *MetaRepo) GetCrawlerMeta(ctx context.Context) (*CrawlerMeta, error) {
	var m CrawlerMeta
	filter := bson.D{{Key: "_id", Value: crawlerMetaID}}
	err := r.coll.FindOne(ctx, filter).Decode(&m)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return &CrawlerMeta{ID: crawlerMetaID}, nil
		}
		return nil, fmt.Errorf("store: get crawler meta: %w", err)
	}
	return &m, nil
}

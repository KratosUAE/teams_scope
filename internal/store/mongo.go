package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

// defaultDatabase is used when the Mongo URI has no path segment.
const defaultDatabase = "teams_con"

// connectTimeout bounds the Connect+Ping handshake in New.
const connectTimeout = 10 * time.Second

// indexTimeout bounds the EnsureIndexes CreateMany calls.
const indexTimeout = 10 * time.Second

// Client is the store entry point. It owns the underlying mongo.Client and
// exposes repositories as exported fields so callers can write
// `store.Calls.Upsert(...)` without a second hop.
//
// Construction (New) connects, pings, and wires up the repos. Close
// disconnects the underlying mongo.Client. EnsureIndexes is idempotent and
// safe to call on every startup.
type Client struct {
	client *mongo.Client
	db     *mongo.Database
	log    *slog.Logger

	Calls        *CallsRepo
	Streams      *StreamsRepo
	Meta         *MetaRepo
	Users        *UsersRepo
	Subnets      *SubnetsRepo
	UserCards    *UserCardsRepo
	DailySummary *DailySummaryRepo
	RelayGeo     *RelayGeoRepo
}

// New connects to MongoDB at uri, pings the primary, and returns a ready
// Client with all repositories wired up. If dbName is empty the database is
// parsed from the URI path; if that is also empty, defaultDatabase is used.
//
// On ping failure the underlying client is disconnected before returning the
// error so we do not leak the connection pool.
func New(ctx context.Context, uri, dbName string, log *slog.Logger) (*Client, error) {
	if log == nil {
		log = slog.Default()
	}

	opts := options.Client().
		ApplyURI(uri).
		SetAppName("teams_con").
		SetServerSelectionTimeout(connectTimeout)

	mc, err := mongo.Connect(opts)
	if err != nil {
		return nil, fmt.Errorf("store: connect mongo: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	if err := mc.Ping(pingCtx, readpref.Primary()); err != nil {
		// Best-effort cleanup — ignore disconnect error, surface ping error.
		_ = mc.Disconnect(context.Background())
		return nil, fmt.Errorf("store: ping mongo: %w", err)
	}

	if dbName == "" {
		dbName = databaseFromURI(uri)
	}
	db := mc.Database(dbName)

	c := &Client{
		client:    mc,
		db:        db,
		log:       log,
		Calls:        &CallsRepo{coll: db.Collection("calls"), log: log},
		Streams:      &StreamsRepo{coll: db.Collection("streams"), log: log},
		Meta:         &MetaRepo{coll: db.Collection("meta"), log: log},
		Users:        &UsersRepo{calls: db.Collection("calls"), log: log},
		Subnets:      &SubnetsRepo{coll: db.Collection("subnets"), log: log},
		UserCards:    &UserCardsRepo{coll: db.Collection("usercards"), log: log},
		DailySummary: newDailySummaryRepo(db),
		RelayGeo:     newRelayGeoRepo(db),
	}

	log.Info("store: connected", slog.String("database", dbName))
	return c, nil
}

// Ping verifies the primary is reachable. Used by the api /health endpoint
// to report mongo liveness without having to reach into the driver directly.
// Returns nil on success or a wrapped driver error.
func (c *Client) Ping(ctx context.Context) error {
	if c == nil || c.client == nil {
		return fmt.Errorf("store: ping mongo: nil client")
	}
	if err := c.client.Ping(ctx, readpref.Primary()); err != nil {
		return fmt.Errorf("store: ping mongo: %w", err)
	}
	return nil
}

// Close disconnects the underlying mongo.Client. It is safe to call on a
// nil Client (no-op) so cleanup paths stay simple.
func (c *Client) Close(ctx context.Context) error {
	if c == nil || c.client == nil {
		return nil
	}
	if err := c.client.Disconnect(ctx); err != nil {
		return fmt.Errorf("store: disconnect mongo: %w", err)
	}
	c.log.Info("store: disconnected")
	return nil
}

// EnsureIndexes creates every index required by the repositories. It is
// idempotent — CreateMany is a no-op for indexes that already exist with
// matching specs. Called once on startup from the crawler/api entry points.
func (c *Client) EnsureIndexes(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, indexTimeout)
	defer cancel()

	callsIdx := []mongo.IndexModel{
		{Keys: bson.D{{Key: "startTimeUtc", Value: -1}}},
		{Keys: bson.D{{Key: "participants", Value: 1}}},
		{Keys: bson.D{
			{Key: "verdict", Value: 1},
			{Key: "startTimeUtc", Value: -1},
		}},
	}
	if _, err := c.db.Collection("calls").Indexes().CreateMany(ctx, callsIdx); err != nil {
		return fmt.Errorf("store: ensure calls indexes: %w", err)
	}

	streamsIdx := []mongo.IndexModel{
		{Keys: bson.D{{Key: "callId", Value: 1}}},
		{Keys: bson.D{{Key: "user", Value: 1}}},
		// Phase 3 peer-baseline: the ComputePeerBaseline query filters
		// streams by `subnet IN (...)` alongside the existing callId $in,
		// so subnet needs its own index to stay bounded.
		{Keys: bson.D{{Key: "subnet", Value: 1}}},
	}
	if _, err := c.db.Collection("streams").Indexes().CreateMany(ctx, streamsIdx); err != nil {
		return fmt.Errorf("store: ensure streams indexes: %w", err)
	}

	subnetsIdx := []mongo.IndexModel{
		{Keys: bson.D{{Key: "office", Value: 1}}},
	}
	if _, err := c.db.Collection("subnets").Indexes().CreateMany(ctx, subnetsIdx); err != nil {
		return fmt.Errorf("store: ensure subnets indexes: %w", err)
	}

	c.log.Info("store: indexes ensured")
	return nil
}

// databaseFromURI extracts the database name from a Mongo connection URI
// path (e.g. "mongodb://host:27017/teams_con" → "teams_con"). It is tolerant
// of malformed URIs and falls back to defaultDatabase on any parse failure,
// since the URI has already been validated by the driver at this point.
func databaseFromURI(uri string) string {
	u, err := url.Parse(uri)
	if err != nil {
		return defaultDatabase
	}
	name := strings.TrimPrefix(u.Path, "/")
	// The URI path may contain auth options after '/' in legacy forms;
	// take only the first segment.
	if i := strings.IndexAny(name, "/?"); i >= 0 {
		name = name[:i]
	}
	if name == "" {
		return defaultDatabase
	}
	return name
}

// wrapNotFound maps mongo.ErrNoDocuments to the store sentinel ErrNotFound.
// Other errors are returned wrapped with context so errors.Is still works
// for ErrNoDocuments further up if callers opt in.
func wrapNotFound(err error, op string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, mongo.ErrNoDocuments) {
		return ErrNotFound
	}
	return fmt.Errorf("store: %s: %w", op, err)
}

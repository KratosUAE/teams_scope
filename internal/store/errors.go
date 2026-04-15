// Package store provides MongoDB persistence for the teams_con project.
//
// It exposes three repositories (Calls, Streams, Meta) plus a Users
// aggregation repo, all backed by go.mongodb.org/mongo-driver/v2. Domain
// types (Call, StreamRow, CrawlerMeta, UserStat) live in this package and
// are the single source of truth for bson + json tags — the api layer
// re-exports them as-is as its DTOs.
//
// Integration tests require a running MongoDB instance and are intentionally
// omitted from this initial commit; they will be added once the
// docker-compose stack is runnable. The only unit tests here cover pure
// helpers (filter construction, limit clamping) that need no database.
package store

import "errors"

// ErrNotFound is returned by repository methods when a requested document
// does not exist. Callers should use errors.Is to check for it.
var ErrNotFound = errors.New("store: not found")

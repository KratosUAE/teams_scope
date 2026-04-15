package store

import (
	"net/url"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// NOTE: integration tests for the store package require a running MongoDB
// instance and will be added once the docker-compose stack is runnable.
// The tests in this file cover only pure helpers (filter construction,
// limit clamping, URI parsing) that do not touch the database.

func TestClampLimit(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{"zero → default", 0, defaultCallListLimit},
		{"negative → default", -10, defaultCallListLimit},
		{"one stays one", 1, 1},
		{"typical stays", 50, 50},
		{"max stays max", maxCallListLimit, maxCallListLimit},
		{"over max clamps", maxCallListLimit + 1, maxCallListLimit},
		{"way over clamps", 10_000, maxCallListLimit},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := clampLimit(tt.in); got != tt.want {
				t.Errorf("clampLimit(%d) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestBuildCallFilter_Empty(t *testing.T) {
	f := buildCallFilter(CallListParams{})
	if len(f) != 0 {
		t.Errorf("empty params should yield empty filter, got %+v", f)
	}
}

func TestBuildCallFilter_VerdictOnly(t *testing.T) {
	v := "Bad"
	f := buildCallFilter(CallListParams{Verdict: &v})
	if len(f) != 1 {
		t.Fatalf("want 1 clause, got %d (%+v)", len(f), f)
	}
	if f[0].Key != "verdict" || f[0].Value != "Bad" {
		t.Errorf("unexpected verdict clause: %+v", f[0])
	}
}

func TestBuildCallFilter_EmptyVerdictSkipped(t *testing.T) {
	empty := ""
	f := buildCallFilter(CallListParams{Verdict: &empty})
	if len(f) != 0 {
		t.Errorf("empty verdict string should be skipped, got %+v", f)
	}
}

func TestBuildCallFilter_UpnOnly(t *testing.T) {
	upn := "alice@corp.com"
	f := buildCallFilter(CallListParams{Upn: &upn})
	if len(f) != 1 {
		t.Fatalf("want 1 clause, got %d", len(f))
	}
	if f[0].Key != "participants" || f[0].Value != upn {
		t.Errorf("unexpected upn clause: %+v", f[0])
	}
}

func TestBuildCallFilter_TimeRange(t *testing.T) {
	from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)
	f := buildCallFilter(CallListParams{From: &from, To: &to})
	if len(f) != 1 {
		t.Fatalf("want 1 clause, got %d", len(f))
	}
	if f[0].Key != "startTimeUtc" {
		t.Errorf("want startTimeUtc key, got %q", f[0].Key)
	}
	rng, ok := f[0].Value.(bson.D)
	if !ok {
		t.Fatalf("want bson.D value, got %T", f[0].Value)
	}
	if len(rng) != 2 {
		t.Fatalf("want 2 range clauses, got %d", len(rng))
	}
	if rng[0].Key != "$gte" || rng[0].Value.(time.Time) != from {
		t.Errorf("unexpected gte clause: %+v", rng[0])
	}
	if rng[1].Key != "$lt" || rng[1].Value.(time.Time) != to {
		t.Errorf("unexpected lt clause: %+v", rng[1])
	}
}

func TestBuildCallFilter_FromOnly(t *testing.T) {
	from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	f := buildCallFilter(CallListParams{From: &from})
	if len(f) != 1 {
		t.Fatalf("want 1 clause, got %d", len(f))
	}
	rng := f[0].Value.(bson.D)
	if len(rng) != 1 || rng[0].Key != "$gte" {
		t.Errorf("want only $gte, got %+v", rng)
	}
}

func TestBuildCallFilter_AllFields(t *testing.T) {
	from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)
	v := "Poor"
	upn := "bob@corp.com"
	f := buildCallFilter(CallListParams{From: &from, To: &to, Verdict: &v, Upn: &upn})
	if len(f) != 3 {
		t.Fatalf("want 3 clauses, got %d (%+v)", len(f), f)
	}
	keys := []string{f[0].Key, f[1].Key, f[2].Key}
	want := []string{"startTimeUtc", "verdict", "participants"}
	for i := range want {
		if keys[i] != want[i] {
			t.Errorf("key[%d] = %q, want %q", i, keys[i], want[i])
		}
	}
}

func TestDatabaseFromURI(t *testing.T) {
	tests := []struct {
		name string
		uri  string
		want string
	}{
		{"with db path", "mongodb://mongo:27017/teams_con", "teams_con"},
		{"with db and options", "mongodb://mongo:27017/teams_con?replicaSet=rs0", "teams_con"},
		{"no db path", "mongodb://mongo:27017", defaultDatabase},
		{"trailing slash", "mongodb://mongo:27017/", defaultDatabase},
		{"with auth", "mongodb://user:pass@mongo:27017/mydb", "mydb"},
		{"garbage", "://not-a-uri", defaultDatabase},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := databaseFromURI(tt.uri); got != tt.want {
				t.Errorf("databaseFromURI(%q) = %q, want %q", tt.uri, got, tt.want)
			}
		})
	}
}

// Guard: url.Parse recognises "mongodb://" scheme so databaseFromURI can
// rely on Path. If future Go versions change this, we want to know.
func TestURLParseMongoScheme(t *testing.T) {
	u, err := url.Parse("mongodb://mongo:27017/teams_con")
	if err != nil {
		t.Fatalf("url.Parse failed: %v", err)
	}
	if u.Path != "/teams_con" {
		t.Errorf("u.Path = %q, want %q", u.Path, "/teams_con")
	}
}

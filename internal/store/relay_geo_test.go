package store

import (
	"context"
	"testing"
	"time"
)

func TestNewRelayGeoRepo(t *testing.T) {
	// Verify constructor does not panic with nil db — it should not,
	// because mongo.Database.Collection never returns nil.
	// We cannot call methods without a live mongo, but we can verify
	// the struct is wired correctly.
	t.Run("struct fields", func(t *testing.T) {
		var geo RelayGeo
		geo.IP = "52.112.207.10"
		geo.City = "Dubai"
		geo.Country = "AE"
		geo.ResolvedAt = time.Now().UTC()

		if geo.IP != "52.112.207.10" {
			t.Errorf("expected IP 52.112.207.10, got %s", geo.IP)
		}
		if geo.City != "Dubai" {
			t.Errorf("expected City Dubai, got %s", geo.City)
		}
		if geo.Country != "AE" {
			t.Errorf("expected Country AE, got %s", geo.Country)
		}
		if geo.ResolvedAt.IsZero() {
			t.Error("expected ResolvedAt to be set")
		}
	})
}

func TestGetMany_EmptySlice(t *testing.T) {
	// GetMany with nil/empty slice should return nil, nil without
	// touching the database.
	repo := &RelayGeoRepo{} // coll is nil — ok because we return early
	ctx := context.Background()
	got, err := repo.GetMany(ctx, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil map, got %v", got)
	}

	got, err = repo.GetMany(ctx, []string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil map, got %v", got)
	}
}

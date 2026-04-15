package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"teams_con/internal/store"
)

func newSilentResolver(repo subnetsListReader) *SubnetResolver {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewSubnetResolver(repo, log)
}

func TestSubnetResolver_LongestPrefixWins(t *testing.T) {
	repo := newFakeSubnets(
		store.SubnetEntry{Cidr: "10.0.0.0/8", Name: "wide"},
		store.SubnetEntry{Cidr: "10.16.0.0/16", Name: "narrow"},
	)
	r := newSilentResolver(repo)

	got := r.Resolve(context.Background(), "10.16.1.42")
	if got == nil {
		t.Fatal("want match, got nil")
	}
	if got.Name != "narrow" {
		t.Errorf("name = %q, want narrow (longest prefix)", got.Name)
	}

	// IP that only the /8 covers must still match the wider block.
	got = r.Resolve(context.Background(), "10.99.1.1")
	if got == nil || got.Name != "wide" {
		t.Errorf("want wide match, got %+v", got)
	}
}

func TestSubnetResolver_AcceptsBareIPAndCidrInput(t *testing.T) {
	repo := newFakeSubnets(store.SubnetEntry{Cidr: "192.168.0.0/24", Name: "office"})
	r := newSilentResolver(repo)

	if got := r.Resolve(context.Background(), "192.168.0.42"); got == nil {
		t.Errorf("bare IP form should resolve")
	}
	if got := r.Resolve(context.Background(), "192.168.0.0/24"); got == nil {
		t.Errorf("CIDR form should resolve")
	}
	if got := r.Resolve(context.Background(), "10.0.0.1"); got != nil {
		t.Errorf("unmatched IP should yield nil, got %+v", got)
	}
	if got := r.Resolve(context.Background(), ""); got != nil {
		t.Errorf("empty input should yield nil")
	}
	if got := r.Resolve(context.Background(), "garbage"); got != nil {
		t.Errorf("garbage input should yield nil")
	}
}

func TestSubnetResolver_DropsUnparsableCidr(t *testing.T) {
	repo := newFakeSubnets(
		store.SubnetEntry{Cidr: "not-a-cidr", Name: "bad"},
		store.SubnetEntry{Cidr: "10.0.0.0/24", Name: "good"},
	)
	r := newSilentResolver(repo)

	got := r.Resolve(context.Background(), "10.0.0.5")
	if got == nil || got.Name != "good" {
		t.Errorf("good entry must still resolve despite bad sibling, got %+v", got)
	}
}

func TestSubnetResolver_InvalidateForcesReload(t *testing.T) {
	repo := newFakeSubnets()
	r := newSilentResolver(repo)

	if got := r.Resolve(context.Background(), "10.0.0.1"); got != nil {
		t.Fatalf("empty resolver must return nil, got %+v", got)
	}
	beforeReloadCount := repo.listCalls

	// Add a row directly to the fake; the resolver should NOT see it
	// until Invalidate is called (cached snapshot is empty + fresh).
	repo.entries["10.0.0.0/24"] = store.SubnetEntry{Cidr: "10.0.0.0/24", Name: "fresh"}
	if got := r.Resolve(context.Background(), "10.0.0.1"); got != nil {
		t.Errorf("cached snapshot should not yet see new row, got %+v", got)
	}
	if repo.listCalls != beforeReloadCount {
		t.Errorf("List should not be called again before Invalidate")
	}

	r.Invalidate()
	if got := r.Resolve(context.Background(), "10.0.0.1"); got == nil || got.Name != "fresh" {
		t.Errorf("after Invalidate, resolver must reload and match, got %+v", got)
	}
}

func TestSubnetResolver_TtlExpiryTriggersReload(t *testing.T) {
	repo := newFakeSubnets(store.SubnetEntry{Cidr: "10.0.0.0/24", Name: "v1"})
	r := newSilentResolver(repo)
	r.ttl = 5 * time.Millisecond

	if got := r.Resolve(context.Background(), "10.0.0.1"); got == nil || got.Name != "v1" {
		t.Fatalf("initial resolve failed: %+v", got)
	}
	repo.entries["10.0.0.0/24"] = store.SubnetEntry{Cidr: "10.0.0.0/24", Name: "v2"}
	time.Sleep(10 * time.Millisecond)
	if got := r.Resolve(context.Background(), "10.0.0.1"); got == nil || got.Name != "v2" {
		t.Errorf("after TTL expiry, resolver should reload, got %+v", got)
	}
}

func TestSubnetResolver_RepoErrorReturnsNil(t *testing.T) {
	repo := newFakeSubnets()
	repo.listErr = errors.New("mongo down")
	r := newSilentResolver(repo)

	if got := r.Resolve(context.Background(), "10.0.0.1"); got != nil {
		t.Errorf("repo error must surface as nil, got %+v", got)
	}
}

func TestSubnetResolver_NilSafe(t *testing.T) {
	var r *SubnetResolver
	if got := r.Resolve(context.Background(), "10.0.0.1"); got != nil {
		t.Errorf("nil resolver must yield nil")
	}
	r2 := NewSubnetResolver(nil, nil)
	if got := r2.Resolve(context.Background(), "10.0.0.1"); got != nil {
		t.Errorf("nil repo must yield nil")
	}
}

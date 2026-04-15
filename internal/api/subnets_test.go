package api

import (
	"context"
	"errors"
	"testing"

	"teams_con/internal/store"
)

func TestUpsertSubnet_BadCidr(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"bare ip rejected", "10.0.0.5"},
		{"garbage", "not-a-cidr"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newTestServiceFull(nil, nil, nil, nil, nil, nil, nil)
			_, err := svc.UpsertSubnet(context.Background(), UpsertSubnetParams{
				Cidr: tt.in, Name: "x",
			})
			if !errors.Is(err, ErrBadRequest) {
				t.Errorf("err = %v, want wrap of ErrBadRequest", err)
			}
		})
	}
}

func TestUpsertSubnet_EmptyName(t *testing.T) {
	svc := newTestServiceFull(nil, nil, nil, nil, nil, nil, nil)
	_, err := svc.UpsertSubnet(context.Background(), UpsertSubnetParams{
		Cidr: "10.0.0.0/24", Name: "",
	})
	if !errors.Is(err, ErrBadRequest) {
		t.Errorf("empty name should be ErrBadRequest, got %v", err)
	}
}

func TestUpsertSubnet_CanonicalisesAndStores(t *testing.T) {
	subs := newFakeSubnets()
	svc := newTestServiceFull(nil, nil, nil, nil, subs, nil, nil)

	// Input with non-zero host bits should canonicalise.
	got, err := svc.UpsertSubnet(context.Background(), UpsertSubnetParams{
		Cidr:   "10.0.0.5/24",
		Name:   "office",
		Office: "Dubai",
		Kind:   "wired",
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if got.Cidr != "10.0.0.0/24" {
		t.Errorf("Cidr = %q, want 10.0.0.0/24 (canonical)", got.Cidr)
	}
	if got.UpdatedAt.IsZero() {
		t.Errorf("UpdatedAt should be stamped")
	}
	if _, ok := subs.entries["10.0.0.0/24"]; !ok {
		t.Errorf("entry not persisted under canonical key, have %+v", subs.entries)
	}
}

func TestUpsertSubnet_InvalidatesResolver(t *testing.T) {
	subs := newFakeSubnets()
	svc := newTestServiceFull(nil, nil, nil, nil, subs, nil, nil)

	// Force the resolver to load an empty snapshot first.
	if got := svc.subnetResolver.Resolve(context.Background(), "10.0.0.1"); got != nil {
		t.Fatalf("empty resolver must miss")
	}
	beforeReload := subs.listCalls

	if _, err := svc.UpsertSubnet(context.Background(), UpsertSubnetParams{
		Cidr: "10.0.0.0/24", Name: "added",
	}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if got := svc.subnetResolver.Resolve(context.Background(), "10.0.0.1"); got == nil || got.Name != "added" {
		t.Errorf("resolver must see new entry after upsert, got %+v", got)
	}
	if subs.listCalls <= beforeReload {
		t.Errorf("Invalidate did not force a reload (List called %d times)", subs.listCalls-beforeReload)
	}
}

func TestDeleteSubnet_NotFound(t *testing.T) {
	svc := newTestServiceFull(nil, nil, nil, nil, nil, nil, nil)
	err := svc.DeleteSubnet(context.Background(), "10.0.0.0/24")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestDeleteSubnet_BadCidr(t *testing.T) {
	svc := newTestServiceFull(nil, nil, nil, nil, nil, nil, nil)
	err := svc.DeleteSubnet(context.Background(), "garbage")
	if !errors.Is(err, ErrBadRequest) {
		t.Errorf("err = %v, want ErrBadRequest", err)
	}
}

func TestDeleteSubnet_HappyPath(t *testing.T) {
	subs := newFakeSubnets(store.SubnetEntry{Cidr: "10.0.0.0/24", Name: "x"})
	svc := newTestServiceFull(nil, nil, nil, nil, subs, nil, nil)
	if err := svc.DeleteSubnet(context.Background(), "10.0.0.0/24"); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if _, ok := subs.entries["10.0.0.0/24"]; ok {
		t.Errorf("entry should have been removed")
	}
}

func TestListSubnets_Propagates(t *testing.T) {
	subs := newFakeSubnets(
		store.SubnetEntry{Cidr: "10.0.0.0/24", Name: "a"},
		store.SubnetEntry{Cidr: "10.1.0.0/24", Name: "b"},
	)
	svc := newTestServiceFull(nil, nil, nil, nil, subs, nil, nil)
	out, err := svc.ListSubnets(context.Background())
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(out) != 2 {
		t.Errorf("want 2 entries, got %d", len(out))
	}
}

func TestGetSubnet_Canonicalises(t *testing.T) {
	subs := newFakeSubnets(store.SubnetEntry{Cidr: "10.0.0.0/24", Name: "x"})
	svc := newTestServiceFull(nil, nil, nil, nil, subs, nil, nil)
	// Input with host bits should still find the canonical entry.
	e, err := svc.GetSubnet(context.Background(), "10.0.0.42/24")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if e.Name != "x" {
		t.Errorf("name = %q, want x", e.Name)
	}
}

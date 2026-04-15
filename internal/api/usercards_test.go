package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"teams_con/internal/store"
)

func TestUpsertUserCard_EmptyUpn(t *testing.T) {
	svc := newTestServiceFull(nil, nil, nil, nil, nil, nil, nil)
	_, err := svc.UpsertUserCard(context.Background(), UpsertUserCardParams{Upn: ""})
	if !errors.Is(err, ErrBadRequest) {
		t.Fatalf("err = %v, want ErrBadRequest", err)
	}
}

func TestUpsertUserCard_HappyPathSetsUpdatedAt(t *testing.T) {
	cards := newFakeUserCards()
	svc := newTestServiceFull(nil, nil, nil, nil, nil, cards, nil)

	before := time.Now().UTC().Add(-time.Second)
	got, err := svc.UpsertUserCard(context.Background(), UpsertUserCardParams{
		Upn:         "alice@corp.com",
		DisplayName: "Alice",
		Location:    "Dubai HQ",
		Tags:        []string{"vip"},
		Notes:       "escalated",
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if got.Upn != "alice@corp.com" || got.DisplayName != "Alice" {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
	if got.UpdatedAt.Before(before) {
		t.Errorf("UpdatedAt not stamped: %v", got.UpdatedAt)
	}
	if _, ok := cards.entries["alice@corp.com"]; !ok {
		t.Errorf("card did not reach the store")
	}
}

func TestUpsertUserCard_TagsNormalisation(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil → nil", nil, nil},
		{"empty → nil", []string{}, nil},
		{"all blank → nil", []string{"", "  ", "\t"}, nil},
		{"trim whitespace", []string{" vip ", "remote\t"}, []string{"vip", "remote"}},
		{"drop blanks, preserve case/order",
			[]string{"VIP", "", "mobile-heavy", "  "},
			[]string{"VIP", "mobile-heavy"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newTestServiceFull(nil, nil, nil, nil, nil, nil, nil)
			got, err := svc.UpsertUserCard(context.Background(), UpsertUserCardParams{
				Upn:  "alice@corp.com",
				Tags: tt.in,
			})
			if err != nil {
				t.Fatalf("unexpected: %v", err)
			}
			if len(got.Tags) != len(tt.want) {
				t.Fatalf("tags = %v, want %v", got.Tags, tt.want)
			}
			for i := range got.Tags {
				if got.Tags[i] != tt.want[i] {
					t.Errorf("tags[%d] = %q, want %q", i, got.Tags[i], tt.want[i])
				}
			}
		})
	}
}

func TestGetUserCard_MissingReturnsNilNil(t *testing.T) {
	svc := newTestServiceFull(nil, nil, nil, nil, nil, nil, nil)
	got, err := svc.GetUserCard(context.Background(), "ghost@corp.com")
	if err != nil {
		t.Fatalf("missing must not error: %v", err)
	}
	if got != nil {
		t.Errorf("missing must be nil, got %+v", got)
	}
}

func TestGetUserCard_EmptyUpnRejected(t *testing.T) {
	svc := newTestServiceFull(nil, nil, nil, nil, nil, nil, nil)
	_, err := svc.GetUserCard(context.Background(), "")
	if !errors.Is(err, ErrBadRequest) {
		t.Fatalf("err = %v, want ErrBadRequest", err)
	}
}

func TestGetUserCard_WrapsUnknownRepoError(t *testing.T) {
	boom := errors.New("mongo down")
	cards := newFakeUserCards()
	cards.getErr = boom
	svc := newTestServiceFull(nil, nil, nil, nil, nil, cards, nil)

	_, err := svc.GetUserCard(context.Background(), "alice@corp.com")
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrBadRequest) {
		t.Errorf("unexpected sentinel: %v", err)
	}
	if !errors.Is(err, boom) {
		t.Errorf("underlying error not wrapped: %v", err)
	}
}

func TestGetUserCard_Hit(t *testing.T) {
	seed := store.UserCard{Upn: "alice@corp.com", DisplayName: "Alice", Location: "Dubai HQ"}
	cards := newFakeUserCards(seed)
	svc := newTestServiceFull(nil, nil, nil, nil, nil, cards, nil)
	got, err := svc.GetUserCard(context.Background(), "alice@corp.com")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if got == nil || got.DisplayName != "Alice" {
		t.Errorf("got = %+v", got)
	}
}

func TestDeleteUserCard_MissingReturnsNotFound(t *testing.T) {
	svc := newTestServiceFull(nil, nil, nil, nil, nil, nil, nil)
	err := svc.DeleteUserCard(context.Background(), "ghost@corp.com")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestDeleteUserCard_EmptyUpnRejected(t *testing.T) {
	svc := newTestServiceFull(nil, nil, nil, nil, nil, nil, nil)
	err := svc.DeleteUserCard(context.Background(), "")
	if !errors.Is(err, ErrBadRequest) {
		t.Fatalf("err = %v, want ErrBadRequest", err)
	}
}

func TestDeleteUserCard_Happy(t *testing.T) {
	cards := newFakeUserCards(store.UserCard{Upn: "alice@corp.com"})
	svc := newTestServiceFull(nil, nil, nil, nil, nil, cards, nil)
	if err := svc.DeleteUserCard(context.Background(), "alice@corp.com"); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if _, ok := cards.entries["alice@corp.com"]; ok {
		t.Errorf("entry not removed")
	}
}

func TestListUserCards_Happy(t *testing.T) {
	cards := newFakeUserCards(
		store.UserCard{Upn: "alice@corp.com"},
		store.UserCard{Upn: "bob@corp.com"},
	)
	svc := newTestServiceFull(nil, nil, nil, nil, nil, cards, nil)
	out, err := svc.ListUserCards(context.Background())
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(out) != 2 {
		t.Errorf("len = %d, want 2", len(out))
	}
}

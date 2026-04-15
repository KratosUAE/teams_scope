package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"teams_con/internal/store"
)

// UpsertUserCardParams is the HTTP-free input to UpsertUserCard. Upn is
// required; every other field is optional and any value supplied becomes
// the new stored value (no merge at this layer — merge semantics live in
// the CLI `usercard set` subcommand which reads the existing card first).
type UpsertUserCardParams struct {
	Upn         string
	DisplayName string
	Location    string
	Tags        []string
	Notes       string
}

// ListUserCards returns every configured user card sorted by UPN. The
// collection is small (one row per annotated user) so paging is
// unnecessary.
func (s *Service) ListUserCards(ctx context.Context) ([]store.UserCard, error) {
	out, err := s.userCards.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("api: list usercards: %w", err)
	}
	return out, nil
}

// GetUserCard fetches the user card for upn.
//
// IMPORTANT: missing cards are returned as (nil, nil) — NOT ErrNotFound.
// Rationale: most callers (BuildUserHealthReport, the MCP tool, the TUI
// portrait) want to optionally decorate a user with an annotation and
// forcing them to handle an ErrNotFound branch is noisy. Callers that
// actually need to distinguish "no card" from an error (e.g. the HTTP
// GET handler, which maps absent → 404 by REST convention) check
// result == nil explicitly.
//
// An empty upn is still rejected with ErrBadRequest — that is a client
// error, not a "no card" state.
func (s *Service) GetUserCard(ctx context.Context, upn string) (*store.UserCard, error) {
	if upn == "" {
		return nil, fmt.Errorf("%w: empty upn", ErrBadRequest)
	}
	c, err := s.userCards.Get(ctx, upn)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("api: get usercard: %w", err)
	}
	return c, nil
}

// UpsertUserCard validates the UPN, normalises the tag slice, stamps
// UpdatedAt, and writes the card. An empty UPN is rejected with
// ErrBadRequest. Tags are trimmed of surrounding whitespace and empty
// tags are dropped (design-usercards.md leaves the exact normalisation
// open — we pick the minimum-surprise behaviour of "trim + drop empty"
// and preserve case/order so operator-chosen tag vocabularies like
// "VIP" vs "vip" remain the operator's choice).
func (s *Service) UpsertUserCard(ctx context.Context, p UpsertUserCardParams) (*store.UserCard, error) {
	if p.Upn == "" {
		return nil, fmt.Errorf("%w: empty upn", ErrBadRequest)
	}

	card := store.UserCard{
		Upn:         p.Upn,
		DisplayName: p.DisplayName,
		Location:    p.Location,
		Tags:        normaliseTags(p.Tags),
		Notes:       p.Notes,
		UpdatedAt:   time.Now().UTC(),
	}
	if err := s.userCards.Upsert(ctx, card); err != nil {
		return nil, fmt.Errorf("api: upsert usercard: %w", err)
	}
	return &card, nil
}

// DeleteUserCard removes the card keyed by upn. Missing entries surface
// as ErrNotFound — unlike GetUserCard, an explicit delete of a
// non-existent thing is a client error. Empty upn is rejected with
// ErrBadRequest.
func (s *Service) DeleteUserCard(ctx context.Context, upn string) error {
	if upn == "" {
		return fmt.Errorf("%w: empty upn", ErrBadRequest)
	}
	if err := s.userCards.Delete(ctx, upn); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		}
		return fmt.Errorf("api: delete usercard: %w", err)
	}
	return nil
}

// normaliseTags trims whitespace and drops empty strings. It returns nil
// when the resulting slice is empty so omitempty drops the field from
// the wire format (design-usercards.md gotcha: nil and empty slice must
// both vanish). Case and order are preserved so the operator keeps full
// control over the tag vocabulary.
func normaliseTags(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, t := range in {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

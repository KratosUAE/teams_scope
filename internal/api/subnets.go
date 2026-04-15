package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"teams_con/internal/store"
)

// UpsertSubnetParams is the HTTP-free input to UpsertSubnet. Cidr and Name
// are required; Office, Kind, and Notes are optional metadata used by the
// resolver to enrich SubnetUsage entries on user_health_report.
type UpsertSubnetParams struct {
	Cidr   string
	Name   string
	Office string
	Kind   string
	Notes  string
}

// ListSubnets returns every configured subnet entry sorted by Name. No
// filters; the collection is small (< 100 rows for a single org) so paging
// is unnecessary.
func (s *Service) ListSubnets(ctx context.Context) ([]store.SubnetEntry, error) {
	out, err := s.subnets.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("api: list subnets: %w", err)
	}
	return out, nil
}

// GetSubnet fetches one entry by its canonical CIDR. The CIDR is
// canonicalised before lookup so callers can pass any equivalent form
// ("10.0.0.5/24" → "10.0.0.0/24"). Missing entries surface as ErrNotFound.
func (s *Service) GetSubnet(ctx context.Context, cidr string) (*store.SubnetEntry, error) {
	canon, err := canonicaliseCidr(cidr)
	if err != nil {
		return nil, err
	}
	e, err := s.subnets.Get(ctx, canon)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("api: get subnet: %w", err)
	}
	return e, nil
}

// UpsertSubnet validates and canonicalises the CIDR, stamps UpdatedAt, and
// writes the entry. On success the SubnetResolver cache is invalidated so
// the next user_health_report sees the new label.
//
// Validation errors (empty/unparsable CIDR, empty Name) are wrapped in
// ErrBadRequest. The returned entry reflects the canonical CIDR Mongo
// actually stores — host bits are zeroed by net.IPNet.String, so callers
// see the same value that future Get/Delete calls will accept.
func (s *Service) UpsertSubnet(ctx context.Context, p UpsertSubnetParams) (*store.SubnetEntry, error) {
	canon, err := canonicaliseCidr(p.Cidr)
	if err != nil {
		return nil, err
	}
	if p.Name == "" {
		return nil, fmt.Errorf("%w: subnet name is required", ErrBadRequest)
	}

	entry := store.SubnetEntry{
		Cidr:      canon,
		Name:      p.Name,
		Office:    p.Office,
		Kind:      p.Kind,
		Notes:     p.Notes,
		UpdatedAt: time.Now().UTC(),
	}
	if err := s.subnets.Upsert(ctx, entry); err != nil {
		return nil, fmt.Errorf("api: upsert subnet: %w", err)
	}
	if s.subnetResolver != nil {
		s.subnetResolver.Invalidate()
	}
	return &entry, nil
}

// DeleteSubnet removes the entry keyed by cidr. The CIDR is canonicalised
// before delete so the same input forms accepted by UpsertSubnet also work
// here. Missing entries surface as ErrNotFound; on success the resolver
// cache is invalidated.
func (s *Service) DeleteSubnet(ctx context.Context, cidr string) error {
	canon, err := canonicaliseCidr(cidr)
	if err != nil {
		return err
	}
	if err := s.subnets.Delete(ctx, canon); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		}
		return fmt.Errorf("api: delete subnet: %w", err)
	}
	if s.subnetResolver != nil {
		s.subnetResolver.Invalidate()
	}
	return nil
}

// canonicaliseCidr parses raw with net.ParseCIDR and returns the canonical
// "network/mask" form (host bits zeroed). Empty or unparsable input is
// reported as ErrBadRequest. Bare IPs without a mask are rejected — the
// write surface requires explicit prefix length so the caller cannot
// accidentally store a /32.
func canonicaliseCidr(raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("%w: empty cidr", ErrBadRequest)
	}
	_, ipnet, err := net.ParseCIDR(raw)
	if err != nil || ipnet == nil {
		return "", fmt.Errorf("%w: invalid cidr %q", ErrBadRequest, raw)
	}
	return ipnet.String(), nil
}

// decorateSubnets walks subs in place and populates Name/Office/Kind from
// the resolver. Unmatched rows are left untouched so the rendering layer
// can fall back to the raw CIDR. Called from BuildUserHealthReport after
// aggregateUserHealth has built the slice.
func (s *Service) decorateSubnets(ctx context.Context, subs []SubnetUsage) {
	if s.subnetResolver == nil {
		return
	}
	for i := range subs {
		e := s.subnetResolver.Resolve(ctx, subs[i].Subnet)
		if e == nil {
			continue
		}
		subs[i].Name = e.Name
		subs[i].Office = e.Office
		subs[i].Kind = e.Kind
	}
}

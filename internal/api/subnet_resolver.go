package api

import (
	"context"
	"log/slog"
	"net"
	"sort"
	"sync"
	"time"

	"teams_con/internal/store"
)

// defaultSubnetTTL bounds how long a loaded snapshot of the subnets table
// stays cached before the next Resolve forces a reload. Invalidate() is the
// primary refresh path (called from UpsertSubnet/DeleteSubnet); the TTL is
// only a safety net in case a write reaches Mongo through some other route.
const defaultSubnetTTL = 5 * time.Minute

// SubnetResolver does longest-prefix CIDR lookups against the in-memory
// snapshot of the subnets collection. It is safe for concurrent use: most
// reads take an RLock, and only the periodic reload promotes to a write
// lock.
//
// The resolver lives on *Service and is shared by every request. Callers
// must Invalidate() it after writes so the next read sees fresh data.
type SubnetResolver struct {
	repo subnetsListReader
	log  *slog.Logger
	ttl  time.Duration

	mu     sync.RWMutex
	loaded time.Time
	blocks []resolvedBlock
}

// resolvedBlock is one parsed row of the subnet table — the *net.IPNet for
// fast Contains checks plus the full entry so callers can read Name/Office/
// Kind without a second lookup.
type resolvedBlock struct {
	ipnet *net.IPNet
	entry store.SubnetEntry
}

// NewSubnetResolver constructs a resolver against repo. A nil log falls back
// to slog.Default so production wiring stays a one-liner. repo need only
// satisfy subnetsListReader (the narrow interface) — production passes
// *store.SubnetsRepo which also satisfies subnetsStore (H3 fix).
func NewSubnetResolver(repo subnetsListReader, log *slog.Logger) *SubnetResolver {
	if log == nil {
		log = slog.Default()
	}
	return &SubnetResolver{
		repo: repo,
		log:  log,
		ttl:  defaultSubnetTTL,
	}
}

// Invalidate clears the cache so the next Resolve forces a reload. Called
// from UpsertSubnet and DeleteSubnet so writes propagate immediately.
func (r *SubnetResolver) Invalidate() {
	r.mu.Lock()
	r.loaded = time.Time{}
	r.mu.Unlock()
}

// Resolve looks up raw — either a bare IP ("10.16.1.42") or a CIDR
// ("10.16.0.0/16") — and returns the entry whose block contains the IP
// with the longest prefix. A nil return is a normal "no match", not an
// error. Repo failures are logged at warn level and also yield nil so the
// enrichment path stays soft-fail.
func (r *SubnetResolver) Resolve(ctx context.Context, raw string) *store.SubnetEntry {
	if raw == "" || r == nil || r.repo == nil {
		return nil
	}

	ip := parseSubnetInput(raw)
	if ip == nil {
		return nil
	}

	if err := r.ensureLoaded(ctx); err != nil {
		r.log.Warn("api: subnet resolver reload failed",
			slog.String("err", err.Error()),
		)
		return nil
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	for i := range r.blocks {
		if r.blocks[i].ipnet.Contains(ip) {
			e := r.blocks[i].entry
			return &e
		}
	}
	return nil
}

// ensureLoaded reloads the in-memory snapshot if the cache is empty or has
// aged past the TTL. The fast path is an RLock check. When a reload is
// needed the lock is fully released before the Mongo round-trip (H1 fix:
// holding the write lock during I/O serialised all concurrent Resolve
// callers). After List returns, the write lock is re-acquired and freshness
// is double-checked so two goroutines that raced past the initial RLock
// check do not both apply the same reload.
func (r *SubnetResolver) ensureLoaded(ctx context.Context) error {
	r.mu.RLock()
	fresh := !r.loaded.IsZero() && time.Since(r.loaded) <= r.ttl
	r.mu.RUnlock()
	if fresh {
		return nil
	}

	// Perform the potentially-slow List OUTSIDE any lock so concurrent
	// Resolve calls are not serialised behind the Mongo round-trip.
	entries, err := r.repo.List(ctx)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	// Double-check after acquiring the write lock: another goroutine may
	// have completed a reload while we were waiting for List to return.
	if !r.loaded.IsZero() && time.Since(r.loaded) <= r.ttl {
		return nil
	}

	r.blocks = buildBlocks(entries, r.log)
	r.loaded = time.Now()
	return nil
}

// buildBlocks parses a slice of SubnetEntries into resolvedBlocks sorted by
// mask length descending so the first Contains hit in Resolve is always the
// longest-prefix match. Unparsable CIDRs are logged at Debug and dropped.
func buildBlocks(entries []store.SubnetEntry, log *slog.Logger) []resolvedBlock {
	blocks := make([]resolvedBlock, 0, len(entries))
	for _, e := range entries {
		_, ipnet, perr := net.ParseCIDR(e.Cidr)
		if perr != nil || ipnet == nil {
			log.Debug("api: subnet resolver dropping unparsable cidr",
				slog.String("cidr", e.Cidr),
			)
			continue
		}
		blocks = append(blocks, resolvedBlock{ipnet: ipnet, entry: e})
	}
	sort.SliceStable(blocks, func(i, j int) bool {
		oi, _ := blocks[i].ipnet.Mask.Size()
		oj, _ := blocks[j].ipnet.Mask.Size()
		return oi > oj
	})
	return blocks
}

// parseSubnetInput accepts both IP-only ("10.16.1.42") and CIDR-form
// ("10.16.0.0/24") strings. Teams stream rows store both shapes across
// platform variants; we want Resolve to handle either.
func parseSubnetInput(raw string) net.IP {
	if ip := net.ParseIP(raw); ip != nil {
		return ip
	}
	if ip, _, err := net.ParseCIDR(raw); err == nil {
		return ip
	}
	return nil
}

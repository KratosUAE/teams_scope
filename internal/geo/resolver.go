package geo

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"teams_con/internal/store"
)

// httpTimeout is the maximum time allowed for a single ipinfo.io lookup.
const httpTimeout = 3 * time.Second

// Resolver resolves relay IPs to city/country, caching results in Mongo.
type Resolver struct {
	repo  *store.RelayGeoRepo
	httpc *http.Client
	log   *slog.Logger
}

// New builds a Resolver. The HTTP client has a 3-second timeout to avoid
// blocking call detail responses on slow ipinfo lookups.
func New(repo *store.RelayGeoRepo, log *slog.Logger) *Resolver {
	if log == nil {
		log = slog.Default()
	}
	return &Resolver{
		repo:  repo,
		httpc: &http.Client{Timeout: httpTimeout},
		log:   log,
	}
}

// ipInfoResponse is the subset of ipinfo.io JSON we care about.
type ipInfoResponse struct {
	City    string `json:"city"`
	Country string `json:"country"`
}

// Resolve returns "City, CC" for a single IP. On any error returns "".
func (r *Resolver) Resolve(ctx context.Context, ip string) string {
	if ip == "" {
		return ""
	}
	// Check cache.
	cached, err := r.repo.Get(ctx, ip)
	if err != nil {
		r.log.Warn("geo: cache get failed", "ip", ip, "err", err)
	}
	if cached != nil {
		return formatGeo(cached.City, cached.Country)
	}
	// Fetch from ipinfo.io.
	city, country, err := r.fetchIPInfo(ctx, ip)
	if err != nil {
		r.log.Warn("geo: ipinfo fetch failed", "ip", ip, "err", err)
		return ""
	}
	// Cache result.
	if err := r.repo.Upsert(ctx, store.RelayGeo{IP: ip, City: city, Country: country}); err != nil {
		r.log.Warn("geo: cache upsert failed", "ip", ip, "err", err)
	}
	return formatGeo(city, country)
}

// ResolveMany resolves multiple IPs, returning a map[ip]"City, CC".
// Batch-reads the cache first, then resolves misses individually.
func (r *Resolver) ResolveMany(ctx context.Context, ips []string) map[string]string {
	if len(ips) == 0 {
		return nil
	}
	// Dedupe.
	unique := make(map[string]struct{}, len(ips))
	for _, ip := range ips {
		if ip != "" {
			unique[ip] = struct{}{}
		}
	}
	keys := make([]string, 0, len(unique))
	for ip := range unique {
		keys = append(keys, ip)
	}

	// Batch cache lookup.
	cached, err := r.repo.GetMany(ctx, keys)
	if err != nil {
		r.log.Warn("geo: batch cache get failed", "err", err)
		cached = make(map[string]store.RelayGeo)
	}

	out := make(map[string]string, len(keys))
	for _, ip := range keys {
		if g, ok := cached[ip]; ok {
			out[ip] = formatGeo(g.City, g.Country)
			continue
		}
		// Cache miss — resolve individually.
		out[ip] = r.Resolve(ctx, ip)
	}
	return out
}

func (r *Resolver) fetchIPInfo(ctx context.Context, ip string) (city, country string, err error) {
	url := fmt.Sprintf("https://ipinfo.io/%s/json", ip)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", err
	}
	resp, err := r.httpc.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("ipinfo: status %d", resp.StatusCode)
	}
	var info ipInfoResponse
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return "", "", err
	}
	return info.City, info.Country, nil
}

func formatGeo(city, country string) string {
	if city == "" && country == "" {
		return ""
	}
	if city == "" {
		return country
	}
	if country == "" {
		return city
	}
	return city + ", " + country
}

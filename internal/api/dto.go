package api

import (
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// errorResponse is the single JSON shape returned for any non-success
// status. A flat {"error": "..."} object keeps the contract stable across
// the codebase (→ handlers.md flat single-key rule).
type errorResponse struct {
	Error string `json:"error"`
}

// parseTimeQ reads an RFC3339 timestamp from the query string. An absent
// or empty key yields (nil, nil) — "no filter". An unparsable value is
// wrapped in ErrBadRequest so the handler can map it to 400.
func parseTimeQ(r *http.Request, key string) (*time.Time, error) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid %s: %v", ErrBadRequest, key, err)
	}
	return &t, nil
}

// parseIntQ reads an integer from the query string, returning def when the
// key is absent. An unparsable value is wrapped in ErrBadRequest.
func parseIntQ(r *http.Request, key string, def int) (int, error) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%w: invalid %s: %v", ErrBadRequest, key, err)
	}
	return n, nil
}

// parseFloatQ reads a float from the query string, returning def when the
// key is absent. An unparsable value is wrapped in ErrBadRequest.
func parseFloatQ(r *http.Request, key string, def float64) (float64, error) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: invalid %s: %v", ErrBadRequest, key, err)
	}
	return n, nil
}

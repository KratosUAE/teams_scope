package mcp_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"teams_con/internal/store"
)

func TestDailySummary_Happy(t *testing.T) {
	ds := &fakeDailySummary{rows: []store.DaySummary{
		{Date: "2026-04-10", Calls: 5, Good: 3, Poor: 1, Bad: 1, Over90Pct: 2, PeakLossMaxPct: 45.3},
		{Date: "2026-04-11", Calls: 3, Good: 2, Poor: 0, Bad: 1, Over90Pct: 1, PeakLossMaxPct: 92.1},
	}}
	srv := newTestServerFull(nil, nil, nil, nil, nil, nil, ds, nil)

	blocks, isErr := callTool(t, srv, "daily_quality_summary", map[string]any{})
	if isErr {
		t.Fatalf("unexpected error: %v", blocks)
	}
	if len(blocks) < 2 {
		t.Fatalf("want summary + json, got %d blocks", len(blocks))
	}
	// Summary should mention totals.
	if !strings.Contains(blocks[0], "2 days") {
		t.Errorf("summary missing '2 days': %q", blocks[0])
	}
	if !strings.Contains(blocks[0], "8 calls") {
		t.Errorf("summary missing '8 calls': %q", blocks[0])
	}
	if !strings.Contains(blocks[0], "2 bad") {
		t.Errorf("summary missing '2 bad': %q", blocks[0])
	}
	if !strings.Contains(blocks[0], "3 streams >90%") {
		t.Errorf("summary missing streams >90%%: %q", blocks[0])
	}
	if !strings.Contains(blocks[0], "peak 92.1%") {
		t.Errorf("summary missing peak: %q", blocks[0])
	}
	// JSON block should unmarshal to two rows.
	var rows []store.DaySummary
	if err := json.Unmarshal([]byte(blocks[1]), &rows); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("want 2 rows, got %d", len(rows))
	}
}

func TestDailySummary_Empty(t *testing.T) {
	srv := newTestServerFull(nil, nil, nil, nil, nil, nil, &fakeDailySummary{}, nil)

	blocks, isErr := callTool(t, srv, "daily_quality_summary", map[string]any{})
	if isErr {
		t.Fatalf("unexpected error: %v", blocks)
	}
	if !strings.Contains(blocks[0], "0 days") {
		t.Errorf("summary = %q", blocks[0])
	}
}

func TestDailySummary_BadFromParam(t *testing.T) {
	srv := newTestServerFull(nil, nil, nil, nil, nil, nil, nil, nil)

	blocks, isErr := callTool(t, srv, "daily_quality_summary", map[string]any{
		"from": "not-a-date",
	})
	if !isErr {
		t.Fatalf("want error for bad from, got %v", blocks)
	}
	if len(blocks) == 0 || !strings.Contains(blocks[0], "bad \"from\"") {
		t.Errorf("error message = %q", blocks)
	}
}

func TestDailySummary_BadToParam(t *testing.T) {
	srv := newTestServerFull(nil, nil, nil, nil, nil, nil, nil, nil)

	blocks, isErr := callTool(t, srv, "daily_quality_summary", map[string]any{
		"to": "not-a-date",
	})
	if !isErr {
		t.Fatalf("want error for bad to, got %v", blocks)
	}
	if len(blocks) == 0 || !strings.Contains(blocks[0], "bad \"to\"") {
		t.Errorf("error message = %q", blocks)
	}
}

func TestDailySummary_WithExplicitWindow(t *testing.T) {
	ds := &fakeDailySummary{rows: []store.DaySummary{
		{Date: "2026-04-01", Calls: 10, Good: 8, Poor: 1, Bad: 1},
	}}
	srv := newTestServerFull(nil, nil, nil, nil, nil, nil, ds, nil)

	blocks, isErr := callTool(t, srv, "daily_quality_summary", map[string]any{
		"from": "2026-04-01T00:00:00Z",
		"to":   "2026-04-02T00:00:00Z",
	})
	if isErr {
		t.Fatalf("unexpected error: %v", blocks)
	}
	if !strings.Contains(blocks[0], "1 days") {
		t.Errorf("summary = %q", blocks[0])
	}
}

func TestDailySummary_ServiceError(t *testing.T) {
	ds := &fakeDailySummary{err: errors.New("mongo boom")}
	srv := newTestServerFull(nil, nil, nil, nil, nil, nil, ds, nil)

	blocks, isErr := callTool(t, srv, "daily_quality_summary", map[string]any{})
	if !isErr {
		t.Fatalf("want tool error, got %v", blocks)
	}
	if len(blocks) == 0 || !strings.Contains(blocks[0], "internal error") {
		t.Errorf("error = %q", blocks)
	}
}

package mcp_test

import (
	"context"
	"encoding/json"
	"testing"
)

// TestServer_ListTools drives the in-process MCPServer with initialize +
// tools/list JSON-RPC messages and asserts that exactly the six expected
// tools are registered with non-empty descriptions. This is the end-to-
// end smoke test that catches registration regressions without needing
// stdio plumbing.
func TestServer_ListTools(t *testing.T) {
	srv := newTestServer(nil, nil, nil, nil, nil)
	m := srv.MCP()

	// 1) initialize handshake — required before tools/list.
	initReq := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}`)
	if resp := m.HandleMessage(context.Background(), initReq); resp == nil {
		t.Fatal("initialize returned nil")
	}

	// 2) tools/list
	listReq := []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	resp := m.HandleMessage(context.Background(), listReq)
	if resp == nil {
		t.Fatal("tools/list returned nil")
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var env struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"tools"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal: %v (raw=%s)", err, string(raw))
	}
	if env.Error != nil {
		t.Fatalf("protocol error: %s", env.Error.Message)
	}

	want := map[string]bool{
		"health":                 false,
		"list_calls":             false,
		"get_call":               false,
		"list_users":             false,
		"list_user_calls":        false,
		"summarize_call":         false,
		"find_cascades":          false,
		"find_flaky_microphones": false,
		"user_health_report":     false,
		"list_subnets":             false,
		"get_user_card":            false,
		"find_bad_network_hotspots": false,
	}
	for _, tool := range env.Result.Tools {
		if _, ok := want[tool.Name]; !ok {
			t.Errorf("unexpected tool: %q", tool.Name)
			continue
		}
		if tool.Description == "" {
			t.Errorf("tool %q has empty description", tool.Name)
		}
		want[tool.Name] = true
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("missing tool: %q", name)
		}
	}
	if len(env.Result.Tools) != len(want) {
		t.Errorf("tool count = %d, want %d", len(env.Result.Tools), len(want))
	}
}

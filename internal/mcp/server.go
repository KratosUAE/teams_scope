// Package mcp exposes the read-only *api.Service surface as an MCP tool
// provider over stdio, so LLM clients (Claude Code, Claude Desktop) can
// query the Teams call-quality history conversationally. This layer is
// purely additive: it never writes to storage and never modifies anything
// under internal/api, internal/store, internal/quality, internal/graph,
// internal/crawler, or internal/tui.
package mcp

import (
	"context"
	"log/slog"

	"github.com/mark3labs/mcp-go/server"

	"teams_con/internal/api"
	"teams_con/internal/geo"
	"teams_con/internal/version"
)

// Server wraps a *server.MCPServer from mcp-go with our Service backend
// plus a stderr-only logger. Exported so cmd/mcp.go can construct and
// drive the lifecycle.
type Server struct {
	svc *api.Service
	geo *geo.Resolver
	log *slog.Logger
	m   *server.MCPServer
}

// NewServer builds an MCP server with all tools registered and ready to
// serve. The logger must be stderr-only — stdout is reserved for the
// JSON-RPC protocol and any leakage breaks the transport.
func NewServer(svc *api.Service, geoResolver *geo.Resolver, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	m := server.NewMCPServer("teams_con", version.Get().Version,
		server.WithToolCapabilities(false),
		server.WithLogging(),
	)
	s := &Server{svc: svc, geo: geoResolver, log: log, m: m}
	s.registerTools()
	return s
}

// Run blocks on stdio until ctx is cancelled or the stdio transport
// returns (client closed its end). mcp-go v0.47 has no MCPServer.Shutdown
// method; cancellation causes the process to exit and the OS tears down
// stdin/stdout, which terminates ServeStdio.
func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	// Intentional goroutine leak: ServeStdio blocks on an os.Stdin read loop
	// and mcp-go v0.47 has no shutdown hook. When ctx is cancelled (Ctrl+C or
	// signal), we return below and the process exits — the OS tears down
	// stdin/stdout, which unblocks ServeStdio and the goroutine exits with it.
	// This is the accepted lifecycle for a stdio-based subprocess MCP server.
	go func() {
		errCh <- server.ServeStdio(s.m)
	}()
	select {
	case <-ctx.Done():
		// The goroutine will unwind when stdin closes on process exit.
		// We return nil so cobra treats Ctrl+C as a clean stop.
		return nil
	case err := <-errCh:
		return err
	}
}

// MCP exposes the underlying *server.MCPServer for in-process tests that
// need to drive HandleMessage directly. It is NOT part of the public
// production contract — callers outside tests should ignore it.
func (s *Server) MCP() *server.MCPServer { return s.m }

package mcp

import (
	"encoding/json"
	"errors"

	mcpsdk "github.com/mark3labs/mcp-go/mcp"

	"teams_con/internal/api"
)

// mapServiceErr converts a Service error into an MCP tool-level error
// result. The mcp-go convention is that tool errors come back as
// (*CallToolResult with IsError=true, nil); only protocol-level faults
// use the second return. Generic errors are logged via s.log (stderr only)
// and masked to "internal error" so we never leak stack details to the LLM.
// Using s.log instead of slog.Default() prevents internal-error messages
// from leaking to stdout if anything in the process replaces the default logger.
func (s *Server) mapServiceErr(err error) *mcpsdk.CallToolResult {
	switch {
	case errors.Is(err, api.ErrBadRequest):
		return mcpsdk.NewToolResultError("bad request: " + err.Error())
	case errors.Is(err, api.ErrNotFound):
		return mcpsdk.NewToolResultError("not found: " + err.Error())
	default:
		s.log.Error("mcp: service error", "err", err)
		return mcpsdk.NewToolResultError("internal error")
	}
}

// textAndJSON packs a two-block result: a short natural-language summary
// first (cheap for the LLM to read without parsing) and a full JSON
// payload second (for structural follow-up questions). Marshal failures
// collapse to a tool error.
func textAndJSON(summary string, v any) *mcpsdk.CallToolResult {
	b, err := json.Marshal(v)
	if err != nil {
		return mcpsdk.NewToolResultError("marshal: " + err.Error())
	}
	r := mcpsdk.NewToolResultText(summary)
	r.Content = append(r.Content, mcpsdk.NewTextContent(string(b)))
	return r
}

// textOnly is a thin alias for NewToolResultText that keeps handler call
// sites symmetric with textAndJSON.
func textOnly(summary string) *mcpsdk.CallToolResult {
	return mcpsdk.NewToolResultText(summary)
}

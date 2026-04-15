// Package quality is a 1:1 Go port of
// PowerShell/scripts/Modules/CallQualityUtils.ps1. The PowerShell file is the
// canonical reference: every threshold, verdict escalation rule, UPN fallback
// order, reason string format, and worst-of-call algorithm in this package is
// intended to match that script line-for-line (with rounding parity).
//
// The package is intentionally pure:
//   - No I/O, no logging, no package-level mutable state.
//   - No dependencies outside the Go standard library and
//     teams_con/internal/graph (for the input types and the ISO 8601 duration
//     parser).
//   - All functions are safe for concurrent use.
//
// Consumers pass decoded graph.CallRecord values in and receive flat CallRow /
// StreamRow structs back, suitable for persistence or rendering. The mapping
// from CallQualityUtils.ps1 function names to Go symbols is:
//
//	Test-StreamQuality        -> EvaluateStream
//	Get-CallVerdict           -> computeWorstOfCall (used by ToCallRow)
//	ConvertTo-CallQualityRow  -> ToCallRow
//	Get-CallStreamRows        -> ToStreamRows
//	Get-CallParticipantUpns   -> ExtractParticipants
package quality

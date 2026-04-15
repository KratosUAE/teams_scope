// Package graph provides a thin Microsoft Graph client for the
// communications/callRecords surface used by teams_con. It is a leaf package:
// it depends only on the standard library and the MSAL Go library.
package graph

import "errors"

// ErrCallNotFound is returned by GetCallRecord when Microsoft Graph responds
// with HTTP 404 — typically meaning the call record is older than the 30-day
// retention window or has been deleted. Callers should use errors.Is to detect
// this and skip the record without failing the whole crawl.
var ErrCallNotFound = errors.New("graph: call record not found")

// ErrAuth is returned when MSAL token acquisition fails. Wrapped with %w so
// callers can errors.Is on it to distinguish auth issues from transport errors.
var ErrAuth = errors.New("graph: auth failure")

// ErrRateLimited is returned when the Graph API responds with 429 and the
// single in-process retry has already been exhausted.
var ErrRateLimited = errors.New("graph: rate limited")

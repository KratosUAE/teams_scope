package quality

import "teams_con/internal/graph"

// ExtractParticipants is the Go port of Get-CallParticipantUpns
// (CallQualityUtils.ps1 lines 443-461).
//
// Only UPNs are collected — participants without a UPN are silently skipped,
// matching the PS reference which never falls back to displayName here.
// The three UPN tiers are:
//
//  1. CallRecord.participants_v2[].identity.user.userPrincipalName
//  2. Per-session fallback: session.caller.associatedIdentity.user.UPN
//  3. Per-session fallback: session.callee.associatedIdentity.user.UPN
//
// The result is de-duplicated preserving first-occurrence order. Dedup is
// case-sensitive — PowerShell's `Select-Object -Unique` is case-sensitive —
// so "Alice@corp.com" and "alice@corp.com" would be kept as two entries.
// Empty strings are filtered out.
func ExtractParticipants(rec *graph.CallRecord) []string {
	if rec == nil {
		return nil
	}
	seen := make(map[string]struct{}, 8)
	out := make([]string, 0, 8)

	add := func(s string) {
		if s == "" {
			return
		}
		if _, dup := seen[s]; dup {
			return
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}

	// Tier 1: participants_v2.
	for i := range rec.ParticipantsV2 {
		add(upnOnly(rec.ParticipantsV2[i].Identity))
	}

	// Tiers 2-3: per-session caller / callee fallback.
	for si := range rec.Sessions {
		s := &rec.Sessions[si]
		if s.Caller != nil {
			add(upnOnly(s.Caller.AssociatedIdentity))
		}
		if s.Callee != nil {
			add(upnOnly(s.Callee.AssociatedIdentity))
		}
	}

	return out
}

// ExtractOrganizer returns the call organizer's UPN (with displayName
// fallback). Mirrors PS `ConvertTo-CallQualityRow` lines 488-494.
func ExtractOrganizer(rec *graph.CallRecord) string {
	if rec == nil || rec.OrganizerV2 == nil {
		return ""
	}
	return upnOrDisplay(rec.OrganizerV2.Identity)
}

// upnOnly returns the UPN of an identity with no displayName fallback.
// Returns "" when the identity or user is nil, or when UserPrincipalName is
// empty. Used by ExtractParticipants to match PS Get-CallParticipantUpns which
// silently skips participants that have no UPN.
// upnOnly returns the UPN string of an identity, considering both the
// nested identitySet form (id.User.UserPrincipalName) and the flat
// userIdentity form (id.UserPrincipalName). Returns "" when neither
// form has a UPN — callers use the empty result to skip / fall through.
func upnOnly(id *graph.Identity) string {
	if id == nil {
		return ""
	}
	if id.User != nil && id.User.UserPrincipalName != "" {
		return id.User.UserPrincipalName
	}
	return id.UserPrincipalName
}

// upnOrDisplay returns a usable label for an identity. It first looks for a
// UPN (in either nested or flat form), then falls back to displayName in
// the same priority order. Used by ExtractOrganizer and attributeStream
// where PS uses displayName as a last-resort label.
func upnOrDisplay(id *graph.Identity) string {
	if id == nil {
		return ""
	}
	if id.User != nil {
		if id.User.UserPrincipalName != "" {
			return id.User.UserPrincipalName
		}
	}
	if id.UserPrincipalName != "" {
		return id.UserPrincipalName
	}
	if id.User != nil && id.User.DisplayName != "" {
		return id.User.DisplayName
	}
	return id.DisplayName
}

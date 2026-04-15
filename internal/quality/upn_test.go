package quality

import (
	"reflect"
	"testing"

	"teams_con/internal/graph"
)

// TestExtractParticipants_Tier1_ParticipantsV2 verifies that participants_v2
// is the primary source when present.
func TestExtractParticipants_Tier1_ParticipantsV2(t *testing.T) {
	t.Parallel()

	rec := &graph.CallRecord{
		ParticipantsV2: []graph.ParticipantV2{
			{Identity: &graph.Identity{User: &graph.User{UserPrincipalName: "alice@corp.com"}}},
			{Identity: &graph.Identity{User: &graph.User{UserPrincipalName: "bob@corp.com"}}},
		},
	}
	got := ExtractParticipants(rec)
	want := []string{"alice@corp.com", "bob@corp.com"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestExtractParticipants_Tier2_SessionCallerFallback verifies that a session
// caller UPN is picked up when participants_v2 is missing.
func TestExtractParticipants_Tier2_SessionCallerFallback(t *testing.T) {
	t.Parallel()
	rec := &graph.CallRecord{
		Sessions: []graph.Session{{
			Caller: newUserEndpoint("alice@corp.com", "Windows"),
			Callee: newServerEndpoint(),
		}},
	}
	got := ExtractParticipants(rec)
	want := []string{"alice@corp.com"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestExtractParticipants_Tier3_SessionCalleeFallback verifies the callee
// fallback when the caller is a server leg.
func TestExtractParticipants_Tier3_SessionCalleeFallback(t *testing.T) {
	t.Parallel()
	rec := &graph.CallRecord{
		Sessions: []graph.Session{{
			Caller: newServerEndpoint(),
			Callee: newUserEndpoint("bob@corp.com", "Mac"),
		}},
	}
	got := ExtractParticipants(rec)
	want := []string{"bob@corp.com"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestExtractParticipants_DisplayNameSkipped verifies that a participant with
// only a displayName (no UPN) is NOT included in the result list.
// PS Get-CallParticipantUpns is UPN-only and silently skips such entries.
func TestExtractParticipants_DisplayNameSkipped(t *testing.T) {
	t.Parallel()
	rec := &graph.CallRecord{
		Sessions: []graph.Session{{
			Caller: &graph.Endpoint{AssociatedIdentity: &graph.Identity{User: &graph.User{DisplayName: "Alice Doe"}}},
		}},
	}
	got := ExtractParticipants(rec)
	if len(got) != 0 {
		t.Errorf("expected empty (displayName-only entry must be skipped), got %v", got)
	}
}

// TestExtractParticipants_EmptyWhenNothing verifies an empty list when
// no UPN is present anywhere (server-only endpoints have no identity).
func TestExtractParticipants_Tier5_EmptyWhenNothing(t *testing.T) {
	t.Parallel()
	rec := &graph.CallRecord{
		Sessions: []graph.Session{{
			Caller: newServerEndpoint(),
			Callee: newServerEndpoint(),
		}},
	}
	if got := ExtractParticipants(rec); len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

// TestExtractParticipants_Dedup verifies case-sensitive dedup preserving
// first-occurrence order.
func TestExtractParticipants_Dedup(t *testing.T) {
	t.Parallel()
	rec := &graph.CallRecord{
		ParticipantsV2: []graph.ParticipantV2{
			{Identity: &graph.Identity{User: &graph.User{UserPrincipalName: "alice@corp.com"}}},
		},
		Sessions: []graph.Session{{
			Caller: newUserEndpoint("alice@corp.com", "Windows"), // dup
			Callee: newUserEndpoint("bob@corp.com", "Mac"),
		}},
	}
	got := ExtractParticipants(rec)
	want := []string{"alice@corp.com", "bob@corp.com"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestExtractOrganizer exercises organizer_v2 UPN extraction with a
// displayName fallback and a nil-record guard.
func TestExtractOrganizer(t *testing.T) {
	t.Parallel()

	if got := ExtractOrganizer(nil); got != "" {
		t.Errorf("nil record: got %q, want empty", got)
	}

	rec := &graph.CallRecord{OrganizerV2: &graph.ParticipantV2{
		Identity: &graph.Identity{User: &graph.User{UserPrincipalName: "carol@corp.com"}},
	}}
	if got := ExtractOrganizer(rec); got != "carol@corp.com" {
		t.Errorf("UPN: got %q", got)
	}

	rec.OrganizerV2.Identity.User.UserPrincipalName = ""
	rec.OrganizerV2.Identity.User.DisplayName = "Carol"
	if got := ExtractOrganizer(rec); got != "Carol" {
		t.Errorf("displayName fallback: got %q", got)
	}
}

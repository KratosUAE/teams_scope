package mcp

import (
	"fmt"
	"strings"

	"teams_con/internal/store"
)

// summarizeUserCardNotesMax bounds the notes excerpt in the text summary
// so the LLM gets a readable one-liner, not a 2KB novel. The full notes
// field is still available in the JSON payload.
const summarizeUserCardNotesMax = 80

// summarizeUserCard produces the one-line text header for the get_user_card
// tool response. A nil card renders the "no card" form so the LLM can
// answer "is this user annotated?" without parsing JSON. A populated card
// renders a compact "upn: location=... tags=[...] notes=..." line with the
// notes truncated at summarizeUserCardNotesMax runes.
func summarizeUserCard(card *store.UserCard, upn string) string {
	if card == nil {
		return fmt.Sprintf("no card for %s", upn)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s:", card.Upn)
	if card.DisplayName != "" {
		fmt.Fprintf(&b, " name=%s", card.DisplayName)
	}
	if card.Location != "" {
		fmt.Fprintf(&b, " location=%s", card.Location)
	}
	if len(card.Tags) > 0 {
		fmt.Fprintf(&b, " tags=[%s]", strings.Join(card.Tags, ","))
	}
	if card.Notes != "" {
		notes := card.Notes
		// Rune-aware truncation so we never split a multibyte codepoint.
		runes := []rune(notes)
		if len(runes) > summarizeUserCardNotesMax {
			notes = string(runes[:summarizeUserCardNotesMax]) + "..."
		}
		fmt.Fprintf(&b, " notes=%s", notes)
	}
	return b.String()
}

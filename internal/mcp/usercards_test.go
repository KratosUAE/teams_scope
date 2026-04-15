package mcp

import (
	"strings"
	"testing"

	"teams_con/internal/store"
)

func TestSummarizeUserCard_Nil(t *testing.T) {
	got := summarizeUserCard(nil, "ghost@corp.com")
	if !strings.Contains(got, "no card for ghost@corp.com") {
		t.Errorf("nil card summary = %q", got)
	}
}

func TestSummarizeUserCard_Present(t *testing.T) {
	tests := []struct {
		name        string
		card        store.UserCard
		wantContain []string
		wantAbsent  []string
	}{
		{
			name: "basic fields",
			card: store.UserCard{
				Upn:      "alice@corp.com",
				Location: "Dubai HQ",
				Tags:     []string{"vip", "mobile-heavy"},
				Notes:    "escalated",
			},
			wantContain: []string{"alice@corp.com", "location=Dubai HQ", "tags=[vip,mobile-heavy]", "notes=escalated"},
		},
		{
			name: "with DisplayName",
			card: store.UserCard{
				Upn:         "bob@corp.com",
				DisplayName: "Bob Smith",
				Location:    "Moscow",
			},
			wantContain: []string{"bob@corp.com", "name=Bob Smith", "location=Moscow"},
			wantAbsent:  []string{"tags=", "notes="},
		},
		{
			name: "no DisplayName does not emit name= field",
			card: store.UserCard{
				Upn:      "carol@corp.com",
				Location: "Remote",
			},
			wantContain: []string{"carol@corp.com", "location=Remote"},
			wantAbsent:  []string{"name="},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := summarizeUserCard(&tc.card, tc.card.Upn)
			for _, want := range tc.wantContain {
				if !strings.Contains(got, want) {
					t.Errorf("summary = %q; want it to contain %q", got, want)
				}
			}
			for _, absent := range tc.wantAbsent {
				if strings.Contains(got, absent) {
					t.Errorf("summary = %q; must NOT contain %q", got, absent)
				}
			}
		})
	}
}

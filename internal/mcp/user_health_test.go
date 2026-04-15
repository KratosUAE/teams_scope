package mcp

import (
	"strings"
	"testing"
	"time"

	"teams_con/internal/api"
)

func TestSummarizeUserHealth_Nil(t *testing.T) {
	if got := summarizeUserHealth(nil); !strings.Contains(got, "nil report") {
		t.Errorf("got %q", got)
	}
}

func TestSummarizeUserHealth_Empty(t *testing.T) {
	r := &api.UserHealthReport{
		Upn:        "alice@corp.com",
		WindowFrom: time.Date(2026, 4, 6, 0, 0, 0, 0, time.UTC),
		WindowTo:   time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC),
		TotalCalls: 0,
	}
	got := summarizeUserHealth(r)
	if !strings.Contains(got, "alice@corp.com") || !strings.Contains(got, "no calls in window") {
		t.Errorf("got %q", got)
	}
}

func TestSummarizeUserHealth_FullReport(t *testing.T) {
	r := &api.UserHealthReport{
		Upn:        "alice@corp.com",
		TotalCalls: 10,
		ByVerdict:  api.VerdictCounts{Good: 3, Poor: 3, Bad: 4},
		Pattern:    "chronic_mic",
		Devices: []api.DeviceUsage{
			{Device: "HeadsetPro", CallCount: 8, AvgConcealedPct: 12.5},
		},
		Subnets: []api.SubnetUsage{
			{Subnet: "10.0.0.0/24", CallCount: 10, ConnType: "wifi"},
		},
	}
	got := summarizeUserHealth(r)
	if !strings.Contains(got, "10 calls") ||
		!strings.Contains(got, "3 Good") ||
		!strings.Contains(got, "4 Bad") ||
		!strings.Contains(got, "pattern=chronic_mic") ||
		!strings.Contains(got, "HeadsetPro") ||
		!strings.Contains(got, "12.5%") ||
		!strings.Contains(got, "10.0.0.0/24") ||
		!strings.Contains(got, "wifi") {
		t.Errorf("summary missing fields: %q", got)
	}
}

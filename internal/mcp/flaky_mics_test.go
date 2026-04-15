package mcp

import (
	"strings"
	"testing"

	"teams_con/internal/api"
)

func TestSummarizeFlakyMics_Empty(t *testing.T) {
	got := summarizeFlakyMics(nil)
	if !strings.Contains(got, "no flaky") {
		t.Errorf("empty summary = %q", got)
	}
}

func TestSummarizeFlakyMics_TopLine(t *testing.T) {
	mics := []api.FlakyMic{
		{
			User:              "alice@corp.com",
			CaptureDevice:     "HeadsetPro",
			Incidents:         5,
			DistinctCalls:     4,
			WorstConcealedPct: 18.2,
			AvgConcealedPct:   12.1,
			Severity:          "Bad",
		},
		{
			User:              "bob@corp.com",
			CaptureDevice:     "BuiltIn",
			Incidents:         3,
			DistinctCalls:     3,
			WorstConcealedPct: 7.0,
			AvgConcealedPct:   6.0,
			Severity:          "Poor",
		},
	}
	got := summarizeFlakyMics(mics)
	if !strings.Contains(got, "2 flaky microphones") {
		t.Errorf("count header missing: %q", got)
	}
	if !strings.Contains(got, "1 Bad") || !strings.Contains(got, "1 Poor") {
		t.Errorf("severity counts missing: %q", got)
	}
	if !strings.Contains(got, "alice@corp.com") || !strings.Contains(got, "HeadsetPro") {
		t.Errorf("top device not named: %q", got)
	}
	if !strings.Contains(got, "worst=18.2") {
		t.Errorf("top worst missing: %q", got)
	}
}

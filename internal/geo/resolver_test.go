package geo

import (
	"testing"
)

func TestFormatGeo(t *testing.T) {
	tests := []struct {
		name    string
		city    string
		country string
		want    string
	}{
		{"both present", "Dubai", "AE", "Dubai, AE"},
		{"city only", "Dubai", "", "Dubai"},
		{"country only", "", "AE", "AE"},
		{"both empty", "", "", ""},
		{"moscow ru", "Moscow", "RU", "Moscow, RU"},
		{"long city name", "San Francisco", "US", "San Francisco, US"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatGeo(tt.city, tt.country)
			if got != tt.want {
				t.Errorf("formatGeo(%q, %q) = %q, want %q", tt.city, tt.country, got, tt.want)
			}
		})
	}
}

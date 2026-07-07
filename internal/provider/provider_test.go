package provider

import (
	"strings"
	"testing"
)

func TestNormalizeSeverity(t *testing.T) {
	tests := []struct {
		name string
		give string
		want string
	}{
		{name: "critical uppercase", give: "CRITICAL", want: SeverityCritical},
		{name: "high mixed case", give: "High", want: SeverityHigh},
		{name: "graphql moderate maps to medium", give: "MODERATE", want: SeverityMedium},
		{name: "rest medium lowercase", give: "medium", want: SeverityMedium},
		{name: "low", give: "low", want: SeverityLow},
		{name: "surrounding whitespace trimmed", give: "  High  ", want: SeverityHigh},
		{name: "unknown passes through lowercased", give: "Informational", want: "informational"},
		{name: "empty stays empty", give: "", want: ""},
		{name: "whitespace only stays empty", give: "   ", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeSeverity(tt.give); got != tt.want {
				t.Errorf("NormalizeSeverity(%q) = %q, want %q", tt.give, got, tt.want)
			}
		})
	}
}

func FuzzNormalizeSeverity(f *testing.F) {
	for _, seed := range []string{"CRITICAL", "moderate", "  Low ", "", "weird value", "HIGH", "info"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got := NormalizeSeverity(s)
		if got != strings.ToLower(got) {
			t.Errorf("NormalizeSeverity(%q) = %q, expected lowercase", s, got)
		}
		if got != strings.TrimSpace(got) {
			t.Errorf("NormalizeSeverity(%q) = %q, expected no surrounding whitespace", s, got)
		}
		if again := NormalizeSeverity(got); again != got {
			t.Errorf("NormalizeSeverity not idempotent: NormalizeSeverity(%q) = %q, want %q", got, again, got)
		}
	})
}

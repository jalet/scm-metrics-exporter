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

func TestRemediationScopeRoundTrip(t *testing.T) {
	scope := RemediationScope("gitlab", "grp/sub/svc", CategoryDependency, ResolutionFixed, SeverityHigh)
	p, r, c, res, sev, ok := ParseRemediationScope(scope)
	if !ok || p != "gitlab" || r != "grp/sub/svc" || c != CategoryDependency || res != ResolutionFixed || sev != SeverityHigh {
		t.Fatalf("round trip failed: got (%q,%q,%q,%q,%q,%v)", p, r, c, res, sev, ok)
	}
}

func TestRemediationScopeRoundTripEmptySeverity(t *testing.T) {
	// When the severity dimension is off, the fifth field is empty and must round-trip.
	scope := RemediationScope("github", "acme/svc", CategoryStaticAnalysis, ResolutionFixed, "")
	p, r, c, res, sev, ok := ParseRemediationScope(scope)
	if !ok || p != "github" || r != "acme/svc" || c != CategoryStaticAnalysis || res != ResolutionFixed || sev != "" {
		t.Fatalf("round trip failed: got (%q,%q,%q,%q,%q,%v)", p, r, c, res, sev, ok)
	}
}

func TestParseRemediationScopeRejectsMalformed(t *testing.T) {
	if _, _, _, _, _, ok := ParseRemediationScope("only:one:field"); ok {
		t.Fatal("expected ok=false for a scope without the unit-separator layout")
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

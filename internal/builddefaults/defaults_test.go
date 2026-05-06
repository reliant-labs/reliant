package builddefaults

import "testing"

func TestValuePrefersEnvironment(t *testing.T) {
	t.Setenv("RELIANT_TEST_DEFAULT", "from-env")

	got := Value("RELIANT_TEST_DEFAULT", "from-build", "fallback")
	if got != "from-env" {
		t.Fatalf("Value() = %q, want %q", got, "from-env")
	}
}

func TestValueUsesCompiledDefaultWhenEnvironmentUnset(t *testing.T) {
	t.Setenv("RELIANT_TEST_DEFAULT", "")

	got := Value("RELIANT_TEST_DEFAULT", "from-build", "fallback")
	if got != "from-build" {
		t.Fatalf("Value() = %q, want %q", got, "from-build")
	}
}

func TestValueUsesFallbackWhenEnvironmentAndCompiledDefaultUnset(t *testing.T) {
	t.Setenv("RELIANT_TEST_DEFAULT", "")

	got := Value("RELIANT_TEST_DEFAULT", "", "fallback")
	if got != "fallback" {
		t.Fatalf("Value() = %q, want %q", got, "fallback")
	}
}

// Copyright (c) 2025 Reliant Labs
package config

import "testing"

// TestRemovedEnvironmentsDoNotBypassAuth is the regression guard for the trap
// that removing the staging/preprod enum values could have introduced.
//
// Before this change GetEnvironment had `case "staging"` / `case "preprod"`
// arms alongside a `default: return EnvironmentDev`, and
// IsDevelopmentEnvironment treated dev, staging and preprod alike as the
// auth-bypass tier. Deleting only the two case arms would have left those
// values falling through to the permissive default — the NAME would be gone
// while the dangerous BEHAVIOUR survived, with no enum left to explain it. A
// machine still carrying a stale RELIANT_ENV=staging would have kept bypassing
// auth silently.
//
// So the deletion is only correct if these values now resolve to prod.
func TestRemovedEnvironmentsDoNotBypassAuth(t *testing.T) {
	for _, name := range []string{"staging", "preprod", "STAGING", "PreProd"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("RELIANT_ENV", name)
			t.Setenv("NODE_ENV", "")

			if got := GetEnvironment(); got != EnvironmentProd {
				t.Errorf("GetEnvironment() = %q, want %q: a removed environment name must fail closed, not fall through to the permissive tier", got, EnvironmentProd)
			}
			if IsDevelopmentEnvironment() {
				t.Error("IsDevelopmentEnvironment() = true: RELIANT_ENV=" + name + " still resolves to the auth-bypassing tier, which is the exact regression this deletion had to avoid")
			}
		})
	}
}

// TestUnrecognisedEnvironmentFailsClosed generalises the rule above: dev is
// reachable only by asking for it. Anything unrecognised — a typo, an
// environment that never existed — must land on prod rather than the tier that
// disables authentication.
func TestUnrecognisedEnvironmentFailsClosed(t *testing.T) {
	for _, name := range []string{"", "qa", "sandbox", "devv", "produciton", "nonprod"} {
		t.Run("RELIANT_ENV="+name, func(t *testing.T) {
			t.Setenv("RELIANT_ENV", name)
			t.Setenv("NODE_ENV", "")

			if got := GetEnvironment(); got != EnvironmentProd {
				t.Errorf("GetEnvironment() = %q, want %q", got, EnvironmentProd)
			}
			if IsDevelopmentEnvironment() {
				t.Errorf("IsDevelopmentEnvironment() = true for unrecognised RELIANT_ENV=%q", name)
			}
		})
	}
}

// TestRecognisedEnvironments pins the values that still resolve to a non-prod
// tier. "e2e" is load-bearing: control-plane's deploy/kcl/e2e/main.k sets
// RELIANT_ENV=e2e on the reliant pods, and that harness relied on the old
// permissive default to reach dev behaviour. With the default now failing
// closed it has to be named explicitly, or the e2e stack would silently switch
// to prod behaviour (live Sentry, Statsig, no env-var feature-flag provider).
func TestRecognisedEnvironments(t *testing.T) {
	cases := []struct {
		reliantEnv string
		want       Environment
		wantDev    bool
	}{
		{"dev", EnvironmentDev, true},
		{"development", EnvironmentDev, true},
		{"local", EnvironmentDev, true},
		{"e2e", EnvironmentDev, true},
		{"test", EnvironmentTest, false},
		{"testing", EnvironmentTest, false},
		{"prod", EnvironmentProd, false},
		{"production", EnvironmentProd, false},
	}

	for _, tc := range cases {
		t.Run(tc.reliantEnv, func(t *testing.T) {
			t.Setenv("RELIANT_ENV", tc.reliantEnv)
			t.Setenv("NODE_ENV", "")

			if got := GetEnvironment(); got != tc.want {
				t.Errorf("GetEnvironment() = %q, want %q", got, tc.want)
			}
			if got := IsDevelopmentEnvironment(); got != tc.wantDev {
				t.Errorf("IsDevelopmentEnvironment() = %v, want %v", got, tc.wantDev)
			}
		})
	}
}

// TestNodeEnvFallback covers the second input. NODE_ENV is only consulted when
// RELIANT_ENV is unset, and it obeys the same fail-closed rule — Node's own
// convention of leaving NODE_ENV empty must not mean "dev".
func TestNodeEnvFallback(t *testing.T) {
	t.Run("development", func(t *testing.T) {
		t.Setenv("RELIANT_ENV", "")
		t.Setenv("NODE_ENV", "development")
		if !IsDevelopmentEnvironment() {
			t.Error("NODE_ENV=development should select the dev tier when RELIANT_ENV is unset")
		}
	})

	t.Run("both unset defaults to prod", func(t *testing.T) {
		t.Setenv("RELIANT_ENV", "")
		t.Setenv("NODE_ENV", "")
		if got := GetEnvironment(); got != EnvironmentProd {
			t.Errorf("GetEnvironment() = %q with no env vars set, want %q", got, EnvironmentProd)
		}
	})

	t.Run("RELIANT_ENV wins over NODE_ENV", func(t *testing.T) {
		t.Setenv("RELIANT_ENV", "prod")
		t.Setenv("NODE_ENV", "development")
		if IsDevelopmentEnvironment() {
			t.Error("RELIANT_ENV=prod must win over NODE_ENV=development")
		}
	})
}

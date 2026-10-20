// Copyright (c) 2025 Reliant Labs
package runenv

import "testing"

// TestRemovedEnvironmentsDoNotBypassAuth is the regression guard for the trap
// that removing the staging/preprod enum values could have introduced.
//
// Before this change Get had `case "staging"` / `case "preprod"`
// arms alongside a `default: return Dev`, and
// IsDev treated dev, staging and preprod alike as the
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
			ConfigureForTest(t, name)

			if got := Get(); got != Prod {
				t.Errorf("Get() = %q, want %q: a removed environment name must fail closed, not fall through to the permissive tier", got, Prod)
			}
			if IsDev() {
				t.Error("IsDev() = true: RELIANT_ENV=" + name + " still resolves to the auth-bypassing tier, which is the exact regression this deletion had to avoid")
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
			ConfigureForTest(t, name)

			if got := Get(); got != Prod {
				t.Errorf("Get() = %q, want %q", got, Prod)
			}
			if IsDev() {
				t.Errorf("IsDev() = true for unrecognised RELIANT_ENV=%q", name)
			}
		})
	}
}

// TestRecognisedEnvironments pins the values that still resolve to a non-prod
// tier. "e2e" is load-bearing: control-plane's deploy/kcl/e2e/main.k sets
// RELIANT_ENV=e2e on the reliant pods, and that harness relied on the old
// permissive default to reach dev behaviour. With the default now failing
// closed it has to be named explicitly, or the e2e stack would silently switch
// to prod behaviour (live Sentry, no env-var feature-flag provider).
func TestRecognisedEnvironments(t *testing.T) {
	cases := []struct {
		reliantEnv string
		want       Environment
		wantDev    bool
	}{
		{"dev", Dev, true},
		{"development", Dev, true},
		{"local", Dev, true},
		{"e2e", Dev, true},
		{"test", Test, false},
		{"testing", Test, false},
		{"prod", Prod, false},
		{"production", Prod, false},
	}

	for _, tc := range cases {
		t.Run(tc.reliantEnv, func(t *testing.T) {
			ConfigureForTest(t, tc.reliantEnv)

			if got := Get(); got != tc.want {
				t.Errorf("Get() = %q, want %q", got, tc.want)
			}
			if got := IsDev(); got != tc.wantDev {
				t.Errorf("IsDev() = %v, want %v", got, tc.wantDev)
			}
		})
	}
}

// Which variable supplies the tier (RELIANT_ENV, else NODE_ENV) is the root
// config's business, pinned by cmd/reliant/commands TestRuntimeEnvContract.
// What runenv owns is what happens before and without that: a process that
// never configured a tier is prod, and a test's tier does not outlive it.
func TestUnconfiguredIsProd(t *testing.T) {
	prev := configured.Load()
	configured.Store(nil)
	t.Cleanup(func() { configured.Store(prev) })

	if got := Get(); got != Prod {
		t.Errorf("Get() = %q with no tier configured, want %q", got, Prod)
	}
	if IsDev() {
		t.Error("IsDev() = true with no tier configured")
	}
}

func TestConfigureForTestRestores(t *testing.T) {
	ConfigureForTest(t, "test")
	t.Run("inner", func(t *testing.T) {
		ConfigureForTest(t, "dev")
		if !IsDev() {
			t.Fatal("ConfigureForTest(dev) did not take")
		}
	})
	if !IsTest() {
		t.Errorf("Get() = %q after the subtest, want the outer %q back", Get(), Test)
	}
}

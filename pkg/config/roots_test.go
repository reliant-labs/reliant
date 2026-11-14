package config

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// A root over two configs: the reach message (database_url required; the
// tick block's token required too, but outside this root's reach) and the
// family message (env families under FAMTEST_OAUTH_ / FAMTEST_FEATURE_ /
// FAMTEST_TIER_, the last one closed to free|pro).
func validateFixture(t *testing.T) Root {
	t.Helper()
	return Root{
		Name: "api",
		Args: []string{"server", "api"},
		Vars: []RootVar{
			{Name: "RW_DATABASE_URL", Config: "AppConfig", Path: "database_url", Type: "string", Required: true, Sensitive: true},
		},
		Families: []RootFamily{
			{Prefix: famOAuth, Suffix: "_CLIENT_ID", Config: "IntegrationsConfig", Path: "client_ids"},
			{Prefix: famFeature, Config: "IntegrationsConfig", Path: "features"},
			{Prefix: "FAMTEST_TIER_", Config: "IntegrationsConfig", Path: "tiers"},
		},
		Configs: []RootConfig{
			{Message: buildReachMessage(t).Interface(), Reach: []string{"database_url"}},
			{Message: buildFamilyMessage(t).Interface(), Reach: []string{}},
		},
	}
}

// Validate runs the boot load over a rendered env, never the process's, and
// reports every problem it finds.
func TestRootValidate_EveryProblemOverTheRenderedEnv(t *testing.T) {
	r := validateFixture(t)
	// The process environment is not what is checked.
	t.Setenv("RW_DATABASE_URL", "from-the-test-process")

	if problems := r.Validate(map[string]string{
		"RW_DATABASE_URL":             "postgres://x",
		famOAuth + "GITHUB_CLIENT_ID": "gh",
		famFeature + "DARK_MODE":      "true",
		"FAMTEST_TIER_ACME":           "pro",
	}); len(problems) != 0 {
		t.Fatalf("a correct env has no problems; got %v", problems)
	}

	problems := r.Validate(map[string]string{
		famOAuth + "GITHUB_CLIENT_SECERT": "typo",      // a misspelt family entry
		famFeature + "DARK_MODE":          "maybe",     // not a bool
		"FAMTEST_TIER_ACME":               "platinum",  // outside allowed_values
		"RW_TICK_TOKEN":                   "not-yours", // a secret this root never reads
	})
	var msgs []string
	var unreached []string
	for _, p := range problems {
		msgs = append(msgs, p.Error())
		var u *UnreachedVarError
		if errors.As(p, &u) {
			unreached = append(unreached, u.Name)
		}
	}
	all := strings.Join(msgs, "\n")
	for _, want := range []string{
		"required config field database_url is not set",
		"FAMTEST_OAUTH_GITHUB_CLIENT_SECERT is under the config family prefix",
		`invalid value "maybe" for config field features["DARK_MODE"]`,
		`value "platinum" is not one of the allowed values`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("problems lack %q:\n%s", want, all)
		}
	}
	if !slices.Equal(unreached, []string{"RW_TICK_TOKEN"}) {
		t.Errorf("unreached = %v, want [RW_TICK_TOKEN]: the misspelt family entry is reported by the load, not twice", unreached)
	}
	if strings.Contains(all, "token is not set") {
		t.Error("a required field outside the root's reach is not demanded")
	}
}

func TestRoot_ROOTSListsAndReach(t *testing.T) {
	r := Root{Vars: []RootVar{
		{Name: "PORT"},
		{Name: "API_KEY", Sensitive: true, Required: true},
		{Name: "SENTRY_DSN", Sensitive: true, Aliases: []string{"DSN"}},
	}, Families: []RootFamily{{Prefix: "APP_FEATURE_"}}}
	if !slices.Equal(r.Env(), []string{"PORT"}) || !slices.Equal(r.Secrets(), []string{"API_KEY", "SENTRY_DSN"}) || !slices.Equal(r.RequiredSecrets(), []string{"API_KEY"}) {
		t.Errorf("env %v secrets %v required %v", r.Env(), r.Secrets(), r.RequiredSecrets())
	}
	got := r.Outside(map[string]string{"PORT": "1", "DSN": "x", "APP_FEATURE_X": "true", "HOME": "/root", "OTHER_SECRET": "s"})
	if !slices.Equal(got, []string{"HOME", "OTHER_SECRET"}) {
		t.Errorf("Outside = %v", got)
	}
}

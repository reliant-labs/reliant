package deploytarget

import (
	"context"
	"testing"
)

// FindHostedEnvironment carries the two-repositories warning of the matched
// environment's app, from the same ListEnvironments answer that resolved it,
// and nothing from another app's entry or from a server that sends none.
func TestFindHostedEnvironmentReadsTheAppsCodeSourceWarning(t *testing.T) {
	t.Parallel()
	const env = `{"id":"env-1","name":"prod","project":"shop"}`
	cases := map[string]struct {
		reply string
		want  string
	}{
		"warning for this app": {
			reply: `{"environments":[` + env + `],"appCodeSources":[{"project":"shop","warning":"  \"shop\" is deployed from 2 repositories.  "}]}`,
			want:  `"shop" is deployed from 2 repositories.`,
		},
		"warning for another app only": {
			reply: `{"environments":[` + env + `],"appCodeSources":[{"project":"blog","warning":"\"blog\" is deployed from 2 repositories."}]}`,
		},
		"sources but no warning": {
			reply: `{"environments":[` + env + `],"appCodeSources":[{"project":"shop","sources":[{"source":{"remote":"github.com/acme/shop"}}]}]}`,
		},
		"older server": {
			reply: `{"environments":[` + env + `]}`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := FindHostedEnvironment(context.Background(), &fakeCP{envs: tc.reply}, "shop", "prod")
			if err != nil {
				t.Fatalf("find: %v", err)
			}
			if got.ID != "env-1" {
				t.Fatalf("resolved %q, want env-1", got.ID)
			}
			if got.CodeSourceWarning != tc.want {
				t.Errorf("warning = %q, want %q", got.CodeSourceWarning, tc.want)
			}
		})
	}
}

// An environment that names no project predates the project dimension and
// belongs to no app, so no app's warning is attached to it.
func TestFindHostedEnvironmentWithNoProjectHasNoWarning(t *testing.T) {
	t.Parallel()
	reply := `{"environments":[{"id":"env-1","name":"prod"}],"appCodeSources":[{"project":"shop","warning":"two repositories"}]}`
	got, err := FindHostedEnvironment(context.Background(), &fakeCP{envs: reply}, "shop", "prod")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if got.CodeSourceWarning != "" {
		t.Errorf("warning = %q on an env with no project", got.CodeSourceWarning)
	}
}

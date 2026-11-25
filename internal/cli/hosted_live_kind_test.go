package cli

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// TestEnvKindOf pins how a live environment's kind is read from either wire
// vocabulary: the retired kind field when the control plane still sends it,
// and lifetime + secret posture + hosted slot when it does not. The refusals
// are the point as much as the derivations: the kind decides whether an
// env's secrets read back, so an env must never read as more readable than
// the control plane said.
func TestEnvKindOf(t *testing.T) {
	t.Parallel()
	const (
		persistent = deployEnvLifetimePersistent
		preview    = deployEnvLifetimePreview
		readable   = deploySecretPostureReadable
		writeOnly  = deploySecretPostureWriteOnly
	)
	for _, tc := range []struct {
		name string
		env  wireLiveEnvironment
		want release.EnvKind // "" means refused
	}{
		// The kind is present: exactly today's behaviour, whatever else
		// the row carries.
		{"kind persistent", wireLiveEnvironment{Kind: string(deployEnvKindPersistent)}, release.EnvPersistent},
		{"kind preview", wireLiveEnvironment{Kind: string(deployEnvKindPreview)}, release.EnvPreview},
		{"kind self-managed", wireLiveEnvironment{Kind: string(deployEnvKindSelfManaged)}, release.EnvSelfManaged},
		{"kind local", wireLiveEnvironment{Kind: string(deployEnvKindLocal)}, release.EnvLocal},
		{"kind wins over the replacements", wireLiveEnvironment{
			Kind: string(deployEnvKindSelfManaged), Lifetime: persistent, SecretPosture: writeOnly, HostedSlot: true,
		}, release.EnvSelfManaged},
		{"kind local agreeing with its posture", wireLiveEnvironment{
			Kind: string(deployEnvKindLocal), Lifetime: persistent, SecretPosture: readable,
		}, release.EnvLocal},
		{"kind unrecognised", wireLiveEnvironment{Kind: "DEPLOY_ENVIRONMENT_KIND_SOMETHING_NEW", Lifetime: persistent, SecretPosture: writeOnly}, ""},
		{"kind local contradicted by a write-only posture", wireLiveEnvironment{
			Kind: string(deployEnvKindLocal), Lifetime: persistent, SecretPosture: writeOnly,
		}, ""},

		// The kind is absent (a control plane that retired it): derived.
		{"readable is local", wireLiveEnvironment{Lifetime: persistent, SecretPosture: readable}, release.EnvLocal},
		{"write-only preview is preview", wireLiveEnvironment{Lifetime: preview, SecretPosture: writeOnly, HostedSlot: true}, release.EnvPreview},
		{"write-only with a hosted slot is persistent", wireLiveEnvironment{Lifetime: persistent, SecretPosture: writeOnly, HostedSlot: true}, release.EnvPersistent},
		{"write-only with no hosted slot is self-managed", wireLiveEnvironment{Lifetime: persistent, SecretPosture: writeOnly}, release.EnvSelfManaged},
		{"UNSPECIFIED kind derives too", wireLiveEnvironment{Kind: deployEnvKindUnspecified, Lifetime: persistent, SecretPosture: writeOnly}, release.EnvSelfManaged},

		// Nothing to read, or nothing to trust: refused.
		{"no kind and nothing else", wireLiveEnvironment{}, ""},
		{"UNSPECIFIED kind and nothing else", wireLiveEnvironment{Kind: deployEnvKindUnspecified}, ""},
		{"no secret posture", wireLiveEnvironment{Lifetime: persistent, HostedSlot: true}, ""},
		{"no lifetime", wireLiveEnvironment{SecretPosture: readable}, ""},
		{"posture unrecognised", wireLiveEnvironment{Lifetime: persistent, SecretPosture: "DEPLOY_SECRET_POSTURE_SOMETHING_NEW"}, ""},
		{"lifetime unrecognised", wireLiveEnvironment{Lifetime: "DEPLOY_ENVIRONMENT_LIFETIME_SOMETHING_NEW", SecretPosture: writeOnly}, ""},
		{"posture UNSPECIFIED", wireLiveEnvironment{Lifetime: persistent, SecretPosture: "DEPLOY_SECRET_POSTURE_UNSPECIFIED"}, ""},
		{"readable but hosted", wireLiveEnvironment{Lifetime: persistent, SecretPosture: readable, HostedSlot: true}, ""},
		{"readable but a preview", wireLiveEnvironment{Lifetime: preview, SecretPosture: readable}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := envKindOf(tc.env)
			if tc.want == "" {
				if err == nil {
					t.Fatalf("envKindOf(%+v) = %q, want a refusal", tc.env, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("envKindOf(%+v): %v", tc.env, err)
			}
			if got != tc.want {
				t.Errorf("envKindOf(%+v) = %q, want %q", tc.env, got, tc.want)
			}
		})
	}
}

// cpLiveEnvNoKindFixture is a DeployLiveEnvironment as protojson writes it
// from a control plane that has reserved DeployEnvironment.kind (tag 3): no
// "kind", and the lifetime / secretPosture / hostedSlot that replaced it.
const cpLiveEnvNoKindFixture = `{
  "environment":  {
    "id":  "env_prod",
    "name":  "prod",
    "namespace":  "acme-prod",
    "project":  "hounders",
    "convergesPromotions":  true,
    "lifetime":  "DEPLOY_ENVIRONMENT_LIFETIME_PERSISTENT",
    "secretPosture":  "DEPLOY_SECRET_POSTURE_WRITE_ONLY",
    "hostedSlot":  true,
    "declaredAt":  "2026-10-02T09:59:00Z"
  },
  "phase":  "DEPLOY_ROLLOUT_PHASE_SUCCEEDED"
}`

func TestLiveEnvFromWire_DecodesAnEnvironmentWithNoKind(t *testing.T) {
	t.Parallel()
	var row wireLiveEnvRow
	if err := json.Unmarshal([]byte(cpLiveEnvNoKindFixture), &row); err != nil {
		t.Fatal(err)
	}
	live, err := liveEnvFromWire(row, time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("liveEnvFromWire: %v", err)
	}
	if live.Env.Kind != release.EnvPersistent {
		t.Errorf("kind = %q, want %q", live.Env.Kind, release.EnvPersistent)
	}
	if live.Env.Name != "prod" || live.EnvironmentID != "env_prod" || !live.ConvergesPromotions {
		t.Errorf("env = %+v", live)
	}

	// The same body from a control plane older than the replacement
	// fields, with no kind either: refused, as before.
	var bare wireLiveEnvRow
	if err := json.Unmarshal([]byte(`{"environment": {"id": "env_prod", "name": "prod"}}`), &bare); err != nil {
		t.Fatal(err)
	}
	if _, err := liveEnvFromWire(bare, time.Now()); err == nil {
		t.Error("an environment with no kind, lifetime or secret posture must be refused")
	}
}

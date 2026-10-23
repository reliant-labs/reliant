package deploytarget

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/release"
)

// capsCaller answers GetCapabilities with features (or err) and records the
// EnsureEnvironment request, so a test sees both what was asked and what was
// sent.
type capsCaller struct {
	features []string
	capsErr  error
	capsAsks int
	ensured  map[string]any
}

func (c *capsCaller) Call(_ context.Context, procedure string, req, out any) error {
	switch procedure {
	case ProcDeployCapabilities:
		c.capsAsks++
		if c.capsErr != nil {
			return c.capsErr
		}
		b, _ := json.Marshal(map[string]any{"bundleDocumentFeatures": c.features})
		return json.Unmarshal(b, out)
	case procEnsureEnvironment:
		c.ensured = req.(map[string]any)
	}
	b, _ := json.Marshal(map[string]any{"environment": map[string]any{"id": "env_1"}})
	return json.Unmarshal(b, out)
}

func telemetryShape() *release.Shape {
	return &release.Shape{
		Kind: release.EnvSelfManaged,
		ObservabilityPolicy: &release.ShapeObservabilityPolicy{
			Enabled: true, OptIn: []string{release.ObsOptInReplay},
			Retention: &release.ShapeRetention{ReplayDays: 7, ErrorsDays: 30, ProductEventsDays: 90},
		},
		FrontendTelemetry: []release.ShapeFrontendTelemetry{{Frontend: "web", Sourcemaps: release.SourcemapsOff}},
	}
}

func sentShape(t *testing.T, c *capsCaller) string {
	t.Helper()
	spec, _ := c.ensured["spec"].(map[string]any)
	raw, err := json.Marshal(spec["shape"])
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// A control plane that predates the feature strict-decodes the declared shape
// and refuses the whole of it on an unknown key, so the ensure leaves the
// telemetry parts out. Every part it still understands is sent.
func TestEnsureHostedEnvironment_WithholdsFrontendTelemetryFromAnOldControlPlane(t *testing.T) {
	for name, tc := range map[string]struct {
		c    *capsCaller
		sent bool
	}{
		"names no feature":                   {&capsCaller{}, false},
		"does not serve GetCapabilities":     {&capsCaller{capsErr: &cloud.Error{HTTPStatus: 404}}, false},
		"names every feature this forge has": {&capsCaller{features: release.BundleDocFeatures()}, true},
	} {
		t.Run(name, func(t *testing.T) {
			ref := HostedEnvRef{Project: "p", Name: "prod", Kind: HostedEnvSelfManaged, Shape: telemetryShape()}
			if _, _, err := EnsureHostedEnvironment(context.Background(), tc.c, ref); err != nil {
				t.Fatal(err)
			}
			got := sentShape(t, tc.c)
			for _, key := range []string{"frontend_telemetry", `"retention"`} {
				if has := strings.Contains(got, key); has != tc.sent {
					t.Errorf("shape %s carries %s = %v, want %v", got, key, has, tc.sent)
				}
			}
			if !strings.Contains(got, `"opt_in":["replay"]`) {
				t.Errorf("the replay opt-in must always be sent: %s", got)
			}
		})
	}
}

// Asking costs a call, so a declaration with nothing to withhold makes none.
func TestEnsureHostedEnvironment_AsksNothingWhenThereIsNothingToWithhold(t *testing.T) {
	c := &capsCaller{}
	ref := HostedEnvRef{Name: "prod", Kind: HostedEnvSelfManaged, Shape: &release.Shape{Kind: release.EnvSelfManaged}}
	if _, _, err := EnsureHostedEnvironment(context.Background(), c, ref); err != nil {
		t.Fatal(err)
	}
	if c.capsAsks != 0 {
		t.Errorf("asked GetCapabilities %d time(s) for a shape with no telemetry", c.capsAsks)
	}
}

// A control plane that answers badly is not evidence that it decodes nothing:
// the ensure fails rather than sending a guess.
func TestEnsureHostedEnvironment_RefusesToGuessWhenCapabilitiesFail(t *testing.T) {
	c := &capsCaller{capsErr: errors.New("connection reset")}
	ref := HostedEnvRef{Name: "prod", Kind: HostedEnvSelfManaged, Shape: telemetryShape()}
	_, _, err := EnsureHostedEnvironment(context.Background(), c, ref)
	if err == nil || !strings.Contains(err.Error(), "GetCapabilities") {
		t.Fatalf("err = %v, want one naming GetCapabilities", err)
	}
	if c.ensured != nil {
		t.Error("an ensure was sent after the capability question failed")
	}
}

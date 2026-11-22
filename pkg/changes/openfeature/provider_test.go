package openfeature

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	of "github.com/open-feature/go-sdk/openfeature"
	"github.com/open-feature/go-sdk/openfeature/isolated"

	"github.com/reliant-labs/forge/pkg/changes"
)

// providerSnapshot holds one change per reason the provider maps.
func providerSnapshot() string {
	return fmt.Sprintf(`{"schema":"forge.changes/v1","revision":9,"changes":{
		"exp":{"variants":{"control":false,"treatment":true},"default_variant":"control",
			"delivery":{"mode":"experiment","unit":"user","run":"r_1","salt":"r_1","percent_bp":10000,
				"audience":{"attribute":"plan","in":["paid"]},
				"split":[{"variant":"treatment","weight_bp":10000}],
				"preview":[{"unit_hash":%q,"variant":"control"}]}},
		"roll":{"variants":{"on":true},
			"delivery":{"mode":"rollout","unit":"user","run":"r_2","salt":"r_2","percent_bp":10000,"rollout_variant":"on"}},
		"zero":{"variants":{"on":true},
			"delivery":{"mode":"rollout","unit":"user","run":"r_3","salt":"r_3","percent_bp":0,"rollout_variant":"on"}},
		"color":{"variants":{"blue":"blue"},"delivery":{"mode":"everyone","variant":"blue"}},
		"limit":{"variants":{"small":10,"half":2.5,"obj":{"max":3}},"delivery":{"mode":"everyone","variant":"small"}},
		"half":{"variants":{"half":2.5},"delivery":{"mode":"everyone","variant":"half"}},
		"obj":{"variants":{"obj":{"max":3}},"delivery":{"mode":"everyone","variant":"obj"}},
		"bad":{"variants":{"on":true},"delivery":{"mode":"sideways"}}
	}}`, changes.PreviewHash("exp", "previewer"))
}

func newTestProvider(t *testing.T) *Provider {
	t.Helper()
	path := filepath.Join(t.TempDir(), "snapshot.json")
	if err := os.WriteFile(path, []byte(providerSnapshot()), 0o600); err != nil {
		t.Fatal(err)
	}
	s := changes.OpenFile(context.Background(), path, changes.Options{OnEvaluate: func(context.Context, string, changes.Result) {}})
	t.Cleanup(func() { _ = s.Close() })
	return NewProvider(s)
}

func flat(key string, attrs map[string]any) of.FlattenedContext {
	f := of.FlattenedContext{}
	if key != "" {
		f[of.TargetingKey] = key
	}
	for k, v := range attrs {
		f[k] = v
	}
	return f
}

func TestProviderMapsReasons(t *testing.T) {
	p := newTestProvider(t)
	ctx := context.Background()
	cases := []struct {
		name      string
		flag      string
		ctx       of.FlattenedContext
		def       bool
		value     bool
		variant   string
		reason    of.Reason
		errorCode of.ErrorCode
		run       string
	}{
		{"experiment", "exp", flat("u1", map[string]any{"plan": "paid"}), false, true, "treatment", of.SplitReason, "", "r_1"},
		{"rollout", "roll", flat("u1", nil), false, true, "on", of.SplitReason, "", "r_2"},
		{"preview", "exp", flat("previewer", nil), true, false, "control", of.TargetingMatchReason, "", ""},
		{"ineligible", "exp", flat("u1", map[string]any{"plan": "free"}), true, false, "control", of.DefaultReason, "", "r_1"},
		{"non-string attribute dropped", "exp", flat("u1", map[string]any{"plan": 7}), true, false, "control", of.DefaultReason, "", "r_1"},
		{"no unit", "exp", flat("", nil), true, false, "control", of.DefaultReason, "", "r_1"},
		{"not enrolled, no declared default", "zero", flat("u1", nil), true, true, "", of.DefaultReason, "", "r_3"},
		{"unknown", "nope", flat("u1", nil), true, true, "", of.ErrorReason, of.FlagNotFoundCode, ""},
		{"invalid", "bad", flat("u1", nil), true, true, "", of.ErrorReason, of.ParseErrorCode, ""},
		{"type mismatch", "color", flat("u1", nil), true, true, "", of.ErrorReason, of.TypeMismatchCode, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := p.BooleanEvaluation(ctx, c.flag, c.def, c.ctx)
			d := got.ResolutionDetail()
			if got.Value != c.value || d.Variant != c.variant || d.Reason != c.reason || d.ErrorCode != c.errorCode {
				t.Fatalf("got value %v variant %q reason %s error %q, want %v %q %s %q",
					got.Value, d.Variant, d.Reason, d.ErrorCode, c.value, c.variant, c.reason, c.errorCode)
			}
			if rev, _ := d.FlagMetadata.GetInt("revision"); rev != 9 {
				t.Errorf("revision metadata %v, want 9", d.FlagMetadata["revision"])
			}
			if run, _ := d.FlagMetadata.GetString("run"); run != c.run {
				t.Errorf("run metadata %q, want %q", run, c.run)
			}
			if _, err := d.FlagMetadata.GetString("reason"); err != nil {
				t.Errorf("no forge reason in the metadata: %v", err)
			}
		})
	}
}

func TestProviderTypes(t *testing.T) {
	p := newTestProvider(t)
	ctx := context.Background()
	everyone := flat("u1", nil)

	if s := p.StringEvaluation(ctx, "color", "red", everyone); s.Value != "blue" || s.Reason != of.StaticReason || s.Variant != "blue" {
		t.Errorf("string: %+v", s)
	}
	if f := p.FloatEvaluation(ctx, "half", 1, everyone); f.Value != 2.5 {
		t.Errorf("float: %+v", f)
	}
	if i := p.IntEvaluation(ctx, "limit", 1, everyone); i.Value != 10 || i.Error() != nil {
		t.Errorf("int: %+v", i)
	}
	if i := p.IntEvaluation(ctx, "half", 1, everyone); i.Value != 1 || i.ResolutionDetail().ErrorCode != of.TypeMismatchCode {
		t.Errorf("int of 2.5 must be a type mismatch with the default: %+v", i)
	}
	if f := p.FloatEvaluation(ctx, "color", 1, everyone); f.Value != 1 || f.ResolutionDetail().ErrorCode != of.TypeMismatchCode {
		t.Errorf("float of a string must be a type mismatch with the default: %+v", f)
	}
	o := p.ObjectEvaluation(ctx, "obj", nil, everyone)
	if m, ok := o.Value.(map[string]any); !ok || m["max"] != float64(3) {
		t.Errorf("object: %+v", o)
	}
	if p.Metadata().Name != "forge" || p.Hooks() != nil {
		t.Errorf("metadata %+v hooks %v", p.Metadata(), p.Hooks())
	}
}

func TestProviderThroughTheSDK(t *testing.T) {
	// The provider satisfies the SDK's interface and works behind a client.
	var _ of.FeatureProvider = (*Provider)(nil)
	api := isolated.NewAPI()
	t.Cleanup(func() { _ = api.Shutdown(context.Background()) })
	if err := api.SetProviderAndWait(context.Background(), newTestProvider(t)); err != nil {
		t.Fatal(err)
	}
	client := api.NewClient()
	on, err := client.BooleanValue(context.Background(), "exp", false, of.NewEvaluationContext("u1", map[string]any{"plan": "paid"}))
	if err != nil || !on {
		t.Fatalf("BooleanValue = %v, %v; want true", on, err)
	}
}

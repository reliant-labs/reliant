package kcleval_test

// format_test.go pins the --format contract, which is where the reason anyone
// reached for `kcl` directly actually lives. It needs no KCL evaluation: the
// input is a decoded value, so these are cheap and run in -short mode.

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/kcleval"
)

// TestRenderRawIsTheBareScalar: raw prints the value's own text with no
// quoting and no trailing newline, so `$(forge kcl eval … --format raw)`
// captures exactly the value.
//
// The `tr -d "\n'\""` pipelines this replaces delete every newline, quote and
// apostrophe ANYWHERE in the value — a value legitimately containing one comes
// back silently corrupted — so "no trailing newline" is not cosmetic: it is
// what lets the caller drop the cleanup entirely.
func TestRenderRawIsTheBareScalar(t *testing.T) {
	for _, tc := range []struct {
		name string
		val  any
		want string
	}{
		{"string is unquoted", "workspace-sbd", "workspace-sbd"},
		// An integer must print as an integer. JSON has one number type, so
		// KCL's `disk_size_gb = 200` decodes to float64(200); printing Go's
		// default "200" vs "2e+02" is the difference between a working
		// `gcloud --boot-disk-size` and a broken one.
		{"integral number keeps integer form", float64(200), "200"},
		{"non-integral number", float64(1.5), "1.5"},
		{"bool", true, "true"},
		// None is an EMPTY capture, not the literal text "null", which would
		// look present to every caller that tested for emptiness.
		{"None is empty", nil, ""},
		// A string that WOULD need quoting in YAML is exactly the case the
		// cleanup pipelines existed for.
		{"a yaml-ambiguous string is still bare", "0755", "0755"},
		{"a string with a quote survives", `it's "fine"`, `it's "fine"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := kcleval.Render(kcleval.Result{Value: tc.val, Scalar: true}, kcleval.FormatRaw)
			if err != nil {
				t.Fatal(err)
			}
			if string(out) != tc.want {
				t.Errorf("raw = %q, want %q", out, tc.want)
			}
			if strings.HasSuffix(string(out), "\n") {
				t.Errorf("raw = %q; it must not end in a newline", out)
			}
		})
	}
}

// TestRenderRawRefusesAComposite: raw's consumer interpolates the result into
// a command line. Printing `{"a":1}` there would interpolate cleanly and mean
// nothing, and the failure would surface as whatever that command did with a
// JSON blob — so the refusal happens at the boundary, naming the fix.
func TestRenderRawRefusesAComposite(t *testing.T) {
	for _, val := range []any{
		map[string]any{"a": float64(1)},
		[]any{"small", "large"},
	} {
		_, err := kcleval.Render(kcleval.Result{Value: val}, kcleval.FormatRaw)
		if err == nil {
			t.Fatalf("--format raw of %T succeeded; want a refusal", val)
		}
		if !strings.Contains(err.Error(), "--format json") {
			t.Errorf("refusal should name the format that does work:\n%v", err)
		}
	}
}

// TestRenderJSONAndYAML: the composite formats round-trip a document, and JSON
// ends in exactly one newline so a terminal prompt lands on its own line.
func TestRenderJSONAndYAML(t *testing.T) {
	val := map[string]any{"name": "cloudnative-pg", "replicas": float64(3)}

	out, err := kcleval.Render(kcleval.Result{Value: val}, kcleval.FormatJSON)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"name": "cloudnative-pg"`) {
		t.Errorf("json = %s", out)
	}
	if !strings.HasSuffix(string(out), "\n") || strings.HasSuffix(string(out), "\n\n") {
		t.Errorf("json must end in exactly one newline, got %q", out)
	}

	out, err = kcleval.Render(kcleval.Result{Value: val}, kcleval.FormatYAML)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "name: cloudnative-pg") {
		t.Errorf("yaml = %s", out)
	}
}

// TestRenderDefaultsToJSON: the zero Format is JSON, so a caller that forgot
// to set it gets the documented default rather than an error.
func TestRenderDefaultsToJSON(t *testing.T) {
	out, err := kcleval.Render(kcleval.Result{Value: "x", Scalar: true}, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != `"x"` {
		t.Errorf("default format = %q, want JSON", out)
	}
}

// TestRenderRejectsAnUnknownFormat names the accepted set, so a typo'd
// --format is one read away from fixed.
func TestRenderRejectsAnUnknownFormat(t *testing.T) {
	_, err := kcleval.Render(kcleval.Result{Value: "x", Scalar: true}, "toml")
	if err == nil || !strings.Contains(err.Error(), "raw") {
		t.Fatalf("unknown format = %v; want a refusal listing the accepted formats", err)
	}
}

package cli

import (
	"errors"
	"strings"
	"testing"
)

// TestClassifyKCLLoadError_ClosedSchemaIsFatal is the incident, as a unit.
//
// `Cannot add member 'registry' to schema 'ClusterTarget'` is what forge #322
// produced on every unmigrated project, and it is the error generate used to
// downgrade to a warning before exiting 0.
func TestClassifyKCLLoadError_ClosedSchemaIsFatal(t *testing.T) {
	err := errors.New("kpm run deploy/kcl/prod: EvaluationError\n" +
		"Cannot add member 'registry' to schema 'ClusterTarget'\n" +
		" --> deploy/kcl/prod/main.k:162:5")
	fatal, marker := classifyKCLLoadError(err)
	if !fatal {
		t.Fatalf("a closed-schema violation must fail generate, got optional (marker %q)", marker)
	}
}

// TestClassifyKCLLoadError_ToolchainAndIncompleteTreesStayOptional pins the
// other half. Failing generate on these would block work forge has no standing
// to block: the module graph is not fetched,
// or the project is simply mid-edit.
func TestClassifyKCLLoadError_ToolchainAndIncompleteTreesStayOptional(t *testing.T) {
	for _, tc := range []struct{ name, msg string }{
		{"module not fetched", "CannotFindModule: failed to load package: lib.stack"},
		{"env not authored yet", "kcl dir deploy/kcl/staging: no such file or directory"},
		{"registry unreachable", "dial tcp 140.82.121.4:443: i/o timeout"},
		{"unrecognised diagnostic", "some future kcl diagnostic nobody has seen"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if fatal, marker := classifyKCLLoadError(errors.New(tc.msg)); fatal {
				t.Errorf("must stay optional, was judged fatal on marker %q: %s", marker, tc.msg)
			}
		})
	}
}

// TestClassifyKCLLoadError_OptionalWinsTies: an unfetched module graph can
// produce a message that ALSO mentions an attribute. The honest reading is the
// module one — the attribute error is downstream of it — and blaming the
// project for its toolchain is the failure mode this ordering prevents.
func TestClassifyKCLLoadError_OptionalWinsTies(t *testing.T) {
	err := errors.New("CannotFindModule lib.stack\n  ... and: no attribute named 'cluster'")
	if fatal, _ := classifyKCLLoadError(err); fatal {
		t.Error("a module-resolution failure must not be reported as the project's KCL being wrong")
	}
}

// TestKCLLoadableRefusalNamesTheEnvAndError: the refusal has to carry the env
// and the compiler's own text. A generate that fails with "deploy KCL does not
// compile" and nothing else has moved the discovery, not prevented it.
func TestKCLLoadableRefusalNamesTheEnvAndError(t *testing.T) {
	got := indentLines("Cannot add member 'registry' to schema 'ClusterTarget'\n --> main.k:162:5", "      ")
	for _, want := range []string{"      Cannot add member", "      --> main.k:162:5"} {
		if !strings.Contains(got, want) {
			t.Errorf("indented error is missing %q:\n%s", want, got)
		}
	}
}

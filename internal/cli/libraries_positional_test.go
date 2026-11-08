package cli

// `forge project libraries` must ANSWER "what does this package export",
// and must stop offering its source directory as the way to find out.
//
// WHY THESE TESTS EXIST — and why they are not the same tests as
// libraries_signatures_test.go, which already covers signature rendering.
//
// The signature RENDERER shipped 2026-07-31. A dogfood run twelve days
// later spent 35.5 minutes across 89 turns grepping forge's own pkg/ for
// signatures that renderer would have printed — `pkg/crud/repo.go` 14
// times, hunting `UpdateMasked` specifically. Three units wrote nothing at
// all; one made 145 bash calls with zero edits. Not one of them passed
// --signatures.
//
// So the gap was never rendering. It was two things this file pins:
//
//  1. REACHABILITY. The capability sat behind a flag, and a flag is
//     discovered by reading --help — which nobody does for a command they
//     believe they already understand. `forge project libraries crud` is
//     what a person types when they want crud's API.
//
//  2. THE DEAD END WE POINTED AT. The output named `go doc <pkg>` as the
//     route to a full API. It is not one: `go doc` renders a struct or
//     interface as `struct{ ... }` and lists no methods, so `go doc
//     .../crud` never mentions Repo.UpdateMasked at all. An agent that
//     followed our advice literally got a page that did not contain its
//     answer, and greping the source was the next honest move. That is
//     pinned by TestGoDoc_PackageViewOmitsMethods below, against the real
//     toolchain, so the claim we print stays true.

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"
)

// TestLibraries_PositionalSelectorsPrintSignatures is the reachability
// guard: the form a caller reaches for WITHOUT reading --help must work,
// and must produce the same answer the flag does.
func TestLibraries_PositionalSelectorsPrintSignatures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		flag string
		args []string
	}{
		{"positional", "", []string{"crud"}},
		{"several positional", "", []string{"crud", "svcerr"}},
		{"comma-separated positional", "", []string{"crud,svcerr"}},
		{"legacy flag still works", "crud", nil},
		{"flag and positional compose", "svcerr", []string{"crud"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spec, err := buildLibrariesSpec(context.Background())
			if err != nil {
				t.Fatalf("buildLibrariesSpec: %v", err)
			}
			if err := attachSignatures(&spec, mergeSignatureSelectors(tc.flag, tc.args)); err != nil {
				t.Fatalf("attachSignatures: %v", err)
			}
			var buf bytes.Buffer
			if err := writeLibraries(&buf, spec, false); err != nil {
				t.Fatalf("writeLibraries: %v", err)
			}
			out := buf.String()

			// The exact signature 14 greps went looking for. Asserting on
			// the parameter list, not the name: a name alone is what
			// `go doc` already gave them, and it was not enough.
			if !strings.Contains(out, "UpdateMasked(ctx context.Context, db orm.Context, entity *M, fields []string, opts ...UpdateOption) error") {
				t.Errorf("no full UpdateMasked signature — the measured query is still unanswered:\n%s", out)
			}
		})
	}
}

// TestMergeSignatureSelectors keeps the two input forms composing rather
// than competing: scripts pass the flag, people type the argument, and a
// caller doing both must get the union rather than a silent winner.
func TestMergeSignatureSelectors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		flag string
		args []string
		want string
	}{
		{"nothing selects nothing", "", nil, ""},
		{"positional only", "", []string{"crud"}, "crud"},
		{"several positional", "", []string{"crud", "orm"}, "crud,orm"},
		{"flag only", "crud,orm", nil, "crud,orm"},
		{"union, flag first", "svcerr", []string{"crud"}, "svcerr,crud"},
		{"blank args are dropped", "", []string{"crud", "  ", "orm"}, "crud,orm"},
		{"whitespace is trimmed", "  svcerr ", []string{" crud "}, "svcerr,crud"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := mergeSignatureSelectors(tc.flag, tc.args); got != tc.want {
				t.Errorf("mergeSignatureSelectors(%q, %v) = %q, want %q", tc.flag, tc.args, got, tc.want)
			}
		})
	}
}

// TestWriteLibraries_DoesNotAdvertiseTheSourcePathAsTheWayIn pins the
// second half of the fix.
//
// The path stays — it is how a reader confirms WHICH forge/pkg this
// project builds against, and the divergence warning below it is
// meaningless without it. What must not survive is the framing: a bare
// "Source on this machine: <path>" above a package list answers "where is
// it" for a reader asking "what does it export", and the only way from
// that path to a signature is a grep.
func TestWriteLibraries_DoesNotAdvertiseTheSourcePathAsTheWayIn(t *testing.T) {
	t.Parallel()
	spec := LibrariesSpec{
		Module: forgePkgModule,
		Dir:    "/abs/path/to/pkg",
		Packages: []LibrarySpec{
			{Name: "crud", ImportPath: forgePkgModule + "/crud", Dir: "/abs/path/to/pkg/crud", Synopsis: "CRUD helpers."},
		},
	}
	var buf bytes.Buffer
	if err := writeLibraries(&buf, spec, false); err != nil {
		t.Fatalf("writeLibraries: %v", err)
	}
	out := buf.String()

	if strings.Contains(out, "Source on this machine") {
		t.Errorf("the source path is still framed as a place to go look:\n%s", out)
	}
	// Still present, still diagnostic — removing it would break the
	// go.work-vs-go.mod divergence warning that depends on it.
	if !strings.Contains(out, "/abs/path/to/pkg") {
		t.Errorf("the resolved directory was dropped entirely; it is real diagnostic info:\n%s", out)
	}
	// The reader must be handed the command in the same breath, or the
	// path is once again the only actionable thing on screen.
	if !strings.Contains(out, "forge project libraries <pkg>") {
		t.Errorf("the resolution line does not name the command that answers the real question:\n%s", out)
	}
}

// TestWriteLibraries_GuidancePrefersTheCommandOverGoDoc: the closing
// paragraph is what a reader acts on. It used to name `go doc <pkg>` as
// THE way to see a full API, which is a route that does not reach methods
// — so it has to lead with the command that does, and it has to say why.
func TestWriteLibraries_GuidancePrefersTheCommandOverGoDoc(t *testing.T) {
	t.Parallel()
	spec := LibrariesSpec{
		Module:   forgePkgModule,
		Dir:      "/abs/path/to/pkg",
		Packages: []LibrarySpec{{Name: "crud", ImportPath: forgePkgModule + "/crud", Dir: "/abs/path/to/pkg/crud", Synopsis: "CRUD helpers."}},
	}
	var buf bytes.Buffer
	if err := writeLibraries(&buf, spec, false); err != nil {
		t.Fatalf("writeLibraries: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "forge project libraries crud") {
		t.Errorf("the guidance does not show the positional form:\n%s", out)
	}
	// The old text told the reader to run `go doc .../svcerr` for "the full
	// API". Recommending it unqualified is the defect, because it silently
	// omits every method.
	for _, dead := range []string{
		"go doc " + forgePkgModule + "/svcerr\n",
		"go doc " + forgePkgModule + "/svcerr Wrap",
	} {
		if strings.Contains(out, dead) {
			t.Errorf("the output still prescribes %q, which cannot show methods:\n%s", dead, out)
		}
	}
	if !strings.Contains(out, "no methods") {
		t.Errorf("the output does not say WHY the command beats `go doc`; an unexplained\n"+
			"preference is one a reader overrides with the habit they already have:\n%s", out)
	}
}

// TestGoDoc_PackageViewOmitsMethods is the load-bearing external check.
//
// Everything above rests on a claim about a tool forge does not own: that
// `go doc <pkg>` cannot answer "what are this type's methods". forge now
// PRINTS that claim to users, so it must be verified against the real
// toolchain rather than asserted. If a future Go release starts listing
// methods in the package view, this fails and the guidance we ship should
// be revisited — which is exactly the signal worth having.
func TestGoDoc_PackageViewOmitsMethods(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain on PATH")
	}
	out, err := exec.CommandContext(context.Background(),
		"go", "doc", forgePkgModule+"/crud").CombinedOutput()
	if err != nil {
		t.Skipf("go doc unavailable here: %v\n%s", err, out)
	}
	text := string(out)
	if !strings.Contains(text, "Repo") {
		t.Fatalf("go doc output does not mention Repo at all; the fixture assumption is wrong:\n%s", text)
	}
	// The needle is the METHOD form — `func (r *Repo[M]) UpdateMasked(`
	// — not the bare name. go doc renders a package-level func flush
	// left and a method with its receiver, and the package now exports a
	// plain function whose name CONTAINS this one (ScopedUpdateMasked),
	// which a bare-substring test read as "methods are being shown".
	// Matching the receiver's closing paren is what keeps this checking
	// the claim forge actually prints.
	if strings.Contains(text, ") UpdateMasked(") {
		t.Errorf("`go doc %s/crud` now shows methods — forge's printed guidance says it does not,\n"+
			"so that guidance needs revisiting:\n%s", forgePkgModule, text)
	}
}

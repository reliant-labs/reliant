package cli

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// `forge scaffold <noun>`'s closing summary is the highest-traffic
// documentation forge has: it is read every single time the verb runs, by
// a human or an agent deciding what to open next. A wrong pointer there
// costs a turn on every invocation.
//
// `forge scaffold adapter blobstore` wrote SIX files and named ONE — and
// the one it named (contract.go) is not where the work is. The adapter's
// implementation, its injected HTTPClient and its health-check example are
// in adapter.go; the deliberate "fill me in" prompt is cache.go; the owned
// observability seam is observe_chain.go. An agent that trusts the summary
// edits contract.go, never opens the other two, and throws away the best
// guidance in the package. The scaffold also announced "Internal package
// 'blobstore' created" for a command invoked as `--type adapter`, which
// reads like a silently downgraded request — `forge scaffold package` is a
// DIFFERENT noun in the same command's help.
//
// Compare `forge scaffold worker`, which already lists what it touched.
//
// This test pins the fix from both sides: the summary names every file the
// scaffold actually wrote (discovered from disk, not from a hardcoded
// list, so a new template file cannot be silently omitted), and it points
// at the seam the user is meant to open next.

// scaffoldAdapterCapturingSummary runs `runPackageNew` with --type=adapter
// in a throwaway project and returns the printed summary plus the files
// that landed in the package directory.
func scaffoldAdapterCapturingSummary(t *testing.T, name string) (summary string, written []string) {
	t.Helper()

	dir := t.TempDir()
	orig, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(orig) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("forge.yaml", []byte(
		"name: testproject\nmodule_path: example.com/testproject\nversion: \"0.1.0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	markServiceProject(t, dir)
	writeAdapterConfigFixture(t, dir)

	cmd := &cobra.Command{Use: "new <name>", Args: cobra.ExactArgs(1), RunE: runPackageNew}
	cmd.Flags().String("kind", "", "")
	cmd.Flags().String("type", "adapter", "")

	summary = captureStdout(t, func() {
		if err := cmd.RunE(cmd, []string{name}); err != nil {
			t.Fatalf("runPackageNew(--type adapter): %v", err)
		}
	})

	entries, err := os.ReadDir(filepath.Join("internal", name))
	if err != nil {
		t.Fatalf("read scaffolded adapter dir: %v", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			written = append(written, e.Name())
		}
	}
	sort.Strings(written)
	return summary, written
}

// TestAdapterScaffoldSummary_NamesEveryFileItWrote is the core N1
// assertion. The expected set is DISCOVERED from the package directory
// rather than hardcoded, so adding a template to the adapter tree makes
// this test demand the summary mention it, instead of quietly passing.
func TestAdapterScaffoldSummary_NamesEveryFileItWrote(t *testing.T) {
	summary, written := scaffoldAdapterCapturingSummary(t, "blobstore")

	if len(written) < 2 {
		t.Fatalf("the adapter scaffold wrote %d file(s) (%v) — too few for this assertion to mean "+
			"anything; the scaffold itself has regressed", len(written), written)
	}

	var missing []string
	for _, f := range written {
		if !strings.Contains(summary, f) {
			missing = append(missing, f)
		}
	}
	if len(missing) > 0 {
		t.Errorf("`forge scaffold adapter` wrote %d file(s) and its summary names none of %v.\n"+
			"The summary is the most-read documentation forge has — an author who trusts it "+
			"never opens the files it omits, and the adapter scaffold's best guidance "+
			"(adapter.go's injected transport, cache.go's fill-me-in prompt, the "+
			"observe_chain.go seam) is exactly what goes unread.\n\nfiles written: %v\n\nsummary:\n%s",
			len(written), missing, written, summary)
	}
}

// TestAdapterScaffoldSummary_PointsAtTheImplementationSeam pins the second
// half: naming the files is not enough if the "Next" line still sends the
// reader to contract.go. The work is in adapter.go.
func TestAdapterScaffoldSummary_PointsAtTheImplementationSeam(t *testing.T) {
	summary, _ := scaffoldAdapterCapturingSummary(t, "blobstore")

	next := summary
	if idx := strings.Index(summary, "Next"); idx >= 0 {
		next = summary[idx:]
	}
	if !strings.Contains(next, "adapter.go") {
		t.Errorf("the adapter scaffold's next-step guidance does not mention adapter.go, which is "+
			"where the implementation, the injected HTTPClient and the health-check example "+
			"live. Pointing only at contract.go sends every reader to the wrong file:\n%s", summary)
	}
}

// TestAdapterScaffoldSummary_SaysAdapterNotInternalPackage pins the naming
// half. `forge scaffold package` is a different noun in the same command's
// help, so confirming an adapter request with "Internal package created"
// reads as a silently downgraded request and costs an `ls` to disprove.
func TestAdapterScaffoldSummary_SaysAdapterNotInternalPackage(t *testing.T) {
	summary, _ := scaffoldAdapterCapturingSummary(t, "blobstore")

	if !strings.Contains(strings.ToLower(summary), "adapter 'blobstore'") {
		t.Errorf("`forge scaffold adapter blobstore` does not confirm an ADAPTER by name. Since "+
			"`forge scaffold package` is a separate noun, answering an adapter request with "+
			"\"Internal package created\" reads like forge silently downgraded it:\n%s", summary)
	}
}

// The default (non-adapter) package scaffold must keep saying what IT is.
// The N1 fix is about the adapter path telling the truth, not about
// renaming every scaffold.
func TestPackageScaffoldSummary_StillSaysInternalPackage(t *testing.T) {
	dir := t.TempDir()
	orig, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(orig) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("forge.yaml", []byte(
		"name: testproject\nmodule_path: example.com/testproject\nversion: \"0.1.0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	markServiceProject(t, dir)

	cmd := newTestPackageNewCmd("")
	summary := captureStdout(t, func() {
		if err := cmd.RunE(cmd, []string{"cache"}); err != nil {
			t.Fatalf("runPackageNew: %v", err)
		}
	})

	if !strings.Contains(summary, "Internal package 'cache'") {
		t.Errorf("the default package scaffold no longer confirms an internal package:\n%s", summary)
	}
}

// The scaffold summary names the construction rule `forge lint --tracing`
// enforces at the end of a run, so it is learned at scaffold time.
func TestAdapterScaffoldSummary_NamesNewInstrumented(t *testing.T) {
	summary, written := scaffoldAdapterCapturingSummary(t, "blobstore")
	if !slices.Contains(written, "observe_chain.go") {
		t.Skipf("the adapter scaffold writes no observability seam (%v)", written)
	}
	if !strings.Contains(summary, "blobstore.NewInstrumented(") {
		t.Errorf("the summary does not tell the author to construct the package as blobstore.NewInstrumented:\n%s", summary)
	}
}

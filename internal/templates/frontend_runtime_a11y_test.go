// frontend_runtime_a11y_test.go — @reliantlabs/forge-web-runtime renders into
// every forge frontend, so an accessibility defect in it is one a project
// CANNOT fix.
//
// Born red on <Resource>'s loading skeleton, which emitted a rows × cols grid
// of table cells each holding nothing but a shimmer <div>:
//
//	<td key={c} className="px-4 py-3">
//	  <div className="h-4 w-full animate-pulse rounded bg-surface-muted" />
//	</td>
//
// Two defects, one visible and one not. The visible one:
// jsx-a11y/control-has-associated-label — which forge's own scaffolded
// eslint config sets to "error" — fires on every one of those cells, so a
// generated frontend failed `npm run lint` out of the box. The deeper one:
// the loading state announced as a real table of blank cells, five rows of
// nothing, while the error and empty states next to it correctly use a
// single colSpan row.
//
// The project cannot repair either. The runtime is delivered as a package
// (and, in older trees, as forge-generated files behind the drift gate), so
// an edit downstream is reverted by the next install or `forge generate`.
// The only fix available to a project is to stop linting the runtime, which
// is what control-plane did — it added "src/lib/runtime/**" to its eslint
// ignores, blinding the whole directory. That is the workaround this guard
// exists to make unnecessary.
//
// The gate is static rather than a real eslint run because it must hold in
// CI with no node toolchain. The live proof is the acceptance run recorded
// in the fix's report: a scaffolded frontend, no ignore entry, `npm run
// lint` clean.
package templates

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// emptyDecorativeCell matches a table cell whose entire content is a single
// self-closing element carrying no accessible name — the shape
// control-has-associated-label rejects. A cell holding {children}, text, or
// an aria-hidden decoration is not matched.
var emptyDecorativeCell = regexp.MustCompile(`(?s)<td\b[^>]*>\s*<(div|span)\b((?:[^>]|\n)*?)/>\s*</td>`)

// ariaHiddenOnTableElement matches aria-hidden on <tr>/<td>/<th>. jsx-a11y
// treats table elements as focusable, so this is
// no-aria-hidden-on-focusable — the trap a naive "just hide the shimmer"
// fix falls into.
var ariaHiddenOnTableElement = regexp.MustCompile(`<(tr|td|th)\b[^>]*\baria-hidden\b`)

// webRuntimeTSX returns every .tsx source file in the runtime package.
func webRuntimeTSX(t *testing.T) map[string]string {
	t.Helper()
	dir := filepath.Join(repoRoot(t), "web-runtime", "src")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	out := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tsx") {
			continue
		}
		if strings.HasSuffix(e.Name(), ".test.tsx") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		out[e.Name()] = string(raw)
	}
	if len(out) == 0 {
		t.Fatalf("%s: found no .tsx sources — this guard has gone blind", dir)
	}
	return out
}

// TestWebRuntimeHasNoUnlabelledTableCells is the gate on the defect itself.
func TestWebRuntimeHasNoUnlabelledTableCells(t *testing.T) {
	t.Parallel()

	for name, body := range webRuntimeTSX(t) {
		for _, m := range emptyDecorativeCell.FindAllString(body, -1) {
			// A decoration explicitly marked aria-hidden is fine — the
			// cell is then required to carry its own label, which the
			// sibling assertion below checks.
			if strings.Contains(m, "aria-hidden") {
				continue
			}
			t.Errorf("web-runtime/src/%s emits a table cell whose only content is an unlabelled "+
				"decoration:\n\n%s\n\n"+
				"jsx-a11y/control-has-associated-label is an ERROR in the eslint config forge "+
				"scaffolds, so this fails `npm run lint` in every generated frontend — and a project "+
				"cannot fix it, because this file ships from the package. Give the cell an sr-only "+
				"label and mark the decoration aria-hidden.", name, strings.TrimSpace(m))
		}

		if m := ariaHiddenOnTableElement.FindString(body); m != "" {
			t.Errorf("web-runtime/src/%s sets aria-hidden on a table element:\n\n  %s\n\n"+
				"jsx-a11y treats <tr>/<td>/<th> as focusable, so this is "+
				"no-aria-hidden-on-focusable — also an error under the scaffolded config. "+
				"Put aria-hidden on the decorative child instead.", name, strings.TrimSpace(m))
		}
	}
}

// TestResourceSkeletonMatchesTheOtherLadderRungs is the named regression.
//
// <Resource>'s tristate ladder has three non-data rungs — loading, error and
// empty. Error and empty were always ONE row with a colSpan cell; loading
// was a rows × cols grid of empty cells, which is both the a11y defect above
// and a lie about the table's structure while the first page loads.
func TestResourceSkeletonMatchesTheOtherLadderRungs(t *testing.T) {
	t.Parallel()

	body := webRuntimeTSX(t)["resource.tsx"]
	if body == "" {
		t.Fatal("web-runtime/src/resource.tsx not found — this guard has gone blind")
	}

	start := strings.Index(body, "function SkeletonRows")
	if start < 0 {
		t.Fatal("resource.tsx no longer defines SkeletonRows — retarget this guard")
	}
	skeleton := body[start:]
	if end := strings.Index(skeleton, "\nfunction "); end > 0 {
		skeleton = skeleton[:end]
	}

	if !strings.Contains(skeleton, "colSpan") {
		t.Errorf("SkeletonRows does not span the table with a colSpan cell the way the error and "+
			"empty rungs do:\n%s", skeleton)
	}
	if !strings.Contains(skeleton, "sr-only") {
		t.Errorf("SkeletonRows announces no loading state to assistive tech "+
			"(expected an sr-only label):\n%s", skeleton)
	}
	if !strings.Contains(skeleton, `aria-hidden="true"`) {
		t.Errorf("SkeletonRows does not mark its shimmer as decoration, so the placeholder "+
			"geometry is announced as content:\n%s", skeleton)
	}
	// One label for the whole state, not one per placeholder cell.
	if n := strings.Count(skeleton, "sr-only"); n != 1 {
		t.Errorf("SkeletonRows carries %d sr-only labels, want exactly 1 — a label per placeholder "+
			"makes a screen reader say \"Loading\" once per cell:\n%s", n, skeleton)
	}
}

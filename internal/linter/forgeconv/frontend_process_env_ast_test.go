package forgeconv

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/linter/finding"
)

// The two holes the textual scan documented in its own header, plus the
// false-positive baseline that must survive closing them.
//
// The original implementation matched `process.env.X` as TEXT, and its
// header was honest about what that costs:
//
//	"An aliased read (`const e = process.env; e.FOO`) is not inlined by
//	 either bundler and is already broken in a browser bundle for a
//	 different reason."
//
//	"Template literals are treated as strings; a `${process.env.X}`
//	 interpolation inside one is therefore missed, which is the safe
//	 direction (a miss is silence, a false hit is noise)."
//
// The first claim is wrong on the facts, and the corpus proves it: the
// control-plane OIDC provider aliases `process.env` to a module-level
// `env` object and reads config off it. Both bundlers DO inline those
// reads — esbuild and webpack substitute `process.env.FOO` wherever the
// member expression appears, including through a `const e = process.env`
// binding they can see is never reassigned. Even where they do not, an
// aliased read is still a config value bypassing the typed module, which
// is what the rule is about.
//
// The second is a real miss with a real shape: `${process.env.API_URL}`
// inside a template literal is the single most natural way to build a URL,
// and it was invisible.

// TestProcessEnv_AliasedReadIsCaught closes hole #1.
func TestProcessEnv_AliasedReadIsCaught(t *testing.T) {
	t.Parallel()
	root, feDir := newFrontendWithConfig(t)
	writeFE(t, feDir, "src/lib/aliased.ts", `
const e = process.env;
export const issuer = e.NEXT_PUBLIC_OIDC_ISSUER;
`)

	res := LintFrontendProcessEnv(root, []string{feDir}, finding.SeverityWarning)
	if len(res.Findings) != 1 {
		t.Fatalf("an aliased read must be reported; got %d findings:\n%s",
			len(res.Findings), res.FormatText())
	}
	f := res.Findings[0]
	if !strings.Contains(f.Message, "NEXT_PUBLIC_OIDC_ISSUER") {
		t.Errorf("finding must name the variable read through the alias; got %q", f.Message)
	}
	if f.Line != 3 {
		t.Errorf("line = %d, want 3 (the READ, not the alias binding)", f.Line)
	}
}

// TestProcessEnv_TemplateLiteralIsCaught closes hole #2.
func TestProcessEnv_TemplateLiteralIsCaught(t *testing.T) {
	t.Parallel()
	root, feDir := newFrontendWithConfig(t)
	writeFE(t, feDir, "src/lib/url.ts",
		"export const u = `${process.env.NEXT_PUBLIC_API_URL}/v1/items`;\n")

	res := LintFrontendProcessEnv(root, []string{feDir}, finding.SeverityWarning)
	if len(res.Findings) != 1 {
		t.Fatalf("a read inside a template-literal substitution must be reported; got %d:\n%s",
			len(res.Findings), res.FormatText())
	}
	if !strings.Contains(res.Findings[0].Message, "NEXT_PUBLIC_API_URL") {
		t.Errorf("finding must name the variable; got %q", res.Findings[0].Message)
	}
	if res.Findings[0].Line != 1 {
		t.Errorf("line = %d, want 1", res.Findings[0].Line)
	}
}

// TestProcessEnv_TemplateLiteralTextIsStillProse is the other side of hole
// #2, and the reason closing it needs a parser rather than a wider regex.
//
// The literal TEXT of a template string is still a string — prose that
// mentions process.env in a template literal must stay silent. Only the
// ${...} SUBSTITUTIONS are code. A regex that simply stopped skipping
// template literals would flag both.
func TestProcessEnv_TemplateLiteralTextIsStillProse(t *testing.T) {
	t.Parallel()
	root, feDir := newFrontendWithConfig(t)
	writeFE(t, feDir, "src/lib/msg.ts",
		"export const help = `set process.env.NEXT_PUBLIC_API_URL before boot`;\n")

	res := LintFrontendProcessEnv(root, []string{feDir}, finding.SeverityWarning)
	if len(res.Findings) != 0 {
		t.Fatalf("template-literal TEXT is a string, not a read; got %d findings:\n%s",
			len(res.Findings), res.FormatText())
	}
}

// TestProcessEnv_AliasRespectsShadowing is what makes the alias tracking
// trustworthy rather than another textual guess.
//
// An identifier named `env` that is NOT bound to process.env must not turn
// every property read on it into a finding. This is the false-positive risk
// the alias feature introduces, so it is pinned before the feature is
// trusted: control-plane's settings-web has an `env.ts` module exporting a
// plain object literal named `env`, and reads off it are correct code.
func TestProcessEnv_AliasRespectsShadowing(t *testing.T) {
	t.Parallel()
	root, feDir := newFrontendWithConfig(t)
	writeFE(t, feDir, "src/lib/notenv.ts", `
const env = { API_URL: "https://example.test" };
export const u = env.API_URL;

function scoped() {
  const e = { OTHER: 1 };
  return e.OTHER;
}
export const s = scoped();
`)

	res := LintFrontendProcessEnv(root, []string{feDir}, finding.SeverityWarning)
	if len(res.Findings) != 0 {
		t.Fatalf("an identifier that is not bound to process.env must not be tracked; got %d:\n%s",
			len(res.Findings), res.FormatText())
	}
}

// TestProcessEnv_TSXParses is the corpus-shaped regression.
//
// The check has to read .tsx: JSX plus TypeScript is what most frontend
// files ARE. Measured on the control-plane corpus, a JS-grammar parser
// fails 63% of files and a bare JS lexer fails 40% (all .tsx) — so a naive
// "just use an AST" would have silently stopped checking most of the
// codebase. Anything that regresses TSX handling shows up here.
func TestProcessEnv_TSXParses(t *testing.T) {
	t.Parallel()
	root, feDir := newFrontendWithConfig(t)
	writeFE(t, feDir, "src/components/widget.tsx", `
type Props = { label: string };

export function Widget({ label }: Props) {
  const url = process.env.NEXT_PUBLIC_WIDGET_URL;
  const dash = "—";
  const re = /\s+/;
  return <div className="p-2">{label}{dash}{url}{String(re)}</div>;
}
`)

	res := LintFrontendProcessEnv(root, []string{feDir}, finding.SeverityWarning)
	if len(res.Findings) != 1 {
		t.Fatalf("a read in a .tsx file with JSX, a regex literal and unicode text must be "+
			"reported exactly once; got %d:\n%s", len(res.Findings), res.FormatText())
	}
	if !strings.Contains(res.Findings[0].Message, "NEXT_PUBLIC_WIDGET_URL") {
		t.Errorf("finding must name the variable; got %q", res.Findings[0].Message)
	}
	if res.Findings[0].Line != 5 {
		t.Errorf("line = %d, want 5 — line numbers must survive whatever normalisation the "+
			"parser needs, or every finding points at the wrong place",
			res.Findings[0].Line)
	}
}

// TestProcessEnv_UnparseableFileFallsBack pins the failure posture.
//
// A file the parser cannot read must not silently stop being checked — that
// would turn a syntax error into a hole in the guardrail. Falling back to
// the textual scan keeps the weaker check rather than no check.
func TestProcessEnv_UnparseableFileFallsBack(t *testing.T) {
	t.Parallel()
	root, feDir := newFrontendWithConfig(t)
	writeFE(t, feDir, "src/lib/broken.ts",
		"const a = process.env.NEXT_PUBLIC_BROKEN;\nfunction ( { { unparseable !!!\n")

	res := LintFrontendProcessEnv(root, []string{feDir}, finding.SeverityWarning)
	if len(res.Findings) == 0 {
		t.Fatal("a file that fails to parse must still be scanned textually, or a syntax " +
			"error becomes a way to hide a raw env read")
	}
	if !strings.Contains(res.Findings[0].Message, "NEXT_PUBLIC_BROKEN") {
		t.Errorf("fallback finding must name the variable; got %q", res.Findings[0].Message)
	}
}

// frontend_design_contract_test.go — keeps the frontend scaffold and the
// `frontend/design` skill telling a unit the SAME story.
//
// Every guard here was born red on a hand-run of the scaffold phase, where
// the scaffold and its own guidance disagreed and the unit paid for it:
//
//   - stylelint's `lightness-notation` autofix rewrote `oklch(0.58 0.09 196)`
//     to `oklch(58.% 0.09 196deg)` — `58.` is not a valid CSS <number>, so
//     the browser drops the declaration. --fix exited 0 and a re-lint passed,
//     so nothing caught it. Fixed upstream in stylelint 17.2.0; the floor in
//     package.json is what keeps it fixed.
//   - the design skill's own oklch exemplar was the spelling the scaffold's
//     stylelint rejects, costing a run its only genuine retry.
//   - the design skill blocklisted Inter while the scaffold loaded Inter.
//   - globals.css forbade raw palette colors while layout.tsx used
//     `bg-amber-400`.
//
// These are cheap string-level guards on the templates. The heavier
// invariant — that the scaffolded lint script's autofix emits CSS that
// still parses — needs a real `npm install`, so it lives in
// internal/cli/scaffold_lint_clean_e2e_test.go behind the `e2e` build tag.
package templates

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// designSkillPath is the skill whose claims must match the scaffold.
const designSkillPath = "skills/forge/frontend/design/SKILL.md"

// minStylelintMajor/Minor is the first stylelint release whose
// `lightness-notation` autofix trims with /\.?0+$/ instead of /0+$/ and so
// can no longer emit a dangling decimal point. Anything below it silently
// corrupts oklch lightnesses ending .07 .14 .28 .29 .55 .56 .57 or .58.
const (
	minStylelintMajor = 17
	minStylelintMinor = 2
)

// minConfigStandardMajor is the first stylelint-config-standard whose peer
// range admits stylelint 17.
const minConfigStandardMajor = 40

// nextJSPackageJSON renders the scaffolded package.json into a parsed map.
func nextJSPackageJSON(t *testing.T) map[string]any {
	t.Helper()
	content, err := FrontendTemplates().Render(
		filepath.Join("nextjs", "package.json.tmpl"),
		FrontendTemplateData{FrontendName: "dashboard", ProjectName: "testproject"},
	)
	if err != nil {
		t.Fatalf("render nextjs/package.json.tmpl: %v", err)
	}
	var pkg map[string]any
	if err := json.Unmarshal(content, &pkg); err != nil {
		t.Fatalf("scaffolded package.json is not valid JSON: %v\n%s", err, content)
	}
	return pkg
}

// semverFloor parses the minimum version a caret/tilde/bare range admits.
func semverFloor(t *testing.T, dep, rng string) (major, minor int) {
	t.Helper()
	m := regexp.MustCompile(`(\d+)\.(\d+)\.`).FindStringSubmatch(rng)
	if m == nil {
		t.Fatalf("cannot read a version floor out of %s range %q", dep, rng)
	}
	major, _ = strconv.Atoi(m[1])
	minor, _ = strconv.Atoi(m[2])
	return major, minor
}

func devDep(t *testing.T, pkg map[string]any, name string) string {
	t.Helper()
	deps, ok := pkg["devDependencies"].(map[string]any)
	if !ok {
		t.Fatalf("scaffolded package.json has no devDependencies object")
	}
	rng, ok := deps[name].(string)
	if !ok {
		t.Fatalf("scaffolded package.json does not depend on %s; the CSS lint lane cannot run", name)
	}
	return rng
}

// TestNextJSStylelintFloorClearsLightnessAutofixBug pins the stylelint floor
// to the release that fixed the corrupting autofix. Lowering it re-arms a
// silent CSS-corruption bug: `npx stylelint --fix` would again turn
// `oklch(0.58 0.09 196)` into `oklch(58.% 0.09 196deg)`, exit 0, and pass a
// subsequent non-fix run — so neither the user nor `forge lint` sees it,
// while the browser drops the declaration and the token resolves to nothing.
func TestNextJSStylelintFloorClearsLightnessAutofixBug(t *testing.T) {
	t.Parallel()
	pkg := nextJSPackageJSON(t)

	rng := devDep(t, pkg, "stylelint")
	major, minor := semverFloor(t, "stylelint", rng)
	if major < minStylelintMajor || (major == minStylelintMajor && minor < minStylelintMinor) {
		t.Errorf("stylelint floor %q admits < %d.%d.0, whose `lightness-notation` autofix emits an invalid CSS number "+
			"(oklch(0.58 …) → oklch(58.%% …)) and then passes its own re-lint; raise the floor",
			rng, minStylelintMajor, minStylelintMinor)
	}

	cfgRange := devDep(t, pkg, "stylelint-config-standard")
	cfgMajor, _ := semverFloor(t, "stylelint-config-standard", cfgRange)
	if cfgMajor < minConfigStandardMajor {
		t.Errorf("stylelint-config-standard floor %q is below v%d, whose peer range is the one that admits stylelint %d.x — "+
			"npm would resolve the config back onto the stylelint major carrying the autofix bug",
			cfgRange, minConfigStandardMajor, minStylelintMajor)
	}
}

// TestNextJSLintStylesScriptsSplitCheckFromFix asserts the gating script
// stays read-only and the autofix gets its own named script.
//
// `forge lint` runs `npm run lint:styles`; if that script carried --fix the
// gate would rewrite the user's CSS on every lint. Conversely, stylelint's
// own output advertises "errors potentially fixable with the --fix option",
// so a user WILL reach for a fix command — `lint:styles:fix` makes the one
// they reach for run through this frontend's pinned stylelint and config.
func TestNextJSLintStylesScriptsSplitCheckFromFix(t *testing.T) {
	t.Parallel()
	pkg := nextJSPackageJSON(t)

	scripts, ok := pkg["scripts"].(map[string]any)
	if !ok {
		t.Fatalf("scaffolded package.json has no scripts object")
	}

	check, _ := scripts["lint:styles"].(string)
	if check == "" {
		t.Fatalf("scaffolded package.json must define lint:styles — `forge lint` runs it as the CSS gate")
	}
	if strings.Contains(check, "--fix") {
		t.Errorf("lint:styles must not carry --fix: `forge lint` runs it as a gate and would rewrite the user's CSS; got %q", check)
	}

	fix, _ := scripts["lint:styles:fix"].(string)
	if fix == "" {
		t.Fatalf("scaffolded package.json must define lint:styles:fix — without a sanctioned fix script users run a bare `npx stylelint --fix`")
	}
	if !strings.Contains(fix, "--fix") {
		t.Errorf("lint:styles:fix must pass --fix; got %q", fix)
	}
}

// oklchLiteral matches an oklch() call and captures its first two arguments
// (lightness and — after chroma — hue).
var oklchLiteral = regexp.MustCompile(`oklch\(\s*([^\s)]+)\s+([^\s)]+)\s+([^\s)]+)\s*\)`)

// passingLightness is the notation stylelint-config-standard's
// `lightness-notation` accepts: a percentage.
var passingLightness = regexp.MustCompile(`^\d+(\.\d+)?%$`)

// passingHue is the notation `hue-degree-notation` accepts: an angle.
var passingHue = regexp.MustCompile(`^\d+(\.\d+)?deg$`)

// TestScaffoldedCSSOklchValuesPassTheScaffoldsOwnLint walks every oklch
// VALUE the scaffold emits (comment prose excluded — the header comments
// deliberately quote the rejected form as a counter-example) and requires
// the notation the scaffold's own stylelint accepts.
func TestScaffoldedCSSOklchValuesPassTheScaffoldsOwnLint(t *testing.T) {
	t.Parallel()

	for _, rel := range []string{
		filepath.Join("nextjs", "src", "app", "globals.css"),
		filepath.Join("vite-spa", "src", "index.css"),
	} {
		content, err := FrontendTemplates().Render(rel, nil)
		if err != nil {
			t.Fatalf("render %s: %v", rel, err)
		}
		for i, line := range strings.Split(string(content), "\n") {
			trimmed := strings.TrimSpace(line)
			// Skip comment prose: the header block quotes the rejected
			// spelling on purpose so the reader learns which one is which.
			if strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "/*") {
				continue
			}
			for _, m := range oklchLiteral.FindAllStringSubmatch(line, -1) {
				if !passingLightness.MatchString(m[1]) {
					t.Errorf("%s:%d: oklch lightness %q is red under `lightness-notation` — write a percentage (72%%), not a number:\n  %s",
						rel, i+1, m[1], trimmed)
				}
				if !passingHue.MatchString(m[3]) {
					t.Errorf("%s:%d: oklch hue %q is red under `hue-degree-notation` — write an angle (250deg), not a number:\n  %s",
						rel, i+1, m[3], trimmed)
				}
			}
		}
	}
}

// TestDesignSkillOklchExemplarPassesScaffoldLint is the direct guard on the
// contradiction that cost a scaffold run its only genuine retry: the skill
// told the unit to write `oklch(0.72 0.12 250)`, and the scaffold's stylelint
// answered with 162 lightness-notation / hue-degree-notation errors.
//
// Rule: the skill may only show the rejected spelling as a labelled
// counter-example, i.e. never on a line that does not also carry the
// accepted spelling.
func TestDesignSkillOklchExemplarPassesScaffoldLint(t *testing.T) {
	t.Parallel()

	skill, err := ProjectTemplates().Get(designSkillPath)
	if err != nil {
		t.Fatalf("read %s: %v", designSkillPath, err)
	}

	var sawAccepted bool
	for i, line := range strings.Split(string(skill), "\n") {
		matches := oklchLiteral.FindAllStringSubmatch(line, -1)
		if len(matches) == 0 {
			continue
		}
		var accepted, rejected []string
		for _, m := range matches {
			if passingLightness.MatchString(m[1]) && passingHue.MatchString(m[3]) {
				accepted = append(accepted, m[0])
				continue
			}
			rejected = append(rejected, m[0])
		}
		if len(accepted) > 0 {
			sawAccepted = true
		}
		if len(rejected) > 0 && len(accepted) == 0 {
			t.Errorf("%s:%d: shows %s with nothing to correct it. That spelling is valid CSS but red under the scaffold's "+
				"stylelint-config-standard; a unit that copies it burns a retry. Show `oklch(72%% 0.12 250deg)` on the same line, "+
				"or drop the counter-example:\n  %s",
				designSkillPath, i+1, strings.Join(rejected, ", "), strings.TrimSpace(line))
		}
	}
	if !sawAccepted {
		t.Errorf("%s no longer shows an oklch exemplar in the notation the scaffold's stylelint accepts "+
			"(percentage lightness + deg hue) — the skill has stopped teaching the form that passes", designSkillPath)
	}
}

// scaffoldedGoogleFont extracts the font family the Next.js scaffold loads,
// e.g. `import { Inter } from "next/font/google";` → "Inter".
var scaffoldedGoogleFont = regexp.MustCompile(`import\s+\{\s*(\w+)\s*\}\s+from\s+"next/font/google"`)

// TestDesignSkillExemptsTheScaffoldedFont resolves the contradiction where
// the scaffold loaded Inter and the design skill listed Inter among the
// fonts that "scream 'model picked this'". A unit that reads the blocklist
// first churns on replacing a font forge itself chose.
//
// The scaffold keeps Inter — it is the neutral default an app with no brief
// yet is owed, self-hosted by next/font — so the SKILL carries the
// exemption, and it must name the token a rebrand actually moves.
func TestDesignSkillExemptsTheScaffoldedFont(t *testing.T) {
	t.Parallel()

	layout, err := FrontendTemplates().Render(
		filepath.Join("nextjs", "src", "app", "layout.tsx.tmpl"),
		FrontendTemplateData{FrontendName: "dashboard", ProjectName: "testproject"},
	)
	if err != nil {
		t.Fatalf("render nextjs/src/app/layout.tsx.tmpl: %v", err)
	}
	m := scaffoldedGoogleFont.FindStringSubmatch(string(layout))
	if m == nil {
		t.Skip("the Next.js scaffold no longer loads a next/font/google family — nothing for the skill to exempt")
	}
	font := m[1]

	skill, err := ProjectTemplates().Get(designSkillPath)
	if err != nil {
		t.Fatalf("read %s: %v", designSkillPath, err)
	}

	// Find the bullet that blocklists AI-default fonts.
	var bullet string
	for _, line := range strings.Split(string(skill), "\n") {
		if strings.Contains(line, "Avoid the AI defaults") {
			bullet = line
			break
		}
	}
	if bullet == "" {
		t.Skipf("%s no longer blocklists default fonts — no contradiction to guard", designSkillPath)
	}
	if !strings.Contains(bullet, font) {
		return // The scaffold's font is not on the blocklist; nothing to reconcile.
	}

	for _, want := range []string{"scaffold", "--font-sans"} {
		if !strings.Contains(bullet, want) {
			t.Errorf("%s blocklists %s, which is exactly the font the Next.js scaffold loads, but the bullet does not mention %q. "+
				"Leaving both claims standing makes a unit churn on replacing forge's own choice — the bullet must exempt the "+
				"scaffolded font and name the token a rebrand moves:\n  %s",
				designSkillPath, font, want, strings.TrimSpace(bullet))
		}
	}
}

// rawPaletteClass matches a Tailwind utility built on a RAW palette color
// (`bg-amber-400`, `text-gray-900`, `bg-white`) as opposed to one of the
// semantic tokens globals.css declares (`bg-surface`, `text-on-warning`).
var rawPaletteClass = regexp.MustCompile(
	`\b(?:bg|text|border|ring|fill|stroke|divide|outline|decoration|shadow|accent|caret|from|via|to)-` +
		`(?:white|black|(?:slate|gray|grey|zinc|neutral|stone|red|orange|amber|yellow|lime|green|emerald|teal|cyan|sky|blue|indigo|violet|purple|fuchsia|pink|rose)-\d{2,3})\b`)

// TestScaffoldedShellsObeyTheirOwnTokenRule holds the app shells to the rule
// their own stylesheet states: semantic utilities only, "never a raw palette
// color". The shell is the one file every page inherits, so a raw color
// there is the one a rebrand silently misses — and it is the example every
// unit reads before writing its first component.
func TestScaffoldedShellsObeyTheirOwnTokenRule(t *testing.T) {
	t.Parallel()

	data := FrontendTemplateData{FrontendName: "dashboard", ProjectName: "testproject"}
	for _, pair := range []struct{ css, shell string }{
		{
			css:   filepath.Join("nextjs", "src", "app", "globals.css"),
			shell: filepath.Join("nextjs", "src", "app", "layout.tsx.tmpl"),
		},
		{
			css:   filepath.Join("vite-spa", "src", "index.css"),
			shell: filepath.Join("vite-spa", "src", "App.tsx.tmpl"),
		},
	} {
		css, err := FrontendTemplates().Render(pair.css, nil)
		if err != nil {
			t.Fatalf("render %s: %v", pair.css, err)
		}
		if !strings.Contains(string(css), "never a raw palette color") {
			t.Errorf("%s no longer states the raw-palette-color rule; either restore it or drop this guard — a rule the scaffold "+
				"states and then breaks is worse than no rule", pair.css)
			continue
		}

		shell, err := FrontendTemplates().Render(pair.shell, data)
		if err != nil {
			t.Fatalf("render %s: %v", pair.shell, err)
		}
		var offenders []string
		for i, line := range strings.Split(string(shell), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") {
				continue // prose may quote the forbidden shape
			}
			for _, hit := range rawPaletteClass.FindAllString(line, -1) {
				offenders = append(offenders, fmt.Sprintf("%s:%d: %s", pair.shell, i+1, hit))
			}
		}
		if len(offenders) > 0 {
			t.Errorf("%s forbids raw palette colors, but %s uses them:\n  %s\nUse the semantic tokens %s declares "+
				"(bg-surface / text-ink / bg-warning / text-on-warning …), or add the token the shell needs.",
				pair.css, pair.shell, strings.Join(offenders, "\n  "), pair.css)
		}
	}
}

// TestScaffoldedCSSTeachesTheOklchTrap keeps the globals.css header honest.
// It already warned about ONE stylelint trap (custom-property-empty-line-before)
// while the trap that actually fired on a real run — oklch notation — went
// unmentioned, in the very file where a unit adds new color tokens.
func TestScaffoldedCSSTeachesTheOklchTrap(t *testing.T) {
	t.Parallel()

	rel := filepath.Join("nextjs", "src", "app", "globals.css")
	content, err := FrontendTemplates().Render(rel, nil)
	if err != nil {
		t.Fatalf("render %s: %v", rel, err)
	}
	s := string(content)

	for _, want := range []string{
		"custom-property-empty-line-before", // the trap it already taught
		"lightness-notation",                // the trap that actually fired
		"hue-degree-notation",
		"oklch(72% 0.12 250deg)", // the spelling that passes
	} {
		if !strings.Contains(s, want) {
			t.Errorf("%s header comment does not mention %q — this is the file where new color tokens get written, "+
				"so every stylelint trap that gates it belongs here", rel, want)
		}
	}
}

// TestStylelintYieldsToPrettierOnCustomPropertyBlankLines pins the rule that
// a scaffolded project could not satisfy.
//
// stylelint-config-standard enables `custom-property-empty-line-before`,
// which demands a blank line before a custom property whose PRECEDING
// declaration spans multiple lines. Prettier is what decides which
// declarations wrap — it re-wraps any oklch(...) past the print width — and
// the scaffold ships and runs both tools. So they disagree about the same
// bytes, and the disagreement does not converge: on the scaffolded
// globals.css, `stylelint --fix` inserted three blank lines, prettier then
// re-wrapped a different set of values, and the next lint reported SIX
// violations instead of three.
//
// The observed cost was `forge project new smoke test` failing on a freshly
// scaffolded project with three errors on lines nobody wrote by hand:
//
//	src/app/globals.css
//	   96:3  ✖  Expected empty line before custom property
//	   98:3  ✖  Expected empty line before custom property
//	  111:3  ✖  Expected empty line before custom property
//
// A generated project failing the lint forge itself runs on it is the whole
// bug. Formatting inside @theme is prettier's job, so this is the rule that
// yields — and it must stay off, because turning it back on re-opens a fight
// that no amount of --fix settles.
func TestStylelintYieldsToPrettierOnCustomPropertyBlankLines(t *testing.T) {
	t.Parallel()

	content, err := FrontendTemplates().Render(
		filepath.Join("nextjs", "stylelint.config.mjs"), nil,
	)
	if err != nil {
		t.Fatalf("render nextjs/stylelint.config.mjs: %v", err)
	}

	// The config is JS, not JSON, so assert on the source. `: null` is the
	// spelling stylelint requires to disable an inherited rule — omitting the
	// key entirely leaves stylelint-config-standard's default in force, which
	// is exactly the state this test exists to prevent.
	if !strings.Contains(string(content), `"custom-property-empty-line-before": null`) {
		t.Errorf("nextjs/stylelint.config.mjs must disable `custom-property-empty-line-before` " +
			"(spelled `\"custom-property-empty-line-before\": null`). Without it, " +
			"stylelint-config-standard's default fights prettier over blank lines inside " +
			"@theme and a freshly scaffolded project fails its own `npm run lint:styles`.")
	}
}

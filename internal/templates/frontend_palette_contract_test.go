// frontend_palette_contract_test.go — the scaffolded color palette, held to
// the claims the scaffold makes about it.
//
// The CSS header comments in globals.css / index.css assert four things
// about every token: that the two frontend kinds declare the SAME token set,
// that colors are oklch in the notation stylelint accepts, that each color
// is inside the sRGB gamut, and that the text pairs clear WCAG AA. Each of
// those was a real defect before it was a test:
//
//   - The Vite scaffold declared 18 tokens while shared components — the
//     src/components/ui/** library and @reliantlabs/forge-web-runtime, which ship
//     BYTE-IDENTICAL into both trees — referenced 28. `bg-success-surface`
//     and friends resolved to nothing, so status badges in a Vite SPA
//     rendered as unstyled text while the same component looked correct
//     under Next.js.
//   - An oklch color outside the sRGB gamut is not rejected; the browser
//     silently clips it. Clipping compresses exactly the hue/lightness
//     relationships the palette is built on, so the failure is invisible in
//     code review and visible only as a palette that has gone muddy.
//
// The gamut and contrast math is duplicated here rather than pulled from a
// color library on purpose: it is ~60 lines, and a scaffold whose palette
// guarantee depends on a third-party dependency has a guarantee that lapses
// the first time that dependency is dropped.
package templates

import (
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/components"
)

// paletteFiles are the two stylesheets that must declare the same tokens.
var paletteFiles = map[string]string{
	"nextjs":   filepath.Join("nextjs", "src", "app", "globals.css"),
	"vite-spa": filepath.Join("vite-spa", "src", "index.css"),
}

// colorTokenDecl matches a `--color-<name>: <value>;` declaration.
var colorTokenDecl = regexp.MustCompile(`--color-([a-z-]+):\s*([^;]+);`)

// oklchValue matches `oklch(L% C Hdeg)` in the notation stylelint accepts.
var oklchValue = regexp.MustCompile(`^oklch\(\s*([\d.]+)%\s+([\d.]+)\s+([\d.]+)deg\s*\)$`)

// themeBlock isolates one palette block so light and dark can be checked
// independently: the `@theme` block (light) and each dark override block.
var (
	themeAtBlock  = regexp.MustCompile(`(?s)@theme\s*\{(.*?)\n\}`)
	darkRootBlock = regexp.MustCompile(`(?s):root\[data-theme="dark"\]\s*\{(.*?)\n\}`)
)

// ── color math ────────────────────────────────────────────────────────

type linRGB struct{ r, g, b float64 }

// oklchToLinearSRGB converts oklch to LINEAR-light sRGB (not yet gamma
// encoded), which is both what the gamut check needs and what WCAG's
// relative-luminance formula is defined over.
func oklchToLinearSRGB(lPct, c, hDeg float64) linRGB {
	l := lPct / 100
	h := hDeg * math.Pi / 180
	a := c * math.Cos(h)
	b := c * math.Sin(h)

	lp := l + 0.3963377774*a + 0.2158037573*b
	mp := l - 0.1055613458*a - 0.0638541728*b
	sp := l - 0.0894841775*a - 1.2914855480*b

	lc, mc, sc := lp*lp*lp, mp*mp*mp, sp*sp*sp

	return linRGB{
		r: 4.0767416621*lc - 3.3077115913*mc + 0.2309699292*sc,
		g: -1.2684380046*lc + 2.6097574011*mc - 0.3413193965*sc,
		b: -0.0041960863*lc - 0.7034186147*mc + 1.7076147010*sc,
	}
}

// encodeSRGB applies the sRGB transfer function to one linear channel.
func encodeSRGB(c float64) float64 {
	if c <= 0.0031308 {
		return 12.92 * c
	}
	return 1.055*math.Pow(c, 1/2.4) - 0.055
}

// inSRGBGamut reports whether every channel lands inside [0,1] once
// encoded. The epsilon absorbs float noise at the exact boundary.
func inSRGBGamut(c linRGB) bool {
	const eps = 0.002
	for _, v := range []float64{c.r, c.g, c.b} {
		e := encodeSRGB(v)
		if e < -eps || e > 1+eps {
			return false
		}
	}
	return true
}

func relativeLuminance(c linRGB) float64 {
	clamp := func(v float64) float64 { return math.Max(0, math.Min(1, v)) }
	return 0.2126*clamp(c.r) + 0.7152*clamp(c.g) + 0.0722*clamp(c.b)
}

func contrastRatio(a, b linRGB) float64 {
	la, lb := relativeLuminance(a), relativeLuminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// ── parsing ───────────────────────────────────────────────────────────

// parsePaletteBlock pulls the `--color-*` declarations out of one CSS block.
func parsePaletteBlock(t *testing.T, file, block string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, m := range colorTokenDecl.FindAllStringSubmatch(block, -1) {
		out[m[1]] = strings.TrimSpace(m[2])
	}
	if len(out) == 0 {
		t.Fatalf("%s: no --color-* declarations found; the palette regex or the file shape changed", file)
	}
	return out
}

func renderPalette(t *testing.T, kind string) string {
	t.Helper()
	content, err := FrontendTemplates().Render(paletteFiles[kind], FrontendTemplateData{
		FrontendName: "dashboard", ProjectName: "testproject", Platform: kind,
	})
	if err != nil {
		t.Fatalf("render %s: %v", paletteFiles[kind], err)
	}
	return string(content)
}

// sortedTokenNames is this file's own helper; the package already has a
// sortedKeys over map[string]bool for a different purpose.
func sortedTokenNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ── the guards ────────────────────────────────────────────────────────

// TestBothScaffoldsDeclareTheSameColorTokens is the one that would have
// caught the missing-token defect. The component library and the web
// runtime ship byte-identical into both trees, so a token declared in only
// one stylesheet is a component that renders unstyled in the other frontend.
func TestBothScaffoldsDeclareTheSameColorTokens(t *testing.T) {
	t.Parallel()

	sets := map[string]map[string]string{}
	for kind := range paletteFiles {
		css := renderPalette(t, kind)
		block := themeAtBlock.FindStringSubmatch(css)
		if block == nil {
			t.Fatalf("%s: no @theme block", paletteFiles[kind])
		}
		sets[kind] = parsePaletteBlock(t, paletteFiles[kind], block[1])
	}

	next, vite := sets["nextjs"], sets["vite-spa"]
	var missingFromVite, missingFromNext []string
	for _, k := range sortedTokenNames(next) {
		if _, ok := vite[k]; !ok {
			missingFromVite = append(missingFromVite, k)
		}
	}
	for _, k := range sortedTokenNames(vite) {
		if _, ok := next[k]; !ok {
			missingFromNext = append(missingFromNext, k)
		}
	}

	if len(missingFromVite) > 0 || len(missingFromNext) > 0 {
		t.Errorf("the two scaffolds declare different color tokens.\n"+
			"missing from %s: %v\nmissing from %s: %v\n\n"+
			"The src/components/ui/** library and @reliantlabs/forge-web-runtime ship BYTE-IDENTICAL into both "+
			"frontends, so a token only one stylesheet declares makes those shared components render with a "+
			"missing color in the other — silently, since an undefined custom property is not a CSS error. "+
			"Declare every token in both files.",
			paletteFiles["vite-spa"], missingFromVite,
			paletteFiles["nextjs"], missingFromNext)
	}
}

// paletteFamily matches a utility in one of the semantic color FAMILIES the
// palette owns, capturing the token name. Deliberately narrow: listing the
// families means a Tailwind built-in like `text-sm` or `border-2` cannot
// trip it. The capture runs to the end of the hyphenated word, so
// `bg-warning-hover` is read as `warning-hover` — never as `warning`
// followed by stray text, which is how an undeclared hover token once
// passed this check: the old pattern ended in `\b`, and the boundary
// between `g` and `-` satisfied it.
var paletteFamily = regexp.MustCompile(`\b(?:bg|text|border|ring|fill|stroke|divide|outline|from|to|via|accent|caret|decoration|placeholder|shadow)-` +
	`((?:surface|ink|accent|danger|success|warning|on)[a-z-]*)`)

// themeTokenSets returns each scaffold kind's declared `--color-*` names,
// read from its own `@theme` block.
func themeTokenSets(t *testing.T) map[string]map[string]bool {
	t.Helper()
	sets := map[string]map[string]bool{}
	for kind := range paletteFiles {
		css := renderPalette(t, kind)
		block := themeAtBlock.FindStringSubmatch(css)
		if block == nil {
			t.Fatalf("%s: no @theme block", paletteFiles[kind])
		}
		sets[kind] = map[string]bool{}
		for k := range parsePaletteBlock(t, paletteFiles[kind], block[1]) {
			sets[kind][k] = true
		}
	}
	return sets
}

// undeclaredPaletteTokens returns every palette utility in src whose token
// is not in declared, as "class (token)" strings, one per occurrence.
// Comment lines are skipped — prose may name a token that does not exist —
// and so is a name that ends in a hyphen, the head of an interpolated class
// (`bg-accent-${tone}`) whose full name is not in the source to check.
func undeclaredPaletteTokens(src string, declared map[string]bool) []string {
	var out []string
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "/*") {
			continue
		}
		for _, m := range paletteFamily.FindAllStringSubmatch(line, -1) {
			if strings.HasSuffix(m[1], "-") || declared[m[1]] {
				continue
			}
			out = append(out, m[0])
		}
	}
	return out
}

// TestEveryScaffoldedComponentTokenIsDeclared closes the loop from the other
// direction: a semantic utility used anywhere in the shipped frontend
// surface must resolve to a token EACH stylesheet declares.
//
// "Each", not "either": the frontend trees and the component library ship
// byte-identical into both kinds, so a token one theme lacks is a component
// that renders transparent in that kind of frontend. The shipped surface is
// both scaffold trees AND every pkg/components entry — `forge component
// install` copies those into a project verbatim, so a library component is
// as much a theme consumer as anything the scaffold writes. Missing the
// library is how `confirmation_dialog`'s `hover:bg-warning-hover` shipped
// against a token neither theme declared.
func TestEveryScaffoldedComponentTokenIsDeclared(t *testing.T) {
	t.Parallel()

	sets := themeTokenSets(t)

	type source struct{ rel, content string }
	var sources []source
	for _, kind := range []string{"nextjs", "vite-spa"} {
		files, err := ListFrontendTree(kind)
		if err != nil {
			t.Fatalf("list %s tree: %v", kind, err)
		}
		for _, f := range files {
			if !strings.HasSuffix(f.Rel, ".tsx") && !strings.HasSuffix(f.Rel, ".tsx.tmpl") {
				continue
			}
			content, err := FrontendTemplates().Render(f.Path, FrontendTemplateData{
				FrontendName: "dashboard", ProjectName: "testproject", Platform: kind,
			})
			if err != nil {
				// Page templates take a richer data shape; they are covered
				// by their own tests and are not palette-bearing shells.
				continue
			}
			sources = append(sources, source{kind + ":" + f.Rel, string(content)})
		}
	}
	lib := components.NewLibrary()
	for _, entry := range lib.Registry() {
		src, err := lib.Get(entry.Name)
		if err != nil {
			t.Fatalf("get component %s: %v", entry.Name, err)
		}
		sources = append(sources, source{"pkg/components/components/" + string(entry.Category) + "/" + entry.Name + ".tsx", src})
	}
	if len(sources) == 0 {
		t.Fatal("no sources scanned; the frontend tree or the component library moved")
	}

	for _, kind := range []string{"nextjs", "vite-spa"} {
		for _, s := range sources {
			for _, class := range undeclaredPaletteTokens(s.content, sets[kind]) {
				t.Errorf("%s uses `%s`, but %s declares no --color-%s.\n"+
					"Add the token to BOTH globals.css and index.css (light and both dark blocks), or use an existing one.",
					s.rel, class, paletteFiles[kind], class[strings.Index(class, "-")+1:])
			}
		}
	}
}

// TestPaletteTokenScanReadsWholeNames pins the scanner itself: a hover or
// state token whose BASE is declared is still undeclared.
func TestPaletteTokenScanReadsWholeNames(t *testing.T) {
	t.Parallel()
	declared := map[string]bool{"warning": true, "accent": true, "on-accent": true}
	src := `<button className="bg-warning hover:bg-warning-hover text-on-accent bg-accent/10 bg-accent-${tone}" />
// prose: bg-made-up-token is fine in a comment`
	got := undeclaredPaletteTokens(src, declared)
	if len(got) != 1 || got[0] != "bg-warning-hover" {
		t.Fatalf("undeclaredPaletteTokens = %v, want exactly [bg-warning-hover]", got)
	}
}

// TestScaffoldedPaletteIsInSRGBGamut keeps every color displayable. An
// out-of-gamut oklch value is not an error — the browser clips it, quietly
// flattening the hue and lightness relationships the palette is built on.
func TestScaffoldedPaletteIsInSRGBGamut(t *testing.T) {
	t.Parallel()

	for kind, rel := range paletteFiles {
		css := renderPalette(t, kind)
		for _, m := range colorTokenDecl.FindAllStringSubmatch(css, -1) {
			name, value := m[1], strings.TrimSpace(m[2])
			// Strip a trailing comment the regex may have swept in.
			if i := strings.Index(value, "/*"); i >= 0 {
				value = strings.TrimSpace(value[:i])
			}
			om := oklchValue.FindStringSubmatch(value)
			if om == nil {
				t.Errorf("%s: --color-%s is %q, which is not oklch in the notation this scaffold's stylelint "+
					"accepts (`oklch(72%% 0.12 250deg)` — percentage lightness, `deg` hue). The palette is "+
					"defined in oklch so that lightness and chroma can be held fixed across hues; a hex or rgb "+
					"value opts that token out of the system.", rel, name, value)
				continue
			}
			l, _ := strconv.ParseFloat(om[1], 64)
			c, _ := strconv.ParseFloat(om[2], 64)
			h, _ := strconv.ParseFloat(om[3], 64)
			if !inSRGBGamut(oklchToLinearSRGB(l, c, h)) {
				t.Errorf("%s: --color-%s = %s is outside the sRGB gamut. The browser will CLIP it rather than "+
					"reject it, which silently changes the color and flattens it toward its neighbours. "+
					"Reduce the chroma (%.3f) and keep the lightness and hue.", rel, name, value, c)
			}
		}
	}
}

// contrastPair is one foreground/background pair the scaffold actually
// renders, with the WCAG floor it must clear.
type contrastPair struct {
	fg, bg string
	min    float64
	why    string
}

// The pairs below are the ones the templates put on screen. 4.5:1 is WCAG AA
// for normal text; body text on the two page surfaces is held to AAA (7:1)
// because it is the text a user reads for minutes at a time.
var contrastPairs = []contrastPair{
	{"ink", "surface", 7.0, "primary body text on a card"},
	{"ink", "surface-muted", 7.0, "primary body text on the page background"},
	{"ink", "surface-sunken", 7.0, "primary body text in an inset"},
	{"surface", "ink", 7.0, "tooltip text on its inverted bubble (pkg/components tooltip.tsx)"},
	{"ink-muted", "surface", 4.5, "secondary text on a card"},
	{"ink-muted", "surface-muted", 4.5, "secondary text on the page background"},
	{"ink-muted", "surface-sunken", 4.5, "secondary text in an inset"},
	{"ink-subtle", "surface", 3.0, "placeholder / icon on a card"},
	{"ink-subtle", "surface-muted", 3.0, "placeholder / icon on the page background"},
	{"accent", "surface", 4.5, "a link or accent-colored label"},
	// The focus outline is the accent (WCAG 2.2 focus appearance, 3:1 against
	// whatever the focused control sits on).
	{"accent", "surface-muted", 3.0, "the focus outline on the page background"},
	{"accent", "surface-sunken", 3.0, "the focus outline in an inset"},
	{"on-accent", "accent", 4.5, "button label on the primary fill"},
	{"on-accent", "accent-hover", 4.5, "button label on a hovered / pressed primary fill"},
	{"accent-ink", "accent-surface", 4.5, "active tab / info badge text"},
	{"accent-ink", "surface", 4.5, "accent text outside a tint"},
	{"danger", "surface", 4.5, "inline error text"},
	{"on-danger", "danger", 4.5, "label on a destructive button"},
	{"on-danger", "danger-hover", 4.5, "label on a hovered / pressed destructive button"},
	{"danger-ink", "danger-surface", 4.5, "error banner text"},
	{"danger-ink", "surface", 4.5, "error text outside a tint"},
	{"success-ink", "success-surface", 4.5, "success badge text"},
	{"success-ink", "surface", 4.5, "success text outside a tint"},
	{"on-warning", "warning", 4.5, "the mock-mode banner"},
	{"on-warning", "warning-hover", 4.5, "label on a hovered warning button"},
	{"warning-ink", "warning-surface", 4.5, "warning badge text"},
	{"warning-ink", "surface", 4.5, "warning text outside a tint"},
}

// TestScaffoldedPaletteMeetsWCAGContrast checks the light and dark palettes
// independently — a pair that passes in light can fail in dark, and the dark
// block is the one nobody looks at.
func TestScaffoldedPaletteMeetsWCAGContrast(t *testing.T) {
	t.Parallel()

	for kind, rel := range paletteFiles {
		css := renderPalette(t, kind)

		blocks := map[string]*regexp.Regexp{
			"light": themeAtBlock,
			"dark":  darkRootBlock,
		}
		for mode, re := range blocks {
			m := re.FindStringSubmatch(css)
			if m == nil {
				t.Fatalf("%s: no %s palette block", rel, mode)
			}
			tokens := parsePaletteBlock(t, rel, m[1])

			resolve := func(name string) (linRGB, bool) {
				raw, ok := tokens[name]
				if !ok {
					return linRGB{}, false
				}
				if i := strings.Index(raw, "/*"); i >= 0 {
					raw = strings.TrimSpace(raw[:i])
				}
				om := oklchValue.FindStringSubmatch(raw)
				if om == nil {
					return linRGB{}, false
				}
				l, _ := strconv.ParseFloat(om[1], 64)
				c, _ := strconv.ParseFloat(om[2], 64)
				h, _ := strconv.ParseFloat(om[3], 64)
				return oklchToLinearSRGB(l, c, h), true
			}

			for _, p := range contrastPairs {
				fg, okF := resolve(p.fg)
				bg, okB := resolve(p.bg)
				if !okF || !okB {
					t.Errorf("%s (%s): cannot check %s-on-%s — a token is missing or not oklch", rel, mode, p.fg, p.bg)
					continue
				}
				got := contrastRatio(fg, bg)
				if got < p.min {
					t.Errorf("%s (%s palette): %s on %s is %.2f:1, below the %.1f:1 floor (%s).\n"+
						"Adjust the LIGHTNESS of one of the two tokens — chroma and hue barely move contrast, "+
						"so retuning those will not fix it.",
						rel, mode, p.fg, p.bg, got, p.min, p.why)
				}
			}
		}
	}
}

// geometryTokens are the non-color theme values. They are asserted for the
// same reason as the colors: `rounded-lg` and `shadow-sm` appear ~160 times
// across the scaffold, the component library and the runtime, so the ONLY
// way to restyle the app's geometry without touching every call site is for
// these to be theme tokens. A scaffold that hardcodes Tailwind's defaults
// leaves the unit editing 160 files to change a corner radius.
var geometryTokens = []string{
	"--radius-md", "--radius-lg", "--radius-xl",
	"--shadow-sm", "--shadow-md", "--shadow-lg",
}

// TestScaffoldedThemeTokenizesGeometry keeps radius and elevation
// overridable from the theme, and keeps the two frontends agreeing on them.
func TestScaffoldedThemeTokenizesGeometry(t *testing.T) {
	t.Parallel()

	values := map[string]map[string]string{}
	for kind, rel := range paletteFiles {
		css := renderPalette(t, kind)
		block := themeAtBlock.FindStringSubmatch(css)
		if block == nil {
			t.Fatalf("%s: no @theme block", rel)
		}
		found := map[string]string{}
		for _, name := range geometryTokens {
			re := regexp.MustCompile(regexp.QuoteMeta(name) + `:\s*([^;]+);`)
			m := re.FindStringSubmatch(block[1])
			if m == nil {
				t.Errorf("%s declares no %s in @theme. Tailwind builds `rounded-*` / `shadow-*` from these "+
					"variables, so without them every corner and elevation in the app is Tailwind's default and "+
					"can only be changed by editing each of the ~160 call sites.", rel, name)
				continue
			}
			// Whitespace-normalized: a line break between two shadows is
			// not geometry, and prettier wraps a long shadow list wherever
			// the print width falls.
			found[name] = strings.Join(strings.Fields(m[1]), " ")
		}
		values[kind] = found
	}

	// Both frontends share the component library byte-for-byte, so a radius
	// that differs between them makes the same card render differently.
	for _, name := range geometryTokens {
		next, vite := values["nextjs"][name], values["vite-spa"][name]
		if next != "" && vite != "" && next != vite {
			t.Errorf("%s differs between the two scaffolds (nextjs: %q, vite-spa: %q). The component library "+
				"ships identical bytes into both, so the same card would render with different geometry.",
				name, next, vite)
		}
	}

	// Shadows tinted with the ink hue rather than neutral black. A pure-black
	// blur over a hue-tinted surface reads as grey haze; it is the specific
	// tell this palette exists to avoid.
	for kind, rel := range paletteFiles {
		for _, name := range []string{"--shadow-sm", "--shadow-md", "--shadow-lg"} {
			v := values[kind][name]
			if v == "" {
				continue
			}
			if strings.Contains(v, "#000") || strings.Contains(v, "rgb(0") || strings.Contains(v, "rgba(0") {
				t.Errorf("%s: %s uses a neutral black shadow (%s). Tint it with the ink hue in oklch — "+
					"black blurred over a hue-tinted surface goes muddy.", rel, name, v)
			}
		}
	}
}

// TestNoRawHexInScaffoldedPalette holds the palette to the design skill's
// own rule. `#fff` and `#000` are called out specifically: a pure white
// surface beside a saturated accent is the single clearest tell of a
// palette nobody tuned, which is exactly the impression a scaffold should
// not leave.
func TestNoRawHexInScaffoldedPalette(t *testing.T) {
	t.Parallel()

	hex := regexp.MustCompile(`--color-[a-z-]+:\s*(#[0-9a-fA-F]{3,8})`)
	for kind, rel := range paletteFiles {
		css := renderPalette(t, kind)
		for _, m := range hex.FindAllStringSubmatch(css, -1) {
			t.Errorf("%s declares a color token as raw hex (%s). The palette is defined in oklch so "+
				"lightness and chroma stay comparable across hues, and so neutrals can carry a trace of the "+
				"accent's hue instead of being literally grey. Convert it: oklch(L%% C Hdeg).", rel, m[1])
		}
		_ = kind
	}
}

// TestThemeStorageKeyAgreesAcrossTheBootScripts pins the one string that is
// duplicated by necessity. The pre-paint script lives in the HTML shell and
// cannot import from the bundle, so the localStorage key exists in three
// places; if they drift, the boot script reads nothing, and every dark-mode
// user gets a white flash on every load — the exact bug the script exists to
// prevent, reintroduced silently.
func TestThemeStorageKeyAgreesAcrossTheBootScripts(t *testing.T) {
	t.Parallel()

	hook, err := FrontendTemplates().Render(
		filepath.Join("shared-web", "src", "lib", "theme", "use-theme.ts.tmpl"),
		FrontendTemplateData{Platform: "nextjs"},
	)
	if err != nil {
		t.Fatalf("render use-theme.ts.tmpl: %v", err)
	}
	keyDecl := regexp.MustCompile(`THEME_STORAGE_KEY\s*=\s*"([^"]+)"`).FindStringSubmatch(string(hook))
	if keyDecl == nil {
		t.Fatalf("use-theme.ts.tmpl no longer exports a THEME_STORAGE_KEY string literal")
	}
	key := keyDecl[1]

	shells := map[string]FrontendTemplateData{
		filepath.Join("nextjs", "src", "app", "layout.tsx.tmpl"): {
			FrontendName: "dashboard", ProjectName: "testproject", Platform: "nextjs",
		},
		filepath.Join("vite-spa", "index.html.tmpl"): {
			FrontendName: "dashboard", ProjectName: "testproject", Platform: "vite-spa",
		},
	}
	for rel, data := range shells {
		content, err := FrontendTemplates().Render(rel, data)
		if err != nil {
			t.Fatalf("render %s: %v", rel, err)
		}
		s := string(content)
		if !strings.Contains(s, "data-theme") {
			t.Errorf("%s has no pre-paint theme script. Without one the page paints the light palette and "+
				"swaps after hydration — a white flash on every load for every dark-mode user.", rel)
			continue
		}
		if !strings.Contains(s, fmt.Sprintf("%q", key)) {
			t.Errorf("%s's pre-paint script does not read the key %q that use-theme.ts writes. "+
				"The script cannot import from the bundle, so this string is duplicated by necessity — "+
				"when the copies drift the script reads nothing and the flash comes back.", rel, key)
		}
	}
}

// TestScaffoldedPaletteHasARecessThatRecessesInBothModes pins the elevation
// ladder the card primitive documents (components/ui/card.tsx):
//
//	page / card     surface
//	inset (a well)  surface-sunken — BELOW surface, in light AND dark
//
// `surface-muted` cannot serve as the inset. It is an interaction/subtle
// fill, and in the dark palette it sits ABOVE `surface` (lighter), so an
// inset painted with it recesses in light mode and LIFTS in dark — a well on
// one theme, a floating slab on the other. That exact inversion shipped in a
// downstream app (reliant's billing surfaces) before being measured, so the
// ordering is asserted rather than trusted to review.
func TestScaffoldedPaletteHasARecessThatRecessesInBothModes(t *testing.T) {
	t.Parallel()

	for kind, rel := range paletteFiles {
		css := renderPalette(t, kind)
		blocks := map[string]*regexp.Regexp{"light": themeAtBlock, "dark": darkRootBlock}
		for mode, re := range blocks {
			m := re.FindStringSubmatch(css)
			if m == nil {
				t.Fatalf("%s: no %s palette block", rel, mode)
			}
			tokens := parsePaletteBlock(t, rel, m[1])
			lightness := func(name string) (float64, bool) {
				raw, ok := tokens[name]
				if !ok {
					return 0, false
				}
				if i := strings.Index(raw, "/*"); i >= 0 {
					raw = strings.TrimSpace(raw[:i])
				}
				om := oklchValue.FindStringSubmatch(raw)
				if om == nil {
					return 0, false
				}
				l, _ := strconv.ParseFloat(om[1], 64)
				return l, true
			}
			surface, okS := lightness("surface")
			sunken, okK := lightness("surface-sunken")
			if !okS || !okK {
				t.Errorf("%s (%s): --color-surface-sunken must be declared as oklch — it is the one token an inset "+
					"may use, and it must recess below --color-surface in every palette", rel, mode)
				continue
			}
			if sunken >= surface {
				t.Errorf("%s (%s): --color-surface-sunken (L=%.1f%%) must be DARKER than --color-surface (L=%.1f%%). "+
					"An inset that is lighter than its card lifts instead of recessing.", rel, mode, sunken, surface)
			}
		}
	}
}

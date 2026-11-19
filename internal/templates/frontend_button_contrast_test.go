// frontend_button_contrast_test.go — the library Button, held to WCAG AA
// against the palettes forge scaffolds.
//
// The palette contract (frontend_palette_contract_test.go) checks token
// PAIRS. A button is not one pair: its neutral variants paint `ink` at a low
// alpha over whatever surface the button sits on, so a label's contrast
// depends on the composite, and that composite differs on a card, on the
// page and inside an inset. This test reads the variant table straight out
// of components/ui/button.tsx, composites every rest and hover fill over all
// three surfaces in the light and dark palettes of both scaffold kinds, and
// checks the label on top — plus the focus outline against the surface,
// which WCAG 2.2 focus appearance (2.4.13) holds to 3:1.
//
// Reading the classes from the source is the point: a palette retune or a
// variant edit that drops a label below 4.5:1 fails here, without a browser.
package templates

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/components"
)

var (
	buttonVariantBlock = regexp.MustCompile(`(?s)const variantStyles[^{]*\{(.*?)\n\};`)
	buttonVariantEntry = regexp.MustCompile(`(?s)(\w+):\s*"([^"]*)"`)
	// The outline's COLOR utility: outline-2 / outline-offset-2 are geometry.
	buttonFocusOutline = regexp.MustCompile(`focus-visible:outline-((?:accent|ink|danger|on)[a-z-]*)(?:\s|"|$)`)
	// bg-ink/6, bg-accent, text-on-accent — a token with an optional alpha.
	buttonColorUtility = regexp.MustCompile(`^(bg|text)-([a-z][a-z-]*?)(?:/(\d+))?$`)
)

// buttonVariants are the variant names the library promises; the rename
// codemod and every downstream project rely on exactly these.
var buttonVariants = []string{"danger", "ghost", "outline", "primary", "secondary"}

// buttonSurfaces are the three backgrounds a button is placed on.
var buttonSurfaces = []string{"surface", "surface-muted", "surface-sunken"}

type paint struct {
	token string
	alpha float64 // 0..1
}

// variantPaint is one variant's fill and label in one state.
type variantPaint struct{ fill, text *paint }

// parseButtonVariants returns rest and hover paints per variant.
func parseButtonVariants(t *testing.T, src string) map[string][2]variantPaint {
	t.Helper()
	block := buttonVariantBlock.FindStringSubmatch(src)
	if block == nil {
		t.Fatal("button_classes.tsx: no `const variantStyles` block — the test reads the variant table from it")
	}
	out := map[string][2]variantPaint{}
	for _, m := range buttonVariantEntry.FindAllStringSubmatch(block[1], -1) {
		var rest, hover variantPaint
		for _, class := range strings.Fields(m[2]) {
			state := &rest
			switch {
			case strings.HasPrefix(class, "not-disabled:hover:"):
				state, class = &hover, strings.TrimPrefix(class, "not-disabled:hover:")
			case strings.Contains(class, ":"):
				continue // pressed / expanded / other states
			}
			cm := buttonColorUtility.FindStringSubmatch(class)
			if cm == nil {
				continue
			}
			p := &paint{token: cm[2], alpha: 1}
			if cm[3] != "" {
				pct, _ := strconv.Atoi(cm[3])
				p.alpha = float64(pct) / 100
			}
			if cm[1] == "bg" {
				state.fill = p
			} else {
				state.text = p
			}
		}
		if hover.fill == nil {
			hover.fill = rest.fill
		}
		if hover.text == nil {
			hover.text = rest.text
		}
		out[m[1]] = [2]variantPaint{rest, hover}
	}
	return out
}

// srgb is a gamma-encoded sRGB color, the space browsers composite in.
type srgb struct{ r, g, b float64 }

func toSRGB(c linRGB) srgb {
	clamp := func(v float64) float64 { return math.Max(0, math.Min(1, encodeSRGB(v))) }
	return srgb{clamp(c.r), clamp(c.g), clamp(c.b)}
}

func decodeSRGB(v float64) float64 {
	if v <= 0.04045 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

func (c srgb) linear() linRGB {
	return linRGB{decodeSRGB(c.r), decodeSRGB(c.g), decodeSRGB(c.b)}
}

// over composites fg at alpha a over bg, as `bg-ink/6` paints.
func over(fg srgb, a float64, bg srgb) srgb {
	mix := func(f, b float64) float64 { return a*f + (1-a)*b }
	return srgb{mix(fg.r, bg.r), mix(fg.g, bg.g), mix(fg.b, bg.b)}
}

func TestButtonVariantsMeetContrastOnEverySurface(t *testing.T) {
	t.Parallel()

	src, err := components.NewLibrary().Get("button_classes")
	if err != nil {
		t.Fatal(err)
	}
	variants := parseButtonVariants(t, src)

	var names []string
	for name := range variants {
		names = append(names, name)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != strings.Join(buttonVariants, ",") {
		t.Fatalf("button.tsx variants = %v, want %v. Renaming or dropping a variant breaks every project "+
			"that installed the button; add an upgrade playbook entry before changing this list.", names, buttonVariants)
	}

	focus := buttonFocusOutline.FindStringSubmatch(src)
	if focus == nil {
		t.Fatal("button.tsx: no `focus-visible:outline-<token>` — the focus indicator must be an outline " +
			"(forced-colors mode drops box-shadow rings)")
	}

	for kind, rel := range paletteFiles {
		css := renderPalette(t, kind)
		for mode, re := range map[string]*regexp.Regexp{"light": themeAtBlock, "dark": darkRootBlock} {
			m := re.FindStringSubmatch(css)
			if m == nil {
				t.Fatalf("%s: no %s palette block", rel, mode)
			}
			tokens := parsePaletteBlock(t, rel, m[1])
			resolve := func(name string) srgb {
				t.Helper()
				raw, ok := tokens[name]
				if !ok {
					t.Fatalf("%s (%s): button.tsx paints --color-%s, which the palette does not declare", rel, mode, name)
				}
				if i := strings.Index(raw, "/*"); i >= 0 {
					raw = strings.TrimSpace(raw[:i])
				}
				om := oklchValue.FindStringSubmatch(raw)
				if om == nil {
					t.Fatalf("%s (%s): --color-%s = %q is not oklch", rel, mode, name, raw)
				}
				l, _ := strconv.ParseFloat(om[1], 64)
				c, _ := strconv.ParseFloat(om[2], 64)
				h, _ := strconv.ParseFloat(om[3], 64)
				return toSRGB(oklchToLinearSRGB(l, c, h))
			}

			for _, surfaceName := range buttonSurfaces {
				surface := resolve(surfaceName)

				ring := resolve(focus[1])
				if got := contrastRatio(ring.linear(), surface.linear()); got < 3 {
					t.Errorf("%s (%s): the focus outline (%s) on %s is %.2f:1, below WCAG 2.2's 3:1 focus-appearance floor",
						rel, mode, focus[1], surfaceName, got)
				}

				for _, name := range buttonVariants {
					for i, state := range []string{"rest", "hover"} {
						p := variants[name][i]
						if p.text == nil {
							t.Fatalf("button.tsx %s has no text-<token> color", name)
						}
						fill := surface
						if p.fill != nil {
							fill = over(resolve(p.fill.token), p.fill.alpha, surface)
						}
						label := over(resolve(p.text.token), p.text.alpha, fill)
						if got := contrastRatio(label.linear(), fill.linear()); got < 4.5 {
							t.Errorf("%s (%s): %s button label at %s on %s is %.2f:1, below WCAG AA 4.5:1 "+
								"(text-%s on %v). Adjust the token's lightness or the variant's fill alpha.",
								rel, mode, name, state, surfaceName, got, p.text.token, p.fill)
						}
					}
				}
			}
		}
	}
}

// TestButtonVariantParserReadsStates pins the parser so a format change in
// button.tsx cannot make the contrast test check nothing.
func TestButtonVariantParserReadsStates(t *testing.T) {
	t.Parallel()
	src := "const variantStyles: Record<ButtonVariant, string> = {\n" +
		"  ghost:\n    \"text-ink-muted not-disabled:hover:bg-ink/6 not-disabled:hover:text-ink aria-expanded:bg-ink/10\",\n" +
		"  primary: \"bg-accent text-on-accent not-disabled:hover:bg-accent-hover\",\n};\n"
	got := parseButtonVariants(t, src)
	ghost := got["ghost"]
	if ghost[0].fill != nil || ghost[0].text.token != "ink-muted" {
		t.Errorf("ghost rest = %+v, want no fill and text ink-muted", ghost[0])
	}
	if ghost[1].fill == nil || ghost[1].fill.token != "ink" || ghost[1].fill.alpha != 0.06 || ghost[1].text.token != "ink" {
		t.Errorf("ghost hover = fill %+v text %+v, want ink/6 under ink", ghost[1].fill, ghost[1].text)
	}
	primary := got["primary"]
	if primary[1].fill.token != "accent-hover" || primary[1].text.token != "on-accent" {
		t.Errorf("primary hover = fill %+v text %+v, want accent-hover under on-accent", primary[1].fill, primary[1].text)
	}
}

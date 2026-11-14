// library_lint_gate_test.go — the component library ships INTO every
// scaffolded frontend, and that frontend's own `npm run typecheck` /
// `npm run lint` are ERROR gates. A defect here is one the project cannot
// fix: the files are delivered by `forge scaffold`, so an edit downstream is
// overwritten by the next generate.
//
// Born red on a real dogfood run, whose scaffolded frontend failed both
// lanes out of the box:
//
//	src/components/ui/metric_card.tsx(34,17): error TS2532: Object is possibly 'undefined'.
//	src/components/ui/metric_card.tsx(34,46): error TS2532: Object is possibly 'undefined'.
//	src/components/ui/confirmation_dialog.tsx  47:7  error  Avoid non-native interactive elements  jsx-a11y/no-static-element-interactions
//	src/components/ui/data_table.tsx           30:9  error  A control must be associated with a text label  jsx-a11y/control-has-associated-label
//	src/components/ui/data_table.tsx           97:17 error  A control must be associated with a text label  jsx-a11y/control-has-associated-label
//	src/components/ui/data_table.tsx          147:21 error  A control must be associated with a text label  jsx-a11y/control-has-associated-label
//	src/components/ui/filter_bar.tsx          150:15 error  A control must be associated with a text label  jsx-a11y/control-has-associated-label
//
// These gates are static rather than a real tsc/eslint run because this
// package must test with no node toolchain present. The live proof is the
// acceptance run recorded in the fix's report: a freshly scaffolded frontend,
// `npm run typecheck` and `npm run lint` both clean.
package components

import (
	"regexp"
	"strings"
	"testing"
)

// bareIndexComparison matches an array element read by index and used
// directly in a COMPARISON — `values[values.length - 1] >= values[0]`. Under
// the scaffolded tsconfig's noUncheckedIndexedAccess that read is
// `T | undefined`, and comparing one is TS2532.
//
// Both sides must be index reads. A single-sided pattern matches TypeScript
// generics — `Record<NonNullable<Props["size"]>, string>` — where the angle
// brackets are type syntax rather than operators, which is a whole different
// construct and not a defect.
var bareIndexComparison = regexp.MustCompile(`\w+\[[^\]]*\]\s*(>=|<=|>|<)\s*\w+\[[^\]]*\]`)

// staticElementWithOnClick matches a <div>/<span> carrying onClick — the
// shape jsx-a11y/no-static-element-interactions rejects. A real <button> is
// the fix; adding a role and key handlers is the other, heavier one.
var staticElementWithOnClick = regexp.MustCompile(`(?s)<(div|span)\b[^>]*\bonClick=`)

// unlabelledCheckbox matches an <input type="checkbox"> with no aria-label,
// aria-labelledby or id (which a sibling <label htmlFor> would target).
// control-has-associated-label is an ERROR under the scaffolded config.
var checkboxInput = regexp.MustCompile(`(?s)<input\b[^>]*type="checkbox"[^>]*>`)

// buttonOpen locates the START of every <button> element. The opening tag's
// full extent is found by scanning (see iconOnlyButtons) rather than by
// regex: an arrow function in a handler (`onClick={() => …}`) puts a `>`
// inside the tag, so a `[^>]*` body ends the match early and the guard
// silently sees no buttons at all.
var buttonOpen = regexp.MustCompile(`<button\b`)

// ariaHiddenOnTableElement matches aria-hidden on <tr>/<td>/<th>. jsx-a11y
// treats table elements as focusable, so this is no-aria-hidden-on-focusable
// — the trap a naive "just hide the shimmer" fix falls into.
var ariaHiddenOnTableElement = regexp.MustCompile(`<(tr|td|th)\b[^>]*\baria-hidden\b`)

// libraryTSX returns every component source the library ships, keyed by its
// registry path.
func libraryTSX(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	lib := NewLibrary()
	for _, entry := range lib.Registry() {
		body, err := lib.Get(entry.Name)
		if err != nil {
			t.Fatalf("get %s: %v", entry.Name, err)
		}
		out[entry.Name] = body
	}
	if len(out) == 0 {
		t.Fatal("component library listed no components — this guard has gone blind")
	}
	return out
}

// TestComponentsSurviveNoUncheckedIndexedAccess is the typecheck half of the
// gate: the scaffolded Next.js tsconfig sets noUncheckedIndexedAccess, so an
// indexed read used as a value must be made total.
func TestComponentsSurviveNoUncheckedIndexedAccess(t *testing.T) {
	t.Parallel()

	for name, body := range libraryTSX(t) {
		for _, line := range strings.Split(body, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") {
				continue
			}
			if !bareIndexComparison.MatchString(trimmed) {
				continue
			}
			// `?? fallback` and a non-null assertion both make the read total.
			if strings.Contains(trimmed, "??") || strings.Contains(trimmed, "!") {
				continue
			}
			t.Errorf("%s reads an array element by index and uses it directly:\n\n  %s\n\n"+
				"The tsconfig forge scaffolds sets noUncheckedIndexedAccess, so that read is "+
				"`T | undefined` and this is TS2532 — `npm run typecheck` fails in every "+
				"generated frontend, and a project cannot fix it because this file ships from "+
				"the library. Give the read a `?? fallback`.", name, trimmed)
		}
	}
}

// TestComponentsHaveNoStaticInteractiveElements pins the dialog defect: a
// clickable backdrop must be a real <button>, so it works by keyboard.
func TestComponentsHaveNoStaticInteractiveElements(t *testing.T) {
	t.Parallel()

	for name, body := range libraryTSX(t) {
		for _, m := range staticElementWithOnClick.FindAllString(body, -1) {
			if strings.Contains(m, "role=") {
				continue // an explicit role + key handlers is the other valid fix
			}
			t.Errorf("%s puts onClick on a non-interactive element:\n\n  %s\n\n"+
				"jsx-a11y/no-static-element-interactions is an ERROR in the eslint config forge "+
				"scaffolds, so this fails `npm run lint` in every generated frontend. Use a real "+
				"<button>, which is also what makes the control reachable by keyboard.",
				name, strings.TrimSpace(collapse(m)))
		}
	}
}

// TestComponentControlsCarryAccessibleNames pins the checkbox/icon-button
// defects: a control with no visible text needs an explicit accessible name.
func TestComponentControlsCarryAccessibleNames(t *testing.T) {
	t.Parallel()

	for name, body := range libraryTSX(t) {
		for _, m := range checkboxInput.FindAllString(body, -1) {
			if hasAccessibleName(m) {
				continue
			}
			t.Errorf("%s renders a checkbox with no accessible name:\n\n  %s\n\n"+
				"jsx-a11y/control-has-associated-label is an ERROR under the scaffolded eslint "+
				"config, so this fails `npm run lint` out of the box. Add an aria-label.",
				name, strings.TrimSpace(collapse(m)))
		}

		// Icon-only buttons: a <button> whose body holds an <svg> and no text.
		for _, m := range iconOnlyButtons(body) {
			if hasAccessibleName(m) {
				continue
			}
			t.Errorf("%s renders an icon-only button with no accessible name:\n\n  %s\n\n"+
				"A screen reader announces it as just \"button\". Add an aria-label.",
				name, strings.TrimSpace(collapse(m)))
		}
	}
}

// TestComponentsDoNotHideTableElements pins the trap the first attempt at
// fixing the skeleton row fell into: aria-hidden on a <td> is itself an
// eslint error (no-aria-hidden-on-focusable), so "just hide the shimmer"
// trades one red gate for another.
func TestComponentsDoNotHideTableElements(t *testing.T) {
	t.Parallel()

	for name, body := range libraryTSX(t) {
		if m := ariaHiddenOnTableElement.FindString(body); m != "" {
			t.Errorf("%s sets aria-hidden on a table element:\n\n  %s\n\n"+
				"jsx-a11y treats <tr>/<td>/<th> as focusable, so this is "+
				"no-aria-hidden-on-focusable — also an error under the scaffolded config. "+
				"Put aria-hidden on the decorative child instead.", name, strings.TrimSpace(m))
		}
	}
}

// hasAccessibleName reports whether an opening tag carries one of the
// attributes that give a control a name eslint will accept.
func hasAccessibleName(tag string) bool {
	for _, attr := range []string{"aria-label", "aria-labelledby", "title", "id="} {
		if strings.Contains(tag, attr) {
			return true
		}
	}
	return false
}

// iconOnlyButtons returns the opening tags of every <button> whose content is
// an svg and nothing else — no text node, no {children}.
func iconOnlyButtons(body string) []string {
	var out []string
	for _, loc := range buttonOpen.FindAllStringIndex(body, -1) {
		tagEnd := endOfOpeningTag(body, loc[0])
		if tagEnd < 0 {
			continue
		}
		open := body[loc[0]:tagEnd]
		rest := body[tagEnd:]
		end := strings.Index(rest, "</button>")
		if end < 0 {
			continue
		}
		inner := rest[:end]
		if !strings.Contains(inner, "<svg") {
			continue
		}
		// Strip the svg markup; whatever remains is the button's text.
		text := regexp.MustCompile(`(?s)<svg.*?</svg>`).ReplaceAllString(inner, "")
		text = regexp.MustCompile(`(?s)<[^>]*>`).ReplaceAllString(text, "")
		if strings.TrimSpace(text) != "" {
			continue
		}
		out = append(out, open)
	}
	return out
}

// endOfOpeningTag returns the index just past the `>` that closes the JSX
// opening tag starting at start, skipping the `>` of an arrow function and
// any `>` nested inside a braced expression. Returns -1 if unterminated.
func endOfOpeningTag(body string, start int) int {
	depth := 0
	for i := start; i < len(body); i++ {
		switch body[i] {
		case '{':
			depth++
		case '}':
			depth--
		case '>':
			if depth > 0 {
				continue // inside {...}: an arrow, a generic, a comparison
			}
			return i + 1
		}
	}
	return -1
}

// collapse squeezes a multi-line JSX tag onto one line for readable failures.
func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

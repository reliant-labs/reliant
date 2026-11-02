package stepsecrets

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

// The vector in secret-resolve-wire.md, asserted on the control plane's side by
// TestRefsDigestGolden: both ends must compute the same bytes.
func TestRefsDigestGolden(t *testing.T) {
	shuffled := []Ref{
		{"trader/staging", "TRADER_SECRET"},
		{"trader/staging", "TRADER_KEY"},
		{"trader/staging", "TRADER_KEY"},
	}
	canon := CanonicalRefs(shuffled)
	require.Equal(t, []Ref{{"trader/staging", "TRADER_KEY"}, {"trader/staging", "TRADER_SECRET"}}, canon)
	raw, _ := json.Marshal(canon)
	require.Equal(t, `[{"environment":"trader/staging","name":"TRADER_KEY"},{"environment":"trader/staging","name":"TRADER_SECRET"}]`, string(raw))
	require.Equal(t, "e595919dfecebc8d8461a93469dc4711b37933130dfbadabbd8ec4932d245fd9", RefsDigest(shuffled))

	assert.Equal(t, RefsDigest([]Ref{{"b/x", "A"}, {"a/y", "B"}}), RefsDigest([]Ref{{"a/y", "B"}, {"b/x", "A"}}), "order is irrelevant")
	assert.NotEqual(t, RefsDigest([]Ref{{"a", "A"}}), RefsDigest([]Ref{{"a", "B"}}))
	assert.NotEqual(t, RefsDigest([]Ref{{"a/x", "A"}}), RefsDigest([]Ref{{"a/y", "A"}}), "the environment is part of the binding")
}

func TestRenderTurnsAuthoredHolesIntoMarkersAndDataIntoNothing(t *testing.T) {
	// A stand-in template engine: it knows no `secrets` namespace, evaluates
	// {{ data }} to attacker-controlled text, and leaves everything else alone.
	attacker := "x " + Marker("K") + " y {{ secrets.K }}"
	eval := func(s string) (interface{}, error) {
		if strings.Contains(s, "{{ secrets") {
			return nil, errors.New("engine has no secrets namespace")
		}
		// Evaluated output is data: it is not parsed again.
		return strings.ReplaceAll(s, "{{ data }}", attacker), nil
	}

	got, err := Render("Bearer {{ secrets.K }} / {{ data }}", eval)
	require.NoError(t, err)
	str := got.(string)
	assert.True(t, strings.HasPrefix(str, "Bearer "+Marker("K")+" / "), "the authored hole is a marker: %q", str)
	assert.Equal(t, []string{"K"}, Names(str), "and it is the ONLY marker; the data's was defanged")
	assert.NotContains(t, str, "\x00", "no render token escapes")

	pure, err := Render("{{ secrets.K }}", eval)
	require.NoError(t, err)
	assert.Equal(t, Marker("K"), pure)

	// A hole that reads `secrets` any other way reaches the engine, which fails closed.
	_, err = Render("{{ secrets['K'] }}", func(s string) (interface{}, error) { return nil, errors.New("fails closed") })
	assert.Error(t, err)

	// No authored hole: nothing to protect, but the data is still defanged.
	plain, err := Render("{{ data }}", eval)
	require.NoError(t, err)
	assert.Empty(t, Names(plain.(string)))

	// Typed results pass through untouched.
	typed, err := Render("{{ n }}", func(string) (interface{}, error) { return int64(7), nil })
	require.NoError(t, err)
	assert.Equal(t, int64(7), typed)
	lst, _ := Render("{{ l }}", func(string) (interface{}, error) { return []any{"a " + Marker("K"), 1}, nil })
	assert.Empty(t, Names(lst.([]any)[0].(string)), "nested data is defanged too")

	// Two renders use different tokens: a token seen once cannot be replayed.
	var seen []string
	capture := func(s string) (interface{}, error) { seen = append(seen, s); return s, nil }
	_, _ = Render("{{ secrets.K }}", capture)
	_, _ = Render("{{ secrets.K }}", capture)
	assert.NotEqual(t, seen[0], seen[1])

	assert.Equal(t, []string{"A", "B"}, AuthoredNames("x {{ secrets.A }} y {{secrets.B}} z {{ secrets.A }}"))
	assert.Empty(t, AuthoredNames("{{ inputs.a }} secrets.A"))
	assert.True(t, HasAuthored("a {{ secrets.A }}"))
	assert.False(t, HasAuthored("a {{ inputs.A }}"))
}

// Data that reaches a step through an expression is sanitized, so data that
// spells a marker (or the placeholder) cannot be substituted with a real value.
func TestDataCannotForgeAMarker(t *testing.T) {
	forged := Marker("K")
	clean := Sanitize("a " + forged + " b")
	assert.NotContains(t, clean, forged)
	assert.Empty(t, Names(clean), "a sanitized marker is not a marker")
	assert.Equal(t, "no marker", Sanitize("no marker"))

	vals := Values{"K": "the-real-secret-value"}
	assert.NotContains(t, vals.Substitute(clean), "the-real-secret-value")
	assert.Equal(t, "{{ secrets.K }}", vals.Substitute("{{ secrets.K }}"), "the authored text alone is not a marker at run time")

	got := SanitizeAny(map[string]any{forged: []any{forged, 1, []string{forged}}, "k": forged}).(map[string]any)
	b, _ := json.Marshal(got)
	assert.NotContains(t, string(b), forged)

	assert.Equal(t, []string{"K"}, Names("a "+Marker("K")+" b "+Marker("K")))
	assert.Equal(t, "{{ secrets.K }}", Unprotect(Marker("K")))
	assert.Equal(t, `curl -H "X: ${K}"`, ShellRefs(`curl -H "X: `+Marker("K")+`"`))
	assert.Equal(t, "plain", ShellRefs("plain"))
}

func TestValueNamesWalksTheWholeTree(t *testing.T) {
	v, err := structpb.NewValue(map[string]any{
		"url":     "https://x/" + Marker("PATH_KEY"),
		"headers": map[string]any{"X-Key": Marker("API")},
		"body":    []any{map[string]any{"k": Marker("BODY")}, 3, true, nil},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"API", "BODY", "PATH_KEY"}, ValueNames(v))
}

func TestSubstitute(t *testing.T) {
	vals := Values{"A": "alpha-secret-value", "B": "bravo-secret-value"}
	in := "Bearer " + Marker("A") + " / " + Marker("B")
	assert.Equal(t, "Bearer alpha-secret-value / bravo-secret-value", vals.Substitute(in))
	assert.Equal(t, "no holes", vals.Substitute("no holes"))
	assert.Equal(t, Marker("C"), vals.Substitute(Marker("C")), "an unresolved name stays visible")
	// One pass: a value that looks like a marker is data, not a second lookup.
	nested := Values{"A": Marker("B"), "B": "never"}
	assert.Equal(t, Marker("B"), nested.Substitute(Marker("A")))

	pv, _ := structpb.NewValue(map[string]any{"h": map[string]any{"k": Marker("A")}, "l": []any{Marker("B")}, "n": 1})
	out := vals.SubstituteValue(pv).GetStructValue().AsMap()
	assert.Equal(t, map[string]any{"h": map[string]any{"k": "alpha-secret-value"}, "l": []any{"bravo-secret-value"}, "n": float64(1)}, out)
	assert.Contains(t, pv.GetStructValue().GetFields()["h"].GetStructValue().GetFields()["k"].GetStringValue(), "secret:", "the input is not mutated")

	any1 := vals.SubstituteAny(map[string]any{"k": []any{Marker("A"), 2}}).(map[string]any)
	assert.Equal(t, []any{"alpha-secret-value", 2}, any1["k"])
}

func TestValuesNeverPrint(t *testing.T) {
	v := Values{"K": "super-secret-value-123"}
	for _, s := range []string{fmt.Sprint(v), fmt.Sprintf("%v", v), fmt.Sprintf("%+v", v), fmt.Sprintf("%#v", v), fmt.Sprintf("%s", v)} {
		assert.NotContains(t, s, "super-secret-value-123")
	}
}

func TestMaskerCoversEncodedForms(t *testing.T) {
	const value = `s3cret/val+ue&x=1 "q"`
	m := NewMasker(value)
	raw := []byte(value)
	jsonEscaped, _ := json.Marshal(value)
	forms := map[string]string{
		"raw":              value,
		"base64 std":       base64.StdEncoding.EncodeToString(raw),
		"base64 raw std":   base64.RawStdEncoding.EncodeToString(raw),
		"base64 url":       base64.URLEncoding.EncodeToString(raw),
		"base64 raw url":   base64.RawURLEncoding.EncodeToString(raw),
		"url query":        url.QueryEscape(value),
		"url path":         url.PathEscape(value),
		"json string body": string(jsonEscaped[1 : len(jsonEscaped)-1]),
	}
	for name, form := range forms {
		got := m.Mask("before " + form + " after")
		assert.Equal(t, "before *** after", got, name)
		assert.NotContains(t, got, form, name)
	}
}

func TestMaskerEdgeCases(t *testing.T) {
	m := NewMasker("shared-prefix-A", "shared-prefix-A-and-more")
	assert.Equal(t, "***", m.Mask("shared-prefix-A-and-more"), "the longer form goes first, leaving no tail")
	assert.Equal(t, "*** and ***", m.Mask("shared-prefix-A and shared-prefix-A-and-more"))

	assert.Equal(t, "plain text", NewMasker("").Mask("plain text"))
	assert.Equal(t, "abc", NewMasker("abc").Mask("abc"), "a value under four bytes is not masked, it would shred text")
	assert.True(t, NewMasker().Empty())
	assert.Equal(t, "x", (*Masker)(nil).Mask("x"))

	multi := NewMasker("line-one-secret\nline-two-secret")
	assert.Equal(t, "> ***\n> ***", multi.Mask("> line-one-secret\n> line-two-secret"), "a multi-line value is masked line by line too")
}

func TestMaskErrorDropsTheChain(t *testing.T) {
	m := NewMasker("leaky-secret-value")
	cause := errors.New("remote said leaky-secret-value")
	err := m.MaskError(fmt.Errorf("step: %w", cause))
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "leaky-secret-value")
	assert.Contains(t, err.Error(), "***")
	assert.False(t, errors.Is(err, cause), "the wrapped cause would still print the value")

	clean := errors.New("nothing here")
	assert.Same(t, clean, m.MaskError(clean))
	assert.NoError(t, m.MaskError(nil))
}

func TestMaskStructAndAny(t *testing.T) {
	m := NewMasker("tok-1234567890")
	s, _ := structpb.NewStruct(map[string]any{
		"text":           "got tok-1234567890",
		"nested":         map[string]any{"b64": base64.StdEncoding.EncodeToString([]byte("tok-1234567890"))},
		"list":           []any{"tok-1234567890", 7},
		"tok-1234567890": "key leak",
	})
	out := m.MaskStruct(s)
	b, _ := json.Marshal(out.AsMap())
	assert.NotContains(t, string(b), "tok-1234567890")
	assert.NotContains(t, string(b), base64.StdEncoding.EncodeToString([]byte("tok-1234567890")))
	assert.Contains(t, string(b), "***")
	assert.Contains(t, s.GetFields()["text"].GetStringValue(), "tok-1234567890", "the input is not mutated")

	a := m.MaskAny(map[string]any{"k": []any{"x tok-1234567890", 1}}).(map[string]any)
	assert.Equal(t, []any{"x ***", 1}, a["k"])
}

func TestEnvIsACopyNamedAfterTheSecrets(t *testing.T) {
	v := Values{"TRADER_KEY": "value-1234567890"}
	env := v.Env()
	assert.Equal(t, map[string]string{"TRADER_KEY": "value-1234567890"}, env)
	env["TRADER_KEY"] = "changed"
	assert.Equal(t, "value-1234567890", v["TRADER_KEY"])
	assert.True(t, NamePattern.MatchString("TRADER_KEY"))
	assert.False(t, NamePattern.MatchString("1BAD"))
	assert.False(t, strings.Contains("TRADER_KEY", "-"))
}

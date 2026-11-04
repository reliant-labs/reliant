package stepsecrets

import (
	"encoding/json"
	"errors"
	"fmt"
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
// spells a marker (or the placeholder) can never fill a credential slot.
func TestDataCannotForgeAMarker(t *testing.T) {
	forged := Marker("K")
	clean := Sanitize("a " + forged + " b")
	assert.NotContains(t, clean, forged)
	assert.Empty(t, Names(clean), "a sanitized marker is not a marker")
	assert.Equal(t, "no marker", Sanitize("no marker"))

	_, _, ok := CredentialSlot("Bearer " + clean)
	assert.False(t, ok, "a sanitized marker never fills a credential slot")
	_, _, ok = CredentialSlot("{{ secrets.K }}")
	assert.False(t, ok, "the authored text alone is not a marker at run time")

	got := SanitizeAny(map[string]any{forged: []any{forged, 1, []string{forged}}, "k": forged}).(map[string]any)
	b, _ := json.Marshal(got)
	assert.NotContains(t, string(b), forged)

	assert.Equal(t, []string{"K"}, Names("a "+Marker("K")+" b "+Marker("K")))
	assert.Equal(t, "{{ secrets.K }}", Unprotect(Marker("K")))
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

func TestCredentialSlot(t *testing.T) {
	for _, tc := range []struct {
		in, prefix, name string
		ok               bool
	}{
		{Marker("K"), "", "K", true},
		{"Bearer " + Marker("K"), "Bearer ", "K", true},
		{"token=" + Marker("API_KEY"), "token=", "API_KEY", true},
		{"plain", "", "", false},
		{Marker("K") + " trailing", "", "", false},
		{Marker("A") + Marker("B"), "", "", false},
		{"x " + Marker("A") + " " + Marker("B"), "", "", false},
		{"\u27e6secret:" + Marker("K"), "", "", false},
		{"\u27e6secret:not a name\u27e7", "", "", false},
		{"Bearer " + Sanitize(Marker("K")), "", "", false},
	} {
		prefix, name, ok := CredentialSlot(tc.in)
		assert.Equal(t, tc.ok, ok, tc.in)
		assert.Equal(t, tc.prefix, prefix, tc.in)
		assert.Equal(t, tc.name, name, tc.in)
	}
	assert.True(t, HasMarker("a "+Marker("K")))
	assert.False(t, HasMarker("a "+Sanitize(Marker("K"))), "sanitized data is never a marker")
}

func TestValuesNeverPrint(t *testing.T) {
	v := Values{"K": "super-secret-value-123"}
	for _, s := range []string{fmt.Sprint(v), fmt.Sprintf("%v", v), fmt.Sprintf("%+v", v), fmt.Sprintf("%#v", v), fmt.Sprintf("%s", v)} {
		assert.NotContains(t, s, "super-secret-value-123")
	}
}

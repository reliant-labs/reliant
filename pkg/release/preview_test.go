package release

import (
	"strings"
	"testing"
	"time"
)

func TestPreviewName(t *testing.T) {
	for _, tc := range []struct {
		template, env string
		key           PreviewKey
		want          string
	}{
		{"", "preview", PreviewKey{PR: 123}, "preview-pr-123"},
		{"", "preview", PreviewKey{Branch: "feat/Login_Page"}, "preview-feat-login-page"},
		{"pr{pr}", "preview", PreviewKey{PR: 7}, "prpr-7"},
		{"{id}", "staging", PreviewKey{PR: 7}, "pr-7"},
		// A name that would start with a digit is prefixed.
		{"{branch}", "preview", PreviewKey{Branch: "123-fix"}, "p-123-fix"},
	} {
		got, err := PreviewName(tc.template, tc.env, tc.key)
		if err != nil || got != tc.want {
			t.Errorf("PreviewName(%q, %q, %+v) = %q, %v; want %q", tc.template, tc.env, tc.key, got, err, tc.want)
		}
	}

	// Long branches are cut with a hash suffix: distinct, valid, bounded,
	// and the same every time.
	a, _ := PreviewName("", "preview", PreviewKey{Branch: "feature/" + strings.Repeat("x", 60) + "-a"})
	b, _ := PreviewName("", "preview", PreviewKey{Branch: "feature/" + strings.Repeat("x", 60) + "-b"})
	again, _ := PreviewName("", "preview", PreviewKey{Branch: "feature/" + strings.Repeat("x", 60) + "-a"})
	if a == b || a != again || len(a) > PreviewNameMax || !previewEnvRE.MatchString(a) {
		t.Errorf("long names: %q %q %q", a, b, again)
	}

	for _, bad := range []struct {
		template string
		key      PreviewKey
	}{
		{"", PreviewKey{}},
		{"", PreviewKey{PR: 1, Branch: "x"}},
		{"", PreviewKey{Branch: "///"}},
		{"static-name", PreviewKey{PR: 1}},
	} {
		if got, err := PreviewName(bad.template, "preview", bad.key); err == nil {
			t.Errorf("PreviewName(%q, %+v) = %q, want an error", bad.template, bad.key, got)
		}
	}
}

func TestParsePreviewTTL(t *testing.T) {
	for in, want := range map[string]time.Duration{"72h": 72 * time.Hour, "7d": 7 * 24 * time.Hour, "90m": 90 * time.Minute} {
		if got, err := ParsePreviewTTL(in); err != nil || got != want {
			t.Errorf("%s: %v, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "0h", "-1h", "31d", "soon", "1.5d"} {
		if _, err := ParsePreviewTTL(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestShapePreview(t *testing.T) {
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	p := ShapePreview{Of: "preview", ExpiresAt: "2026-11-11T23:59:59Z"}
	if !p.Expired(now) {
		t.Error("past expiry is expired")
	}
	if (ShapePreview{Of: "preview", ExpiresAt: "2026-11-12T00:00:01Z"}).Expired(now) {
		t.Error("future expiry is not expired")
	}
	// An expiry nobody can read never expires: a reaper must not delete on it.
	if (ShapePreview{Of: "preview", ExpiresAt: "tomorrow"}).Expired(now) {
		t.Error("unreadable expiry must not read as expired")
	}
	if err := (Shape{Kind: EnvLocal, Preview: &ShapePreview{Of: "Preview", ExpiresAt: "x"}}).Validate(); err == nil {
		t.Error("an invalid preview must not validate")
	}
	if err := (Shape{Kind: EnvLocal, Preview: &p}).Validate(); err != nil {
		t.Errorf("valid preview refused: %v", err)
	}
}

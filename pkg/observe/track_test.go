package observe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/authn"
)

func trackLine(t *testing.T, ctx context.Context, name string, attrs ...slog.Attr) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	ctx = WithLogger(ctx, slog.New(slog.NewJSONHandler(&buf, nil)))
	Track(ctx, name, attrs...)
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("bad log line %q: %v", buf.String(), err)
	}
	return m
}

func TestTrackShape(t *testing.T) {
	SetAppVersion("1.2.3")
	t.Cleanup(func() { SetAppVersion("") })
	ctx := authn.ContextWithClaims(context.Background(), &authn.Claims{UserID: "u-1"})
	m := trackLine(t, ctx, "project_created", slog.String("template", "go"), slog.Int("repos", 2))
	want := map[string]any{
		"level": "INFO", "event.name": "product.project_created", "user.id": "u-1",
		"app.client": "backend", "app.version": "1.2.3",
		"product.template": "go", "product.repos": "2",
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %v, want %v (line %v)", k, m[k], v, m)
		}
	}
}

func TestTrackAnonymousHasNoUserID(t *testing.T) {
	m := trackLine(t, context.Background(), "page_viewed")
	if _, ok := m["user.id"]; ok {
		t.Errorf("anonymous event carries user.id: %v", m)
	}
	if m["event.name"] != "product.page_viewed" || m["app.client"] != "backend" {
		t.Errorf("unexpected line: %v", m)
	}
}

func TestTrackRejectsBadNames(t *testing.T) {
	for _, n := range []string{"", "Project", "project-created", "1abc", "a b", strings.Repeat("a", 65), "product.x"} {
		m := trackLine(t, context.Background(), n)
		if _, ok := m["event.name"]; ok {
			t.Errorf("name %q was emitted as a product event", n)
		}
		if m["level"] != "WARN" {
			t.Errorf("name %q: want a WARN, got %v", n, m)
		}
	}
}

func TestTrackBoundsProps(t *testing.T) {
	attrs := make([]slog.Attr, 0, 40)
	for i := 0; i < 40; i++ {
		attrs = append(attrs, slog.String(fmt.Sprintf("k%02d", i), "v"))
	}
	m := trackLine(t, context.Background(), "many", attrs...)
	n := 0
	for k := range m {
		if strings.HasPrefix(k, "product.") {
			n++
		}
	}
	if n != MaxProductProps {
		t.Errorf("kept %d props, want %d", n, MaxProductProps)
	}
	if _, ok := m["product.k39"]; ok {
		t.Error("prop past the limit was kept")
	}

	m = trackLine(t, context.Background(), "long", slog.String("v", strings.Repeat("é", 300)), slog.String("short", "ok"))
	if got := m["product.v"].(string); got != strings.Repeat("é", MaxProductPropValueLen) {
		t.Errorf("value len = %d runes, want %d", len([]rune(got)), MaxProductPropValueLen)
	}
	if m["product.short"] != "ok" {
		t.Errorf("short value mangled: %v", m["product.short"])
	}
}

func TestTrackFlattensGroups(t *testing.T) {
	m := trackLine(t, context.Background(), "grouped", slog.Group("plan", slog.String("from", "free"), slog.String("to", "pro")))
	if m["product.plan.from"] != "free" || m["product.plan.to"] != "pro" {
		t.Errorf("group not flattened: %v", m)
	}
}

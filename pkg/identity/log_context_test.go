package identity

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

type identityLogContextKey struct{}

// Inspect the context at the handler boundary, not in the formatted output:
// slog.Info drops it even when the message and attributes are unchanged.
type identityContextHandler struct {
	slog.Handler
	t        *testing.T
	messages map[string]int
}

func (h *identityContextHandler) Handle(ctx context.Context, rec slog.Record) error {
	if got := ctx.Value(identityLogContextKey{}); got != "caller-trace" {
		h.t.Errorf("log %q received context marker %v, want caller-trace", rec.Message, got)
	}
	h.messages[rec.Message]++
	return h.Handler.Handle(ctx, rec)
}

func TestReconcileLogsPreserveCallerContext(t *testing.T) {
	var logs bytes.Buffer
	h := &identityContextHandler{
		Handler: slog.NewJSONHandler(&logs, nil), t: t, messages: make(map[string]int),
	}
	f := newFake(t)
	r, _ := newRec(t, f, func(o *Options) { o.Logger = slog.New(h) })
	ctx := context.WithValue(context.Background(), identityLogContextKey{}, "caller-trace")
	if _, err := r.Reconcile(ctx, fullSpec()); err != nil {
		t.Fatal(err)
	}
	for _, msg := range []string{
		"created project", "created application", "created machine user",
		"issued and delivered a machine key", "issued and delivered a token", "created user",
	} {
		if h.messages["identity: "+msg] == 0 {
			t.Errorf("expected log %q was not exercised", msg)
		}
	}
}

func TestImportUsersLogsPreserveCallerContext(t *testing.T) {
	// Converge before attaching the handler so every captured record belongs
	// to this import call, rather than the setup's background context.
	_, r := convergedForImport(t, nil)
	var logs bytes.Buffer
	h := &identityContextHandler{
		Handler: slog.NewJSONHandler(&logs, nil), t: t, messages: make(map[string]int),
	}
	r.c.log = slog.New(h)
	ctx := context.WithValue(context.Background(), identityLogContextKey{}, "caller-trace")
	res, err := r.ImportUsers(ctx, importSpec(), []ImportUser{ada()}, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("got %d results, want 1", len(res))
	}
	wantResult(t, res[0], ImportCreated, "")
	if got := h.messages["identity: import"]; got != 1 {
		t.Errorf("got %d import log records, want 1", got)
	}
	if strings.Contains(logs.String(), testBcrypt) || strings.Contains(logs.String(), "gh-secret") {
		t.Error("import logs exposed credentials")
	}
	for _, attr := range []string{`"user_id":"` + ada().ID + `"`, `"outcome":"created"`, `"reason":""`, `"dry_run":false`} {
		if !strings.Contains(logs.String(), attr) {
			t.Errorf("import log missing attribute %s", attr)
		}
	}
}

package forgeconv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const productEventPIISrc = `package billing

import (
	"context"
	"log/slog"

	"github.com/reliant-labs/forge/pkg/observe"
	ev "github.com/reliant-labs/forge/pkg/observe/cliev"
)

func record(ctx context.Context, email string) {
	observe.Track(ctx, "invite_sent",
		slog.String("role", "admin"),
		slog.String("invitee_email", email),
		slog.Attr{Key: "repo.path", Value: slog.StringValue("x")},
	)
	ev.Track(ctx, "forge_cli_command", slog.String("ip", "x"), slog.Bool("ok", true))
	// forge:lint-disable-next-line forgeconv-product-event-pii: a fixed plan's name, not a person's
	observe.Track(ctx, "plan_chosen", slog.String("plan_name", "pro"))
	slog.String("email", "not an event")
}
`

func TestProductEventPII_FlagsPIIKeys(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "billing.go"), []byte(productEventPIISrc), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := LintProductEventPII(dir, LintOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, f := range res.Findings {
		if f.Rule != ruleProductEventPII || f.Severity != SeverityWarning {
			t.Errorf("unexpected finding %+v", f)
		}
		for _, k := range []string{"invitee_email", "repo.path", "ip", "plan_name"} {
			if strings.Contains(f.Message, `"`+k+`"`) {
				keys = append(keys, k)
			}
		}
	}
	// plan_name is reported here; `forge lint` applies the disable comment
	// to every Go finding afterwards (suppress.ApplyFiles).
	if got, want := strings.Join(keys, ","), "invitee_email,repo.path,ip,plan_name"; got != want {
		t.Errorf("flagged keys = %s, want %s (findings %+v)", got, want, res.Findings)
	}
	if res.Findings[0].Line != 14 {
		t.Errorf("first finding line = %d, want 14 (the attr's key)", res.Findings[0].Line)
	}

	strict, err := LintProductEventPII(dir, LintOptions{Strict: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strict.HasErrors() {
		t.Error("--strict must make the findings errors")
	}
}

func TestProductEventPII_IgnoresFilesWithoutAnEmitter(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := "package x\n\nimport \"log/slog\"\n\nfunc f() { _ = slog.String(\"email\", \"a\") }\n"
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := LintProductEventPII(dir, LintOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 0 {
		t.Errorf("want no findings, got %+v", res.Findings)
	}
}

func TestKeyNamesPII(t *testing.T) {
	t.Parallel()
	for key, want := range map[string]bool{
		"email": true, "user_email": true, "file-path": true, "Token": true, "repo.url": true,
		"mode": false, "first_for_org": false, "emails_sent": false, "named": false, "pathway": false,
	} {
		if got := keyNamesPII(key); got != want {
			t.Errorf("keyNamesPII(%q) = %v, want %v", key, got, want)
		}
	}
}

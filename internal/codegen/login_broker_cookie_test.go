package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// forge#2276 item 3: an upgrade that adopts v1's pkg/middleware (which reads
// only the __Host- session cookie outside development) and keeps an owned
// login broker from before it (which sets the plain name) breaks deployed
// sign-in silently. generate says so; it never rewrites the owned broker.
func TestLoginBrokerCookieMismatch(t *testing.T) {
	const secureMW = "package middleware\n\nconst SessionCookieName = \"app_session\"\n\nconst SecureSessionCookieName = \"__Host-\" + SessionCookieName\n"
	const plainMW = "package middleware\n\nconst SessionCookieName = \"app_session\"\n"
	const oldBroker = "package app\n\nfunc set() string { return middleware.SessionCookieName }\n"
	const newBroker = "package app\n\nconst SecureSessionCookieName = middleware.SecureSessionCookieName\n\nfunc set() string { return middleware.SessionCookieName }\n"
	for _, tc := range []struct {
		name, mw, broker string
		warn             bool
	}{
		{"pre-__Host- broker over v1 middleware", secureMW, oldBroker, true},
		{"current broker", secureMW, newBroker, false},
		{"middleware without the __Host- name", plainMW, oldBroker, false},
		{"no broker", secureMW, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			write := func(rel, src string) {
				p := filepath.Join(dir, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			write("pkg/middleware/middleware.go", tc.mw)
			if tc.broker != "" {
				write("internal/app/login_broker.go", tc.broker)
			}
			got := LoginBrokerCookieMismatch(dir)
			if (got != "") != tc.warn {
				t.Fatalf("warning = %q, want warn=%v", got, tc.warn)
			}
			if tc.warn && !strings.Contains(got, "SecureSessionCookieName") || tc.warn && !strings.Contains(got, "__Host-") {
				t.Errorf("the warning must name the constant and the fix: %s", got)
			}
		})
	}
}

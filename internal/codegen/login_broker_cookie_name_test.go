package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `forge generate` runs in the project root and passed filepath.Base(".") as
// the project's name, so a broker scaffolded beside a pkg/middleware that
// declares no session cookie named it "._session", and devidp's binding
// cookies "._pk", "._ml". The stem is a cookie-name token now, from the
// project's name, and the Secure name is __Host- prefixed like the one
// pkg/middleware declares.
func TestGenerateLoginBroker_CookieNamesAreValidAndPrefixed(t *testing.T) {
	for _, tc := range []struct {
		name, projectName, want string
	}{
		{name: "project root", projectName: ".", want: "shop"},
		{name: "no name", projectName: "", want: "shop"},
		{name: "a project name", projectName: "my-shop", want: "my-shop"},
		{name: "a name with separators", projectName: "my shop;v2", want: "myshopv2"},
		{name: "only separators", projectName: "-._", want: "shop"},
		{name: "trimmed", projectName: "-My_App-2-", want: "My_App-2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/shop\n\ngo 1.22\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := GenerateLoginBroker("example.com/shop", tc.projectName, dir); err != nil {
				t.Fatalf("GenerateLoginBroker: %v", err)
			}
			b, err := os.ReadFile(filepath.Join(dir, "internal", "app", "login_broker.go"))
			if err != nil {
				t.Fatal(err)
			}
			src := string(b)
			for _, want := range []string{
				`const SessionCookieName = "` + tc.want + `_session"`,
				`const SecureSessionCookieName = "__Host-" + SessionCookieName`,
				`CookiePrefix:     "` + tc.want + `",`,
			} {
				if !strings.Contains(src, want) {
					t.Errorf("broker is missing %s", want)
				}
			}
			if strings.Contains(src, `"._session"`) || strings.Contains(src, `CookiePrefix:     ".",`) {
				t.Error(`broker names a cookie after "."`)
			}
		})
	}
}

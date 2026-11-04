package codegen

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/templates"
)

// LoginBrokerTemplateData is the data shape for the owned
// internal/app/login_broker.go scaffold (app-login-broker.go.tmpl).
type LoginBrokerTemplateData struct {
	Module string
	// Name is the project's binary name, used in the error message that
	// tells an operator which command provisions the broker credential.
	Name string
	// MiddlewareDeclaresSessionCookie reports whether the project's OWN
	// pkg/middleware/middleware.go declares SessionCookieName — the one
	// name the broker sets and the auth interceptor reads. Every project
	// scaffolded since the interceptor learned the cookie does; an older
	// one keeps a local constant so this file still compiles against the
	// middleware it actually has.
	MiddlewareDeclaresSessionCookie bool
	// ConfigType is the pkg/config type the broker reads its idp_* / oidc_*
	// settings from — the config that declares them; "" is Config.
	ConfigType string
}

// middlewareSessionCookieDecl matches the constant the scaffolded
// pkg/middleware/middleware.go declares.
var middlewareSessionCookieDecl = regexp.MustCompile(`(?m)^const SessionCookieName\b`)

// GenerateLoginBroker scaffolds internal/app/login_broker.go ONCE — the
// OWNED server half of the API-only sign-in flow, which lets an app render
// its own sign-in screen instead of redirecting the user to the identity
// provider's login pages.
//
// WHY IT IS SCAFFOLDED UNCONDITIONALLY rather than behind a config check.
// Whether a project uses this flow is decided by config at RUN time
// (idp_login_uri), and config is per-environment: the same build serves an
// environment that uses the issuer's pages and one that does not. Emitting
// the file only when some environment happens to set the field would make
// the code's existence depend on which env file was read at generate time,
// which is exactly the kind of spooky action this generator avoids.
//
// The file costs nothing when unused — nothing calls RegisterLoginBroker
// unless the project wires it — and a project that will never use it
// deletes the file, which the scaffold-once guard respects permanently.
//
// Scaffold-once, never overwritten after first emit: the user owns the
// bytes, including the security-critical handler that decides which
// credentials get checked. See the template's header for what must not be
// changed carelessly.
func GenerateLoginBroker(modulePath, projectName, projectDir string) error {
	appDir := filepath.Join(projectDir, "internal", "app")
	path := filepath.Join(appDir, "login_broker.go")
	rel := filepath.Join("internal", "app", "login_broker.go")
	if !checksums.ScaffoldOnceDecision(projectDir, rel) {
		// Never clobber an existing copy, and never resurrect a deleted
		// one. A project that uses the issuer-hosted login pages deletes
		// this file and it stays gone.
		return nil
	}
	configType, declared := configDeclaring(projectDir, brokerConfigFields)
	if !declared && GraphComposition(projectDir) {
		// No config the binary loads declares the broker's settings, so
		// it could read none of them: nothing to scaffold (providers.go
		// wires no sign-in routes). Not recorded, so declaring them later
		// births it.
		return nil
	}
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		return fmt.Errorf("create internal/app dir: %w", err)
	}
	mw, _ := os.ReadFile(filepath.Join(projectDir, "pkg", "middleware", "middleware.go"))
	content, err := templates.ProjectTemplates().Render("app-login-broker.go.tmpl", LoginBrokerTemplateData{
		Module:                          modulePath,
		Name:                            projectName,
		MiddlewareDeclaresSessionCookie: middlewareSessionCookieDecl.Match(mw),
		ConfigType:                      configType,
	})
	if err != nil {
		return fmt.Errorf("render app-login-broker.go.tmpl: %w", err)
	}
	if err := writeUserScaffold(path, content); err != nil {
		return err
	}
	checksums.RecordScaffold(projectDir, rel)
	return nil
}

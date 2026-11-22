package codegen

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/configproto"
	"github.com/reliant-labs/forge/internal/templates"
)

// LoginBrokerTemplateData is the data shape for the owned
// internal/app/login_broker.go scaffold (app-login-broker.go.tmpl).
type LoginBrokerTemplateData struct {
	Module string
	// Name is the stem of the cookie names the broker declares itself
	// (brokerCookieStem): the project's name, as pkg/middleware's
	// SessionCookieName was scaffolded from it.
	Name string
	// MiddlewareDeclaresSessionCookie reports whether the project's OWN
	// pkg/middleware/middleware.go declares SessionCookieName — the one
	// name the broker sets and the auth interceptor reads. Every project
	// scaffolded since the interceptor learned the cookie does; an older
	// one keeps a local constant so this file still compiles against the
	// middleware it actually has.
	MiddlewareDeclaresSessionCookie bool
	// MiddlewareDeclaresSecureSessionCookie reports whether it also declares
	// SecureSessionCookieName (the __Host- name it reads first). Without it
	// the broker keeps the plain name, which that middleware reads.
	MiddlewareDeclaresSecureSessionCookie bool
	// ConfigType is the pkg/config type the broker reads its idp_* / oidc_*
	// settings from — the config that declares them; "" is Config.
	ConfigType string
	// SelfSignupField reports whether that config declares auth_self_signup,
	// the opt-in that serves public sign-up outside development. A project
	// whose config predates it gets a broker that serves sign-up in
	// development only, and still compiles.
	SelfSignupField bool
	// LoginPolicyField reports whether that config declares
	// idp_login_policy, the delivered LOGIN_POLICY document (identity v2).
	// Without it the broker serves the legacy policy and still compiles.
	LoginPolicyField bool
	// AudienceField reports whether that config declares jwt_audience, the
	// identity project a self-signed-up account's signup_role is granted on.
	AudienceField bool
	// TrustedProxyHopsField reports whether that config declares
	// trusted_proxy_hops, how many proxies' X-Forwarded-For entries the
	// broker's rate limits may trust.
	TrustedProxyHopsField bool
	// ScopesField reports whether that config declares oidc_scopes, which a
	// broker keeping sessions server-side asks the issuer for (with
	// offline_access added). Without it the standard OIDC set is asked for.
	ScopesField bool
	// HandoffRedirectsField reports whether that config declares
	// handoff_redirects, the allowlist that turns the sign-in handoff
	// on. Without it the broker serves no handoff and still compiles.
	HandoffRedirectsField bool
	// NoEnvironmentField reports that config declares no environment, which
	// the broker otherwise reads to tell local development (exactly
	// "development") from a deployed environment. A process that loads the
	// broker's settings from its own command config keeps its tier on
	// another config, and may not declare the same field twice; it gets a
	// broker that runs as deployed unless the caller passes WithDevelopment.
	// Negative so the zero value renders the broker every scaffold has had.
	NoEnvironmentField bool
}

// Go names of the optional broker fields.
const (
	selfSignupConfigField  = "AuthSelfSignup"
	loginPolicyConfigField = "IdpLoginPolicy"
	audienceConfigField    = "JwtAudience"
	proxyHopsConfigField   = "TrustedProxyHops"
	scopesConfigField      = "OidcScopes"
	handoffConfigField     = "HandoffRedirects"
	environmentConfigField = "Environment"
)

// brokerCookieStem is the stem of the cookie names the broker declares
// itself (<stem>_session, and devidp's <stem>_pk, <stem>_ml, …): the project's
// name reduced to cookie-name characters. A name with none — "." is what
// filepath.Base gives for the project root generate usually runs in, and
// "._session" names nothing — falls back to forge.yaml's name, then "app",
// devidp's own default prefix.
func brokerCookieStem(projectName, projectDir, modulePath string) string {
	if stem := cookieNameChars(projectName); stem != "" {
		return stem
	}
	if stem := cookieNameChars(configproto.ProjectName(projectDir, modulePath)); stem != "" {
		return stem
	}
	return "app"
}

// cookieNameChars keeps the letters, digits, '-' and '_' of name (a subset of
// RFC 6265's cookie-name token, safe after the __Host- prefix), and "" when
// it holds no letter or digit.
func cookieNameChars(name string) string {
	var b strings.Builder
	alnum := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			alnum = true
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		}
	}
	if !alnum {
		return ""
	}
	return strings.Trim(b.String(), "-_")
}

// middlewareSessionCookieDecl matches the constant the scaffolded
// pkg/middleware/middleware.go declares.
var (
	middlewareSessionCookieDecl       = regexp.MustCompile(`(?m)^const SessionCookieName\b`)
	middlewareSecureSessionCookieDecl = regexp.MustCompile(`(?m)^const SecureSessionCookieName\b`)
)

// LoginBrokerCookieMismatch is the warning for an owned login broker that
// sets the session cookie by its PLAIN name while the project's middleware
// reads only the __Host- name outside development (forge#2276): sign-in
// answers 200 and then every rpc is 401 in every deployed env, with nothing
// saying why. It is what an upgrade from before the __Host- cookie leaves
// behind when pkg/middleware is adopted and the owned broker is not. "" when
// there is no broker, the middleware has no __Host- name, or the broker uses
// it.
func LoginBrokerCookieMismatch(projectDir string) string {
	mw, err := os.ReadFile(filepath.Join(projectDir, "pkg", "middleware", "middleware.go"))
	if err != nil || !middlewareSecureSessionCookieDecl.Match(mw) {
		return ""
	}
	broker, err := os.ReadFile(filepath.Join(projectDir, "internal", "app", "login_broker.go"))
	if err != nil || !bytes.Contains(broker, []byte("SessionCookieName")) || bytes.Contains(broker, []byte("SecureSessionCookieName")) {
		return ""
	}
	return "internal/app/login_broker.go sets the session cookie by its plain name (SessionCookieName), but pkg/middleware reads only " +
		"SecureSessionCookieName (the __Host- name) outside development: deployed sign-in succeeds and every rpc after it is 401. " +
		"Set `SecureSessionCookieName` wherever the broker sets, reads or clears the session cookie on a secure origin " +
		"(declare `SecureSessionCookieName = \"__Host-\" + SessionCookieName` in pkg/middleware and use it when the request is secure), " +
		"or delete login_broker.go to have forge scaffold the current one"
}

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
//
// The bool reports whether this call created the file: false when the
// scaffold-once guard declined (the file exists, or was deleted on purpose)
// or no config declares the broker's settings. Callers print a "Scaffolded"
// line only on true; one printed on false is a claim that a file exists
// which does not.
func GenerateLoginBroker(modulePath, projectName, projectDir string) (bool, error) {
	appDir := filepath.Join(projectDir, "internal", "app")
	path := filepath.Join(appDir, "login_broker.go")
	rel := filepath.Join("internal", "app", "login_broker.go")
	if !checksums.ScaffoldOnceDecision(projectDir, rel) {
		// Never clobber an existing copy, and never resurrect a deleted
		// one. A project that uses the issuer-hosted login pages deletes
		// this file and it stays gone.
		return false, nil
	}
	configType, declared := configDeclaring(projectDir, brokerConfigFields)
	if !declared && GraphComposition(projectDir) {
		// No config the binary loads declares the broker's settings, so
		// it could read none of them: nothing to scaffold (providers.go
		// wires no sign-in routes). Not recorded, so declaring them later
		// births it.
		return false, nil
	}
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		return false, fmt.Errorf("create internal/app dir: %w", err)
	}
	declares := func(field string) bool {
		in, ok := configDeclaring(projectDir, append(append([]string{}, brokerConfigFields...), field))
		return ok && in == configType
	}
	mw, _ := os.ReadFile(filepath.Join(projectDir, "pkg", "middleware", "middleware.go"))
	content, err := templates.ProjectTemplates().Render("app-login-broker.go.tmpl", LoginBrokerTemplateData{
		Module:                                modulePath,
		Name:                                  brokerCookieStem(projectName, projectDir, modulePath),
		MiddlewareDeclaresSessionCookie:       middlewareSessionCookieDecl.Match(mw),
		MiddlewareDeclaresSecureSessionCookie: middlewareSecureSessionCookieDecl.Match(mw),
		ConfigType:                            configType,
		SelfSignupField:                       declares(selfSignupConfigField),
		LoginPolicyField:                      declares(loginPolicyConfigField),
		AudienceField:                         declares(audienceConfigField),
		TrustedProxyHopsField:                 declares(proxyHopsConfigField),
		ScopesField:                           declares(scopesConfigField),
		HandoffRedirectsField:                 declares(handoffConfigField),
		NoEnvironmentField:                    !declares(environmentConfigField),
	})
	if err != nil {
		return false, fmt.Errorf("render app-login-broker.go.tmpl: %w", err)
	}
	if err := writeUserScaffold(path, content); err != nil {
		return false, err
	}
	checksums.RecordScaffold(projectDir, rel)
	return true, nil
}

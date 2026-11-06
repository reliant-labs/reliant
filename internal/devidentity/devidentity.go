// Package devidentity is `forge env up`'s half of managed identity: it
// converges an env whose identity runs on the env's own host-run Zitadel
// (forge.IdentityOnHost) with the SAME library the control plane runs against
// hosted instances, pkg/identity.
//
// What differs from the hosted path is only where things are written:
//
//   - the non-secret outputs go to .forge/identity/<name>.json;
//   - every secret (the broker token, a web client secret, machine keys) goes
//     to the env's local secret store, under the env-var name each consumer's
//     forge.IdentityRef resolves to, so a host-run process is handed
//     jwt_issuer, jwt_audience, oidc_*, idp_base and idp_broker_token with
//     nothing copied by hand;
//   - the env lands in the Zitadel server's DEFAULT instance. The dev IdP
//     answers only to localhost, so there is no per-instance hostname and no
//     System API: that path is covered by the docker smoke against the real
//     image instead.
//
// The package performs no process management and reads no KCL. The caller
// resolves everything it needs into [Input].
package devidentity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/identity"
	"github.com/reliant-labs/forge/pkg/release"
)

// Ref is one secret-backed env var of a host process: the shape of
// forge.IdentityRef after the render lowered it for a host-run identity.
type Ref struct {
	// EnvName is the variable the process reads, and so the secret-store key.
	EnvName string
	// SecretName and SecretKey are what the render lowered the reference to:
	// Secret forge-identity-<name>, key ISSUER, DEV_API_CLIENT_ID, ...
	SecretName string
	SecretKey  string
}

// Input is everything one convergence needs.
type Input struct {
	// Project is the Bundle's project, the identity project's name unless the
	// declaration sets project_name.
	Project string
	// Identity is the rendered declaration. Its runtime must be "host".
	Identity *release.ShapeIdentity

	// BaseURL is the origin the dev IdP is dialled (and registered) at, e.g.
	// http://localhost:8080. It is also the issuer.
	BaseURL string
	// PATPath is the bootstrap service account's personal access token file.
	PATPath string

	// FrontendOrigin is the browser-facing origin (http://localhost:3000 plus
	// the frontend's base path). The sign-in and callback routes are derived
	// from it. Empty falls back to WorkloadOrigins.
	FrontendOrigin string
	// WorkloadOrigins maps a workload to the origin it listens on, for an
	// application whose sign-in route has no frontend to live on.
	WorkloadOrigins map[string]string

	// Refs are the identity-backed env vars of the env's host workloads.
	Refs []Ref
	// Secret resolves a value from the env's secret store by name (seed-user
	// and SMTP passwords, social client secrets).
	Secret func(name string) (string, bool)

	// MailpitPort is the SMTP port of the env's Mailpit (the HostInfra the
	// declaration's dev_mail names); zero when the env has none. The dev IdP
	// then mails invitations, verification codes and magic links to it,
	// unless the declaration brings its own smtp.
	MailpitPort int

	// StorePath is the local secret store credentials are written to.
	StorePath string
	// OutputsPath is the non-secret outputs file, .forge/identity/<name>.json.
	OutputsPath string
	// KeysDir is the library's own memory of the credentials it issued.
	KeysDir string

	Logger *slog.Logger
}

// readyTimeout bounds the wait for the IdP to answer: a first boot runs the
// whole declarative setup before it serves.
const readyTimeout = 3 * time.Minute

// Result is what a convergence did.
type Result struct {
	Report *identity.Report
	// StoreKeys are the secret-store keys written or refreshed this pass.
	StoreKeys []string
	// Warnings are things the pass left alone on purpose.
	Warnings []string
	// SocialCallbackURL is the redirect URI to register on the dev OAuth
	// app of each social provider; empty when none is declared.
	SocialCallbackURL string
	// Mail is the LOGIN_POLICY mail state: dev (Mailpit), smtp or none.
	Mail string
}

// Converge makes the dev instance match the declaration and publishes its
// outputs.
func Converge(ctx context.Context, in Input) (*Result, error) {
	spec, warnings, err := Spec(in)
	if err != nil {
		return nil, err
	}
	mail := MailState(in)
	keys := identity.DirKeyStore{Dir: in.KeysDir}
	files := identity.FileOutputs{Path: in.OutputsPath}
	rec, err := identity.New(identity.Options{
		BaseURL:    in.BaseURL,
		Issuer:     in.BaseURL,
		Credential: identity.PATFile(in.PATPath),
		Keys:       keys,
		Outputs: identity.OutputsFunc(func(ctx context.Context, out identity.Output) error {
			return files.Publish(ctx, withMail(out, mail))
		}),
		AllowSeedUsers: true,
		SignInCodes:    hasDeclaredMembers(spec),
		ReadyTimeout:   readyTimeout,
		Logger:         in.Logger,
	})
	if err != nil {
		return nil, err
	}
	res := &Result{Warnings: warnings, Mail: mail}
	if len(spec.IdPs) > 0 {
		res.SocialCallbackURL = strings.TrimRight(in.BaseURL, "/") + "/idps/callback"
	}
	rep, err := rec.Reconcile(ctx, spec)
	res.Report = rep
	if err != nil {
		return res, fmt.Errorf("converge identity %q: %w", in.Identity.Name, err)
	}

	values, err := StoreValues(in, withMail(rep.Output, mail), func(user string) ([]byte, error) {
		return keys.Load(ctx, identity.MachineKeyEntry(user))
	})
	if err != nil {
		return res, err
	}
	if res.StoreKeys, err = writeStore(in.StorePath, values); err != nil {
		return res, err
	}
	return res, nil
}

// MailState is how the dev instance's mail is delivered, as the LOGIN_POLICY
// document reports it: the declaration's own smtp, else the env's Mailpit,
// else nothing.
func MailState(in Input) string {
	switch {
	case in.Identity != nil && in.Identity.SMTP != nil:
		return identity.MailSMTP
	case in.MailpitPort > 0:
		return identity.MailDev
	default:
		return identity.MailNone
	}
}

// withMail fills the login policy document's mail state, which only the
// caller of the library knows.
func withMail(out identity.Output, mail string) identity.Output {
	if out.LoginPolicy != nil {
		d := out.LoginPolicy.WithMail(mail)
		out.LoginPolicy = &d
	}
	return out
}

func hasDeclaredMembers(s identity.Spec) bool {
	for _, r := range s.Roles {
		if r.Declared && len(r.Members) > 0 {
			return true
		}
	}
	return false
}

// Spec derives the library's declaration from the rendered shape. Redirect,
// logout and login URIs are DERIVED from the env's origins unless the
// declaration gives a literal.
func Spec(in Input) (identity.Spec, []string, error) {
	id := in.Identity
	if id == nil {
		return identity.Spec{}, nil, errors.New("devidentity: the env declares no identity")
	}
	if id.Runtime != release.IdentityRuntimeHost {
		return identity.Spec{}, nil, fmt.Errorf("devidentity: identity %q runs on %q, not on the host IdP", id.Name, id.Runtime)
	}
	var warnings []string
	spec := identity.Spec{Project: identity.ProjectSpec{Name: firstNonEmpty(id.ProjectName, in.Project)}}

	for _, r := range id.Roles {
		spec.Roles = append(spec.Roles, identity.Role{
			Key: r.Key, DisplayName: r.DisplayName, Group: r.Group,
			Declared: r.MembersFrom == release.IdentityMembersDeclared,
			Members:  r.Members,
		})
	}

	apps, errs := applicationsSpec(in, id)
	spec.Applications = apps

	for _, m := range id.MachineUsers {
		spec.MachineUsers = append(spec.MachineUsers, identity.MachineUser{
			Username: m.Name, Name: m.Name, Roles: m.Roles, Key: true,
		})
	}

	for _, u := range id.SeedUsers {
		if u.PasswordSecret == "" {
			errs = append(errs, fmt.Errorf("seed user %q: declare password_secret, the store entry holding the password", u.Email))
			continue
		}
		pw, ok := secret(in, u.PasswordSecret)
		if !ok {
			errs = append(errs, fmt.Errorf("seed user %q: secret %s is not in the env's store: `forge secret set --env <env> %s`", u.Email, u.PasswordSecret, u.PasswordSecret))
			continue
		}
		spec.SeedUsers = append(spec.SeedUsers, identity.SeedUser{Email: u.Email, Password: pw, Roles: u.Roles})
	}

	var fromName, replyTo, fromLocal string
	if e := id.Email; e != nil {
		fromName, replyTo, fromLocal = e.FromName, e.ReplyTo, e.FromLocal
	}
	smtp, smtpWarnings := smtpSpec(in, id, spec.Project.Name, fromName, replyTo, fromLocal)
	spec.SMTP = smtp
	warnings = append(warnings, smtpWarnings...)
	mailAvailable := spec.SMTP != nil

	// The sign-in origin the invitation and sign-in links land on.
	loginOrigin := in.FrontendOrigin
	for _, a := range id.Applications {
		if loginOrigin == "" && a.Kind == release.IdentityAppNativeBroker {
			loginOrigin = in.WorkloadOrigins[a.Workload]
		}
	}

	if len(id.Owners) > 0 {
		var invite *identity.Invite
		if mailAvailable && loginOrigin != "" {
			invite = &identity.Invite{
				URLTemplate:     loginOrigin + "/auth/invite?userID={{.UserID}}&code={{.Code}}&orgID={{.OrgID}}",
				ApplicationName: firstNonEmpty(fromName, in.Project),
			}
		}
		var roles []string
		if id.OwnerRole != "" {
			roles = []string{id.OwnerRole}
		}
		for _, email := range id.Owners {
			spec.Owners = append(spec.Owners, identity.Owner{Email: email, Roles: roles, Invite: invite})
		}
	}

	for _, s := range id.Social {
		idp, warn := socialIDP(in, s)
		if warn != "" {
			warnings = append(warnings, warn)
			continue
		}
		spec.IdPs = append(spec.IdPs, idp)
	}

	if id.DeclaresLoginPolicy() {
		si := identity.SignIn{Signup: identity.SignupMode(id.SignupMode())}
		if s := id.Signup; s != nil {
			si.SignupDomains, si.SignupRole = s.Domains, s.Role
		}
		for _, m := range id.SignInMethods() {
			si.Methods = append(si.Methods, identity.Method(m))
		}
		// A passkey is bound to the host the browser is on. In dev that is
		// the loopback host, whatever relying party the declaration names
		// for its hosted envs.
		if containsMethod(si.Methods, identity.MethodPasskey) {
			si.PasskeyRPID = originHost(loginOrigin)
		}
		if len(si.Methods) == 0 && len(spec.IdPs) == 0 {
			errs = append(errs, errors.New("social sign-in is the only method declared, and no provider's secrets are in the env's store (see the warnings above), so nobody could sign in"))
		}
		login := identity.LoginPolicyFor(si, len(spec.IdPs) > 0)
		spec.Policies.Login = &login
		spec.SignIn = &si
	}

	if len(errs) > 0 {
		return identity.Spec{}, warnings, errors.Join(errs...)
	}
	if err := spec.Validate(); err != nil {
		return identity.Spec{}, warnings, fmt.Errorf("devidentity: %w", err)
	}
	return spec, warnings, nil
}

// applicationsSpec derives each declared application's client, filling the
// redirect, logout and sign-in URIs from the env's origins unless the
// declaration gives a literal.
func applicationsSpec(in Input, id *release.ShapeIdentity) (apps []identity.Application, errs []error) {
	for _, a := range id.Applications {
		origin := in.FrontendOrigin
		if origin == "" {
			origin = in.WorkloadOrigins[a.Workload]
		}
		app := identity.Application{
			Name:           a.Name,
			DevMode:        true,
			RedirectURIs:   a.RedirectURIs,
			PostLogoutURIs: a.LogoutURIs,
			LoginURI:       a.LoginURI,
		}
		if len(app.RedirectURIs) == 0 {
			if origin == "" {
				errs = append(errs, fmt.Errorf("application %q: no frontend origin and workload %q binds no port, so its redirect URI cannot be derived; set redirect_uris", a.Name, a.Workload))
				continue
			}
			app.RedirectURIs = []string{origin + "/auth/callback"}
		}
		if len(app.PostLogoutURIs) == 0 && origin != "" {
			app.PostLogoutURIs = []string{origin}
		}
		switch a.Kind {
		case release.IdentityAppNativeBroker:
			app.Kind = identity.AppNativeBroker
			if app.LoginURI == "" {
				if origin == "" {
					errs = append(errs, fmt.Errorf("application %q: no frontend origin, so its sign-in route cannot be derived; set login_uri", a.Name))
					continue
				}
				app.LoginURI = origin + "/auth/sign-in"
			}
		case release.IdentityAppOIDCWeb:
			app.Kind = identity.AppWeb
		default:
			errs = append(errs, fmt.Errorf("application %q: unknown kind %q", a.Name, a.Kind))
			continue
		}
		apps = append(apps, app)
	}
	return apps, errs
}

// smtpSpec is the dev IdP's mail relay: the declaration's own smtp, else the
// env's host Mailpit, else none.
func smtpSpec(in Input, id *release.ShapeIdentity, project, fromName, replyTo, fromLocal string) (smtp *identity.SMTP, warnings []string) {
	switch s := id.SMTP; {
	case s != nil:
		pw, _ := secret(in, s.PasswordSecret)
		if pw == "" {
			warnings = append(warnings, fmt.Sprintf("smtp: secret %s is not in the env's store, so the relay is configured without a password", s.PasswordSecret))
		}
		smtp = &identity.SMTP{
			Host: fmt.Sprintf("%s:%d", s.Host, s.Port), User: s.User, Password: pw,
			SenderAddress: s.Sender, SenderName: fromName, ReplyTo: replyTo,
		}
	case in.MailpitPort > 0:
		// Mailpit on loopback: plain SMTP, no credentials. The sender is a
		// name under .localhost, which no real mailbox can own.
		smtp = &identity.SMTP{
			Host:          fmt.Sprintf("127.0.0.1:%d", in.MailpitPort),
			SenderAddress: firstNonEmpty(fromLocal, "no-reply") + "@" + mailDomain(project),
			SenderName:    firstNonEmpty(fromName, in.Project),
			ReplyTo:       replyTo,
		}
	}
	return smtp, warnings
}

// socialIDP resolves one social provider's client from the env's store. A
// provider whose secret is missing is left out with a warning rather than
// failing the whole pass: dev sign-in through the other methods still works.
func socialIDP(in Input, s release.ShapeIdentitySocial) (identity.IdP, string) {
	t := identity.IdPType(s.Type)
	clientID := s.ClientID
	if s.ClientIDSecret != "" {
		v, ok := secret(in, s.ClientIDSecret)
		if !ok {
			return identity.IdP{}, missingSocialSecret(s.Type, s.ClientIDSecret)
		}
		clientID = v
	}
	clientSecret, ok := secret(in, s.ClientSecretSecret)
	if !ok {
		return identity.IdP{}, missingSocialSecret(s.Type, s.ClientSecretSecret)
	}
	return identity.IdP{Name: identity.IdPName(t), Type: t, ClientID: clientID, ClientSecret: clientSecret, Scopes: s.Scopes}, ""
}

func missingSocialSecret(typ, name string) string {
	return fmt.Sprintf("social %s: secret %s is not in the env's store, so %s sign-in is off in this env. Create a DEV OAuth app (its callback is printed below), then `forge secret set --env <env> %s`", typ, name, typ, name)
}

func containsMethod(ms []identity.Method, m identity.Method) bool {
	for _, x := range ms {
		if x == m {
			return true
		}
	}
	return false
}

// originHost is the host of an origin URL, "localhost" when it has none.
func originHost(origin string) string {
	if u, err := url.Parse(origin); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return "localhost"
}

// mailDomain is the dev sender's domain: the project under .localhost.
func mailDomain(project string) string {
	label := strings.Trim(nonAlnum.ReplaceAllString(strings.ToUpper(project), "-"), "-")
	if label == "" {
		label = "forge"
	}
	return strings.ToLower(label) + ".localhost"
}

func secret(in Input, name string) (string, bool) {
	if in.Secret == nil {
		return "", false
	}
	return in.Secret(name)
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

var nonAlnum = regexp.MustCompile(`[^A-Z0-9]+`)

// Token folds a name the way the KCL render does for a delivered key: upper
// case, each run of non-alphanumerics becoming one underscore. "dev:api" is
// DEV_API.
func Token(s string) string { return nonAlnum.ReplaceAllString(strings.ToUpper(s), "_") }

// MachineKeyStoreKey is the secret-store key a machine user's key document is
// kept under: <USER>_MACHINE_KEY.
func MachineKeyStoreKey(user string) string { return Token(user) + "_MACHINE_KEY" }

// OutputKeys is the identity's outputs under the delivered key names the KCL
// render lowers an IdentityRef to (ISSUER, AUDIENCE, <APP>_CLIENT_ID, ...).
func OutputKeys(out identity.Output) map[string]string {
	kv := map[string]string{
		"ISSUER":   out.Issuer,
		"AUDIENCE": out.Audience,
		"SCOPES":   strings.Join(out.Scopes, " "),
	}
	if len(out.JWKS) > 0 {
		kv["JWKS"] = string(out.JWKS)
	}
	if out.LoginPolicy != nil {
		if raw, err := json.Marshal(out.LoginPolicy); err == nil {
			kv[release.IdentityLoginPolicyKey] = string(raw)
		}
	}
	for name, a := range out.Apps {
		t := Token(name)
		kv[t+"_CLIENT_ID"] = a.ClientID
		if len(a.RedirectURIs) > 0 {
			kv[t+"_REDIRECT_URI"] = a.RedirectURIs[0]
		}
		if a.LoginURI != "" {
			kv[t+"_LOGIN_URI"] = a.LoginURI
		}
		if a.BrokerToken != "" {
			kv[t+"_BROKER_TOKEN"] = a.BrokerToken
		}
		if a.ClientSecret != "" {
			kv[t+"_CLIENT_SECRET"] = a.ClientSecret
		}
	}
	return kv
}

// StoreValues maps the outputs onto secret-store keys. Each host workload's
// identity-backed env var gets the value its reference points at, under the
// variable's own name, because that is the key a host launch resolves a
// secret reference by. A reference whose output does not exist this pass
// (a snapshot that could not be taken) is simply absent.
func StoreValues(in Input, out identity.Output, machineKey func(user string) ([]byte, error)) (map[string]string, error) {
	kv := OutputKeys(out)
	prefix := release.IdentitySecretPrefix + in.Identity.Name
	values := map[string]string{}
	for _, r := range in.Refs {
		if r.SecretName != prefix {
			continue
		}
		if v, ok := kv[r.SecretKey]; ok && v != "" {
			values[r.EnvName] = v
		}
	}
	for _, m := range in.Identity.MachineUsers {
		raw, err := machineKey(m.Name)
		if err != nil {
			return nil, fmt.Errorf("read the key of machine user %q: %w", m.Name, err)
		}
		if len(raw) > 0 {
			values[MachineKeyStoreKey(m.Name)] = string(raw)
		}
	}
	return values, nil
}

// writeStore sets each key in the local store, touching the file only when a
// value actually changed. The store keeps its comments and every other key.
func writeStore(path string, values map[string]string) ([]string, error) {
	store := secretFile{Path: path}
	var written []string
	for _, k := range sortedKeys(values) {
		cur, err := store.get(k)
		if err != nil {
			return written, fmt.Errorf("read %s: %w", path, err)
		}
		if cur == values[k] {
			continue
		}
		if err := store.set(k, values[k]); err != nil {
			return written, fmt.Errorf("write %s to %s: %w", k, path, err)
		}
		written = append(written, k)
	}
	return written, nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

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
	"errors"
	"fmt"
	"log/slog"
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
	// and SMTP passwords).
	Secret func(name string) (string, bool)

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
}

// Converge makes the dev instance match the declaration and publishes its
// outputs.
func Converge(ctx context.Context, in Input) (*Result, error) {
	spec, warnings, err := Spec(in)
	if err != nil {
		return nil, err
	}
	keys := identity.DirKeyStore{Dir: in.KeysDir}
	rec, err := identity.New(identity.Options{
		BaseURL:        in.BaseURL,
		Issuer:         in.BaseURL,
		Credential:     identity.PATFile(in.PATPath),
		Keys:           keys,
		Outputs:        identity.FileOutputs{Path: in.OutputsPath},
		AllowSeedUsers: true,
		SignInCodes:    hasDeclaredMembers(spec),
		ReadyTimeout:   readyTimeout,
		Logger:         in.Logger,
	})
	if err != nil {
		return nil, err
	}
	rep, err := rec.Reconcile(ctx, spec)
	if err != nil {
		return &Result{Report: rep, Warnings: warnings}, fmt.Errorf("converge identity %q: %w", in.Identity.Name, err)
	}

	values, err := StoreValues(in, rep.Output, func(user string) ([]byte, error) {
		return keys.Load(ctx, identity.MachineKeyEntry(user))
	})
	if err != nil {
		return &Result{Report: rep, Warnings: warnings}, err
	}
	written, err := writeStore(in.StorePath, values)
	if err != nil {
		return &Result{Report: rep, Warnings: warnings}, err
	}
	return &Result{Report: rep, StoreKeys: written, Warnings: warnings}, nil
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

	var errs []error
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
		spec.Applications = append(spec.Applications, app)
	}

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

	if s := id.SMTP; s != nil {
		pw, _ := secret(in, s.PasswordSecret)
		if pw == "" {
			warnings = append(warnings, fmt.Sprintf("smtp: secret %s is not in the env's store, so the relay is configured without a password", s.PasswordSecret))
		}
		spec.SMTP = &identity.SMTP{
			Host: fmt.Sprintf("%s:%d", s.Host, s.Port), User: s.User, Password: pw,
			SenderAddress: s.Sender,
		}
	}

	if len(errs) > 0 {
		return identity.Spec{}, warnings, errors.Join(errs...)
	}
	if err := spec.Validate(); err != nil {
		return identity.Spec{}, warnings, fmt.Errorf("devidentity: %w", err)
	}
	return spec, warnings, nil
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

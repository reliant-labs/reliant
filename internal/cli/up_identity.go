package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/devidentity"
	"github.com/reliant-labs/forge/internal/hostinfra"
	"github.com/reliant-labs/forge/internal/secrets"
	"github.com/reliant-labs/forge/pkg/identity"
	"github.com/reliant-labs/forge/pkg/release"
)

// hostIdentity returns the env's managed identity when it runs on the env's
// own host-run IdP (forge.IdentityOnHost), else nil.
func hostIdentity(e *KCLEntities) *release.ShapeIdentity {
	if e == nil || e.Identity == nil || e.Identity.Runtime != release.IdentityRuntimeHost {
		return nil
	}
	return e.Identity
}

// withoutHostIdentityRefs drops the secret references a host-run identity
// FILLS: `forge env up` writes those store keys itself, after the IdP is up,
// so demanding them from the operator beforehand would be asking for values
// that cannot exist yet.
func withoutHostIdentityRefs(e *KCLEntities, refs []secrets.SecretRef) []secrets.SecretRef {
	id := hostIdentity(e)
	if id == nil {
		return refs
	}
	owned := release.IdentitySecretPrefix + id.Name
	out := refs[:0:0]
	for _, r := range refs {
		if r.SecretName != owned {
			out = append(out, r)
		}
	}
	return out
}

// hostIdentityRefs are the identity-backed env vars of the env's host
// workloads: what the render lowered each forge.IdentityRef to.
func hostIdentityRefs(e *KCLEntities, id *release.ShapeIdentity) []devidentity.Ref {
	owned := release.IdentitySecretPrefix + id.Name
	var refs []devidentity.Ref
	for i := range e.Workloads {
		if !e.Workloads[i].OnRuntime(RuntimeHost) {
			continue
		}
		for _, r := range secretRefsForService(&e.Workloads[i]) {
			if r.SecretName == owned {
				refs = append(refs, devidentity.Ref{EnvName: r.EnvName, SecretName: r.SecretName, SecretKey: r.SecretKey})
			}
		}
	}
	return refs
}

// devIdentityInput resolves everything one convergence needs from the
// rendered env. It performs no I/O beyond reading the project's own
// declaration, so it is testable without an IdP.
func devIdentityInput(e *KCLEntities, id *release.ShapeIdentity, projectDir string, prov secrets.Provider) (devidentity.Input, error) {
	zitadel, err := hostZitadel(e, id)
	if err != nil {
		return devidentity.Input{}, err
	}
	if e.SecretProvider == nil || e.SecretProvider.Type != "file" {
		return devidentity.Input{}, fmt.Errorf("identity %q keeps its credentials in the env's local secret store: declare `secret_provider = forge.FileSecrets {path = \"secrets/%s.yaml\"}` on the Bundle", id.Name, e.Env)
	}
	store := e.SecretProvider.Path
	if store == "" {
		store = filepath.Join("secrets", e.Env+".yaml")
	}
	// THIS checkout's store, never the primary checkout's: a worktree runs its
	// own IdP, so its tokens are its own, and writing them into the shared
	// store would hand every other worktree a credential its IdP rejects.
	if !filepath.IsAbs(store) {
		store = filepath.Join(projectDir, store)
	}

	in := devidentity.Input{
		Project:         e.Project,
		Identity:        id,
		BaseURL:         fmt.Sprintf("http://localhost:%d", zitadel.Port),
		PATPath:         idpPATLocations(zitadel, projectDir).Checkout,
		WorkloadOrigins: map[string]string{},
		Refs:            hostIdentityRefs(e, id),
		StorePath:       store,
		OutputsPath:     filepath.Join(projectDir, ".forge", "identity", id.Name+".json"),
		KeysDir:         filepath.Join(projectDir, ".forge", "identity", id.Name+".keys"),
		Logger:          slog.New(slog.DiscardHandler),
	}
	if prov != nil {
		in.Secret = func(name string) (string, bool) {
			v, ok := prov.Resolve(name)
			return v, ok && v != ""
		}
	}
	for _, f := range e.Frontends {
		if f.Runtime.Type != "" && f.Runtime.Type != FrontendRuntimeHost {
			continue
		}
		if f.Port > 0 {
			in.FrontendOrigin = fmt.Sprintf("http://localhost:%d%s", f.Port, strings.TrimSuffix(f.BasePath, "/"))
			break
		}
	}
	for _, w := range e.Workloads {
		if w.OnRuntime(RuntimeHost) {
			if ports := w.HostPorts(); len(ports) > 0 {
				in.WorkloadOrigins[w.Name] = fmt.Sprintf("http://localhost:%d", ports[0])
			}
		}
	}
	mail, err := devMailInfra(e, id)
	if err != nil {
		return devidentity.Input{}, err
	}
	if mail != nil {
		in.MailpitPort = mail.Port
	}
	return in, nil
}

// hostZitadel is the env's one zitadel HostInfra, which a host-run identity
// converges on.
func hostZitadel(e *KCLEntities, id *release.ShapeIdentity) (*HostInfraEntity, error) {
	var zitadel *HostInfraEntity
	for i := range e.Infra {
		if e.Infra[i].Engine == "zitadel" {
			if zitadel != nil {
				return nil, fmt.Errorf("identity %q runs on the host IdP, but the env declares two zitadel infra entries (%s, %s)", id.Name, zitadel.Name, e.Infra[i].Name)
			}
			zitadel = &e.Infra[i]
		}
	}
	if zitadel == nil {
		return nil, fmt.Errorf("identity %q runs on the host IdP, but the env declares no forge.HostInfra with engine \"zitadel\"", id.Name)
	}
	return zitadel, nil
}

// idpPATLocations is where the host IdP's bootstrap PAT is written and looked
// for: the same resolution the IdP's start uses (hostinfra.Spec), from the
// same declaration.
func idpPATLocations(zitadel *HostInfraEntity, projectDir string) hostinfra.IDPPATLocations {
	return hostinfra.Spec{
		IDPPATPath:      zitadel.IDPPATPath,
		IDPDatabase:     zitadel.IDPDatabase,
		IDPDatabasePort: zitadel.IDPDatabasePort,
	}.IDPPATLocations(projectDir)
}

// convergeDevIdentityFn is devidentity.Converge, swappable so a test needs no
// IdP.
var convergeDevIdentityFn = devidentity.Converge

// convergeWithIDPPAT runs the convergence with the bootstrap PAT it finds,
// and keeps the PAT's two homes coherent afterwards:
//
//   - the shared file (beside the IdP database, where Zitadel writes it) is
//     tried first. Once it has authenticated, the checkout's copy is
//     refreshed from it, for anything reading .forge/idp/pat.txt;
//   - the checkout's copy is the fallback: a database an older forge
//     initialized has its token only there. Once THAT has authenticated it is
//     published to the shared file, so the next checkout of this database
//     finds it. Only a token that authenticated is published: the shared file
//     is trusted by every checkout.
//
// It also retries with the checkout's copy when a shared token is refused
// and the two differ, which recovers a database an older forge re-initialized
// after this one wrote the shared file.
//
// pat is "" when neither file exists; the caller reports that.
func convergeWithIDPPAT(ctx context.Context, in devidentity.Input, loc hostinfra.IDPPATLocations) (res *devidentity.Result, notes []string, err error) {
	in.PATPath = loc.Find()
	if in.PATPath == "" {
		return nil, nil, errNoIDPPAT
	}
	res, err = convergeDevIdentityFn(ctx, in)
	if err != nil && in.PATPath == loc.Shared && differentFiles(loc.Shared, loc.Checkout) {
		in.PATPath = loc.Checkout
		res, err = convergeDevIdentityFn(ctx, in)
	}
	if err != nil {
		return nil, nil, err
	}
	switch in.PATPath {
	case loc.Shared:
		if mErr := loc.MirrorToCheckout(); mErr != nil {
			notes = append(notes, fmt.Sprintf("could not refresh %s from %s: %v", loc.Checkout, loc.Shared, mErr))
		}
	case loc.Checkout:
		if loc.Shared == "" {
			break
		}
		if pErr := loc.PublishShared(loc.Checkout); pErr != nil {
			notes = append(notes, fmt.Sprintf("could not publish the IdP token to %s, where other checkouts of this IdP database look: %v", loc.Shared, pErr))
		} else {
			notes = append(notes, fmt.Sprintf("published the IdP token to %s, where every checkout whose IdP uses this database finds it", loc.Shared))
		}
	}
	return res, notes, nil
}

var errNoIDPPAT = errors.New("no IdP personal access token found")

// differentFiles reports whether both files exist and hold different bytes.
func differentFiles(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ab, aErr := os.ReadFile(a) // #nosec G304 -- forge's own PAT locations
	bb, bErr := os.ReadFile(b) // #nosec G304 -- forge's own PAT locations
	return aErr == nil && bErr == nil && len(bb) > 0 && !bytes.Equal(ab, bb)
}

// missingIDPPATMessage explains a host IdP whose bootstrap PAT is nowhere
// forge looks, and the two ways out. The usual cause: the IdP's database was
// initialized by another checkout (or by an older forge, which kept the
// token in that checkout), so this checkout's IdP boot found the instance
// already there and Zitadel wrote no token.
func missingIDPPATMessage(id string, zitadel *HostInfraEntity, loc hostinfra.IDPPATLocations, env string) string {
	var b strings.Builder
	db := firstNonEmptyStr(zitadel.IDPDatabase, "zitadel")
	fmt.Fprintf(&b, "[up] identity %s: no personal access token for the dev IdP, so forge cannot authenticate to converge it.\n", id)
	fmt.Fprintf(&b, "  Zitadel writes that token ONCE, when it initializes its database (%q on localhost:%d), and this checkout's\n", db, zitadel.IDPDatabasePort)
	b.WriteString("  IdP found that database already initialized: by another checkout, or by an older forge that kept the token\n")
	b.WriteString("  in that checkout's .forge/idp/pat.txt.\n")
	fmt.Fprintf(&b, "  Looked in: %s\n", strings.Join(loc.Searched(), ", "))
	if loc.Shared != "" {
		b.WriteString("  Fix, either:\n")
		fmt.Fprintf(&b, "    - run `forge env up %s` once in the checkout that initialized it: this forge publishes its token to\n", env)
		fmt.Fprintf(&b, "      %s, where every checkout looks (or copy that checkout's .forge/idp/pat.txt there yourself);\n", loc.Shared)
	} else {
		b.WriteString("  Fix, either:\n")
		fmt.Fprintf(&b, "    - copy the token from the checkout that initialized it to %s;\n", loc.Checkout)
	}
	b.WriteString("    - or start the IdP over: stop every stack whose IdP uses that database, drop it, and re-run `forge env up`.\n")
	fmt.Fprintf(&b, "      psql -h localhost -p %d -U %s -d postgres -c 'DROP DATABASE \"%s\"'\n", zitadel.IDPDatabasePort, firstNonEmptyStr(zitadel.User, "postgres"), db)
	b.WriteString("      This deletes the dev IdP's users and app registrations; forge re-creates what the env declares.\n")
	b.WriteString("[up] sign-in is unavailable this run; everything else still starts.\n")
	return b.String()
}

// devMailInfra is the Mailpit the host-run identity's mail goes to: the
// HostInfra the declaration names (dev_mail), else the env's only mailpit,
// else none.
func devMailInfra(e *KCLEntities, id *release.ShapeIdentity) (*HostInfraEntity, error) {
	var found []*HostInfraEntity
	for i := range e.Infra {
		if e.Infra[i].Engine != hostinfra.EngineMailpit {
			continue
		}
		if id.DevMail == e.Infra[i].Name {
			return &e.Infra[i], nil
		}
		found = append(found, &e.Infra[i])
	}
	switch {
	case id.DevMail != "":
		return nil, fmt.Errorf("identity %q sends its mail to %q, which is not a forge.HostInfra with engine \"mailpit\" in this env", id.Name, id.DevMail)
	case len(found) == 1:
		return found[0], nil
	case len(found) > 1:
		return nil, fmt.Errorf("identity %q: the env declares %d mailpit infra entries; name one with `runtime = forge.IdentityOnHost {mail = \"<name>\"}`", id.Name, len(found))
	}
	return nil, nil
}

// convergeHostIdentity is the identity step of `forge env up`: it runs the
// same pkg/identity reconciler the control plane runs against hosted
// instances, against the env's host-run Zitadel, and publishes what it
// produced. A failure degrades rather than stops the stack, as the
// idp-provision job it replaces did: the backend and frontend come up without
// sign-in, and the message says what is missing.
func convergeHostIdentity(ctx context.Context, e *KCLEntities, projectDir string, prov secrets.Provider) {
	id := hostIdentity(e)
	if id == nil {
		return
	}
	in, err := devIdentityInput(e, id, projectDir, prov)
	if err != nil {
		fmt.Printf("[up] identity %s: %v\n[up] sign-in is unavailable this run; everything else still starts.\n", id.Name, err)
		return
	}
	zitadel, _ := hostZitadel(e, id) // devIdentityInput just found it
	loc := idpPATLocations(zitadel, projectDir)
	res, notes, err := convergeWithIDPPAT(ctx, in, loc)
	if errors.Is(err, errNoIDPPAT) {
		fmt.Print(missingIDPPATMessage(id.Name, zitadel, loc, e.Env))
		return
	}
	if err != nil {
		fmt.Printf("[up] identity %s FAILED — continuing, because sign-in is the only thing that needs it:\n%v\n", id.Name, err)
		fmt.Printf("[up] the IdP's log is %s\n", filepath.Join(".forge", "hostinfra", "idp", "zitadel.log"))
		return
	}
	for _, n := range notes {
		fmt.Printf("[up] identity %s: %s\n", id.Name, n)
	}
	for _, w := range res.Warnings {
		fmt.Printf("[up] identity %s: warning: %s\n", id.Name, w)
	}
	for _, p := range res.SeedPasswords {
		fmt.Printf("[up] identity %s: seed user %s password: generated and stored as %s in %s\n", id.Name, p.Email, p.Secret, relTo(projectDir, in.StorePath))
	}
	for _, code := range res.Report.SignInCodes {
		fmt.Printf("[up] identity %s: one-time sign-in code for %s: %s\n", id.Name, code.Email, code.Code)
	}
	mailUI := ""
	if in.MailpitPort > 0 {
		mailUI = hostinfra.MailpitUIURL(hostinfra.Spec{Port: in.MailpitPort})
	}
	for _, email := range res.Report.OwnersInvited {
		fmt.Printf("[up] identity %s: invited owner %s — open the invitation in %s\n", id.Name, email, firstNonEmptyStr(mailUI, "their inbox"))
	}
	for _, email := range res.Report.OwnersPending {
		if res.Mail == identity.MailNone {
			fmt.Printf("[up] identity %s: owner %s has no credential and this env sends no mail; add a forge.HostInfra {engine = \"mailpit\"} to invite them\n", id.Name, email)
		}
	}
	if res.SocialCallbackURL != "" {
		fmt.Printf("[up] identity %s: social sign-in callback (register it on your DEV OAuth apps): %s\n", id.Name, res.SocialCallbackURL)
	}
	if mailUI != "" {
		fmt.Printf("[up] identity %s: Mail: %s\n", id.Name, mailUI)
	}
	switch {
	case res.Report.Changed():
		fmt.Printf("[up] identity %s: converged %s (%d change(s))\n", id.Name, in.BaseURL, len(res.Report.Changes))
		for _, c := range res.Report.Changes {
			fmt.Printf("[up]   %s\n", c)
		}
	default:
		fmt.Printf("[up] identity %s: already converged at %s\n", id.Name, in.BaseURL)
	}
	if len(res.StoreKeys) > 0 {
		fmt.Printf("[up] identity %s: wrote %s to %s\n", id.Name, strings.Join(res.StoreKeys, ", "), relTo(projectDir, in.StorePath))
	}
	fmt.Printf("[up] identity %s: outputs in %s\n", id.Name, relTo(projectDir, in.OutputsPath))
}

func relTo(base, p string) string {
	if rel, err := filepath.Rel(base, p); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return p
}

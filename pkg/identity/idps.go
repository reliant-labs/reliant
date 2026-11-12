package identity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// ---------------------------------------------------------------------------
// identity providers (social sign-in)
// ---------------------------------------------------------------------------

// providerObj is an instance identity provider as /admin/v1/idps/templates
// reads it.
type providerObj struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Config struct {
		Options map[string]any `json:"options"`
		Google  *idpClient     `json:"google"`
		GitHub  *idpClient     `json:"github"`
	} `json:"config"`
}

type idpClient struct {
	ClientID string   `json:"clientId"`
	Scopes   []string `json:"scopes"`
}

func (o providerObj) client() *idpClient {
	if o.Config.Google != nil {
		return o.Config.Google
	}
	return o.Config.GitHub
}

// idpProviderType is Zitadel's provider type for each managed kind.
var idpProviderType = map[IdPType]string{
	IdPGoogle: "PROVIDER_TYPE_GOOGLE",
	IdPGitHub: "PROVIDER_TYPE_GITHUB",
}

// idpOptions are the provider options every managed IdP carries. Creation,
// linking and auto-update through Zitadel's own login are all off: the app's
// login broker decides who may sign up and when an account may be linked
// (only when both the provider and the account assert a verified address),
// and Zitadel's hosted login is not used. The v2 user API the broker links and
// creates accounts with does not consult these flags (Zitadel v4.19.5
// command/user_human.go addLink), so they bind only the hosted login.
var idpOptions = map[string]any{
	"isLinkingAllowed":  false,
	"isCreationAllowed": false,
	"isAutoCreation":    false,
	"isAutoUpdate":      false,
	"autoLinking":       "AUTO_LINKING_OPTION_UNSPECIFIED",
}

func idpOptionsMatch(have map[string]any) bool {
	for k, w := range idpOptions {
		switch w := w.(type) {
		case bool:
			h, _ := have[k].(bool)
			if h != w {
				return false
			}
		case string:
			h, _ := have[k].(string)
			if h == "" {
				h = "AUTO_LINKING_OPTION_UNSPECIFIED" // the gateway omits the zero value
			}
			if h != w {
				return false
			}
		}
	}
	return true
}

// ensureIdPs converges the declared identity providers and their place in the
// login policy.
//
// Each is matched by name, created or updated through the provider-specific
// endpoint, and linked into the instance login policy. Zitadel never returns a
// client secret, so a digest of the last one applied is kept in the KeyStore:
// a rotated secret is detected and pushed, an unchanged one costs no write.
//
// A managed IdP (IdPNamePrefix) that is no longer declared is UNLINKED from
// the policy, so nobody can start a new sign-in with it, and never deleted:
// deleting it destroys every user's link to it, which re-declaring it could
// not restore. IdPs without the prefix are someone else's and left alone.
//
// It runs when IdPs or SignIn are declared, so a pass with neither (an
// instance predating sign-in declarations) reads nothing new.
func (p *run) ensureIdPs(ctx context.Context) error {
	if len(p.spec.IdPs) == 0 && p.spec.SignIn == nil {
		return nil
	}
	all, err := pageAll[providerObj](ctx, p.c, "/admin/v1/idps/templates/_search", map[string]any{}, noOrg)
	if err != nil {
		return objErr("identity providers", "instance", err)
	}
	byName := map[string]providerObj{}
	for _, o := range all {
		byName[o.Name] = o
	}

	var errs []error
	// failed holds the ids of declared providers whose reconciliation failed
	// this pass. They are still declared, so their existing policy link is
	// kept (a transient store or update failure must not unlink a provider
	// people sign in with), but no new link is made for them.
	failed := map[string]bool{}
	for _, want := range p.spec.IdPs {
		id, err := p.ensureIdP(ctx, want, byName)
		if err != nil {
			errs = append(errs, objErr("identity provider", want.Name, err))
			if cur, ok := byName[want.Name]; ok {
				failed[cur.ID] = true
			}
			continue
		}
		if id != "" {
			p.idpIDs[want.Name] = id
		}
	}
	if err := p.ensureIdPLinks(ctx, failed); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (p *run) ensureIdP(ctx context.Context, want IdP, byName map[string]providerObj) (string, error) {
	digest := idpSecretDigest(want)
	stored, err := p.o.Keys.Load(ctx, keyIdPPrefix+want.Name)
	if err != nil {
		return "", fmt.Errorf("read the stored secret digest: %w", err)
	}
	scopes := slices.Clone(want.Scopes)
	body := map[string]any{
		"name": want.Name, "clientId": want.ClientID, "scopes": scopes, "providerOptions": idpOptions,
	}
	base := "/admin/v1/idps/" + string(want.Type)

	cur, ok := byName[want.Name]
	if !ok {
		p.rep.change(Created, "identity provider", want.Name, string(want.Type))
		if p.dry() {
			return "", nil
		}
		body["clientSecret"] = want.ClientSecret
		var out struct {
			ID string `json:"id"`
		}
		if err := p.c.call(ctx, http.MethodPost, base, body, &out, mint); err != nil {
			return "", err
		}
		return out.ID, p.keepIdPDigest(ctx, want, digest)
	}
	if cur.Type != idpProviderType[want.Type] {
		return cur.ID, fmt.Errorf("an identity provider of type %s already holds this name; rename or remove it in the console", cur.Type)
	}

	var drift []string
	have := cur.client()
	if have == nil || have.ClientID != want.ClientID || !sameSet(have.Scopes, want.Scopes) || !idpOptionsMatch(cur.Config.Options) {
		drift = append(drift, "settings")
	}
	secretChanged := string(stored) != digest
	if secretChanged {
		drift = append(drift, "client secret")
	}
	if len(drift) == 0 {
		return cur.ID, nil
	}
	p.rep.change(Updated, "identity provider", want.Name, strings.Join(drift, ", "))
	if p.dry() {
		return cur.ID, nil
	}
	// An empty client secret leaves the stored one unchanged.
	body["clientSecret"] = ""
	if secretChanged {
		body["clientSecret"] = want.ClientSecret
	}
	if err := p.c.call(ctx, http.MethodPut, base+"/"+url.PathEscape(cur.ID), body, nil, idem); err != nil && !isNoChange(err) {
		return cur.ID, err
	}
	if secretChanged {
		return cur.ID, p.keepIdPDigest(ctx, want, digest)
	}
	return cur.ID, nil
}

func (p *run) keepIdPDigest(ctx context.Context, want IdP, digest string) error {
	if err := p.o.Keys.Save(ctx, keyIdPPrefix+want.Name, []byte(digest)); err != nil {
		return fmt.Errorf("record the secret digest: %w", err)
	}
	return nil
}

func idpSecretDigest(i IdP) string {
	sum := sha256.Sum256([]byte("forge-identity-idp\x00" + i.Name + "\x00" + i.ClientID + "\x00" + i.ClientSecret))
	return hex.EncodeToString(sum[:])
}

// ensureIdPLinks makes the login policy list exactly the declared managed
// IdPs: each declared one is linked, each managed one no longer declared is
// unlinked. A link to a provider in keep (declared, but its reconciliation
// failed this pass) is left as it is.
func (p *run) ensureIdPLinks(ctx context.Context, keep map[string]bool) error {
	var resp struct {
		Policy struct {
			IdPs []struct {
				IdPID   string `json:"idpId"`
				IdPName string `json:"idpName"`
			} `json:"idps"`
		} `json:"policy"`
	}
	if err := p.c.call(ctx, http.MethodGet, "/admin/v1/policies/login", nil, &resp, idem); err != nil {
		return objErr("login policy", "identity providers", err)
	}
	linked := map[string]bool{}
	declared := map[string]bool{}
	for _, id := range p.idpIDs {
		declared[id] = true
	}
	var errs []error
	const path = "/admin/v1/policies/login/idps"
	for _, l := range resp.Policy.IdPs {
		linked[l.IdPID] = true
		if declared[l.IdPID] || keep[l.IdPID] || !strings.HasPrefix(l.IdPName, IdPNamePrefix) {
			continue
		}
		p.rep.change(Revoked, "login policy identity provider", l.IdPName, "no longer declared; kept, not deleted")
		if p.dry() {
			continue
		}
		if err := p.c.call(ctx, http.MethodDelete, path+"/"+url.PathEscape(l.IdPID), nil, nil, idem); err != nil && !isNotFound(err) {
			errs = append(errs, objErr("login policy identity provider", l.IdPName, fmt.Errorf("unlink: %w", err)))
		}
	}
	for _, want := range p.spec.IdPs {
		id, ok := p.idpIDs[want.Name]
		if !ok && !p.dry() {
			continue // its reconciliation failed; reported above
		}
		if linked[id] {
			continue
		}
		p.rep.change(Granted, "login policy identity provider", want.Name, "")
		if p.dry() || id == "" {
			continue
		}
		if err := p.c.call(ctx, http.MethodPost, path, map[string]any{"idpId": id}, nil, idem); err != nil && !isAlreadyExists(err) {
			errs = append(errs, objErr("login policy identity provider", want.Name, fmt.Errorf("link: %w", err)))
		}
	}
	return errors.Join(errs...)
}

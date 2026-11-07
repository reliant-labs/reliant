package identity

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
)

type appObj struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	OIDCConfig *observedConfig `json:"oidcConfig"`
}

func (a appObj) clientID() string {
	if a.OIDCConfig == nil {
		return ""
	}
	return a.OIDCConfig.ClientID
}

// oidcConfig is the OIDC application body for kind, sent whole on create and on
// every update (Zitadel's update takes the full config, not a patch).
//
// Two fields are load-bearing and easy to get wrong by hand:
//
//   - accessTokenType JWT. The default is an OPAQUE access token — a random
//     string that never validates against a JWKS however correct the rest is,
//     while sign-in itself looks completely successful. The visible symptom is
//     every API call 401ing with "invalid number of segments".
//   - accessTokenRoleAssertion. Without it the access token carries no roles
//     claim and every operator reads as unprivileged (see RolesScope).
//
// A public client (SPA, native, broker) uses authMethodType NONE and PKCE; a
// cannot-hold-a-secret client proves possession of the code instead.
func (a Application) oidcConfig() map[string]any {
	cfg := map[string]any{
		"redirectUris":             nonNil(a.RedirectURIs),
		"postLogoutRedirectUris":   nonNil(a.PostLogoutURIs),
		"responseTypes":            []string{"OIDC_RESPONSE_TYPE_CODE"},
		"grantTypes":               []string{"OIDC_GRANT_TYPE_AUTHORIZATION_CODE", "OIDC_GRANT_TYPE_REFRESH_TOKEN"},
		"accessTokenType":          "OIDC_TOKEN_TYPE_JWT",
		"accessTokenRoleAssertion": true,
		"idTokenRoleAssertion":     a.IDTokenRoleAssertion,
		"idTokenUserinfoAssertion": a.IDTokenUserinfoAssertion,
		"devMode":                  a.DevMode,
	}
	switch a.Kind {
	case AppWeb:
		cfg["appType"] = "OIDC_APP_TYPE_WEB"
		cfg["authMethodType"] = "OIDC_AUTH_METHOD_TYPE_BASIC"
	case AppNative:
		cfg["appType"] = "OIDC_APP_TYPE_NATIVE"
		cfg["authMethodType"] = "OIDC_AUTH_METHOD_TYPE_NONE"
	default: // spa, native_broker
		cfg["appType"] = "OIDC_APP_TYPE_USER_AGENT"
		cfg["authMethodType"] = "OIDC_AUTH_METHOD_TYPE_NONE"
	}
	// Per application, not per instance: two apps sharing an instance each
	// point at their own sign-in route. Always sent, so removing LoginURI
	// restores the issuer-hosted pages instead of keeping the last value.
	if a.LoginURI != "" {
		cfg["loginVersion"] = map[string]any{"loginV2": map[string]any{"baseUri": a.LoginURI}}
	} else {
		cfg["loginVersion"] = map[string]any{"loginV1": map[string]any{}}
	}
	return cfg
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// observedConfig is the part of an application's OIDC config that is compared
// for drift. It is read back from the instance in the same terms oidcConfig
// writes, so an unchanged app compares equal and costs no write.
type observedConfig struct {
	ClientID                 string   `json:"clientId"`
	RedirectURIs             []string `json:"redirectUris"`
	PostLogoutURIs           []string `json:"postLogoutRedirectUris"`
	ResponseTypes            []string `json:"responseTypes"`
	GrantTypes               []string `json:"grantTypes"`
	AppType                  string   `json:"appType"`
	AuthMethodType           string   `json:"authMethodType"`
	AccessTokenType          string   `json:"accessTokenType"`
	AccessTokenRoleAssertion bool     `json:"accessTokenRoleAssertion"`
	IDTokenRoleAssertion     bool     `json:"idTokenRoleAssertion"`
	IDTokenUserinfoAssertion bool     `json:"idTokenUserinfoAssertion"`
	DevMode                  bool     `json:"devMode"`
	LoginVersion             struct {
		LoginV2 *struct {
			BaseURI string `json:"baseUri"`
		} `json:"loginV2"`
	} `json:"loginVersion"`
}

// drift lists the fields of cur that differ from the declaration.
func (a Application) drift(cur *observedConfig) []string {
	if cur == nil {
		return []string{"oidc config"}
	}
	want := a.oidcConfig()
	var d []string
	set := func(name string, equal bool) {
		if !equal {
			d = append(d, name)
		}
	}
	set("redirect URIs", sameSet(cur.RedirectURIs, a.RedirectURIs))
	set("post-logout URIs", sameSet(cur.PostLogoutURIs, a.PostLogoutURIs))
	set("response types", sameSet(cur.ResponseTypes, want["responseTypes"].([]string)))
	set("grant types", sameSet(cur.GrantTypes, want["grantTypes"].([]string)))
	// The gateway omits an enum's zero value, so WEB and BASIC read back empty.
	set("app type", orDefault(cur.AppType, "OIDC_APP_TYPE_WEB") == want["appType"])
	set("auth method", orDefault(cur.AuthMethodType, "OIDC_AUTH_METHOD_TYPE_BASIC") == want["authMethodType"])
	set("access token type", cur.AccessTokenType == "OIDC_TOKEN_TYPE_JWT")
	set("access token role assertion", cur.AccessTokenRoleAssertion)
	set("id token role assertion", cur.IDTokenRoleAssertion == a.IDTokenRoleAssertion)
	set("id token userinfo assertion", cur.IDTokenUserinfoAssertion == a.IDTokenUserinfoAssertion)
	set("dev mode", cur.DevMode == a.DevMode)
	have := ""
	if cur.LoginVersion.LoginV2 != nil {
		have = cur.LoginVersion.LoginV2.BaseURI
	}
	set("login URI", have == a.LoginURI)
	return d
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func sameSet(a, b []string) bool {
	x, y := append([]string{}, a...), append([]string{}, b...)
	sort.Strings(x)
	sort.Strings(y)
	if len(x) == 0 && len(y) == 0 {
		return true
	}
	return reflect.DeepEqual(x, y)
}

func (p *run) loadApps(ctx context.Context) error {
	if p.projectID == "" || len(p.spec.Applications) == 0 {
		return nil
	}
	apps, err := pageAll[appObj](ctx, p.c, "/management/v1/projects/"+p.projectID+"/apps/_search", map[string]any{}, idem)
	if err != nil {
		return objErr("applications", p.spec.Project.Name, err)
	}
	p.apps0 = apps
	return nil
}

// preflightAdoption refuses an adoption that cannot succeed BEFORE the first
// write: with Options.Adopt every declared application must already exist
// (or be found by its pinned client id), and a pinned id that does not match
// is an error. Failing here, rather than at the application step, is what
// keeps a refused adoption from having already minted keys and tokens.
func (p *run) preflightAdoption(ctx context.Context) error {
	if err := p.loadApps(ctx); err != nil {
		return err
	}
	var errs []error
	for _, a := range p.spec.Applications {
		cur, err := matchApp(a, p.apps0)
		switch {
		case err != nil:
			errs = append(errs, objErr("application", a.Name, err))
		case cur == nil && p.o.Adopt:
			errs = append(errs, objErr("application", a.Name, errors.New("not found, and Options.Adopt forbids creating it")))
		}
	}
	return errors.Join(errs...)
}

func (p *run) ensureApplications(ctx context.Context) error {
	existing := p.apps0
	var errs []error
	for _, a := range p.spec.Applications {
		if err := p.ensureApplication(ctx, a, existing); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// matchApp finds the application a declaration refers to: by pinned client id
// first (so a console rename cannot orphan it), else by name.
func matchApp(a Application, existing []appObj) (*appObj, error) {
	var byName *appObj
	for i := range existing {
		if existing[i].Name == a.Name {
			byName = &existing[i]
		}
	}
	if a.AdoptClientID == "" {
		return byName, nil
	}
	var byID *appObj
	for i := range existing {
		if existing[i].clientID() == a.AdoptClientID {
			byID = &existing[i]
		}
	}
	switch {
	case byID == nil:
		return nil, fmt.Errorf("the pinned client id %s does not exist in this project; refusing to create an application under a different id", a.AdoptClientID)
	case byName != nil && byName.ID != byID.ID:
		return nil, fmt.Errorf("the pinned client id %s belongs to application %q, but another application is named %q; resolve the conflict by hand", a.AdoptClientID, byID.Name, a.Name)
	}
	return byID, nil
}

func (p *run) ensureApplication(ctx context.Context, a Application, existing []appObj) error {
	cur, err := matchApp(a, existing)
	if err != nil {
		return objErr("application", a.Name, err)
	}
	out := AppOutput{RedirectURIs: a.RedirectURIs, PostLogoutURIs: a.PostLogoutURIs, LoginURI: a.LoginURI}
	created := false
	if cur == nil {
		if p.o.Adopt {
			return objErr("application", a.Name, errors.New("not found, and Options.Adopt forbids creating it"))
		}
		p.rep.change(Created, "application", a.Name, string(a.Kind))
		if p.dry() {
			p.apps[a.Name] = out
			return nil
		}
		body := a.oidcConfig()
		body["name"] = a.Name
		var res struct {
			AppID        string `json:"appId"`
			ClientID     string `json:"clientId"`
			ClientSecret string `json:"clientSecret"`
		}
		if err := p.c.call(ctx, http.MethodPost, "/management/v1/projects/"+p.projectID+"/apps/oidc", body, &res, mint); err != nil {
			return objErr("application", a.Name, err)
		}
		if res.ClientID == "" {
			return objErr("application", a.Name, errors.New("the instance created it but returned no client id"))
		}
		out.AppID, out.ClientID, out.ClientSecret = res.AppID, res.ClientID, res.ClientSecret
		created = true
		p.logf("created application", "name", a.Name, "client_id", res.ClientID)
		if a.Kind == AppWeb {
			if err := p.keepSecret(ctx, a, res.ClientSecret); err != nil {
				return err
			}
		}
	} else {
		out.AppID, out.ClientID = cur.ID, cur.clientID()
		fields := a.drift(cur.OIDCConfig)
		renamed := cur.Name != a.Name
		if len(fields) > 0 || renamed {
			detail := strings.Join(fields, ", ")
			if renamed {
				detail = strings.TrimPrefix(detail+", name (was "+cur.Name+")", ", ")
			}
			p.rep.change(Updated, "application", a.Name, detail)
			if !p.dry() {
				base := "/management/v1/projects/" + p.projectID + "/apps/" + url.PathEscape(cur.ID)
				if renamed {
					err := p.c.call(ctx, http.MethodPut, base, map[string]any{"name": a.Name}, nil, idem)
					if err != nil && !isNoChange(err) {
						return objErr("application", a.Name, fmt.Errorf("rename: %w", err))
					}
				}
				if len(fields) > 0 {
					err := p.c.call(ctx, http.MethodPut, base+"/oidc_config", a.oidcConfig(), nil, idem)
					if err != nil && !isNoChange(err) {
						return objErr("application", a.Name, err)
					}
				}
			}
		}
	}

	if a.Kind == AppWeb && !created {
		if err := p.ensureClientSecret(ctx, a, cur, &out); err != nil {
			return err
		}
	}
	if a.Kind == AppWeb && created {
		out.ClientSecret = strings.TrimSpace(out.ClientSecret)
	}
	if a.Kind == AppNativeBroker {
		user := a.BrokerUsername()
		out.BrokerUser = user
		m := p.machine[user]
		out.BrokerToken, out.BrokerTokenID, out.BrokerTokenExpires = m.Token, m.TokenID, m.TokenExpires
	}
	p.apps[a.Name] = out
	return nil
}

func (p *run) keepSecret(ctx context.Context, a Application, secret string) error {
	if secret == "" {
		return objErr("application", a.Name, errors.New("the instance returned no client secret for a web application"))
	}
	if err := p.o.Keys.Save(ctx, ClientSecretEntry(a.Name), []byte(secret)); err != nil {
		return objErr("application", a.Name, fmt.Errorf("deliver the client secret: %w", err))
	}
	return nil
}

// ensureClientSecret recovers the client secret of an existing web app from the
// KeyStore. Zitadel never shows it again, so an empty store is not silently
// "fixed" by regenerating it — that would break the deployed server holding the
// old secret. It is an explicit opt-in (Application.RegenerateSecret).
func (p *run) ensureClientSecret(ctx context.Context, a Application, cur *appObj, out *AppOutput) error {
	stored, err := p.o.Keys.Load(ctx, ClientSecretEntry(a.Name))
	if err != nil {
		return objErr("application", a.Name, fmt.Errorf("read the stored client secret: %w", err))
	}
	if len(stored) > 0 && !a.RegenerateSecret {
		out.ClientSecret = string(stored)
		return nil
	}
	if !a.RegenerateSecret {
		p.rep.warn("web application %q has no client secret in the key store and Zitadel cannot show it again; set RegenerateSecret to mint a new one (this invalidates the old)", a.Name)
		return nil
	}
	p.rep.change(Issued, "client secret", a.Name, "regenerated")
	if p.dry() {
		return nil
	}
	var res struct {
		ClientSecret string `json:"clientSecret"`
	}
	err = p.c.call(ctx, http.MethodPost, "/management/v1/projects/"+p.projectID+"/apps/"+url.PathEscape(cur.ID)+"/oidc_config/_generate_client_secret", map[string]any{}, &res, mint)
	if err != nil {
		return objErr("application", a.Name, fmt.Errorf("regenerate the client secret: %w", err))
	}
	if err := p.keepSecret(ctx, a, res.ClientSecret); err != nil {
		return err
	}
	out.ClientSecret = res.ClientSecret
	return nil
}

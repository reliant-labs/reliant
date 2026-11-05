package identity

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// RolesScope is the reserved scope that asks for project roles to be asserted
// into a token. It is one of THREE things a roles claim needs in an ACCESS
// token: the project's projectRoleAssertion, this scope in the authorize
// request, and accessTokenRoleAssertion on the application. Omit any one and
// the token is well-formed, passes signature, issuer and audience checks, and
// carries no roles — so every operator reads as unprivileged.
const RolesScope = "urn:zitadel:iam:org:projects:roles"

// AudienceScope is the reserved scope that puts the project id into a token's
// `aud`. Zitadel has no arbitrary "API resource indicator": the audience IS
// the project's generated id, so a client must request this scope or its token
// names its own client id instead of the API.
func AudienceScope(projectID string) string {
	return "urn:zitadel:iam:org:project:id:" + projectID + ":aud"
}

// ProjectRolesClaim is the claim the project's roles are asserted under.
// Zitadel does not mint a flat `roles` list: the claim is namespaced by project
// id and its value is a MAP of role key to {org id: org domain}.
func ProjectRolesClaim(projectID string) string {
	return "urn:zitadel:iam:org:project:" + projectID + ":roles"
}

// pageAll collects every page of a Zitadel search endpoint.
func pageAll[T any](ctx context.Context, c *client, path string, body map[string]any, opt callOpt) ([]T, error) {
	const limit = 100
	var all []T
	for page := 0; page < 1000; page++ {
		b := make(map[string]any, len(body)+1)
		for k, v := range body {
			b[k] = v
		}
		b["query"] = map[string]any{"offset": page * limit, "limit": limit}
		var resp struct {
			Result []T `json:"result"`
		}
		if err := c.call(ctx, http.MethodPost, path, b, &resp, opt); err != nil {
			return nil, err
		}
		all = append(all, resp.Result...)
		if len(resp.Result) < limit {
			return all, nil
		}
	}
	return nil, errors.New("search did not terminate")
}

// ---------------------------------------------------------------------------
// project
// ---------------------------------------------------------------------------

type projectObj struct {
	ID                   string `json:"id"`
	Name                 string `json:"name"`
	ProjectRoleAssertion bool   `json:"projectRoleAssertion"`
	ProjectRoleCheck     bool   `json:"projectRoleCheck"`
	HasProjectCheck      bool   `json:"hasProjectCheck"`
}

// locateProject reads the project the declaration refers to (nil when it does
// not exist) without writing anything, so an adoption can be refused before the
// first write.
func (p *run) locateProject(ctx context.Context) error {
	cur, err := p.findProject(ctx, p.spec.Project)
	if err != nil {
		return err
	}
	if cur == nil && p.o.Adopt {
		return objErr("project", p.spec.Project.Name, errors.New("not found, and Options.Adopt forbids creating it"))
	}
	p.projCur = cur
	if cur != nil {
		p.projectID = cur.ID
	}
	return nil
}

func (p *run) applyProject(ctx context.Context) error {
	want, cur := p.spec.Project, p.projCur
	body := func(name string) map[string]any {
		return map[string]any{
			"name":                 name,
			"projectRoleAssertion": true,
			"projectRoleCheck":     want.MustHoldRole,
			"hasProjectCheck":      want.ProjectCheck,
		}
	}
	if cur == nil {
		p.rep.change(Created, "project", want.Name, "")
		if p.dry() {
			return nil
		}
		var out struct {
			ID string `json:"id"`
		}
		if err := p.c.call(ctx, http.MethodPost, "/management/v1/projects", body(want.Name), &out, mint); err != nil {
			return objErr("project", want.Name, err)
		}
		if out.ID == "" {
			return objErr("project", want.Name, errors.New("the instance created it but returned no id"))
		}
		p.projectID = out.ID
		p.logf("created project", "name", want.Name, "id", out.ID)
		return nil
	}
	if cur.Name == want.Name && cur.ProjectRoleAssertion && cur.ProjectRoleCheck == want.MustHoldRole && cur.HasProjectCheck == want.ProjectCheck {
		return nil
	}
	detail := "settings"
	if cur.Name != want.Name {
		detail = fmt.Sprintf("renamed from %q", cur.Name)
	}
	p.rep.change(Updated, "project", want.Name, detail)
	if p.dry() {
		return nil
	}
	// projectRoleAssertion is not decoration: without it Zitadel omits the
	// roles claim even when the roles scope is requested.
	err := p.c.call(ctx, http.MethodPut, "/management/v1/projects/"+url.PathEscape(cur.ID), body(want.Name), nil, idem)
	if err != nil && !isNoChange(err) {
		return objErr("project", want.Name, err)
	}
	return nil
}

// findProject matches by pinned id, else by name. nil means it does not exist.
func (p *run) findProject(ctx context.Context, want ProjectSpec) (*projectObj, error) {
	if want.AdoptID != "" {
		var out struct {
			Project projectObj `json:"project"`
		}
		err := p.c.call(ctx, http.MethodGet, "/management/v1/projects/"+url.PathEscape(want.AdoptID), nil, &out, idem)
		if isNotFound(err) || (err == nil && out.Project.ID == "") {
			return nil, objErr("project", want.Name, fmt.Errorf("the pinned id %s does not exist on this instance; refusing to create a project under a different id", want.AdoptID))
		}
		if err != nil {
			return nil, objErr("project", want.Name, err)
		}
		return &out.Project, p.checkNoNameClash(ctx, want, out.Project.ID)
	}
	return p.projectByName(ctx, want.Name)
}

func (p *run) checkNoNameClash(ctx context.Context, want ProjectSpec, id string) error {
	other, err := p.projectByName(ctx, want.Name)
	if err != nil {
		return err
	}
	if other != nil && other.ID != id {
		return objErr("project", want.Name, fmt.Errorf("the pinned id %s is a different project from the one named %q (%s); resolve the conflict by hand", id, want.Name, other.ID))
	}
	return nil
}

func (p *run) projectByName(ctx context.Context, name string) (*projectObj, error) {
	all, err := pageAll[projectObj](ctx, p.c, "/management/v1/projects/_search", map[string]any{
		"queries": []any{map[string]any{"nameQuery": map[string]any{"name": name, "method": "TEXT_QUERY_METHOD_EQUALS"}}},
	}, idem)
	if err != nil {
		return nil, objErr("project", name, err)
	}
	for i := range all {
		if all[i].Name == name {
			return &all[i], nil
		}
	}
	return nil, nil
}

// ---------------------------------------------------------------------------
// roles
// ---------------------------------------------------------------------------

type roleObj struct {
	Key         string `json:"key"`
	DisplayName string `json:"displayName"`
	Group       string `json:"group"`
}

func (p *run) ensureRoles(ctx context.Context) error {
	var have map[string]roleObj
	if p.projectID != "" {
		list, err := pageAll[roleObj](ctx, p.c, "/management/v1/projects/"+p.projectID+"/roles/_search", map[string]any{}, idem)
		if err != nil {
			return objErr("roles", p.spec.Project.Name, err)
		}
		have = make(map[string]roleObj, len(list))
		for _, r := range list {
			have[r.Key] = r
		}
	}
	var errs []error
	declared := map[string]bool{}
	for _, r := range p.spec.Roles {
		declared[r.Key] = true
		display := r.DisplayName
		if display == "" {
			display = r.Key
		}
		cur, ok := have[r.Key]
		switch {
		case !ok:
			p.rep.change(Created, "role", r.Key, "")
			if p.dry() {
				continue
			}
			err := p.c.call(ctx, http.MethodPost, "/management/v1/projects/"+p.projectID+"/roles",
				map[string]any{"roleKey": r.Key, "displayName": display, "group": r.Group}, nil, mint)
			if err != nil && !isAlreadyExists(err) {
				errs = append(errs, objErr("role", r.Key, err))
			}
		case cur.DisplayName != display || cur.Group != r.Group:
			p.rep.change(Updated, "role", r.Key, "label")
			if p.dry() {
				continue
			}
			err := p.c.call(ctx, http.MethodPut, "/management/v1/projects/"+p.projectID+"/roles/"+url.PathEscape(r.Key),
				map[string]any{"displayName": display, "group": r.Group}, nil, idem)
			if err != nil && !isNoChange(err) {
				errs = append(errs, objErr("role", r.Key, err))
			}
		}
	}
	// A role nobody declared is reported and kept: it may hold grants, and
	// deleting it would strip them silently.
	for _, key := range sortedKeys(have) {
		if !declared[key] {
			p.rep.warn("role %q exists in project %q but is not declared; left alone", key, p.spec.Project.Name)
		}
	}
	return errors.Join(errs...)
}

// ---------------------------------------------------------------------------
// policies
// ---------------------------------------------------------------------------

func (p *run) ensurePolicies(ctx context.Context) error {
	var errs []error
	if l := p.spec.Policies.Login; l != nil {
		errs = append(errs, p.ensureLoginPolicy(ctx, *l))
	}
	if pw := p.spec.Policies.PasswordComplexity; pw != nil {
		errs = append(errs, p.ensurePasswordComplexity(ctx, *pw))
	}
	if lk := p.spec.Policies.Lockout; lk != nil {
		errs = append(errs, p.ensureLockout(ctx, *lk))
	}
	return errors.Join(errs...)
}

// getPolicy reads an instance policy as a generic map, so a PUT can send back
// every field it does not manage unchanged.
func (p *run) getPolicy(ctx context.Context, path string) (map[string]any, error) {
	var out struct {
		Policy map[string]any `json:"policy"`
	}
	if err := p.c.call(ctx, http.MethodGet, path, nil, &out, idem); err != nil {
		return nil, err
	}
	if out.Policy == nil {
		out.Policy = map[string]any{}
	}
	return out.Policy, nil
}

func (p *run) ensureLoginPolicy(ctx context.Context, want LoginPolicy) error {
	const path = "/admin/v1/policies/login"
	cur, err := p.getPolicy(ctx, path)
	if err != nil {
		return objErr("login policy", "instance", err)
	}
	passwordless := want.PasswordlessType
	if passwordless == "" {
		passwordless = "PASSWORDLESS_TYPE_NOT_ALLOWED"
	}
	wantFields := map[string]any{
		"allowUsernamePassword": want.AllowUsernamePassword,
		"allowRegister":         want.AllowRegister,
		"allowExternalIdp":      want.AllowExternalIDP,
		"forceMfa":              want.ForceMFA,
		"hidePasswordReset":     want.HidePasswordReset,
		"allowDomainDiscovery":  want.AllowDomainDiscovery,
		"passwordlessType":      passwordless,
		"mfaInitSkipLifetime":   strconv.FormatInt(int64(want.MFAInitSkipLifetime/time.Second), 10) + "s",
	}
	var drift []string
	for _, k := range sortedKeys(wantFields) {
		if !policyFieldEqual(k, cur[k], wantFields[k]) {
			drift = append(drift, k)
		}
	}
	if len(drift) == 0 {
		return nil
	}
	p.rep.change(Updated, "login policy", "instance", strings.Join(drift, ", "))
	if p.dry() {
		return nil
	}
	// PUT replaces the policy. Send back everything read (check lifetimes,
	// default redirect, ...) with only the managed fields overlaid, and drop
	// the read-only ones.
	body := map[string]any{}
	for k, v := range cur {
		switch k {
		case "details", "isDefault", "secondFactors", "multiFactors":
		default:
			body[k] = v
		}
	}
	for k, v := range wantFields {
		body[k] = v
	}
	if err := p.c.call(ctx, http.MethodPut, path, body, nil, idem); err != nil && !isNoChange(err) {
		return objErr("login policy", "instance", err)
	}
	return nil
}

// policyFieldEqual compares a policy field as read (absent means the zero
// value, since the gateway omits defaults) against its declared value.
func policyFieldEqual(key string, have, want any) bool {
	switch w := want.(type) {
	case bool:
		h, _ := have.(bool)
		return h == w
	case string:
		h, _ := have.(string)
		if key == "passwordlessType" && h == "" {
			h = "PASSWORDLESS_TYPE_NOT_ALLOWED"
		}
		if strings.HasSuffix(w, "s") && isDigits(strings.TrimSuffix(w, "s")) {
			return durationSeconds(h) == durationSeconds(w)
		}
		return h == w
	}
	return false
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func durationSeconds(s string) int64 {
	f, _ := strconv.ParseFloat(strings.TrimSuffix(s, "s"), 64)
	return int64(f)
}

// num reads a gateway number: 64-bit integers arrive as strings.
func num(v any) int64 {
	switch n := v.(type) {
	case string:
		i, _ := strconv.ParseInt(n, 10, 64)
		return i
	case float64:
		return int64(n)
	}
	return 0
}

func (p *run) ensurePasswordComplexity(ctx context.Context, want PasswordComplexity) error {
	const path = "/admin/v1/policies/password/complexity"
	cur, err := p.getPolicy(ctx, path)
	if err != nil {
		return objErr("password complexity policy", "instance", err)
	}
	flag := func(k string) bool { b, _ := cur[k].(bool); return b }
	if num(cur["minLength"]) == int64(want.MinLength) &&
		flag("hasUppercase") == want.RequireUppercase && flag("hasLowercase") == want.RequireLowercase &&
		flag("hasNumber") == want.RequireNumber && flag("hasSymbol") == want.RequireSymbol {
		return nil
	}
	p.rep.change(Updated, "password complexity policy", "instance", "")
	if p.dry() {
		return nil
	}
	err = p.c.call(ctx, http.MethodPut, path, map[string]any{
		"minLength":    strconv.Itoa(want.MinLength),
		"hasUppercase": want.RequireUppercase,
		"hasLowercase": want.RequireLowercase,
		"hasNumber":    want.RequireNumber,
		"hasSymbol":    want.RequireSymbol,
	}, nil, idem)
	if err != nil && !isNoChange(err) {
		return objErr("password complexity policy", "instance", err)
	}
	return nil
}

func (p *run) ensureLockout(ctx context.Context, want Lockout) error {
	cur, err := p.getPolicy(ctx, "/admin/v1/policies/lockout")
	if err != nil {
		return objErr("lockout policy", "instance", err)
	}
	if num(cur["maxPasswordAttempts"]) == int64(want.MaxPasswordAttempts) && num(cur["maxOtpAttempts"]) == int64(want.MaxOTPAttempts) {
		return nil
	}
	p.rep.change(Updated, "lockout policy", "instance", "")
	if p.dry() {
		return nil
	}
	// The read path is /policies/lockout; the update path is
	// /policies/password/lockout. They are not the same.
	err = p.c.call(ctx, http.MethodPut, "/admin/v1/policies/password/lockout", map[string]any{
		"maxPasswordAttempts": want.MaxPasswordAttempts,
		"maxOtpAttempts":      want.MaxOTPAttempts,
	}, nil, idem)
	if err != nil && !isNoChange(err) {
		return objErr("lockout policy", "instance", err)
	}
	return nil
}

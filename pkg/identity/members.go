package identity

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

type grantObj struct {
	ID       string   `json:"id"`
	UserID   string   `json:"userId"`
	UserName string   `json:"userName"`
	Email    string   `json:"email"`
	UserType string   `json:"userType"`
	RoleKeys []string `json:"roleKeys"`
}

func (g grantObj) machine() bool { return g.UserType == "TYPE_MACHINE" }

func (p *run) listGrants(ctx context.Context) ([]grantObj, error) {
	return pageAll[grantObj](ctx, p.c, "/management/v1/users/grants/_search", map[string]any{
		"queries": []any{map[string]any{"projectIdQuery": map[string]any{"projectId": p.projectID}}},
	}, idem)
}

// ensureMembers converges who holds each project role.
//
// A Declared role's holders are a SET: the declared humans plus the machine
// users that list the role. A holder outside it loses THIS role only (a grant
// is a list of role keys, and the others in it are kept). A role that is not
// Declared is never touched, so a console-managed role keeps its console
// managed members. Revocations run first, so a removed operator loses access
// even if a later grant in the same pass fails. Accounts are never deleted:
// that would destroy audit history and is not reversible from config.
func (p *run) ensureMembers(ctx context.Context) error {
	declared := map[string]bool{}
	for _, r := range p.spec.Roles {
		if r.Declared {
			declared[r.Key] = true
		}
	}
	wantHuman := map[string]map[string]bool{} // email -> declared roles
	for _, r := range p.spec.Roles {
		for _, m := range r.Members {
			e := strings.ToLower(strings.TrimSpace(m))
			if wantHuman[e] == nil {
				wantHuman[e] = map[string]bool{}
			}
			wantHuman[e][r.Key] = true
		}
	}
	// A seed user's roles are wanted state too. Without this a seed user who
	// holds a Declared role would be revoked, then re-granted, on every pass.
	for _, u := range p.spec.SeedUsers {
		e := strings.ToLower(strings.TrimSpace(u.Email))
		for _, r := range u.Roles {
			if wantHuman[e] == nil {
				wantHuman[e] = map[string]bool{}
			}
			wantHuman[e][r] = true
		}
	}
	wantMachine := map[string]map[string]bool{}
	for _, m := range p.spec.MachineUsers {
		for _, r := range m.Roles {
			if wantMachine[m.Username] == nil {
				wantMachine[m.Username] = map[string]bool{}
			}
			wantMachine[m.Username][r] = true
		}
	}
	if len(declared) == 0 && len(wantMachine) == 0 {
		return nil
	}

	var grants []grantObj
	if p.projectID != "" {
		var err error
		if grants, err = p.listGrants(ctx); err != nil {
			return objErr("role members", p.spec.Project.Name, err)
		}
	}
	byUser := map[string]grantObj{}
	for _, g := range grants {
		byUser[g.UserID] = g
	}

	var errs []error
	fail := func(kind, name string, err error) { errs = append(errs, objErr(kind, name, err)) }

	// Revoke first.
	for _, g := range grants {
		var want map[string]bool
		label := g.Email
		// A declared machine user's roles are entirely declared state: a role
		// it holds and does not list is withdrawn, whether or not the role
		// itself is Declared. Every other holder only loses Declared roles.
		allDeclared := false
		if g.machine() {
			want, label = wantMachine[g.UserName], g.UserName
			allDeclared = p.declaredMachine(g.UserName)
		} else {
			want = wantHuman[strings.ToLower(g.Email)]
		}
		var keep, drop []string
		for _, k := range g.RoleKeys {
			if (declared[k] || allDeclared) && !want[k] {
				drop = append(drop, k)
			} else {
				keep = append(keep, k)
			}
		}
		if len(drop) == 0 {
			continue
		}
		sort.Strings(drop)
		p.rep.change(Revoked, "role grant", label, strings.Join(drop, ", "))
		if p.dry() {
			continue
		}
		var err error
		if len(keep) == 0 {
			err = p.c.call(ctx, http.MethodDelete, "/management/v1/users/"+url.PathEscape(g.UserID)+"/grants/"+url.PathEscape(g.ID), nil, nil, idem)
			if isNotFound(err) {
				err = nil
			}
		} else {
			err = p.c.call(ctx, http.MethodPut, "/management/v1/users/"+url.PathEscape(g.UserID)+"/grants/"+url.PathEscape(g.ID), map[string]any{"roleKeys": keep}, nil, idem)
			if isNoChange(err) {
				err = nil
			}
		}
		if err != nil {
			fail("role grant", label, fmt.Errorf("revoke %s: %w", strings.Join(drop, ", "), err))
		}
	}

	seeded := map[string]bool{}
	for _, u := range p.spec.SeedUsers {
		seeded[strings.ToLower(strings.TrimSpace(u.Email))] = true
	}
	for _, email := range sortedKeys(wantHuman) {
		if seeded[email] {
			continue // created with its password, and granted, by ensureSeedUsers
		}
		uid, err := p.ensureHuman(ctx, email)
		if err != nil {
			fail("member", email, err)
			continue
		}
		if err := p.grantRoles(ctx, email, uid, byUser[uid], wantHuman[email]); err != nil {
			fail("member", email, err)
		}
		if p.o.SignInCodes && uid != "" && p.write() {
			if err := p.ensureSignInPath(ctx, email, uid); err != nil {
				fail("member", email, err)
			}
		}
	}
	for _, name := range sortedKeys(wantMachine) {
		uid := p.users[name]
		if uid == "" {
			continue // dry run: the user does not exist yet
		}
		if err := p.grantRoles(ctx, name, uid, byUser[uid], wantMachine[name]); err != nil {
			fail("machine user", name, err)
		}
	}
	return errors.Join(errs...)
}

func (p *run) declaredMachine(username string) bool {
	for _, m := range p.spec.MachineUsers {
		if m.Username == username {
			return true
		}
	}
	return false
}

// grantRoles makes sure uid holds every role in want, keeping the others it
// already has.
func (p *run) grantRoles(ctx context.Context, label, uid string, cur grantObj, want map[string]bool) error {
	have := map[string]bool{}
	for _, k := range cur.RoleKeys {
		have[k] = true
	}
	var add []string
	for k := range want {
		if !have[k] {
			add = append(add, k)
		}
	}
	if len(add) == 0 {
		return nil
	}
	sort.Strings(add)
	p.rep.change(Granted, "role grant", label, strings.Join(add, ", "))
	if p.dry() || uid == "" {
		return nil
	}
	if cur.ID != "" {
		keys := append(append([]string{}, cur.RoleKeys...), add...)
		err := p.c.call(ctx, http.MethodPut, "/management/v1/users/"+url.PathEscape(uid)+"/grants/"+url.PathEscape(cur.ID), map[string]any{"roleKeys": keys}, nil, idem)
		if err != nil && !isNoChange(err) {
			return fmt.Errorf("grant %s: %w", strings.Join(add, ", "), err)
		}
		return nil
	}
	err := p.c.call(ctx, http.MethodPost, "/management/v1/users/"+url.PathEscape(uid)+"/grants", map[string]any{
		"projectId": p.projectID, "roleKeys": add,
	}, nil, mint)
	// The grants search lags the write that created it; the grant is there.
	if err != nil && !isAlreadyExists(err) {
		return fmt.Errorf("grant %s: %w", strings.Join(add, ", "), err)
	}
	return nil
}

// humanUser is a v2 user search hit.
type humanUser struct {
	UserID string `json:"userId"`
}

func (p *run) findHuman(ctx context.Context, email string) (string, error) {
	hits, err := pageAll[humanUser](ctx, p.c, "/v2/users", map[string]any{
		"queries": []any{map[string]any{"emailQuery": map[string]any{
			"emailAddress": email, "method": "TEXT_QUERY_METHOD_EQUALS_IGNORE_CASE",
		}}},
	}, idem)
	if err != nil {
		return "", fmt.Errorf("search users: %w", err)
	}
	if len(hits) == 0 {
		return "", nil
	}
	return hits[0].UserID, nil
}

// ensureHuman finds a user by email, creating a credential-less account when
// absent. The email is marked verified because the address came from reviewed,
// version-controlled config rather than a sign-up form, so the thing
// verification proves is already established; leaving it unverified would also
// block the first sign-in behind a mail round trip that dev cannot complete.
func (p *run) ensureHuman(ctx context.Context, email string) (string, error) {
	if uid, ok := p.users[email]; ok {
		return uid, nil
	}
	uid, err := p.findHuman(ctx, email)
	if err != nil {
		return "", err
	}
	if uid == "" {
		p.rep.change(Created, "user", email, "")
		if p.dry() {
			return "", nil
		}
		var out struct {
			UserID string `json:"userId"`
		}
		err := p.c.call(ctx, http.MethodPost, "/v2/users/human", map[string]any{
			"username":     email,
			"organization": map[string]any{"orgId": p.c.orgID},
			"profile":      profileFromEmail(email),
			"email":        map[string]any{"email": email, "isVerified": true},
		}, &out, mint)
		if err != nil {
			return "", fmt.Errorf("create user: %w", err)
		}
		uid = out.UserID
		p.logf("created user", "user_id", uid)
	}
	p.users[email] = uid
	return uid, nil
}

// profileFromEmail derives the names Zitadel REQUIRES on a human from the
// address, the only thing a declaration gives. They are placeholders, not an
// attempt at identity: the person can correct them at first sign-in. Refusing
// to create the account for want of a surname would be worse.
func profileFromEmail(email string) map[string]any {
	local := email
	if at := strings.IndexByte(local, '@'); at > 0 {
		local = local[:at]
	}
	given, family := local, local
	if dot := strings.IndexByte(local, '.'); dot > 0 && dot < len(local)-1 {
		given, family = local[:dot], local[dot+1:]
	}
	return map[string]any{"givenName": given, "familyName": family}
}

// ensureSignInPath mints a one-time sign-in (password reset) code for a member
// who has NO credential of any kind, and leaves everyone else untouched.
//
// The condition is "has no credential", not "was just created": the account may
// have arrived by any route (seeded, created by a run that failed before the
// grant, added in a console), and the created-only rule stranded exactly such
// an account in prod. Asking what credentials the account HAS is also strictly
// more conservative: a member who set a password, passkey or IdP link is
// skipped, so a working credential is never invalidated. Any method counts,
// not only a password. A member the caller still holds a live code for
// (Options.HeldSignInCode) keeps it.
func (p *run) ensureSignInPath(ctx context.Context, email, uid string) error {
	var methods struct {
		AuthMethodTypes []string `json:"authMethodTypes"`
	}
	if err := p.c.call(ctx, http.MethodGet, "/v2/users/"+url.PathEscape(uid)+"/authentication_methods", nil, &methods, idem); err != nil {
		return fmt.Errorf("read authentication methods: %w", err)
	}
	if len(methods.AuthMethodTypes) > 0 {
		p.rep.Credentialed = append(p.rep.Credentialed, email)
		return nil
	}
	if p.o.HeldSignInCode != nil && p.o.HeldSignInCode(email) {
		return nil
	}
	var out struct {
		VerificationCode string `json:"verificationCode"`
	}
	// return_code, not the default send: where nothing can deliver mail a sent
	// code has nowhere to go.
	err := p.c.call(ctx, http.MethodPost, "/v2/users/"+url.PathEscape(uid)+"/password_reset", map[string]any{"returnCode": map[string]any{}}, &out, mint)
	if err != nil {
		return fmt.Errorf("issue a sign-in code: %w", err)
	}
	if out.VerificationCode == "" {
		return errors.New("the instance accepted the password-reset request but returned no code")
	}
	p.rep.change(Issued, "sign-in code", email, "")
	p.rep.SignInCodes = append(p.rep.SignInCodes, SignInCode{Email: email, Code: out.VerificationCode, ExpiresAt: p.o.Now().Add(SignInCodeLifetime)})
	return nil
}

// ---------------------------------------------------------------------------
// seed users
// ---------------------------------------------------------------------------

// ensureSeedUsers creates throwaway accounts. The password is set at creation
// and never again, so a seed user who changed theirs is not reset on every
// pass. Roles are granted additively and never revoked.
func (p *run) ensureSeedUsers(ctx context.Context) error {
	if len(p.spec.SeedUsers) == 0 {
		return nil
	}
	var grants []grantObj
	if p.projectID != "" {
		var err error
		if grants, err = p.listGrants(ctx); err != nil {
			return objErr("seed users", p.spec.Project.Name, err)
		}
	}
	byUser := map[string]grantObj{}
	for _, g := range grants {
		byUser[g.UserID] = g
	}
	var errs []error
	for _, u := range p.spec.SeedUsers {
		email := strings.ToLower(strings.TrimSpace(u.Email))
		uid, err := p.findHuman(ctx, email)
		if err != nil {
			errs = append(errs, objErr("seed user", u.Email, err))
			continue
		}
		if uid == "" {
			p.rep.change(Created, "seed user", u.Email, "")
			if p.dry() {
				continue
			}
			given, family := u.GivenName, u.FamilyName
			if given == "" || family == "" {
				prof := profileFromEmail(email)
				if given == "" {
					given = prof["givenName"].(string)
				}
				if family == "" {
					family = prof["familyName"].(string)
				}
			}
			var out struct {
				UserID string `json:"userId"`
			}
			err := p.c.call(ctx, http.MethodPost, "/v2/users/human", map[string]any{
				"username":     email,
				"organization": map[string]any{"orgId": p.c.orgID},
				"profile":      map[string]any{"givenName": given, "familyName": family},
				"email":        map[string]any{"email": email, "isVerified": true},
				"password":     map[string]any{"password": u.Password, "changeRequired": false},
			}, &out, mint)
			if err != nil {
				errs = append(errs, objErr("seed user", u.Email, err))
				continue
			}
			uid = out.UserID
		}
		if len(u.Roles) == 0 {
			continue
		}
		want := map[string]bool{}
		for _, r := range u.Roles {
			want[r] = true
		}
		if err := p.grantRoles(ctx, u.Email, uid, byUser[uid], want); err != nil {
			errs = append(errs, objErr("seed user", u.Email, err))
		}
	}
	return errors.Join(errs...)
}

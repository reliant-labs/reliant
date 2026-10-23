package identity

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

const (
	tokenTypeBearer = "ACCESS_TOKEN_TYPE_BEARER"
	tokenTypeJWT    = "ACCESS_TOKEN_TYPE_JWT"
	loginClientRole = "IAM_LOGIN_CLIENT"
)

// RotateTarget names credentials to replace NOW, whatever their remaining
// lifetime. The old credential keeps working for the overlap window.
type RotateTarget struct {
	// Application rotates the broker token of this native_broker application.
	Application string
	// MachineUser rotates this machine user's key (and token, if it has one).
	MachineUser string
}

// Rotate runs a full pass and additionally replaces the named credentials.
// The superseded ones stay valid for the configured overlap (Rotation) and are
// listed in Report.Retiring; a later pass deletes them once it has elapsed.
// The Output published at the end carries the new credentials, so a consumer
// that re-reads it switches over before the old one is removed.
func (r *Reconciler) Rotate(ctx context.Context, spec Spec, targets ...RotateTarget) (*Report, error) {
	return r.reconcile(ctx, spec, targets)
}

func (p *run) forced(username string) bool {
	for _, t := range p.force {
		if t.MachineUser == username {
			return true
		}
		for _, a := range p.spec.Applications {
			if a.Kind == AppNativeBroker && t.Application == a.Name && a.BrokerUsername() == username {
				return true
			}
		}
	}
	return false
}

// accountPlan is a machine account to converge: a declared MachineUser or the
// broker account of a native_broker application.
type accountPlan struct {
	MachineUser
	tokenType string
	broker    bool
	app       string
}

func (p *run) accounts() []accountPlan {
	var out []accountPlan
	for _, m := range p.spec.MachineUsers {
		out = append(out, accountPlan{MachineUser: m, tokenType: tokenTypeJWT})
	}
	for _, a := range p.spec.Applications {
		if a.Kind != AppNativeBroker {
			continue
		}
		out = append(out, accountPlan{
			MachineUser: MachineUser{
				Username:      a.BrokerUsername(),
				Name:          a.BrokerUsername(),
				Description:   "Completes browser sign-in for " + a.Name + " via the API-only login flow (forge)",
				InstanceRoles: []string{loginClientRole},
				Token:         true,
			},
			// A bearer account: its credential is a PAT, which needs no JWT
			// access-token type.
			tokenType: tokenTypeBearer,
			broker:    true,
			app:       a.Name,
		})
	}
	return out
}

type userObj struct {
	ID       string `json:"id"`
	UserName string `json:"userName"`
	Machine  *struct {
		Name            string `json:"name"`
		Description     string `json:"description"`
		AccessTokenType string `json:"accessTokenType"`
	} `json:"machine"`
}

func (p *run) ensureMachineUsers(ctx context.Context) error {
	var errs []error
	for _, acct := range p.accounts() {
		if err := p.ensureAccount(ctx, acct); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (p *run) ensureAccount(ctx context.Context, a accountPlan) error {
	id, err := p.ensureMachineUser(ctx, a)
	if err != nil {
		return err
	}
	out := MachineOutput{UserID: id}
	for _, role := range a.InstanceRoles {
		if err := p.ensureInstanceRole(ctx, a.Username, id, role); err != nil {
			return err
		}
	}
	if id == "" { // dry run, user not created yet
		p.machine[a.Username] = out
		return nil
	}
	p.users[a.Username] = id

	if a.Key {
		key, err := p.ensureMachineKey(ctx, a, id)
		if err != nil {
			return err
		}
		out.KeyID, out.KeyExpires = key.id, key.expires
	}
	if a.Token {
		tok, err := p.ensureToken(ctx, a, id)
		if err != nil {
			return err
		}
		out.TokenID, out.TokenExpires, out.Token = tok.id, tok.expires, tok.secret
	}
	p.machine[a.Username] = out
	return nil
}

func (p *run) findUser(ctx context.Context, username string) (*userObj, error) {
	users, err := pageAll[userObj](ctx, p.c, "/management/v1/users/_search", map[string]any{
		"queries": []any{map[string]any{"userNameQuery": map[string]any{"userName": username, "method": "TEXT_QUERY_METHOD_EQUALS"}}},
	}, idem)
	if err != nil {
		return nil, err
	}
	for i := range users {
		if users[i].UserName == username {
			return &users[i], nil
		}
	}
	return nil, nil
}

// ensureMachineUser finds the machine user by username or creates it. A
// non-machine user of that name is an error, never adopted: quietly granting a
// service account's privileges to a human who happens to share the name would
// be the wrong way to find out.
func (p *run) ensureMachineUser(ctx context.Context, a accountPlan) (string, error) {
	name := a.Name
	if name == "" {
		name = a.Username
	}
	cur, err := p.findUser(ctx, a.Username)
	if err != nil {
		return "", objErr("machine user", a.Username, err)
	}
	if cur == nil {
		p.rep.change(Created, "machine user", a.Username, "")
		if p.dry() {
			return "", nil
		}
		var out struct {
			UserID string `json:"userId"`
		}
		err := p.c.call(ctx, http.MethodPost, "/management/v1/users/machine", map[string]any{
			"userName": a.Username, "name": name, "description": a.Description, "accessTokenType": a.tokenType,
		}, &out, mint)
		if isAlreadyExists(err) {
			// The search lags the write that created it; the user is there.
			if cur, ferr := p.findUser(ctx, a.Username); ferr == nil && cur != nil {
				return cur.ID, nil
			}
		}
		if err != nil {
			return "", objErr("machine user", a.Username, err)
		}
		p.logf("created machine user", "username", a.Username, "user_id", out.UserID)
		return out.UserID, nil
	}
	if cur.Machine == nil {
		return "", objErr("machine user", a.Username, errors.New("a user of that name exists and is a human, not a machine; refusing to adopt it as a service account"))
	}
	// BEARER is the enum's zero value, which the gateway omits on reads.
	if cur.Machine.Name == name && cur.Machine.Description == a.Description &&
		orDefault(cur.Machine.AccessTokenType, tokenTypeBearer) == a.tokenType {
		return cur.ID, nil
	}
	p.rep.change(Updated, "machine user", a.Username, "name, description or token type")
	if p.dry() {
		return cur.ID, nil
	}
	err = p.c.call(ctx, http.MethodPut, "/management/v1/users/"+url.PathEscape(cur.ID)+"/machine", map[string]any{
		"name": name, "description": a.Description, "accessTokenType": a.tokenType,
	}, nil, idem)
	if err != nil && !isNoChange(err) {
		return "", objErr("machine user", a.Username, err)
	}
	return cur.ID, nil
}

// ensureInstanceRole adds an instance-level membership role (for example
// IAM_LOGIN_CLIENT), keeping the roles the account already holds.
//
// ADD and UPDATE are different endpoints, and picking the wrong one fails in a
// way that names the INSTANCE rather than the missing membership: PUT against a
// user with no membership yet answers "Errors.NotFound (INSTANCE-D8JxR)". A
// freshly created service account is always in that state.
func (p *run) ensureInstanceRole(ctx context.Context, username, userID, role string) error {
	if userID == "" {
		p.rep.change(Granted, "instance role", username, role)
		return nil
	}
	members, err := pageAll[struct {
		UserID string   `json:"userId"`
		Roles  []string `json:"roles"`
	}](ctx, p.c, "/admin/v1/members/_search", map[string]any{}, noOrg)
	if err != nil {
		return objErr("instance role", username, fmt.Errorf("list instance members: %w", err))
	}
	var roles []string
	member := false
	for _, m := range members {
		if m.UserID == userID {
			roles, member = m.Roles, true
		}
	}
	for _, r := range roles {
		if r == role {
			return nil
		}
	}
	p.rep.change(Granted, "instance role", username, role)
	if p.dry() {
		return nil
	}
	if member {
		err = p.c.call(ctx, http.MethodPut, "/admin/v1/members/"+url.PathEscape(userID),
			map[string]any{"roles": append(append([]string{}, roles...), role)}, nil, idem)
	} else {
		err = p.c.call(ctx, http.MethodPost, "/admin/v1/members",
			map[string]any{"userId": userID, "roles": []string{role}}, nil, idem)
	}
	if err != nil && !isNoChange(err) {
		return objErr("instance role", username, fmt.Errorf("grant %s: %w", role, err))
	}
	return nil
}

// ---------------------------------------------------------------------------
// credentials
// ---------------------------------------------------------------------------

// cred is the current credential of one kind.
type cred struct {
	id      string
	expires time.Time
	secret  string
}

type liveCred struct {
	ID      string `json:"id"`
	Expires string `json:"expirationDate"`
	Details struct {
		Created string `json:"creationDate"`
	} `json:"details"`
}

func (l liveCred) expiry() time.Time { t, _ := time.Parse(time.RFC3339Nano, l.Expires); return t }
func (l liveCred) created() time.Time {
	t, _ := time.Parse(time.RFC3339Nano, l.Details.Created)
	return t
}

// hasLife reports whether the credential outlives now+within. One with no
// expiry never lapses.
func (l liveCred) hasLife(now time.Time, within time.Duration) bool {
	exp := l.expiry()
	return exp.IsZero() || exp.After(now.Add(within))
}

func (p *run) listCreds(ctx context.Context, userID, kind string) ([]liveCred, error) {
	return pageAll[liveCred](ctx, p.c, "/management/v1/users/"+url.PathEscape(userID)+"/"+kind+"/_search", map[string]any{}, idem)
}

// retire reports every live credential other than `current` as superseded, and
// deletes those whose overlap has elapsed. The overlap is measured from when
// the current credential was created, which Zitadel records, so the rule needs
// no state of its own and survives an emptied KeyStore.
func (p *run) retire(ctx context.Context, username, kind, kindPath, userID string, live []liveCred, currentID string, currentCreated time.Time, overlap time.Duration) error {
	var errs []error
	now := p.c.now()
	for _, l := range live {
		if l.ID == currentID {
			continue
		}
		deleteAfter := currentCreated.Add(overlap)
		if !now.Before(deleteAfter) && !p.o.KeepSuperseded {
			p.rep.change(Pruned, kind, username, "id "+l.ID)
			if p.dry() {
				continue
			}
			err := p.c.call(ctx, http.MethodDelete, "/management/v1/users/"+url.PathEscape(userID)+"/"+kindPath+"/"+url.PathEscape(l.ID), nil, nil, idem)
			if err != nil && !isNotFound(err) {
				errs = append(errs, objErr(kind, username, fmt.Errorf("delete superseded %s %s: %w", kind, l.ID, err)))
			}
			continue
		}
		p.rep.Retiring = append(p.rep.Retiring, Retiring{Username: username, Kind: kind, ID: l.ID, DeleteAfter: deleteAfter})
	}
	return errors.Join(errs...)
}

// ensureMachineKey leaves the machine user with one current key, the one held
// by the KeyStore, and reports it.
//
// ORDER IS THE POINT. A replacement is issued and DELIVERED to the store before
// anything is deleted, so the store never holds a key Zitadel no longer
// accepts. If delivery fails the new key is deleted again and the previous one
// is untouched. The stored key is kept only while it names this user, is still
// live at Zitadel and has more than Rotation.MachineKeyRenewWithin left; an
// empty store, a deleted-and-recreated user or a key near expiry all replace
// it, which is also what makes an emptied store recoverable by running again.
func (p *run) ensureMachineKey(ctx context.Context, a accountPlan, userID string) (cred, error) {
	rot := p.o.Rotation
	live, err := p.listCreds(ctx, userID, "keys")
	if err != nil {
		return cred{}, objErr("machine key", a.Username, err)
	}
	stored, err := p.o.Keys.Load(ctx, MachineKeyEntry(a.Username))
	if err != nil {
		return cred{}, objErr("machine key", a.Username, fmt.Errorf("read the stored key: %w", err))
	}
	var current *liveCred
	if stored != nil {
		if doc, perr := ParseMachineKey(stored); perr == nil && doc.UserID == userID {
			for i := range live {
				if live[i].ID == doc.KeyID && live[i].hasLife(p.c.now(), rot.MachineKeyRenewWithin) {
					current = &live[i]
				}
			}
		}
	}
	created := time.Time{}
	if current == nil || p.forced(a.Username) {
		p.rep.change(Issued, "machine key", a.Username, "")
		if p.dry() {
			return cred{}, nil
		}
		now := p.c.now()
		expires := now.Add(rot.MachineKeyTTL).UTC()
		var out struct {
			KeyID      string `json:"keyId"`
			KeyDetails string `json:"keyDetails"`
		}
		err := p.c.call(ctx, http.MethodPost, "/management/v1/users/"+url.PathEscape(userID)+"/keys", map[string]any{
			"type": "KEY_TYPE_JSON", "expirationDate": expires.Format(time.RFC3339),
		}, &out, mint)
		if err != nil {
			return cred{}, objErr("machine key", a.Username, err)
		}
		drop := func() { p.dropCred(ctx, userID, "keys", out.KeyID) }
		doc, err := base64.StdEncoding.DecodeString(out.KeyDetails)
		if err != nil {
			drop()
			return cred{}, objErr("machine key", a.Username, fmt.Errorf("decode the issued key: %w", err))
		}
		if _, err := ParseMachineKey(doc); err != nil {
			drop()
			return cred{}, objErr("machine key", a.Username, fmt.Errorf("the issued key is unusable: %w", err))
		}
		if err := p.o.Keys.Save(ctx, MachineKeyEntry(a.Username), doc); err != nil {
			drop()
			return cred{}, objErr("machine key", a.Username, fmt.Errorf("deliver the key (the previous key, if any, is untouched): %w", err))
		}
		p.logf("issued and delivered a machine key", "username", a.Username, "key_id", out.KeyID)
		live = append(live, liveCred{ID: out.KeyID, Expires: expires.Format(time.RFC3339)})
		current = &live[len(live)-1]
		created = now
	} else {
		created = current.created()
		if created.IsZero() {
			created = p.c.now()
		}
	}
	err = p.retire(ctx, a.Username, "machine key", "keys", userID, live, current.ID, created, rot.MachineKeyOverlap)
	return cred{id: current.ID, expires: current.expiry()}, err
}

// dropCred deletes a credential, best effort: a failure here must not undo a
// delivery that succeeded.
func (p *run) dropCred(ctx context.Context, userID, kind, id string) {
	if id == "" {
		return
	}
	err := p.c.call(ctx, http.MethodDelete, "/management/v1/users/"+url.PathEscape(userID)+"/"+kind+"/"+url.PathEscape(id), nil, nil, idem)
	if err != nil && !isNotFound(err) {
		p.c.log.WarnContext(ctx, "identity: could not delete a credential", "user_id", userID, "kind", kind, "id", id, "err", err.Error())
	}
}

// tokenRecord is what the KeyStore holds for a personal access token: Zitadel
// shows the token exactly once.
type tokenRecord struct {
	TokenID string    `json:"token_id"`
	Token   string    `json:"token"`
	Expires time.Time `json:"expires"`
}

// ensureToken keeps a current expiring personal access token for a broker or
// service account. It follows the same order as ensureMachineKey: mint, deliver,
// only then retire the old one after its overlap.
//
// A stored token is kept only if the instance still lists it, it has more than
// the renew window left, AND it authenticates as THIS account — a token for a
// different account is not the broker's however valid, and reusing it would
// hand the login path that account's authority.
func (p *run) ensureToken(ctx context.Context, a accountPlan, userID string) (cred, error) {
	rot := p.o.Rotation
	ttl, within, overlap := rot.MachineKeyTTL, rot.MachineKeyRenewWithin, rot.MachineKeyOverlap
	if a.broker {
		ttl, within, overlap = rot.BrokerTokenTTL, rot.BrokerRenewWithin, rot.BrokerOverlap
	}
	live, err := p.listCreds(ctx, userID, "pats")
	if err != nil {
		return cred{}, objErr("token", a.Username, err)
	}
	var rec tokenRecord
	raw, err := p.o.Keys.Load(ctx, BrokerTokenEntry(a.Username))
	if err != nil {
		return cred{}, objErr("token", a.Username, fmt.Errorf("read the stored token: %w", err))
	}
	if raw != nil {
		_ = json.Unmarshal(raw, &rec)
	}
	var current *liveCred
	if rec.TokenID != "" && rec.Token != "" {
		for i := range live {
			if live[i].ID == rec.TokenID && live[i].hasLife(p.c.now(), within) {
				current = &live[i]
			}
		}
		if current != nil {
			owner, err := p.tokenOwner(ctx, rec.Token)
			if err != nil {
				return cred{}, objErr("token", a.Username, err)
			}
			if owner != userID {
				current = nil
			}
		}
	}
	var secret string
	created := time.Time{}
	if current == nil || p.forced(a.Username) {
		p.rep.change(Issued, "token", a.Username, "")
		if p.dry() {
			return cred{}, nil
		}
		now := p.c.now()
		expires := now.Add(ttl).UTC()
		var out struct {
			TokenID string `json:"tokenId"`
			Token   string `json:"token"`
		}
		err := p.c.call(ctx, http.MethodPost, "/management/v1/users/"+url.PathEscape(userID)+"/pats",
			map[string]any{"expirationDate": expires.Format(time.RFC3339)}, &out, mint)
		if err != nil {
			return cred{}, objErr("token", a.Username, err)
		}
		if out.Token == "" || out.TokenID == "" {
			return cred{}, objErr("token", a.Username, errors.New("the instance accepted the request but returned no token"))
		}
		blob, _ := json.Marshal(tokenRecord{TokenID: out.TokenID, Token: out.Token, Expires: expires})
		if err := p.o.Keys.Save(ctx, BrokerTokenEntry(a.Username), blob); err != nil {
			p.dropCred(ctx, userID, "pats", out.TokenID)
			return cred{}, objErr("token", a.Username, fmt.Errorf("deliver the token (the previous token, if any, is untouched): %w", err))
		}
		p.logf("issued and delivered a token", "username", a.Username, "token_id", out.TokenID)
		live = append(live, liveCred{ID: out.TokenID, Expires: expires.Format(time.RFC3339)})
		current = &live[len(live)-1]
		secret, created = out.Token, now
	} else {
		secret = rec.Token
		if created = current.created(); created.IsZero() {
			created = p.c.now()
		}
	}
	err = p.retire(ctx, a.Username, "token", "pats", userID, live, current.ID, created, overlap)
	return cred{id: current.ID, expires: current.expiry(), secret: secret}, err
}

// tokenOwner returns the id of the user token authenticates as, or "" when the
// instance rejects it. Any other failure is an error: an unreachable instance
// must not be mistaken for a dead token, which would mint a fresh credential
// every time the instance is slow to answer.
func (p *run) tokenOwner(ctx context.Context, token string) (string, error) {
	var me struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	err := p.c.call(ctx, http.MethodGet, "/auth/v1/users/me", nil, &me, callOpt{idempotent: true, bearer: token, noOrg: true})
	if err != nil {
		if ae := asAPI(err); ae != nil && (ae.Status == http.StatusUnauthorized || ae.Status == http.StatusForbidden) {
			return "", nil
		}
		return "", fmt.Errorf("check the stored token: %w", err)
	}
	return me.User.ID, nil
}

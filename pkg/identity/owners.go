package identity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// owners
// ---------------------------------------------------------------------------

// ownerInviteRecord is the KeyStore's memory of the invitations one owner was
// sent. The instance cannot answer "was this person invited, and when": an
// invite code is a write-only event, and asking would mean reading the user's
// event history.
type ownerInviteRecord struct {
	Sent int       `json:"sent"`
	Last time.Time `json:"last"`
}

// ownerInviteEntry is the KeyStore name of an owner's invitation record. The
// address is hashed: the store's names may appear in logs and paths.
func ownerInviteEntry(email string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return keyOwnerPrefix + hex.EncodeToString(sum[:])
}

// ensureOwners makes every owner able to get in, and never takes anything
// away from one.
//
// Each owner is created (email verified, as ensureHuman does: the address is
// reviewed config) and granted their roles ADDITIVELY. An owner with no
// credential of any kind is then invited, at most once per
// OwnerInviteInterval and OwnerInviteLimit times in all, through Zitadel's
// invite code (the mail links to Invite.URLTemplate). "No credential" is the
// same rule ensureSignInPath uses, with the same short-circuit for an account
// this pass created. Owners are never locked, deleted or revoked, including
// ones removed from the declaration: after the first pass they are managed in
// the console.
func (p *run) ensureOwners(ctx context.Context) error {
	if len(p.spec.Owners) == 0 {
		return nil
	}
	var grants []grantObj
	if p.projectID != "" {
		var err error
		if grants, err = p.listGrants(ctx); err != nil {
			return objErr("owners", p.spec.Project.Name, err)
		}
	}
	byUser := map[string]grantObj{}
	for _, g := range grants {
		byUser[g.UserID] = g
	}
	var errs []error
	for _, o := range p.spec.Owners {
		email := strings.ToLower(strings.TrimSpace(o.Email))
		uid, err := p.ensureHuman(ctx, email)
		if err != nil {
			errs = append(errs, objErr("owner", email, err))
			continue
		}
		want := map[string]bool{}
		for _, r := range o.Roles {
			want[r] = true
		}
		if err := p.grantRoles(ctx, email, uid, byUser[uid], want); err != nil {
			errs = append(errs, objErr("owner", email, err))
		}
		if uid == "" {
			continue // dry run: the account does not exist yet
		}
		if err := p.ensureOwnerInvite(ctx, o, email, uid); err != nil {
			errs = append(errs, objErr("owner", email, err))
		}
	}
	return errors.Join(errs...)
}

func (p *run) ensureOwnerInvite(ctx context.Context, o Owner, email, uid string) error {
	if !p.createdHumans[email] {
		var methods struct {
			AuthMethodTypes []string `json:"authMethodTypes"`
		}
		if err := p.c.call(ctx, http.MethodGet, "/v2/users/"+url.PathEscape(uid)+"/authentication_methods", nil, &methods, idem); err != nil {
			return fmt.Errorf("read authentication methods: %w", err)
		}
		if len(methods.AuthMethodTypes) > 0 {
			return nil // they can get in; nothing to send
		}
	}
	if o.Invite == nil {
		p.rep.OwnersPending = append(p.rep.OwnersPending, email)
		return nil
	}
	entry := ownerInviteEntry(email)
	var rec ownerInviteRecord
	raw, err := p.o.Keys.Load(ctx, entry)
	if err != nil {
		return fmt.Errorf("read the invitation record: %w", err)
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &rec); err != nil {
			// A record that cannot be read must not become a mail loop:
			// treat it as the cap reached and say so.
			p.rep.warn("owner %s: the invitation record is unreadable (%v); no further invitation is sent until it is removed from the key store", email, err)
			p.rep.OwnersPending = append(p.rep.OwnersPending, email)
			return nil
		}
	}
	now := p.o.Now()
	switch {
	case rec.Sent >= OwnerInviteLimit:
		p.rep.warn("owner %s has been invited %d times without signing in; no further invitation is sent (invite them from the console)", email, rec.Sent)
		p.rep.OwnersPending = append(p.rep.OwnersPending, email)
		return nil
	case rec.Sent > 0 && now.Sub(rec.Last) < OwnerInviteInterval:
		p.rep.OwnersPending = append(p.rep.OwnersPending, email)
		return nil
	}

	p.rep.change(Issued, "owner invitation", email, "")
	if p.dry() {
		return nil
	}
	send := map[string]any{}
	if o.Invite.URLTemplate != "" {
		send["urlTemplate"] = o.Invite.URLTemplate
	}
	if o.Invite.ApplicationName != "" {
		send["applicationName"] = o.Invite.ApplicationName
	}
	err = p.c.call(ctx, http.MethodPost, "/v2/users/"+url.PathEscape(uid)+"/invite_code", map[string]any{"sendCode": send}, nil, mint)
	if ae := asAPI(err); ae != nil && ae.ID == "COMMAND-EF34g" {
		// "already initialised": the account gained a credential after the
		// read above (the read lags the write). Nothing to send after all.
		p.rep.Changes = p.rep.Changes[:len(p.rep.Changes)-1]
		return nil
	}
	if err != nil {
		return fmt.Errorf("send the invitation: %w", err)
	}
	rec.Sent++
	rec.Last = now
	b, _ := json.Marshal(rec)
	if err := p.o.Keys.Save(ctx, entry, b); err != nil {
		// The mail is already on its way, and with no record the next pass
		// sends another. A duplicate invitation is the lesser failure.
		return fmt.Errorf("record the invitation: %w", err)
	}
	p.rep.OwnersInvited = append(p.rep.OwnersInvited, email)
	return nil
}

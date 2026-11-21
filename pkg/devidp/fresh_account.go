package devidp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ── A fresh account for an address whose account holds unproven credentials
//
// An unverified account that holds a credential — set before forge#2121's
// fix, or a contact-only account (MetaContactOnly) — is never verified by a
// proof (proveAddress). Its owner proves the address into a NEW account
// instead, and that account takes the address over once it is proven:
//
//  1. Before the proof: a credential-less account N is created for the
//     address with a username of its own, forge-fresh-<S's id>, so the
//     address's login name keeps naming the old account S, and N carries
//     MetaSupersedes = S. The username is derived from S, so concurrent
//     requests converge on one N (freshAccountFor). The issuer mails N's code. Zitadel accepts a
//     second user with the same email when the usernames differ (checked
//     against v4.19.5). S is not touched: an unproven request changes
//     nothing on it, and S cannot sign in meanwhile — an unverified account
//     is refused by the verified-email gate.
//  2. N's proof verifies N. Only then (finishFreshAccount), in this order:
//     a. S is annotated (MetaSupersededEmail, then MetaSupersededBy);
//     b. S's username, if it is the address, becomes forge-superseded-<id>,
//        so the address stops naming S as a login name;
//     c. S's email becomes superseded-<id>@forge.invalid (set with a
//        returned code, so nothing is mailed), so the address stops naming
//        S in a search;
//     d. N's username becomes the address;
//     e. N's MetaSupersedes is cleared: the takeover is complete.
//
// Every step reads before it writes and is idempotent, so the sequence
// resumes wherever it stopped: from a retried verification, and from any
// lookup of the address (accountFor). Between (2) and (2d) the address
// resolves to N wherever the broker resolves it (FindUserByEmail prefers the
// one verified account), never to S. Nothing is deleted, and no credential on
// S is removed: S keeps whatever it holds, under a name nobody types.

// freshUsername is the username of the fresh account that takes held's
// address over. It is DERIVED from held, not random: the issuer keeps
// usernames unique, so of two requests that race to create it — on one
// replica or several — exactly one succeeds, and the other reads the winner
// and adopts it. The replacement converges on one account with no lock.
func freshUsername(heldID string) string { return "forge-fresh-" + heldID }

func supersededUsername(id string) string { return "forge-superseded-" + id }
func supersededEmail(id string) string    { return "superseded-" + id + "@forge.invalid" }

// usersByEmail lists every human account with the address,
// case-insensitively. Zitadel does not keep emails unique.
func (c *Client) usersByEmail(ctx context.Context, email string) ([]User, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return nil, fmt.Errorf("an email address is required")
	}
	hits, err := searchAll[humanUser](ctx, c, "/v2/users", map[string]any{
		"queries": []any{map[string]any{"emailQuery": map[string]any{
			"emailAddress": email,
			"method":       "TEXT_QUERY_METHOD_EQUALS_IGNORE_CASE",
		}}},
	}, "result", true)
	if err != nil {
		return nil, fmt.Errorf("search users by email: %w", err)
	}
	var out []User
	for _, h := range hits {
		if u, ok := h.user(); ok && strings.EqualFold(u.Email, email) {
			out = append(out, u)
		}
	}
	return out, nil
}

// setUsername renames an account (the v2 UpdateHumanUser).
func (c *Client) setUsername(ctx context.Context, userID, username string) error {
	if _, err := c.sendJSON(ctx, http.MethodPut, "/v2/users/human/"+url.PathEscape(userID), map[string]any{"username": username}); err != nil {
		return fmt.Errorf("rename the account: %w", err)
	}
	return nil
}

// setEmailUnmailed changes an account's address without mailing it: the
// issuer returns the verification code instead of sending one, and the
// address stays unverified.
func (c *Client) setEmailUnmailed(ctx context.Context, userID, email string) error {
	if _, err := c.postJSON(ctx, "/v2/users/"+url.PathEscape(userID)+"/email", map[string]any{"email": email, "returnCode": map[string]any{}}); err != nil {
		return fmt.Errorf("change the account's address: %w", err)
	}
	return nil
}

// holdsCredentials reports whether an unverified account holds anything a
// proof would make usable. A verified account is never "held".
func (r *SignInRoutes) holdsCredentials(ctx context.Context, u User) (bool, error) {
	if u.EmailVerified {
		return false, nil
	}
	kinds, err := r.cfg.Client.credentialKinds(ctx, u.ID)
	return len(kinds) > 0, err
}

// errAmbiguousAddress: several accounts have the address and none of the
// shapes above picks one. Nothing is created for it, or acted on.
var errAmbiguousAddress = errors.New("several accounts have the address and none is the one to act on")

// errNoMailToProve: the policy delivers no mail, so no fresh account is made.
var errNoMailToProve = errors.New("no mail is delivered here, so no address can be proven")

// accountFor resolves an address to the account the sign-in routes act on.
// None: ErrUserNotFound. One: that one — finishing its takeover first, when
// it is a verified fresh account whose takeover was cut short. Several (a fresh account beside the
// one it supersedes): the one verified account — finishing its takeover
// first —, else the one fresh account still waiting for its proof. Anything
// else is errAmbiguousAddress.
func (r *SignInRoutes) accountFor(ctx context.Context, email string) (User, error) {
	c := r.cfg.Client
	users, err := c.usersByEmail(ctx, email)
	if err != nil {
		return User{}, err
	}
	switch len(users) {
	case 0:
		return User{}, ErrUserNotFound
	case 1:
		// One match can still be a takeover cut short AFTER the old
		// account's email moved (the rename of this one, or the clearing of
		// its MetaSupersedes, failed): finish it. Idempotent, and a no-op
		// for an account that supersedes nothing.
		if users[0].EmailVerified {
			if _, err := r.finishFreshAccount(ctx, users[0]); err != nil {
				r.cfg.Logger.ErrorContext(ctx, "the fresh account could not take its address over yet; it resumes on the next sign-in", "user_id", users[0].ID, "error", err)
			}
		}
		return users[0], nil
	}
	var verified, fresh []User
	for _, u := range users {
		if u.EmailVerified {
			verified = append(verified, u)
			continue
		}
		if sup, _, err := c.Metadata(ctx, u.ID, MetaSupersedes); err == nil && sup != "" {
			fresh = append(fresh, u)
		}
	}
	switch {
	case len(verified) == 1:
		if _, err := r.finishFreshAccount(ctx, verified[0]); err != nil {
			r.cfg.Logger.ErrorContext(ctx, "the fresh account could not take its address over yet; it resumes on the next sign-in", "user_id", verified[0].ID, "error", err)
		}
		return verified[0], nil
	case len(verified) == 0 && len(fresh) == 1:
		return fresh[0], nil
	}
	return User{}, errAmbiguousAddress
}

// freshAccountFor is step 1: the credential-less account the owner of held's
// address proves into, its code mailed with template.
//
// It CONVERGES: there is one fresh account per held account, named
// freshUsername(held.ID). One already there is reused (its code mailed
// again). Otherwise it is created; if the issuer refuses the username as
// taken, a concurrent request created it first, and that one is read and
// adopted. Asking twice, on any replica, never makes two.
func (r *SignInRoutes) freshAccountFor(ctx context.Context, held User, template string, nu NewUser) (User, error) {
	if !r.cfg.Policy.MailDelivers() {
		// No mail can prove an address here: a fresh account could never
		// be proven, and would only stand beside held.
		return User{}, errNoMailToProve
	}
	c := r.cfg.Client
	email := normalizeEmail(held.Email)
	username := freshUsername(held.ID)
	adopt := func() (User, bool, error) {
		users, err := c.usersByEmail(ctx, email)
		if err != nil {
			return User{}, false, err
		}
		for _, u := range users {
			if u.ID != held.ID && strings.EqualFold(u.Username, username) {
				if !u.EmailVerified {
					if err := c.ResendEmailCode(ctx, u.ID, template); err != nil {
						return User{}, false, err
					}
				}
				return u, true, nil
			}
		}
		return User{}, false, nil
	}
	if u, ok, err := adopt(); err != nil || ok {
		return u, err
	}
	nu.Email, nu.VerifyURLTemplate = email, template
	nu.EmailVerified, nu.Password, nu.IDPLinks, nu.ContactOnly = false, "", nil, false
	nu.username, nu.supersedes = username, held.ID
	id, err := c.CreateUser(ctx, nu)
	if isAlreadyExists(err) {
		// Created first by a concurrent request. The issuer's search may
		// lag its write by a moment, so the winner is looked for a few
		// times before giving up.
		for wait := 50 * time.Millisecond; wait <= 800*time.Millisecond; wait *= 2 {
			if u, ok, aerr := adopt(); aerr != nil || ok {
				return u, aerr
			}
			t := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				t.Stop()
				return User{}, ctx.Err()
			case <-t.C:
			}
		}
		return User{}, fmt.Errorf("the fresh account for %s exists but cannot be read yet: %w", held.ID, err)
	}
	if err != nil {
		return User{}, err
	}
	r.cfg.Logger.InfoContext(ctx, "a fresh account was created for an address whose account holds unproven credentials",
		"user_id", id, "supersedes", held.ID)
	return User{ID: id, Email: email, Username: username}, nil
}

// isAlreadyExists reports the issuer's "already exists" refusal (gRPC
// ALREADY_EXISTS, HTTP 409): here, a username another account holds.
func isAlreadyExists(err error) bool {
	var apiErr *apiError
	return errors.As(err, &apiErr) && (apiErr.status == http.StatusConflict || strings.Contains(apiErr.body, `"code":6`))
}

// takeAddressOver runs finishFreshAccount for an account a proof just
// verified, read again so it is seen verified. A failure is logged, not
// returned: the address is proven either way, the account is reachable
// through it (FindUserByEmail prefers the verified account), and the
// takeover resumes on the next lookup of the address. superseded reports
// whether it replaced an account holding unproven credentials.
func (r *SignInRoutes) takeAddressOver(ctx context.Context, userID string) (superseded bool) {
	n, err := r.cfg.Client.FindUserByID(ctx, userID)
	if err == nil {
		superseded, err = r.finishFreshAccount(ctx, n)
	}
	if err != nil {
		r.cfg.Logger.ErrorContext(ctx, "the proven account could not take its address over yet; it resumes on the next lookup", "user_id", userID, "error", err)
	}
	return superseded
}

// finishFreshAccount is step 2, for an account whose address is proven: if
// it supersedes another (MetaSupersedes), move that one out of the address's
// way and give this one the address as its username. superseded reports
// whether it took an address over. Idempotent; see the file header.
func (r *SignInRoutes) finishFreshAccount(ctx context.Context, n User) (superseded bool, err error) {
	if !n.EmailVerified {
		return false, nil
	}
	c := r.cfg.Client
	sup, _, err := c.Metadata(ctx, n.ID, MetaSupersedes)
	if err != nil || sup == "" {
		return false, err
	}
	address := normalizeEmail(n.Email)
	s, err := c.FindUserByID(ctx, sup)
	switch {
	case errors.Is(err, ErrUserNotFound):
		// Gone already: nothing stands in the address's way.
	case err != nil:
		return false, err
	case s.EmailVerified:
		// Proven meanwhile by something other than a proof here (an
		// administrator): it is somebody's real account. Leave both as
		// they are, for a person to decide.
		return false, fmt.Errorf("the account %s it supersedes has a verified address now; an administrator decides", s.ID)
	default:
		if _, recorded, err := c.Metadata(ctx, s.ID, MetaSupersededBy); err != nil {
			return false, err
		} else if !recorded {
			if err := c.SetMetadata(ctx, s.ID, MetaSupersededEmail, s.Email); err != nil {
				return false, err
			}
			if err := c.SetMetadata(ctx, s.ID, MetaSupersededBy, n.ID); err != nil {
				return false, err
			}
		}
		if strings.EqualFold(s.Username, address) || strings.EqualFold(s.LoginName, address) {
			if err := c.setUsername(ctx, s.ID, supersededUsername(s.ID)); err != nil {
				return false, err
			}
		}
		if strings.EqualFold(s.Email, address) {
			if err := c.setEmailUnmailed(ctx, s.ID, supersededEmail(s.ID)); err != nil {
				return false, err
			}
		}
	}
	if !strings.EqualFold(n.Username, address) {
		if err := c.setUsername(ctx, n.ID, address); err != nil {
			return false, err
		}
	}
	if err := c.DeleteMetadata(ctx, n.ID, MetaSupersedes); err != nil {
		return false, err
	}
	r.cfg.Logger.InfoContext(ctx, "a fresh account took its address over; the account it superseded was renamed, nothing deleted",
		"user_id", n.ID, "superseded", sup)
	return true, nil
}

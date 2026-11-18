package devidp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// shrinkNewUserWait bounds the new-user wait for one test.
func shrinkNewUserWait(t *testing.T, d time.Duration) {
	t.Helper()
	prev := newUserVisibleWithin
	newUserVisibleWithin = d
	t.Cleanup(func() { newUserVisibleWithin = prev })
}

var testFlow = FlowConfig{ClientID: "client", RedirectURI: testAppBase + "/cb"}

// exactCalls counts calls to exactly "METHOD /path" (callCount matches a prefix).
func exactCalls(z *fakeZitadel, call string) int {
	z.mu.Lock()
	defer z.mu.Unlock()
	n := 0
	for _, c := range z.calls {
		if c == call {
			n++
		}
	}
	return n
}

// The issuer's user projection lags the creation: the first session
// creations after the account was made answer "User could not be found".
// Observed against a dev Zitadel 12s after it started, where the sign-up
// created the account and then answered "That account could not be created".
func TestSignUp_WaitsForTheNewAccountToBecomeVisible(t *testing.T) {
	z := newFakeZitadel(t)
	z.projectionLag = 3
	tokens, err := z.client().SignUp(context.Background(), testFlow,
		Registration{Email: "new@acme.com", Password: "a-long-password"})
	if err != nil {
		t.Fatalf("SignUp: %v", err)
	}
	u := z.userByEmail("new@acme.com")
	if tokens.AccessToken != "at-"+u.id {
		t.Errorf("tokens are for %q, want the new account %s", tokens.AccessToken, u.id)
	}
	if got := z.callCount("POST /v2/sessions"); got != 4 {
		t.Errorf("session creations = %d, want 4 (three refused while unprojected, one accepted)", got)
	}
	if got := exactCalls(z, "POST /v2/users"); got != 0 {
		t.Errorf("SignUp searched users %d times: the new account is named by the id its creation returned", got)
	}
}

// A new account that never becomes visible is still an account: the error
// says the sign-in failed, not the creation.
func TestSignUp_ASignInThatNeverSucceedsIsNotACreationFailure(t *testing.T) {
	shrinkNewUserWait(t, 50*time.Millisecond)
	z := newFakeZitadel(t)
	z.projectionLag = -1
	_, err := z.client().SignUp(context.Background(), testFlow,
		Registration{Email: "new@acme.com", Password: "a-long-password"})
	if !errors.Is(err, ErrSignInAfterSignUp) {
		t.Fatalf("SignUp error = %v, want ErrSignInAfterSignUp", err)
	}
	if !isUserNotFound(err) {
		t.Errorf("SignUp error = %v, want it to carry the issuer's QUERY-Dfbg2", err)
	}
	if z.userByEmail("new@acme.com") == nil {
		t.Fatal("the account was not created")
	}
}

// Only a sign-in after a creation waits. An ordinary sign-in naming an
// unknown account fails at once: waiting would make "no such account"
// slower than "wrong password", an account oracle.
func TestSignIn_AnUnknownAccountIsNotRetried(t *testing.T) {
	z := newFakeZitadel(t)
	_, err := z.client().SignIn(context.Background(), testFlow, Credentials{LoginName: "ghost@acme.com", Password: "a-long-password"})
	if !isUserNotFound(err) {
		t.Fatalf("SignIn error = %v, want QUERY-Dfbg2", err)
	}
	if got := z.callCount("POST /v2/sessions"); got != 1 {
		t.Errorf("session creations = %d, want 1", got)
	}
}

// Any refusal other than not-found returns at once, even for a new account:
// a wrong password is an answer, not lag.
func TestCreateSessionForNewUser_OnlyNotFoundIsRetried(t *testing.T) {
	z := newFakeZitadel(t)
	u := z.addUser("a@acme.com", true, "a-long-password")
	_, err := z.client().createSessionForNewUser(context.Background(), Credentials{UserID: u.id, Password: "wrong-password"})
	if err == nil || isUserNotFound(err) {
		t.Fatalf("error = %v, want the wrong-password refusal", err)
	}
	if got := z.callCount("POST /v2/sessions"); got != 1 {
		t.Errorf("session creations = %d, want 1", got)
	}
}

func TestCreateSessionForNewUser_StopsWhenTheContextEnds(t *testing.T) {
	z := newFakeZitadel(t)
	u := z.addUser("a@acme.com", true, "a-long-password")
	z.mu.Lock()
	u.unprojected = -1
	z.mu.Unlock()
	// The context ends after the first answer arrives and before the retry,
	// with no clock involved: the transport cancels it on the response.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := z.client()
	c.http = &http.Client{Transport: cancelOnResponse{cancel: cancel}}
	_, err := c.createSessionForNewUser(ctx, Credentials{UserID: u.id, Password: "a-long-password"})
	if !errors.Is(err, context.Canceled) || !isUserNotFound(err) {
		t.Fatalf("error = %v, want the not-found and the context's cancellation", err)
	}
	if got := z.callCount("POST /v2/sessions"); got != 1 {
		t.Errorf("session creations = %d, want 1: no retry after the context ended", got)
	}
}

// cancelOnResponse cancels a context once a response has been received in
// full (the body is buffered first, so reading it cannot see the cancel).
type cancelOnResponse struct{ cancel context.CancelFunc }

func (c cancelOnResponse) RoundTrip(r *http.Request) (*http.Response, error) {
	defer c.cancel()
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp, nil
}

// The policy's mail-less sign-up creates and signs in in one request, so it
// waits for the new account too.
func TestRegisterWithoutMail_WaitsForTheNewAccount(t *testing.T) {
	z := newFakeZitadel(t)
	z.projectionLag = 2
	h := newHarnessOn(t, z, LoginPolicy{Present: true, Version: 1, Signup: SignupOpen, SignupRole: "member",
		Methods: []string{MethodPassword}, Mail: MailNone})
	if status, body := h.browser().post("/auth/register", map[string]any{"email": "new@else.com", "password": "a-long-password"}); status != 200 {
		t.Fatalf("register: %d %v", status, body)
	}
}

// A social sign-in that creates the account signs it in once it is visible.
func TestSocialSignUp_WaitsForTheNewAccount(t *testing.T) {
	z := newFakeZitadel(t)
	z.projectionLag = 2
	h := newHarnessOn(t, z, policyWith(SignupOpen, allMethods, google))
	b := h.browser()
	id := startSocial(t, b)
	z.addIntent(id, "tok", intentResult("g@acme.com", true))
	if status, body := b.post("/auth/social/callback", map[string]any{"id": id, "token": "tok"}); status != 200 {
		t.Fatalf("callback: %d %v", status, body)
	}
}

// ── Cross-account identity selection ──────────────────────────────────
//
// The sign-in after a creation names the account by the id the creation
// returned. These pin that it can never land on ANOTHER account, while the
// new one is still invisible to the issuer's projection and the other one
// is not. Each neighbour holds the same password as the new account, the
// worst case: a sign-in that resolved to it would succeed.
//
// Two of them are real hazards of resolving by address instead (what
// SignUp did before): Zitadel does not enforce email uniqueness, and a
// username account can have a login name equal to someone else's address.

type neighbour struct {
	name       string
	email      string // the existing account's address
	loginName  string // its login name ("" = its address)
	newAddress string // the address the new account signs up with
	// routeRefuses: the policy sign-up refuses the address as already
	// registered (its pre-check sees the neighbour), so no account is made.
	routeRefuses bool
}

var neighbours = []neighbour{
	{name: "login name equal to the new address", email: "bob.personal@else.com", loginName: "bob@acme.com", newAddress: "bob@acme.com"},
	{name: "same address, other case", email: "BOB@acme.com", loginName: "bob-old", newAddress: "bob@acme.com", routeRefuses: true},
	{name: "shared local-part prefix and domain", email: "bob.smith@acme.com", newAddress: "bob@acme.com"},
	{name: "address extending the new one", email: "bob@acme.com.au", newAddress: "bob@acme.com"},
}

const sharedPassword = "a-long-password"

// seedNeighbour adds the existing, projected account.
func seedNeighbour(t *testing.T, z *fakeZitadel, n neighbour) *fakeUser {
	t.Helper()
	other := z.addUser(n.email, true, sharedPassword)
	if n.loginName != "" {
		z.mu.Lock()
		other.loginName = n.loginName
		z.mu.Unlock()
	}
	return other
}

// createdBesides returns the one account with the new address that is not
// the neighbour.
func createdBesides(t *testing.T, z *fakeZitadel, other *fakeUser, address string) *fakeUser {
	t.Helper()
	z.mu.Lock()
	defer z.mu.Unlock()
	var found *fakeUser
	for _, u := range z.users {
		if u.id != other.id && u.email == address {
			if found != nil {
				t.Fatalf("two accounts were created for %s", address)
			}
			found = u
		}
	}
	if found == nil {
		t.Fatalf("no account was created for %s", address)
	}
	return found
}

// noSessionFor fails when any session at the issuer belongs to the neighbour.
func noSessionFor(t *testing.T, z *fakeZitadel, other *fakeUser) {
	t.Helper()
	z.mu.Lock()
	defer z.mu.Unlock()
	for id, s := range z.sessions {
		if s.userID == other.id {
			t.Errorf("session %s was created for the EXISTING account %s (%s)", id, other.id, other.email)
		}
	}
}

func TestSignUp_SignsInOnlyTheAccountItCreated(t *testing.T) {
	for _, n := range neighbours {
		t.Run(n.name, func(t *testing.T) {
			z := newFakeZitadel(t)
			other := seedNeighbour(t, z, n)
			z.projectionLag = 3
			tokens, err := z.client().SignUp(context.Background(), testFlow, Registration{Email: n.newAddress, Password: sharedPassword})
			if err != nil {
				t.Fatalf("SignUp: %v", err)
			}
			created := createdBesides(t, z, other, n.newAddress)
			if got := strings.TrimPrefix(tokens.AccessToken, "at-"); got != created.id {
				t.Errorf("tokens are for %s, want the created account %s (the existing one is %s)", got, created.id, other.id)
			}
			if got := tokenSubject(tokens); got != created.id {
				t.Errorf("token subject = %s, want the created account %s (the existing one is %s)", got, created.id, other.id)
			}
			noSessionFor(t, z, other)
			if got := z.callCount("POST /v2/sessions"); got != 4 {
				t.Errorf("session creations = %d, want 4: the projection lag was exercised", got)
			}
		})
	}
}

func TestRegisterWithoutMail_SignsInOnlyTheAccountItCreated(t *testing.T) {
	for _, n := range neighbours {
		t.Run(n.name, func(t *testing.T) {
			z := newFakeZitadel(t)
			other := seedNeighbour(t, z, n)
			z.projectionLag = 3
			h := newHarnessOn(t, z, LoginPolicy{Present: true, Version: 1, Signup: SignupOpen, SignupRole: "member",
				Methods: []string{MethodPassword}, Mail: MailNone})
			status, body := h.browser().post("/auth/register", map[string]any{"email": n.newAddress, "password": sharedPassword})
			noSessionFor(t, z, other)
			if n.routeRefuses {
				if status != http.StatusConflict || body["code"] != CodeAccountExists {
					t.Fatalf("register: %d %v, want 409 %s", status, body, CodeAccountExists)
				}
				return
			}
			if status != http.StatusOK {
				t.Fatalf("register: %d %v", status, body)
			}
			created := createdBesides(t, z, other, n.newAddress)
			if body["subject"] != created.id {
				t.Errorf("signed in as %v, want the created account %s (the existing one is %s)", body["subject"], created.id, other.id)
			}
		})
	}
}

package devidp

import (
	"context"
	"errors"
	"testing"
)

// The console invitation flow (control-plane identityapi Invite with a
// return code), mirrored: an administrator's client issues a first-password
// code for an EXISTING unverified, credential-less account, and the person
// redeems it through the login broker, whose client is ProvenCredentialsOnly.
// The console's caller does not adopt the account first today, so the
// redemption itself must work: the code proves the address, which it marks
// verified. Adopted first, it works the same way.
func TestAdoptPendingAccount_TheConsoleInviteFlowSetsTheFirstPassword(t *testing.T) {
	ctx := context.Background()
	for name, adopt := range map[string]bool{"without adoption": false, "adopted first": true} {
		t.Run(name, func(t *testing.T) {
			z := newFakeZitadel(t)
			admin := z.client() // the console's client: no ProvenCredentialsOnly
			broker := z.client()
			broker.ProvenCredentialsOnly = true
			u := z.addUser("invitee@acme.com", false, "")
			if adopt {
				if ok, err := admin.AdoptPendingAccount(ctx, u.id); err != nil || !ok {
					t.Fatalf("AdoptPendingAccount = %v, %v; want adopted", ok, err)
				}
			}
			code, err := admin.MintPasswordResetCode(ctx, u.id) // the return-code invitation
			if err != nil {
				t.Fatal(err)
			}
			if err := broker.ResetPassword(ctx, PasswordReset{UserID: u.id, Code: code, NewPassword: "invitee-password-1"}); err != nil {
				t.Fatalf("redemption: %v", err)
			}
			if verified, pw := z.passwordState(u.id); !verified || pw != "invitee-password-1" {
				t.Fatalf("the invitee: verified=%v password set=%v", verified, pw != "")
			}
		})
	}
}

func TestAdoptPendingAccount_RefusesASquatterAndLeavesTheVerifiedAlone(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, policyWith(SignupOpen, allMethods))
	c := h.z.client()
	s := squat(t, h)
	before := h.z.snapshot(s.id)
	if ok, err := c.AdoptPendingAccount(ctx, s.id); ok || !errors.Is(err, ErrAccountHoldsCredentials) {
		t.Fatalf("a squatted account: %v, %v; want ErrAccountHoldsCredentials", ok, err)
	}
	if after := h.z.snapshot(s.id); after != before {
		t.Fatalf("a refused adoption changed the account:\n before %s\n after  %s", before, after)
	}
	v := h.z.addUser("proven@acme.com", true, "proven-password-1")
	before = h.z.snapshot(v.id)
	if ok, err := c.AdoptPendingAccount(ctx, v.id); ok || err != nil || h.z.snapshot(v.id) != before {
		t.Fatalf("a verified account: %v, %v; want left alone", ok, err)
	}
	// Fail closed: an account that cannot be read is not verified.
	p := h.z.addUser("pending@acme.com", false, "")
	h.z.mu.Lock()
	h.z.failMethodReads = 1
	h.z.mu.Unlock()
	if ok, err := c.AdoptPendingAccount(ctx, p.id); ok || err == nil || h.z.user(p.id).verified {
		t.Fatalf("an unreadable account: %v, %v; want refused and unverified", ok, err)
	}
}

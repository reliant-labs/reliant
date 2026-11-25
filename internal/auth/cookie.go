// Copyright (c) 2025 Reliant Labs
package auth

import (
	"net/http"
	"strings"

	"github.com/reliant-labs/forge/pkg/authn"
)

// SessionCookieName is the cookie the login broker keeps a person's sign-in
// session in: an opaque server-side session id (forge/pkg/session), never a
// token. HttpOnly, SameSite=Lax and host-only, so a page's script cannot read
// it and no other site's request carries it.
//
// It is the plain name, which only local development reads (the dev stack
// serves plain HTTP, where a browser refuses a __Host- cookie).
const SessionCookieName = "reliant_session"

// SecureSessionCookieName is the session cookie's name wherever it is Secure
// (every deployed environment). The __Host- prefix makes the browser accept
// it only as Secure, host-only and Path=/, so a sibling host of the same site
// can neither plant nor overwrite it.
const SecureSessionCookieName = "__Host-" + SessionCookieName

// CredentialFromHeader is the credential a request presents and the channel
// it arrived on, forge's credentialFrom order: the Authorization header when
// the request has one (a bearer; one that is not is "", and never falls
// through to the cookie), else the sign-in session cookie — its __Host- name,
// and its plain name only when devCookie (local development). "" is no
// credential at all.
func CredentialFromHeader(h http.Header, devCookie bool) (string, authn.Channel) {
	// Select by presence, not validity: an empty or malformed Authorization
	// header must not fall through to another principal.
	if len(h.Values("Authorization")) != 0 {
		bearer, ok := strings.CutPrefix(h.Get("Authorization"), "Bearer ")
		if !ok {
			return "", authn.ChannelAuthorization
		}
		return strings.TrimSpace(bearer), authn.ChannelAuthorization
	}
	r := &http.Request{Header: h}
	if c, err := r.Cookie(SecureSessionCookieName); err == nil {
		return c.Value, authn.ChannelCookie
	}
	if devCookie {
		if c, err := r.Cookie(SessionCookieName); err == nil {
			return c.Value, authn.ChannelCookie
		}
	}
	return "", authn.ChannelUnknown
}

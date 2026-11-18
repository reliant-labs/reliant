// Copyright (c) 2026 Reliant Labs
package llm

import "net/http"

// A driver whose request path recovers an OAuth 401 by itself — reloading the
// stored tokens, then refreshing through the token coordinator and retrying —
// marks the provider's response when that recovery ran and the credential was
// still refused (reliant#2833). The mark rides on the response the SDK wraps in
// its error (anthropic.Error.Response), so the layer that judges the failure
// (the activity's providerFailure) reads a fact the driver established rather
// than guessing from the driver's name which drivers recover.
//
// It is a header on the in-process response object: nothing sends it anywhere.
const (
	// OAuthRecoveryHeader is the response header that carries the mark.
	OAuthRecoveryHeader = "X-Reliant-Oauth-Recovery"
	// OAuthRefreshedStillRejected is its value: the driver refreshed the
	// session and the provider still refused it.
	OAuthRefreshedStillRejected = "refreshed-still-rejected"
)

// MarkOAuthRefreshedStillRejected marks resp as a refusal that survived the
// driver's refresh. Nil-safe.
func MarkOAuthRefreshedStillRejected(resp *http.Response) {
	if resp == nil {
		return
	}
	if resp.Header == nil {
		resp.Header = http.Header{}
	}
	resp.Header.Set(OAuthRecoveryHeader, OAuthRefreshedStillRejected)
}

// OAuthRefreshedStillRejectedResponse reports whether resp carries the mark.
func OAuthRefreshedStillRejectedResponse(resp *http.Response) bool {
	return resp != nil && resp.Header.Get(OAuthRecoveryHeader) == OAuthRefreshedStillRejected
}

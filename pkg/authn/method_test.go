package authn

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/auth"
)

// legacyCredentialFrom is the scaffolded pkg/middleware credentialFrom as
// projects scaffolded before Policy.ExtractCredential carry it: a token-only
// Policy.ExtractToken reading, by presence, the Authorization header, then
// X-API-Key, then the __Host- session cookie. AuthMethod reports its headers
// by inference and its cookie as ChannelUnknown, so a browser-session check
// fails closed for it.
func legacyCredentialFrom(h http.Header) string {
	if len(h.Values("Authorization")) != 0 {
		bearer, ok := strings.CutPrefix(h.Get("Authorization"), "Bearer ")
		if !ok {
			return ""
		}
		return strings.TrimSpace(bearer)
	}
	if len(h.Values("X-API-Key")) != 0 {
		return strings.TrimSpace(h.Get("X-API-Key"))
	}
	cookie, err := (&http.Request{Header: h}).Cookie("__Host-app_session")
	if err != nil {
		return ""
	}
	return cookie.Value
}

// scaffoldCredentialFrom is the scaffolded credentialFrom of today: the same
// reads as legacyCredentialFrom, naming the channel it read for
// Policy.ExtractCredential.
func scaffoldCredentialFrom(h http.Header) (string, Channel) {
	if len(h.Values("Authorization")) != 0 {
		return legacyCredentialFrom(h), ChannelAuthorization
	}
	if len(h.Values("X-API-Key")) != 0 {
		return legacyCredentialFrom(h), ChannelAPIKeyHeader
	}
	if v := legacyCredentialFrom(h); v != "" {
		return v, ChannelCookie
	}
	return "", ChannelUnknown
}

// credentialPolicy is a Validate-mode policy with the library's own claims
// stash whose validator proves every credential as cred.
func credentialPolicy(cred auth.Credential) Policy {
	return Policy{
		ValidatorConfigured: true,
		Validate: func(string) (*auth.Claims, error) {
			return &auth.Claims{UserID: "u1", Credential: cred}, nil
		},
		Unauthenticated: healthAllowList(),
	}
}

// channelCase is one request and the Method a policy must record for it.
type channelCase struct {
	name    string
	policy  func(Policy) Policy
	headers map[string]string
	want    Method
}

func channelCases() []channelCase {
	token := Method{Credential: auth.CredentialToken}
	with := func(ch Channel) Method { m := token; m.Channel = ch; return m }
	legacy := func(p Policy) Policy { p.ExtractToken = legacyCredentialFrom; return p }
	scaffold := func(p Policy) Policy { p.ExtractCredential = scaffoldCredentialFrom; return p }
	explicit := func(ch Channel) func(Policy) Policy {
		return func(p Policy) Policy {
			p.ExtractCredential = func(http.Header) (string, Channel) { return "tok", ch }
			return p
		}
	}
	return []channelCase{
		{"default reads the Authorization header", nil, map[string]string{"Authorization": "Bearer tok"}, with(ChannelAuthorization)},
		{"legacy extractor, bearer", legacy, map[string]string{"Authorization": "Bearer tok"}, with(ChannelAuthorization)},
		{"legacy extractor, api key header", legacy, map[string]string{"X-API-Key": "tok"}, with(ChannelAPIKeyHeader)},
		{"legacy extractor, session cookie: never inferred", legacy, map[string]string{"Cookie": "__Host-app_session=tok"}, with(ChannelUnknown)},
		{"scaffolded extractor, session cookie", scaffold, map[string]string{"Cookie": "__Host-app_session=tok"}, with(ChannelCookie)},
		{"scaffolded extractor, bearer", scaffold, map[string]string{"Authorization": "Bearer tok", "Cookie": "__Host-app_session=tok"}, with(ChannelAuthorization)},
		{"legacy extractor, bearer beats an equal cookie", legacy, map[string]string{"Authorization": "Bearer tok", "Cookie": "__Host-app_session=tok"}, with(ChannelAuthorization)},
		{"extractor names the cookie", explicit(ChannelCookie), nil, with(ChannelCookie)},
		{"extractor names the api key header", explicit(ChannelAPIKeyHeader), nil, with(ChannelAPIKeyHeader)},
		{"extractor names the Authorization header", explicit(ChannelAuthorization), nil, with(ChannelAuthorization)},
		{"extractor says it does not know", explicit(ChannelUnknown), nil, with(ChannelUnknown)},
	}
}

// methodOf answers a call with the Method the handler saw, or "none".
func methodOf(ctx context.Context) (string, error) {
	m, ok := AuthMethod(ctx)
	if !ok {
		return "none", nil
	}
	return m.Channel.String() + "/" + m.Credential.String(), nil
}

func methodString(m Method) string { return m.Channel.String() + "/" + m.Credential.String() }

func TestAuthMethod_V1Unary(t *testing.T) {
	t.Parallel()
	for _, tc := range channelCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := credentialPolicy(auth.TokenCredential())
			if tc.policy != nil {
				p = tc.policy(p)
			}
			ic, err := NewInterceptor(p)
			if err != nil {
				t.Fatal(err)
			}
			got, err := callV1(t, methodOf, ic, tc.headers)
			if err != nil {
				t.Fatal(err)
			}
			if want := methodString(tc.want); got != want {
				t.Fatalf("AuthMethod = %s, want %s", got, want)
			}
		})
	}
}

// fakeConn is a minimal connect.StreamingHandlerConn: the interceptor reads
// only Spec().Procedure and RequestHeader().
type fakeConn struct {
	connect.StreamingHandlerConn
	header http.Header
}

func (c *fakeConn) Spec() connect.Spec         { return connect.Spec{Procedure: "/demo.v1.Service/Watch"} }
func (c *fakeConn) RequestHeader() http.Header { return c.header }

func TestAuthMethod_V1Streaming(t *testing.T) {
	t.Parallel()
	for _, tc := range channelCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := credentialPolicy(auth.TokenCredential())
			if tc.policy != nil {
				p = tc.policy(p)
			}
			ic, err := NewInterceptor(p)
			if err != nil {
				t.Fatal(err)
			}
			conn := &fakeConn{header: http.Header{}}
			for k, v := range tc.headers {
				conn.header.Set(k, v)
			}
			var got Method
			var ok bool
			err = ic.WrapStreamingHandler(func(ctx context.Context, _ connect.StreamingHandlerConn) error {
				got, ok = AuthMethod(ctx)
				return nil
			})(context.Background(), conn)
			if err != nil {
				t.Fatal(err)
			}
			if !ok || got != tc.want {
				t.Fatalf("AuthMethod = %+v (ok=%v), want %+v", got, ok, tc.want)
			}
		})
	}
}

func TestAuthMethod_V2(t *testing.T) {
	t.Parallel()
	for _, tc := range channelCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := credentialPolicy(auth.TokenCredential())
			if tc.policy != nil {
				p = tc.policy(p)
			}
			ic, err := NewInterceptorV2(p)
			if err != nil {
				t.Fatal(err)
			}
			c := serveV2(t, methodOf, ic)
			for _, stream := range []bool{false, true} {
				got, err := callV2(t, c, stream, tc.headers)
				if err != nil {
					t.Fatalf("stream=%v: %v", stream, err)
				}
				if want := methodString(tc.want); got != want {
					t.Fatalf("stream=%v: AuthMethod = %s, want %s", stream, got, want)
				}
			}
		})
	}
}

// Where a legacy ExtractToken found its token is inferred in
// credentialFrom's presence order for the two headers, and never as the
// cookie: a token-only extractor cannot prove it read one.
func TestInferChannel(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		headers map[string][]string
		token   string
		want    Channel
	}{
		{"bearer", map[string][]string{"Authorization": {"Bearer tok"}}, "tok", ChannelAuthorization},
		{"bearer with padding", map[string][]string{"Authorization": {"Bearer  tok "}}, "tok", ChannelAuthorization},
		{"raw authorization value", map[string][]string{"Authorization": {"tok"}}, "tok", ChannelAuthorization},
		{"api key header", map[string][]string{"X-API-Key": {" tok "}}, "tok", ChannelAPIKeyHeader},
		{"one cookie of several", map[string][]string{"Cookie": {"theme=dark; __Host-app_session=tok; lang=en"}}, "tok", ChannelUnknown},
		{"cookie under any name", map[string][]string{"Cookie": {"sid=tok"}}, "tok", ChannelUnknown},
		{"bearer and cookie carry it: the header wins", map[string][]string{"Authorization": {"Bearer tok"}, "Cookie": {"sid=tok"}}, "tok", ChannelAuthorization},
		{"api key and cookie carry it: the header wins", map[string][]string{"X-API-Key": {"tok"}, "Cookie": {"sid=tok"}}, "tok", ChannelAPIKeyHeader},
		{"bearer and api key carry it: Authorization wins", map[string][]string{"Authorization": {"Bearer tok"}, "X-API-Key": {"tok"}}, "tok", ChannelAuthorization},
		{"a different bearer, the cookie carries it", map[string][]string{"Authorization": {"Bearer other"}, "Cookie": {"sid=tok"}}, "tok", ChannelUnknown},
		{"a cookie name is not its value", map[string][]string{"Cookie": {"tok=other"}}, "tok", ChannelUnknown},
		{"a header inference does not read", map[string][]string{"X-Session": {"tok"}}, "tok", ChannelUnknown},
		// An extractor reading X-Session accepts the header's token; a copy
		// in a cookie does not make it the cookie channel.
		{"a header inference does not read, copied into a cookie", map[string][]string{"X-Session": {"tok"}, "Cookie": {"sid=tok"}}, "tok", ChannelUnknown},
		{"any other header carrying it, even inside a value", map[string][]string{"X-Forwarded-Auth": {"scheme tok"}, "Cookie": {"sid=tok"}}, "tok", ChannelUnknown},
		{"a substring is not a match", map[string][]string{"Authorization": {"Bearer tok2"}, "Cookie": {"sid=xtok"}}, "tok", ChannelUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := http.Header{}
			for k, vs := range tc.headers {
				for _, v := range vs {
					h.Add(k, v)
				}
			}
			if got := inferChannel(h, tc.token); got != tc.want {
				t.Fatalf("inferChannel = %s, want %s", got, tc.want)
			}
		})
	}
}

// The credential kind in the Method is the validator's finding, restored
// after Enrich: an enricher cannot turn a token into a key, nor a key into
// a token, and it has no say over the channel.
func TestAuthMethod_EnrichCannotChangeIt(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		validated, forged auth.Credential
	}{
		"token stays a token": {auth.TokenCredential(), auth.APIKeyCredential("forged12")},
		"key stays a key":     {auth.APIKeyCredential("abcd1234"), auth.TokenCredential()},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := credentialPolicy(tc.validated)
			p.ExtractCredential = scaffoldCredentialFrom
			p.Enrich = func(_ context.Context, c *auth.Claims) (*auth.Claims, error) {
				out := *c
				out.Credential = tc.forged
				return &out, nil
			}
			var decorated Method
			p.Decorate = func(ctx context.Context, _ *auth.Claims, _ string) context.Context {
				decorated, _ = AuthMethod(ctx)
				return ctx
			}
			ic, err := NewInterceptor(p)
			if err != nil {
				t.Fatal(err)
			}
			got, err := callV1(t, methodOf, ic, map[string]string{"Cookie": "__Host-app_session=tok"})
			if err != nil {
				t.Fatal(err)
			}
			want := Method{Channel: ChannelCookie, Credential: tc.validated.Kind()}
			if got != methodString(want) {
				t.Fatalf("AuthMethod = %s, want %s", got, methodString(want))
			}
			if decorated != want {
				t.Fatalf("Decorate read %+v, want %+v", decorated, want)
			}
		})
	}
}

// No Method without an admitted caller: an anonymous request, an
// allow-listed procedure and a passthrough interceptor all leave
// AuthMethod empty, so IsBrowserSession is false for them.
func TestAuthMethod_AbsentWithoutAnAdmittedCaller(t *testing.T) {
	t.Parallel()

	t.Run("anonymous", func(t *testing.T) {
		t.Parallel()
		p := credentialPolicy(auth.TokenCredential())
		p.AnonymousOK = true
		p.ExtractToken = legacyCredentialFrom
		ic, err := NewInterceptor(p)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := callV1(t, methodOf, ic, nil); err != nil || got != "none" {
			t.Fatalf("AuthMethod = %s (err %v), want none", got, err)
		}
	})

	t.Run("allow-listed", func(t *testing.T) {
		t.Parallel()
		p := credentialPolicy(auth.TokenCredential())
		p.Unauthenticated = map[string]struct{}{echoProcedure: {}}
		ic, err := NewInterceptor(p)
		if err != nil {
			t.Fatal(err)
		}
		got, err := callV1(t, methodOf, ic, map[string]string{"Authorization": "Bearer tok"})
		if err != nil || got != "none" {
			t.Fatalf("AuthMethod = %s (err %v), want none", got, err)
		}
	})

	t.Run("passthrough", func(t *testing.T) {
		t.Parallel()
		ic, err := NewInterceptor(Policy{ExternalAuth: true})
		if err != nil {
			t.Fatal(err)
		}
		got, err := callV1(t, methodOf, ic, map[string]string{"Cookie": "__Host-app_session=tok"})
		if err != nil || got != "none" {
			t.Fatalf("AuthMethod = %s (err %v), want none", got, err)
		}
		ic2, err := NewInterceptorV2(Policy{ExternalAuth: true})
		if err != nil {
			t.Fatal(err)
		}
		got, err = callV2(t, serveV2(t, methodOf, ic2), false, map[string]string{"Cookie": "__Host-app_session=tok"})
		if err != nil || got != "none" {
			t.Fatalf("v2: AuthMethod = %s (err %v), want none", got, err)
		}
	})

	if (Method{}).IsBrowserSession() || (Method{}).ViaCookie() {
		t.Fatal("the zero Method must not pass a cookie check")
	}
}

// A project-owned claims stash does not hide the Method: it lives under the
// library's own key either way.
func TestAuthMethod_RecordedWithAProjectClaimsStash(t *testing.T) {
	t.Parallel()
	p := validatePolicy(func(string) (*auth.Claims, error) {
		return &auth.Claims{UserID: "u1", Credential: auth.TokenCredential()}, nil
	})
	ic, err := NewInterceptor(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := callV1(t, methodOf, ic, map[string]string{"Authorization": "Bearer tok"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "authorization/token"; got != want {
		t.Fatalf("AuthMethod = %s, want %s", got, want)
	}
}

// An ExtractCredential that finds nothing is a missing credential, exactly
// as an ExtractToken returning "" is.
func TestExtractCredential_EmptyIsMissingCredential(t *testing.T) {
	t.Parallel()
	p := credentialPolicy(auth.TokenCredential())
	p.ExtractCredential = func(http.Header) (string, Channel) { return "", ChannelCookie }
	ic, err := NewInterceptor(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := callV1(t, methodOf, ic, nil); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("want CodeUnauthenticated, got %v", err)
	}
}

// Two readers of one credential is a configuration error, refused at
// construction by both majors.
func TestNewInterceptor_ExtractTokenAndExtractCredentialAreExclusive(t *testing.T) {
	t.Parallel()
	p := credentialPolicy(auth.TokenCredential())
	p.ExtractToken = legacyCredentialFrom
	p.ExtractCredential = func(http.Header) (string, Channel) { return "", ChannelUnknown }
	if _, err := NewInterceptor(p); err == nil || !strings.Contains(err.Error(), "ExtractCredential") {
		t.Fatalf("NewInterceptor: want a refusal naming ExtractCredential, got %v", err)
	}
	if _, err := NewInterceptorV2(p); err == nil || !strings.Contains(err.Error(), "ExtractCredential") {
		t.Fatalf("NewInterceptorV2: want a refusal naming ExtractCredential, got %v", err)
	}
}

func TestMethod_Predicates(t *testing.T) {
	t.Parallel()
	cases := []struct {
		m                  Method
		viaCookie, browser bool
	}{
		{Method{}, false, false},
		// Only a server-side session id in a cookie is a browser session: a
		// bearer token replayed as a cookie, an unknown kind and an API key
		// are not.
		{Method{Channel: ChannelCookie, Credential: auth.CredentialSession}, true, true},
		{Method{Channel: ChannelCookie, Credential: auth.CredentialToken}, true, false},
		{Method{Channel: ChannelCookie, Credential: auth.CredentialUnknown}, true, false},
		{Method{Channel: ChannelCookie, Credential: auth.CredentialAPIKey}, true, false},
		{Method{Channel: ChannelAuthorization, Credential: auth.CredentialSession}, false, false},
		{Method{Channel: ChannelAuthorization, Credential: auth.CredentialToken}, false, false},
		{Method{Channel: ChannelAPIKeyHeader, Credential: auth.CredentialAPIKey}, false, false},
		{Method{Channel: ChannelUnknown, Credential: auth.CredentialToken}, false, false},
	}
	for _, tc := range cases {
		if got := tc.m.ViaCookie(); got != tc.viaCookie {
			t.Errorf("%s: ViaCookie = %v, want %v", methodString(tc.m), got, tc.viaCookie)
		}
		if got := tc.m.IsBrowserSession(); got != tc.browser {
			t.Errorf("%s: IsBrowserSession = %v, want %v", methodString(tc.m), got, tc.browser)
		}
	}
	for ch, want := range map[Channel]string{
		ChannelUnknown: "unknown", ChannelAuthorization: "authorization",
		ChannelAPIKeyHeader: "api_key_header", ChannelCookie: "cookie", Channel(99): "unknown",
	} {
		if got := ch.String(); got != want {
			t.Errorf("Channel(%d).String() = %q, want %q", ch, got, want)
		}
	}
	ctx := ContextWithAuthMethod(context.Background(), Method{Channel: ChannelCookie})
	if m, ok := AuthMethod(ctx); !ok || !m.ViaCookie() {
		t.Fatalf("ContextWithAuthMethod round trip: %+v, %v", m, ok)
	}
}

// The browser-session guard end to end: a policy whose validator proves the
// credential as ok, the request's headers, and whether a handler gating on
// IsBrowserSession admits it.
func TestAuthMethod_BrowserSessionGuard(t *testing.T) {
	t.Parallel()
	token := func(f func(http.Header) string) func(*Policy) { return func(p *Policy) { p.ExtractToken = f } }
	scaffold := func(p *Policy) { p.ExtractCredential = scaffoldCredentialFrom }
	xSession := token(func(h http.Header) string { return h.Get("X-Session") })
	// A supported custom extractor that decodes its header: the token it
	// returns is in no header verbatim.
	xSessionB64 := token(func(h http.Header) string {
		v, err := base64.StdEncoding.DecodeString(h.Get("X-Session"))
		if err != nil {
			return ""
		}
		return string(v)
	})
	for _, tc := range []struct {
		name    string
		cred    auth.Credential
		policy  func(*Policy)
		headers map[string]string
		admit   bool
	}{
		{"a session id in the session cookie", auth.SessionCredential(), scaffold, map[string]string{"Cookie": "__Host-app_session=tok"}, true},
		{"a bearer JWT replayed as the session cookie", auth.TokenCredential(), scaffold, map[string]string{"Cookie": "__Host-app_session=tok"}, false},
		{"an unknown credential kind in the cookie", auth.Credential{}, scaffold, map[string]string{"Cookie": "__Host-app_session=tok"}, false},
		{"a session id as a bearer", auth.SessionCredential(), scaffold, map[string]string{"Authorization": "Bearer tok", "Cookie": "__Host-app_session=tok"}, false},
		{"a token-only extractor's session cookie: fails closed", auth.SessionCredential(), token(legacyCredentialFrom), map[string]string{"Cookie": "__Host-app_session=tok"}, false},
		{"a custom header extractor, the token copied into a cookie", auth.SessionCredential(), xSession, map[string]string{"X-Session": "tok", "Cookie": "sid=tok"}, false},
		{"a decoding header extractor, the decoded id copied into a cookie", auth.SessionCredential(), xSessionB64,
			map[string]string{"X-Session": base64.StdEncoding.EncodeToString([]byte("tok")), "Cookie": "sid=tok"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := credentialPolicy(tc.cred)
			tc.policy(&p)
			ic, err := NewInterceptor(p)
			if err != nil {
				t.Fatal(err)
			}
			got, err := callV1(t, func(ctx context.Context) (string, error) {
				m, _ := AuthMethod(ctx)
				if m.IsBrowserSession() {
					return "admitted", nil
				}
				return "refused", nil
			}, ic, tc.headers)
			if err != nil {
				t.Fatal(err)
			}
			if want := map[bool]string{true: "admitted", false: "refused"}[tc.admit]; got != want {
				t.Fatalf("browser-session guard = %s, want %s", got, want)
			}
		})
	}
}

package hostedhttp

import (
	"context"
	"strings"
	"testing"
)

type rawSecret []byte

func (r rawSecret) Use(_ context.Context, f func([]byte) error) error { return f(r) }

func TestRequestSizeLimitBeforeSecretRead(t *testing.T) {
	s := &testSecret{}
	req := Request{URL: "https://example.com/rpc", Method: "POST", Body: strings.Repeat("x", MaxRequestBytes)}
	if _, err := RequestDigest(req); err == nil {
		t.Fatal("oversized request digest accepted")
	}
	if _, err := New().Execute(context.Background(), testPolicy(), req, s); err == nil || s.calls != 0 {
		t.Fatal("oversized request reached credential")
	}
}
func TestInvalidCredentialCannotReachHeader(t *testing.T) {
	for _, value := range []string{"short", "long-enough-but\n-header-injection", "long-enough-but\x00-invalid"} {
		out, err := New().Execute(context.Background(), testPolicy(), Request{URL: "https://example.com/rpc", Method: "POST"}, rawSecret(value))
		if err == nil || out != nil || strings.Contains(err.Error(), value) {
			t.Fatal("invalid credential accepted or disclosed")
		}
	}
}
func TestPrivateLiteralGrantDenied(t *testing.T) {
	p := Policy{Origin: "https://169.254.169.254", Endpoints: []Endpoint{{Method: "POST", Path: "/rpc"}}}
	s := &testSecret{}
	if _, err := New().Execute(context.Background(), p, Request{URL: p.Origin + "/rpc", Method: "POST"}, s); err == nil || s.calls != 0 {
		t.Fatal("private destination accepted")
	}
}

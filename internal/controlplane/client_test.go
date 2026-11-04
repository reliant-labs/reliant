package controlplane

import (
	"context"
	"net/http/httptest"
	"testing"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"

	"connectrpc.com/connect"
	accesstokenv1 "github.com/reliant-labs/reliant/gen/controlplane/services/access_token/v1"
	accesstokenv1connect "github.com/reliant-labs/reliant/gen/controlplane/services/access_token/v1/controlplanev1connect"
)

func TestClient_MintLLMKey_RequestsAPerDeviceRotatingLLMToken(t *testing.T) {
	var (
		gotAuth string
		gotReq  *accesstokenv1.CreateMyTokenRequest
	)
	handler := connect.NewUnaryHandler(
		accesstokenv1connect.AccessTokenServiceCreateMyTokenProcedure,
		func(_ context.Context, req *connect.Request[accesstokenv1.CreateMyTokenRequest]) (*connect.Response[accesstokenv1.CreateMyTokenResponse], error) {
			gotAuth = req.Header().Get("Authorization")
			gotReq = req.Msg
			return connect.NewResponse(&accesstokenv1.CreateMyTokenResponse{Secret: "rlat_minted", Rotated: true}), nil
		},
	)
	server := httptest.NewServer(handler)
	defer server.Close()

	key, err := New(Deps{ControlPlane: &configv1.ControlPlaneConfig{Url: server.URL}}).MintLLMKey(context.Background(), " jwt-token ", "laptop")
	if err != nil {
		t.Fatalf("MintLLMKey: %v", err)
	}
	if gotAuth != "Bearer jwt-token" {
		t.Fatalf("authorization = %q, want %q", gotAuth, "Bearer jwt-token")
	}
	if gotReq.GetName() != "laptop" || !gotReq.GetRotate() ||
		len(gotReq.GetScopes()) != 1 || gotReq.GetScopes()[0] != "llm:invoke" {
		t.Fatalf("request %+v, want name=laptop rotate=true scopes=[llm:invoke]", gotReq)
	}
	if key.Plaintext != "rlat_minted" || !key.Rotated {
		t.Fatalf("key %+v, want the minted secret and rotated=true", key)
	}
}

func TestAttachAuthorization_LeavesHeaderUnsetWhenBlank(t *testing.T) {
	req := connect.NewRequest(&accesstokenv1.CreateMyTokenRequest{})
	attachAuthorization(req, "  ")
	if got := req.Header().Get("Authorization"); got != "" {
		t.Fatalf("authorization header = %q, want empty", got)
	}
}

// An adapter given no base URL dials the local default. The deployment's own
// control plane is configured (ControlPlaneConfig) and passed in; it is never
// read from the environment here.
// The control plane comes from ServerProcessConfig (ControlPlaneConfig);
// cmd/reliant/cmd TestControlPlaneEnvContract pins the variables.
func TestEndpointFrom(t *testing.T) {
	got := EndpointFrom(&configv1.ControlPlaneConfig{Url: " http://admin-server:8090 ", InternalServiceSecret: " s "})
	if want := (Endpoint{URL: "http://admin-server:8090", InternalServiceSecret: "s"}); got != want {
		t.Fatalf("EndpointFrom = %+v, want %+v", got, want)
	}
	if got := EndpointFrom(nil); got != (Endpoint{}) || got.Hosted() {
		t.Fatalf("EndpointFrom(nil) = %+v, want the zero (self-hosted) Endpoint", got)
	}
}

func TestNew_NoBaseURLIsTheLocalDefault(t *testing.T) {
	t.Setenv("RELIANT_CONTROL_PLANE_URL", "http://not-read.example")
	if got := New(Deps{}).(*client).baseURL; got != defaultBaseURL {
		t.Fatalf("base url = %q, want %q", got, defaultBaseURL)
	}
}

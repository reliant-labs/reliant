package controlplane

import (
	"context"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/v1"
	"github.com/reliant-labs/reliant/gen/controlplane/v1/controlplanev1connect"
)

func TestClient_IssueMyReliantAPIKey_AttachesJWT(t *testing.T) {
	var gotAuth string

	handler := connect.NewUnaryHandler(
		controlplanev1connect.BillingServiceIssueMyReliantAPIKeyProcedure,
		func(ctx context.Context, req *connect.Request[controlplanev1.IssueMyReliantAPIKeyRequest]) (*connect.Response[controlplanev1.IssueMyReliantAPIKeyResponse], error) {
			gotAuth = req.Header().Get("Authorization")
			return connect.NewResponse(&controlplanev1.IssueMyReliantAPIKeyResponse{PlaintextKey: "rlnt_minted_key"}), nil
		},
	)
	server := httptest.NewServer(handler)
	defer server.Close()

	client := NewClient(server.URL)
	key, err := client.IssueMyReliantAPIKey(context.Background(), " jwt-token ")
	if err != nil {
		t.Fatalf("IssueMyReliantAPIKey: %v", err)
	}
	if gotAuth != "Bearer jwt-token" {
		t.Fatalf("authorization = %q, want %q", gotAuth, "Bearer jwt-token")
	}
	if key != "rlnt_minted_key" {
		t.Fatalf("plaintext key = %q, want rlnt_minted_key", key)
	}
}

func TestAttachAuthorization_LeavesHeaderUnsetWhenBlank(t *testing.T) {
	req := connect.NewRequest(&controlplanev1.IssueMyReliantAPIKeyRequest{})
	attachAuthorization(req, "  ")
	if got := req.Header().Get("Authorization"); got != "" {
		t.Fatalf("authorization header = %q, want empty", got)
	}
}

func TestGetBaseURL_UsesDefaultWhenEnvEmpty(t *testing.T) {
	t.Setenv("RELIANT_CONTROL_PLANE_URL", "")
	t.Setenv("CONTROL_PLANE_API_URL", "")
	t.Setenv("CONTROL_PLANE_BASE_URL", "")
	if got := getBaseURL(); got != defaultBaseURL {
		t.Fatalf("base url = %q, want %q", got, defaultBaseURL)
	}
}

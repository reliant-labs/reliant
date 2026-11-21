package controlplane

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
	deployv1 "github.com/reliant-labs/reliant/gen/controlplane/services/deploy/v1"
	deployv1connect "github.com/reliant-labs/reliant/gen/controlplane/services/deploy/v1/controlplanev1connect"
	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
)

// listAppsServer answers DeployService.ListApps with resp or err, recording
// the bearer and the request.
func listAppsServer(t *testing.T, resp *deployv1.ListDeployAppsResponse, err error) (Service, *string, **deployv1.ListDeployAppsRequest) {
	t.Helper()
	var gotAuth string
	var gotReq *deployv1.ListDeployAppsRequest
	handler := connect.NewUnaryHandler(deployv1connect.DeployServiceListAppsProcedure,
		func(_ context.Context, req *connect.Request[deployv1.ListDeployAppsRequest]) (*connect.Response[deployv1.ListDeployAppsResponse], error) {
			gotAuth, gotReq = req.Header().Get("Authorization"), req.Msg
			if err != nil {
				return nil, err
			}
			return connect.NewResponse(resp), nil
		})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return New(Deps{ControlPlane: &configv1.ControlPlaneConfig{Url: server.URL}}), &gotAuth, &gotReq
}

func TestListHostedApps_ReadsTheOrgsAppsAsTheCaller(t *testing.T) {
	created := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	deployed := created.Add(48 * time.Hour)
	svc, gotAuth, gotReq := listAppsServer(t, &deployv1.ListDeployAppsResponse{Apps: []*deployv1.DeployApp{
		{
			Name: "shop", CreatedAt: timestamppb.New(created), LastDeployedAt: timestamppb.New(deployed),
			Environments: []string{"prod", "staging"}, HasStaticSite: true,
			CodeSources: &controlplanev1.DeployAppCodeSources{Project: "shop", Warning: "two repositories", Sources: []*controlplanev1.DeployCodeSourceSighting{{
				Source:     &controlplanev1.DeployCodeSource{Remote: "github.com/acme/shop", Subpath: "web", Commit: "abc", ReliantProjectId: "hint"},
				LastSeenAt: timestamppb.New(deployed),
			}}},
		},
		{Name: "draft", CreatedAt: timestamppb.New(created)},
	}}, nil)

	apps, err := svc.ListHostedApps(context.Background(), " user-jwt ", "acme")
	if err != nil {
		t.Fatal(err)
	}
	if *gotAuth != "Bearer user-jwt" || (*gotReq).GetOrgId() != "acme" {
		t.Fatalf("authorization %q org %q, want the caller's bearer in acme", *gotAuth, (*gotReq).GetOrgId())
	}
	if len(apps) != 2 {
		t.Fatalf("apps %+v", apps)
	}
	shop := apps[0]
	if shop.Name != "shop" || !shop.CreatedAt.Equal(created) || shop.LastDeployedAt == nil || !shop.LastDeployedAt.Equal(deployed) ||
		len(shop.Environments) != 2 || !shop.HasStaticSite || shop.Warning != "two repositories" {
		t.Fatalf("shop %+v", shop)
	}
	if len(shop.Sources) != 1 || shop.Sources[0] != (HostedAppSource{Remote: "github.com/acme/shop", Subpath: "web", Commit: "abc", LastSeenAt: deployed}) {
		t.Fatalf("shop's sources %+v", shop.Sources)
	}
	if apps[1].LastDeployedAt != nil || len(apps[1].Sources) != 0 {
		t.Fatalf("never-deployed app %+v", apps[1])
	}
}

func TestListHostedApps_Errors(t *testing.T) {
	for _, tc := range []struct {
		code   connect.Code
		denied bool
	}{
		{connect.CodePermissionDenied, true},
		{connect.CodeUnauthenticated, true},
		{connect.CodeUnavailable, false},
		{connect.CodeInternal, false},
	} {
		t.Run(tc.code.String(), func(t *testing.T) {
			svc, _, _ := listAppsServer(t, nil, connect.NewError(tc.code, errors.New("no")))
			_, err := svc.ListHostedApps(context.Background(), "jwt", "")
			if err == nil || errors.Is(err, ErrHostedAppsDenied) != tc.denied {
				t.Fatalf("err %v: denied %v, want %v", err, errors.Is(err, ErrHostedAppsDenied), tc.denied)
			}
		})
	}
}

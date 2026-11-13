package cloud

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResolveOrganization_ReadsTheOrgOfTheCredential(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"tenant":{"orgId":"4f3c2b1a-0000-4000-8000-000000000001","slug":"x"}}`))
	}))
	defer srv.Close()

	ep, _ := ResolveEndpoint("prod", &Declaration{Endpoint: srv.URL})
	org, err := ResolveOrganization(context.Background(), NewClient(ep, Credential{Token: "rlat_t"}))
	if err != nil {
		t.Fatal(err)
	}
	if org != "4f3c2b1a-0000-4000-8000-000000000001" {
		t.Errorf("org = %q", org)
	}
	if gotPath != "/controlplane.v1.DeployService/GetTenant" {
		t.Errorf("called %q, want DeployService/GetTenant", gotPath)
	}
	if gotAuth != "Bearer rlat_t" {
		t.Errorf("the credential was not presented: %q", gotAuth)
	}
}

func TestResolveOrganization_RefusesAnAnswerWithNoOrg(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tenant":{}}`))
	}))
	defer srv.Close()

	ep, _ := ResolveEndpoint("prod", &Declaration{Endpoint: srv.URL})
	_, err := ResolveOrganization(context.Background(), NewClient(ep, Credential{Token: "t"}))
	if err == nil || !strings.Contains(err.Error(), "no organization id") {
		t.Fatalf("an empty org was accepted: %v", err)
	}
}

func TestOrganizationReply_IsWhatResolveOrganizationReads(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(OrganizationReply("4f3c2b1a-0000-4000-8000-000000000002"))
	}))
	defer srv.Close()
	ep, _ := ResolveEndpoint("prod", &Declaration{Endpoint: srv.URL})
	org, err := ResolveOrganization(context.Background(), NewClient(ep, Credential{Token: "t"}))
	if err != nil || org != "4f3c2b1a-0000-4000-8000-000000000002" {
		t.Errorf("org = %q, err = %v", org, err)
	}
}

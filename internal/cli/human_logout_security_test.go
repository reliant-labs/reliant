package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/credentials"
)

func TestHumanLogoutOutageRetainsRevocationHandle(t *testing.T) {
	path := isolatedCredentials(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer srv.Close()
	if err := credentials.Store(path, srv.URL, cloud.HumanClientID, credentials.Credential{Kind: "human", Token: "session-A"}); err != nil {
		t.Fatal(err)
	}
	cmd := newLogoutCmd()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"--endpoint", srv.URL})
	if err := cmd.Execute(); err == nil {
		t.Fatal("outage reported logout success")
	}
	if got, err := credentials.Lookup(path, srv.URL, cloud.HumanClientID); err != nil || got.Token != "session-A" {
		t.Fatal("outage erased revocation handle")
	}
}

func TestHumanLogoutRetainsConcurrentNewLogin(t *testing.T) {
	path := isolatedCredentials(t)
	revoked := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/revoke" {
			t.Errorf("unexpected endpoint %s", r.URL.Path)
		}
		revoked = r.Header.Get("Authorization")
		// Deterministically interleave the second login during A's network revoke.
		if err := credentials.Store(path, "http://"+r.Host, cloud.HumanClientID, credentials.Credential{Kind: "human", Token: "session-B"}); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if err := credentials.Store(path, srv.URL, cloud.HumanClientID, credentials.Credential{Kind: "human", Token: "session-A"}); err != nil {
		t.Fatal(err)
	}
	if err := credentials.Store(path, srv.URL, cloud.ClientID, credentials.Credential{Token: "delegated"}); err != nil {
		t.Fatal(err)
	}
	cmd := newLogoutCmd()
	cmd.SetArgs([]string{"--endpoint", srv.URL})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "newer login retained") {
		t.Fatalf("concurrent login must be reported, got %v", err)
	}
	if revoked != "Bearer session-A" {
		t.Fatalf("revoked %q, want A", revoked)
	}
	got, err := credentials.Lookup(path, srv.URL, cloud.HumanClientID)
	if err != nil || got.Token != "session-B" {
		t.Fatalf("new login lost: %+v %v", got, err)
	}
	other, err := credentials.Lookup(path, srv.URL, cloud.ClientID)
	if err != nil || other.Token != "delegated" {
		t.Fatal("unrelated namespace changed")
	}
}

func TestHumanLogoutIgnoresCorruptTargetBinding(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "implicit", true: "explicit"}[explicit], func(t *testing.T) {
			path := isolatedCredentials(t)
			root := t.TempDir()
			t.Chdir(root)
			if err := os.WriteFile(filepath.Join(root, "forge.yaml"), []byte("name: demo\nmodule_path: example.com/demo\nforge_version: v0.1.47\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(root, ".forge"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, hostedProjectBindingFile), []byte("{broken"), 0600); err != nil {
				t.Fatal(err)
			}
			revoked := false
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/oauth/revoke" || r.Header.Get("Authorization") != "Bearer human" {
					t.Errorf("wrong issuer/request %s", r.URL.Path)
				}
				revoked = true
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()
			if err := credentials.Store(path, srv.URL, cloud.HumanClientID, credentials.Credential{Kind: "human", Token: "human"}); err != nil {
				t.Fatal(err)
			}
			prev := declaredControlPlanes
			declaredControlPlanes = func(context.Context) ([]declaredControlPlane, []string, error) {
				return distinctControlPlanes([]envDeclaration{{Env: "prod", Decl: &cloud.Declaration{Endpoint: srv.URL}}}), nil, nil
			}
			t.Cleanup(func() { declaredControlPlanes = prev })
			cmd := newLogoutCmd()
			if explicit {
				cmd.SetArgs([]string{"--endpoint", srv.URL})
			}
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if !revoked {
				t.Fatal("declared non-default issuer was not revoked")
			}
			if _, err := projectEndpoint("prod", &cloud.Declaration{Endpoint: srv.URL}); err == nil {
				t.Fatal("target operations must still reject corrupt bindings")
			}
		})
	}
}

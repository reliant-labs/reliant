package httpaction

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/reliant-labs/reliant/internal/integrations/manifest"
	"github.com/reliant-labs/reliant/internal/netguard/netguardtest"
)

// The org integrations policy at the runner (control-plane
// docs/design/org-policies.md §4.3): asked once per call, before any
// credential is chosen, so no way of authenticating gets around it.

// gatedSource refuses the integrations in refuse, and records what it was
// asked; its Credential is fixedSource's.
type gatedSource struct {
	fixedSource
	refuse map[string]bool
	asked  []string
}

type judgedCtxKey struct{}

func (s *gatedSource) AdmitIntegration(ctx context.Context, runID, integrationID string) (context.Context, error) {
	s.asked = append(s.asked, runID+"/"+integrationID)
	if s.refuse[integrationID] {
		return ctx, &CredentialError{Code: CodeFailedPrecondition, Reason: "integration_not_allowed",
			Message: "your organization's policy does not allow the " + integrationID + " integration in its runs"}
	}
	return context.WithValue(ctx, judgedCtxKey{}, integrationID), nil
}

const unauthManifest = `
id: open
version: 1
display_name: Open
connection:
  base_url: "https://open.example.com"
actions:
  - id: ping
    placement: server
    params: { type: object }
    request: { method: GET, path: "/ping" }
`

func TestRunAuthenticated_TheIntegrationGateRefusesBeforeAnyCredential(t *testing.T) {
	srv := netguardtest.NewTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("a refused call reached the provider")
	}))
	tenant, err := manifest.Parse([]byte(tenantManifest), manifest.TrustCurated)
	if err != nil {
		t.Fatal(err)
	}
	open, err := manifest.Parse([]byte(unauthManifest), manifest.TrustCurated)
	if err != nil {
		t.Fatal(err)
	}
	stepSecret := fakeCred{id: "", secret: "declared-secret", params: map[string]string{"tenant": "blue"}}
	for name, tc := range map[string]struct {
		run  func(src *gatedSource) error
		want string
	}{
		"a declared secret (the step's own credential)": {run: func(src *gatedSource) error {
			_, err := tenantRunner(t, srv).RunAuthenticated(context.Background(), tenant, tenant.GetActions()[0],
				map[string]any{"id": "1"}, src, CallSite{RunID: "run-1", StepCredential: stepSecret})
			return err
		}, want: "acme"},
		"the owner's saved connection": {run: func(src *gatedSource) error {
			_, err := tenantRunner(t, srv).RunAuthenticated(context.Background(), tenant, tenant.GetActions()[0],
				map[string]any{"id": "1"}, src, CallSite{RunID: "run-1"})
			return err
		}, want: "acme"},
		"an integration that needs no credential": {run: func(src *gatedSource) error {
			_, err := tenantRunner(t, srv).RunAuthenticated(context.Background(), open, open.GetActions()[0],
				map[string]any{}, src, CallSite{RunID: "run-1"})
			return err
		}, want: "open"},
	} {
		t.Run(name, func(t *testing.T) {
			src := &gatedSource{refuse: map[string]bool{"acme": true, "open": true}}
			err := tc.run(src)
			var ce *CredentialError
			if !errors.As(err, &ce) || ce.Code != CodeFailedPrecondition || ce.Reason != "integration_not_allowed" {
				t.Fatalf("want the typed refusal, got %v", err)
			}
			if len(src.asked) != 1 || src.asked[0] != "run-1/"+tc.want {
				t.Fatalf("gate asked %v", src.asked)
			}
			if len(src.got) != 0 {
				t.Fatalf("a credential was resolved for a refused call: %+v", src.got)
			}
		})
	}
}

// An admitted call proceeds, on the context the gate returned (so the
// source's own resolution sees the use as already judged).
func TestRunAuthenticated_AnAdmittedIntegrationProceeds(t *testing.T) {
	var gotKey string
	srv := netguardtest.NewTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Key")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	m, err := manifest.Parse([]byte(tenantManifest), manifest.TrustCurated)
	if err != nil {
		t.Fatal(err)
	}
	src := &judgedSeeingSource{gatedSource: gatedSource{fixedSource: fixedSource{cred: fakeCred{id: "conn_1", secret: "k", params: map[string]string{"tenant": "blue"}}}}}
	res, err := tenantRunner(t, srv).RunAuthenticated(context.Background(), m, m.GetActions()[0], map[string]any{"id": "7"}, src, CallSite{RunID: "run-1"})
	if err != nil || res.IsError {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if gotKey != "k" || src.sawJudged != "acme" {
		t.Fatalf("key=%q sawJudged=%q", gotKey, src.sawJudged)
	}
}

type judgedSeeingSource struct {
	gatedSource
	sawJudged string
}

func (s *judgedSeeingSource) Credential(ctx context.Context, req CredentialRequest) (Credential, error) {
	s.sawJudged, _ = ctx.Value(judgedCtxKey{}).(string)
	return s.fixedSource.Credential(ctx, req)
}

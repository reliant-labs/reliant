// Copyright (c) 2025 Reliant Labs
package controlplane

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
)

// vendoredFields is the protojson names of message's fields in the vendored
// OrgNotifyInternalService contract (testdata/org_notify_internal.proto; its
// header names the control-plane commit). The request's notice oneof lists
// every alternative; a request carries exactly one.
func vendoredFields(t *testing.T, src, message string) []string {
	t.Helper()
	body := regexp.MustCompile(`(?s)message ` + message + ` \{(.*?)\n\}`).FindStringSubmatch(src)
	require.NotNil(t, body, "the vendored contract declares %s", message)
	var out []string
	for _, m := range regexp.MustCompile(`(?m)^\s+(?:string|int32|bool|OrgProviderKeyRejected|OrgAutomationNoOrgAccount) (\w+) = \d+;`).FindAllStringSubmatch(body[1], -1) {
		parts := strings.Split(m[1], "_")
		for i := 1; i < len(parts); i++ {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
		out = append(out, strings.Join(parts, ""))
	}
	sort.Strings(out)
	return out
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// The client speaks the contract control-plane merged: the path of
// OrgNotifyInternalService.NotifyOrgAdmins, the internal-service bearer, and
// exactly the request's and the notice's protojson field names, read from
// the vendored proto rather than restated here.
func TestNotifyOrgKeyRejected_SpeaksTheVendoredContract(t *testing.T) {
	raw, err := os.ReadFile("testdata/org_notify_internal.proto")
	require.NoError(t, err)
	src := string(raw)
	require.Contains(t, src, "service OrgNotifyInternalService {")
	require.Contains(t, src, "rpc NotifyOrgAdmins(NotifyOrgAdminsRequest) returns (NotifyOrgAdminsResponse)")
	require.Contains(t, src, "package controlplane.v1;")

	var path, auth string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		_, _ = w.Write([]byte(`{"recipients":0,"deduplicated":true}`))
	}))
	defer srv.Close()
	c := New(Deps{ControlPlane: &configv1.ControlPlaneConfig{Url: srv.URL}, Sign: signWith("s")})

	dedup, err := c.NotifyOrgKeyRejected(context.Background(), OrgKeyRejectedNotice{
		OrgID: "org-acme", IncidentKey: "2030-01-02T03:04:05Z", AccountID: "acct-1",
		Provider: "Anthropic API", Label: "prod", KeyHint: "a1b2", Reason: "401",
	})
	require.NoError(t, err)
	assert.True(t, dedup, "the response's deduplicated is read")
	assert.Equal(t, "/controlplane.v1.OrgNotifyInternalService/NotifyOrgAdmins", path)
	assert.Equal(t, "Bearer signed-s", auth)

	assert.Equal(t, []string{"incidentKey", "orgId", "orgProviderKeyRejected"}, keys(body))
	assert.Subset(t, vendoredFields(t, src, "NotifyOrgAdminsRequest"), keys(body), "the request's fields, by their protojson names")
	notice, ok := body["orgProviderKeyRejected"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, vendoredFields(t, src, "OrgProviderKeyRejected"), keys(notice))
	assert.Equal(t, map[string]any{"accountId": "acct-1", "provider": "Anthropic API", "label": "prod", "keyHint": "a1b2", "reason": "401"}, notice)
	assert.Equal(t, "org-acme", body["orgId"])
	assert.Equal(t, "2030-01-02T03:04:05Z", body["incidentKey"])
	assert.Contains(t, vendoredFields(t, src, "NotifyOrgAdminsResponse"), "deduplicated")
}

// The automation-paused notice (P11) speaks the same contract: the same path
// and bearer, and exactly the vendored OrgAutomationNoOrgAccount's protojson
// field names under the oneof's org_automation_no_org_account.
func TestNotifyOrgAutomationNoOrgAccount_SpeaksTheVendoredContract(t *testing.T) {
	raw, err := os.ReadFile("testdata/org_notify_internal.proto")
	require.NoError(t, err)
	src := string(raw)
	require.Contains(t, src, "OrgAutomationNoOrgAccount org_automation_no_org_account = 4;")

	var path, auth string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		_, _ = w.Write([]byte(`{"recipients":2}`))
	}))
	defer srv.Close()
	c := New(Deps{ControlPlane: &configv1.ControlPlaneConfig{Url: srv.URL}, Sign: signWith("s")})

	dedup, err := c.NotifyOrgAutomationNoOrgAccount(context.Background(), OrgAutomationNoOrgAccountNotice{
		OrgID: "org-acme", IncidentKey: "2030-01-02T03:04:05Z", TriggerID: "trg-1",
		Automation: "Nightly triage", Model: "claude-sonnet-5", OwnerUserID: "user-7",
	})
	require.NoError(t, err)
	assert.False(t, dedup, "a first send is not deduplicated")
	assert.Equal(t, "/controlplane.v1.OrgNotifyInternalService/NotifyOrgAdmins", path)
	assert.Equal(t, "Bearer signed-s", auth)

	assert.Equal(t, []string{"incidentKey", "orgAutomationNoOrgAccount", "orgId"}, keys(body))
	assert.Subset(t, vendoredFields(t, src, "NotifyOrgAdminsRequest"), keys(body), "the request's fields, by their protojson names")
	notice, ok := body["orgAutomationNoOrgAccount"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, vendoredFields(t, src, "OrgAutomationNoOrgAccount"), keys(notice))
	assert.Equal(t, map[string]any{"triggerId": "trg-1", "automation": "Nightly triage", "model": "claude-sonnet-5", "ownerUserId": "user-7"}, notice)
}

// The lease answer ("being sent; retry") is Unavailable: retryable, never a
// verdict. A control plane without the service is ErrOrgNotifyUnsupported.
func TestNotifyOrgKeyRejected_RefusalsAreTyped(t *testing.T) {
	for name, tc := range map[string]struct {
		status      int
		code        string
		unsupported bool
		permanent   bool
	}{
		"leased":     {status: http.StatusServiceUnavailable, code: "unavailable"},
		"no service": {status: http.StatusNotFound},
		"org gone":   {status: http.StatusNotFound, code: "not_found", permanent: true},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				if tc.code != "" {
					_, _ = w.Write([]byte(`{"code":"` + tc.code + `","message":"being sent; retry"}`))
				}
			}))
			defer srv.Close()
			c := New(Deps{ControlPlane: &configv1.ControlPlaneConfig{Url: srv.URL}, Sign: signWith("s")})
			_, err := c.NotifyOrgKeyRejected(context.Background(), OrgKeyRejectedNotice{OrgID: "o", IncidentKey: "i", AccountID: "a", Provider: "p"})
			require.Error(t, err)
			if tc.unsupported || tc.code == "" {
				require.ErrorIs(t, err, ErrOrgNotifyUnsupported)
				return
			}
			var refused *ProjectOrgMoveError
			require.ErrorAs(t, err, &refused)
			assert.Equal(t, tc.permanent, refused.Permanent())
		})
	}
}

// A kind control-plane has switched off (orgs.admin_notice_kinds_off,
// control-plane#2783) is answered as a success with nobody emailed and
// `suppressed`, a field this client does not read: the call succeeds, not
// deduplicated, so the caller settles the incident as it does any success.
func TestNotifyOrgKeyRejected_ASuppressedAnswerIsASuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"recipients":0,"suppressed":true}`))
	}))
	defer srv.Close()
	c := New(Deps{ControlPlane: &configv1.ControlPlaneConfig{Url: srv.URL}, Sign: signWith("s")})

	dedup, err := c.NotifyOrgKeyRejected(context.Background(), OrgKeyRejectedNotice{OrgID: "o", IncidentKey: "i", AccountID: "a", Provider: "p"})
	require.NoError(t, err)
	assert.False(t, dedup)
}

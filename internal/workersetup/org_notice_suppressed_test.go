// Copyright (c) 2025 Reliant Labs
package workersetup

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	enumspb "go.temporal.io/api/enums/v1"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	"github.com/reliant-labs/reliant/internal/controlplane"
)

// control-plane can switch a notice kind's email off
// (orgs.admin_notice_kinds_off, control-plane#2783): NotifyOrgAdmins then
// answers a success with nobody emailed, `{"recipients":0,"suppressed":true}`.
// That answer settles the incident: through the real client its notice
// completes after one call, and a completed incident's workflow id cannot be
// started again (ALLOW_DUPLICATE_FAILED_ONLY), so turning the kind back on
// never resends it. The inbox item stays the notice.
func TestOrgKeyRejectedNotice_ASuppressedAnswerSettlesTheIncident(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		_, _ = w.Write([]byte(`{"recipients":0,"suppressed":true}`))
	}))
	defer srv.Close()
	admins := controlplane.New(controlplane.Deps{
		ControlPlane: &configv1.ControlPlaneConfig{Url: srv.URL},
		Sign:         func() (string, error) { return "signed", nil },
	})

	require.NoError(t, runNotice(t, admins), "a suppressed answer is a success: the notice completes")
	mu.Lock()
	assert.Equal(t, 1, calls, "asked once, never retried")
	mu.Unlock()

	s := &recordingStarter{}
	require.NoError(t, startOrgKeyNotice(context.Background(), s, "q", controlplane.OrgKeyRejectedNotice{OrgID: "o", AccountID: "a", IncidentKey: "i"}))
	require.Len(t, s.started, 1)
	assert.Equal(t, enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY, s.started[0].WorkflowIDReusePolicy,
		"a completed incident, suppressed included, is never started again")
}

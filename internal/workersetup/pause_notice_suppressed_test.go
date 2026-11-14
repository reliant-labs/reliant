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

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	"github.com/reliant-labs/reliant/internal/controlplane"
)

// The pause notice's twin of
// TestOrgKeyRejectedNotice_ASuppressedAnswerSettlesTheIncident: with
// org_automation_no_org_account switched off in control-plane
// (orgs.admin_notice_kinds_off, control-plane#2783), NotifyOrgAdmins answers
// `{"recipients":0,"suppressed":true}`, a success. Through the real client the
// pause's notice completes after one call and records the pause as notified
// (ConfirmPauseNotice), so no sweep starts it again, and turning the kind back
// on never resends it. The inbox item stays the notice.
func TestOrgAutomationNoOrgAccountNotice_ASuppressedAnswerSettlesThePause(t *testing.T) {
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

	store, err := runPauseNotice(t, admins)
	require.NoError(t, err, "a suppressed answer is a success: the notice completes")
	assert.Equal(t, []string{"t-1"}, store.confirmed, "the pause is recorded as notified")
	mu.Lock()
	assert.Equal(t, 1, calls, "asked once, never retried")
	mu.Unlock()

	starter := &pauseStarter{}
	assert.Equal(t, 0, sweepPauseNotices(context.Background(), store, orgKeyNoticeQueue{starter: starter, taskQueue: "q"}))
	assert.Empty(t, starter.ids, "a settled pause is never started again")
}

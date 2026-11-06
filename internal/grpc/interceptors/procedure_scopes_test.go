// Copyright (c) 2025 Reliant Labs
package interceptors

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/golang-jwt/jwt/v5"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
)

// scopeRow is one rpc and the scope a narrow token needs for it. need == ""
// means no narrow scope covers the rpc: it needs reliant:api.
type scopeRow struct {
	procedure string
	need      fat.Scope
}

var runTriggerRows = []scopeRow{
	{reliantv1connect.RunServiceStartRunProcedure, fat.ScopeRunsStart},
	{reliantv1connect.RunServiceGetRunProcedure, fat.ScopeRunsRead},
	{reliantv1connect.RunServiceListRunsProcedure, fat.ScopeRunsRead},
	{reliantv1connect.RunServiceLastRunPerWorkflowProcedure, fat.ScopeRunsRead},
	{reliantv1connect.RunServiceSignalRunProcedure, fat.ScopeRunsControl},
	{reliantv1connect.RunServicePauseRunProcedure, fat.ScopeRunsControl},
	{reliantv1connect.RunServiceResumeRunProcedure, fat.ScopeRunsControl},
	{reliantv1connect.RunServiceCancelRunProcedure, fat.ScopeRunsControl},
	{reliantv1connect.RunServiceInterruptRunProcedure, fat.ScopeRunsControl},

	{reliantv1connect.TriggerServiceFireTriggerProcedure, fat.ScopeTriggersFire},
	{reliantv1connect.TriggerServiceCreateTriggerProcedure, fat.ScopeTriggersManage},
	{reliantv1connect.TriggerServiceUpdateTriggerProcedure, fat.ScopeTriggersManage},
	{reliantv1connect.TriggerServiceDeleteTriggerProcedure, fat.ScopeTriggersManage},
	{reliantv1connect.TriggerServiceSetTriggerEnabledProcedure, fat.ScopeTriggersManage},
	{reliantv1connect.TriggerServiceRotateWebhookTokenProcedure, fat.ScopeTriggersManage},
	{reliantv1connect.TriggerServiceEnableWorkflowProcedure, fat.ScopeTriggersManage},
	{reliantv1connect.TriggerServiceDisableWorkflowProcedure, fat.ScopeTriggersManage},

	// No narrow scope yet (forge's mapping): reliant:api only.
	{reliantv1connect.TriggerServiceGetTriggerProcedure, ""},
	{reliantv1connect.TriggerServiceListTriggersProcedure, ""},
	{reliantv1connect.TriggerServiceListTriggerEventsProcedure, ""},
	{reliantv1connect.TriggerServiceGetLaunchEventProcedure, ""},
	{reliantv1connect.TriggerServiceResolveTriggerSendersProcedure, ""},
	{reliantv1connect.TriggerServiceListReplayEventsProcedure, ""},
	{reliantv1connect.TriggerServicePreviewReplayProcedure, ""},
	{reliantv1connect.TriggerServiceDraftAutomationProcedure, ""},
}

// inboxRows is the inbox slice. Approvals are listed with need "": their
// decisions stay on reliant:api until approvals:decide is decided.
var inboxRows = []scopeRow{
	{reliantv1connect.InboxServiceListInboxProcedure, fat.ScopeInboxRead},
	{reliantv1connect.InboxServiceDismissInboxItemProcedure, fat.ScopeInboxManage},
	{reliantv1connect.InboxServiceRestoreInboxItemProcedure, fat.ScopeInboxManage},
	{reliantv1connect.InboxServiceCleanupStorageProcedure, ""},
	{reliantv1connect.InboxServiceForgetStorageWorktreeProcedure, ""},

	{reliantv1connect.QuestionServiceResolveQuestionProcedure, fat.ScopeQuestionsAnswer},
	{reliantv1connect.QuestionServiceListPendingQuestionsProcedure, ""},
	{reliantv1connect.QuestionServiceListAsyncQuestionsProcedure, ""},

	{reliantv1connect.ApprovalServiceListApprovalsByChatProcedure, ""},
	{reliantv1connect.ApprovalServiceApproveProcedure, ""},
	{reliantv1connect.ApprovalServiceDenyProcedure, ""},
	{reliantv1connect.ApprovalServiceBatchApproveProcedure, ""},
	{reliantv1connect.ApprovalServiceBatchDenyProcedure, ""},
}

// wrongScope is a scope that does not permit need, nor satisfy an unmapped rpc.
func wrongScope(need fat.Scope) fat.Scope {
	if need == fat.ScopeRunsRead {
		return fat.ScopeTriggersFire
	}
	return fat.ScopeRunsRead
}

type scopeHarness struct {
	interceptor *AuthInterceptor
	url         string
	authority   *tokenauthority.Memory
	session     string
}

// newScopeHarness serves every RunService, TriggerService and ChatService rpc
// as Unimplemented behind the real AuthInterceptor, so the status says how far
// a credential got: Unimplemented is "admitted", PermissionDenied is "refused
// by the scope check". The session is a real signed app-session JWT, the path
// the web app uses.
func newScopeHarness(t *testing.T) *scopeHarness {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	require.NoError(t, err)
	pubPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))

	interceptor, err := NewAuthInterceptor(auth.Settings{PublicKeyPEM: pubPEM}, nil)
	require.NoError(t, err)
	authority := tokenauthority.NewMemory()
	interceptor.SetAccessTokenIntrospector(authority)

	session, err := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"sub": "user-web", "session_id": "s1", "exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString(key)
	require.NoError(t, err)

	mux := http.NewServeMux()
	opts := connect.WithInterceptors(interceptor)
	for _, mount := range []func() (string, http.Handler){
		func() (string, http.Handler) {
			return reliantv1connect.NewRunServiceHandler(reliantv1connect.UnimplementedRunServiceHandler{}, opts)
		},
		func() (string, http.Handler) {
			return reliantv1connect.NewTriggerServiceHandler(reliantv1connect.UnimplementedTriggerServiceHandler{}, opts)
		},
		func() (string, http.Handler) {
			return reliantv1connect.NewChatServiceHandler(reliantv1connect.UnimplementedChatServiceHandler{}, opts)
		},
		func() (string, http.Handler) {
			return reliantv1connect.NewInboxServiceHandler(reliantv1connect.UnimplementedInboxServiceHandler{}, opts)
		},
		func() (string, http.Handler) {
			return reliantv1connect.NewQuestionServiceHandler(reliantv1connect.UnimplementedQuestionServiceHandler{}, opts)
		},
		func() (string, http.Handler) {
			return reliantv1connect.NewApprovalServiceHandler(reliantv1connect.UnimplementedApprovalServiceHandler{}, opts)
		},
	} {
		path, h := mount()
		mux.Handle(path, h)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &scopeHarness{interceptor: interceptor, url: srv.URL, authority: authority, session: session}
}

func (h *scopeHarness) mint(t *testing.T, scopes ...fat.Scope) string {
	t.Helper()
	return mintFor(t, h.authority, "user-tok", scopes...).Plaintext
}

type scopeResult struct {
	code   string
	reason string
	scopes string
}

func (h *scopeHarness) call(t *testing.T, procedure, bearer string) scopeResult {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, h.url+procedure, strings.NewReader("{}"))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var e struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(body, &e), string(body))
	return scopeResult{
		code:   e.Code,
		reason: resp.Header.Get(svcerr.ReasonHeader),
		scopes: resp.Header.Get(fat.ScopesHeader),
	}
}

func (r scopeResult) admitted() bool { return r.code == "unimplemented" }

func TestRunAndTriggerRPCsEnforceTokenScopes(t *testing.T) {
	enforceRows(t, runTriggerRows, fat.RunsAndTriggersScopes)
}

func TestInboxRPCsEnforceTokenScopes(t *testing.T) {
	enforceRows(t, inboxRows, fat.InboxScopes)
}

func enforceRows(t *testing.T, rows []scopeRow, every []fat.Scope) {
	h := newScopeHarness(t)
	reliantAPI := h.mint(t, fat.ScopeReliantAPI)

	for _, row := range rows {
		t.Run(strings.TrimPrefix(row.procedure, "/reliant.v1."), func(t *testing.T) {
			t.Run("web session is admitted", func(t *testing.T) {
				require.True(t, h.call(t, row.procedure, h.session).admitted())
			})
			t.Run("reliant:api token is admitted", func(t *testing.T) {
				require.True(t, h.call(t, row.procedure, reliantAPI).admitted())
			})

			wanted := row.need
			if wanted == "" {
				wanted = fat.ScopeReliantAPI
			} else {
				t.Run("narrow token with the right scope is admitted", func(t *testing.T) {
					require.True(t, h.call(t, row.procedure, h.mint(t, row.need)).admitted())
				})
			}

			t.Run("narrow token with the wrong scope is refused scope_missing", func(t *testing.T) {
				wrong := []fat.Scope{wrongScope(row.need)}
				if row.need == "" {
					wrong = every // every narrow scope together still is not reliant:api
				}
				res := h.call(t, row.procedure, h.mint(t, wrong...))
				require.Equal(t, "permission_denied", res.code)
				require.Equal(t, fat.ReasonScopeMissing, res.reason)
				require.Equal(t, string(wanted), res.scopes, "the missing scope is named")
			})
		})
	}
}

// Every rpc of the two services is in the table above, so a new rpc cannot ship
// without a decision about its scope.
func TestRunAndTriggerTableCoversEveryRPC(t *testing.T) {
	declared := map[string]bool{}
	for _, svc := range []struct{ name, prefix string }{{"RunService", "/reliant.v1.RunService/"}, {"TriggerService", "/reliant.v1.TriggerService/"}} {
		var methods []string
		switch svc.name {
		case "RunService":
			sd := reliantv1.File_reliant_v1_run_proto.Services().ByName("RunService")
			for i := 0; i < sd.Methods().Len(); i++ {
				methods = append(methods, string(sd.Methods().Get(i).Name()))
			}
		case "TriggerService":
			sd := reliantv1.File_reliant_v1_trigger_proto.Services().ByName("TriggerService")
			for i := 0; i < sd.Methods().Len(); i++ {
				methods = append(methods, string(sd.Methods().Get(i).Name()))
			}
		}
		require.NotEmpty(t, methods)
		for _, m := range methods {
			declared[svc.prefix+m] = false
		}
	}
	for _, row := range runTriggerRows {
		_, ok := declared[row.procedure]
		require.True(t, ok, "%s is in the table but not in the service", row.procedure)
		declared[row.procedure] = true
	}
	for procedure, covered := range declared {
		require.True(t, covered, "%s has no row in runTriggerRows: decide its scope", procedure)
	}
}

// The table in the test and the shipped map must agree.
func TestScopeForProcedureMatchesTable(t *testing.T) {
	for _, row := range runTriggerRows {
		want := row.need
		if want == "" {
			want = fat.ScopeReliantAPI
		}
		require.Equal(t, want, ScopeForProcedure(row.procedure), row.procedure)
	}
	for _, row := range inboxRows {
		want := row.need
		if want == "" {
			want = fat.ScopeReliantAPI
		}
		require.Equal(t, want, ScopeForProcedure(row.procedure), row.procedure)
	}
	require.Len(t, procedureScopes, 21, "an entry beyond the 21 documented rpcs widens what a narrow token reaches")
}

// Every rpc of the inbox, question and approval services has a row, so a new
// rpc cannot ship without a decision about its scope.
func TestInboxTableCoversEveryRPC(t *testing.T) {
	declared := map[string]bool{}
	for _, sd := range []protoreflect.ServiceDescriptor{
		reliantv1.File_reliant_v1_inbox_proto.Services().ByName("InboxService"),
		reliantv1.File_reliant_v1_question_proto.Services().ByName("QuestionService"),
		reliantv1.File_reliant_v1_approval_proto.Services().ByName("ApprovalService"),
	} {
		for i := 0; i < sd.Methods().Len(); i++ {
			declared["/reliant.v1."+string(sd.Name())+"/"+string(sd.Methods().Get(i).Name())] = false
		}
	}
	for _, row := range inboxRows {
		_, ok := declared[row.procedure]
		require.True(t, ok, "%s is in the table but not in the service", row.procedure)
		declared[row.procedure] = true
	}
	for procedure, covered := range declared {
		require.True(t, covered, "%s has no row in inboxRows: decide its scope", procedure)
	}
}

// approvals:decide does not reach the decisions yet: a token holding it, with
// every other new scope, still cannot approve or deny.
func TestApprovalDecisionsStayOnReliantAPI(t *testing.T) {
	h := newScopeHarness(t)
	tok := h.mint(t, fat.ScopeApprovalsDecide, fat.ScopeInboxManage, fat.ScopeQuestionsAnswer)
	for _, p := range []string{
		reliantv1connect.ApprovalServiceApproveProcedure, reliantv1connect.ApprovalServiceDenyProcedure,
		reliantv1connect.ApprovalServiceBatchApproveProcedure, reliantv1connect.ApprovalServiceBatchDenyProcedure,
	} {
		res := h.call(t, p, tok)
		require.Equal(t, "permission_denied", res.code, p)
		require.Equal(t, string(fat.ScopeReliantAPI), res.scopes, p)
	}
}

// inbox:manage implies inbox:read (forge's Permits); read does not manage; the
// scopes of the inbox do not reach runs.
func TestInboxScopeImplications(t *testing.T) {
	h := newScopeHarness(t)
	require.True(t, h.call(t, reliantv1connect.InboxServiceListInboxProcedure, h.mint(t, fat.ScopeInboxManage)).admitted())
	require.Equal(t, "permission_denied", h.call(t, reliantv1connect.InboxServiceDismissInboxItemProcedure, h.mint(t, fat.ScopeInboxRead)).code)
	for _, s := range fat.InboxScopes {
		require.Equal(t, "permission_denied", h.call(t, reliantv1connect.RunServiceGetRunProcedure, h.mint(t, s)).code, s)
	}
}

// A narrow token is limited to its slice: every rpc outside it needs reliant:api.
func TestNarrowTokenCannotReachOtherServices(t *testing.T) {
	h := newScopeHarness(t)
	all := h.mint(t, fat.RunsAndTriggersScopes...)
	res := h.call(t, reliantv1connect.ChatServiceGetChatProcedure, all)
	require.Equal(t, "permission_denied", res.code)
	require.Equal(t, fat.ReasonScopeMissing, res.reason)
	require.Equal(t, string(fat.ScopeReliantAPI), res.scopes)
}

// Regression: the web app's session and a pre-existing reliant:api token reach
// everything they did before, including rpcs outside runs and triggers.
func TestSessionAndReliantAPIUnaffectedByScopes(t *testing.T) {
	h := newScopeHarness(t)
	reliantAPI := h.mint(t, fat.ScopeReliantAPI)
	for _, bearer := range []string{h.session, reliantAPI} {
		require.True(t, h.call(t, reliantv1connect.ChatServiceGetChatProcedure, bearer).admitted())
	}
	// A session that is not a token carries no principal and no scope.
	ctx, _, err := h.interceptor.authenticateRequest(context.Background(), reliantv1connect.RunServiceCancelRunProcedure, bearerHeader(h.session))
	require.NoError(t, err)
	_, isMachine := auth.MachineTokenFromContext(ctx)
	require.False(t, isMachine)
}

// runs:start and runs:control imply runs:read (forge's Permits); neither
// implies the other.
func TestScopeImplicationsAreHonoured(t *testing.T) {
	h := newScopeHarness(t)
	for _, scope := range []fat.Scope{fat.ScopeRunsStart, fat.ScopeRunsControl} {
		require.True(t, h.call(t, reliantv1connect.RunServiceGetRunProcedure, h.mint(t, scope)).admitted(), scope)
	}
	require.Equal(t, "permission_denied", h.call(t, reliantv1connect.RunServiceCancelRunProcedure, h.mint(t, fat.ScopeRunsStart)).code)
	require.Equal(t, "permission_denied", h.call(t, reliantv1connect.RunServiceStartRunProcedure, h.mint(t, fat.ScopeRunsControl)).code)
	// triggers:fire starts a run through the trigger, not through runs:start.
	require.Equal(t, "permission_denied", h.call(t, reliantv1connect.RunServiceStartRunProcedure, h.mint(t, fat.ScopeTriggersFire)).code)
}

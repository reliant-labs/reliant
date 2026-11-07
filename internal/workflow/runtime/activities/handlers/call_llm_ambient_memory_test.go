package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	cfgpkg "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/openai"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	wfruntime "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

const (
	ambientGlobalCanary  = "PRIVATE_GLOBAL_40e5b620"
	ambientProjectCanary = "PRIVATE_PROJECT_40e5b620"
	ambientRepoCanary    = "PRIVATE_REPO_40e5b620"
	approvedBriefCanary  = "APPROVED_BRIEF_40e5b620"
	skillCatalogCanary   = "PRIVATE_SKILL_DESCRIPTION_40e5b620"
	skillBodyCanary      = "PRIVATE_SKILL_BODY_40e5b620"
)

type ambientConfigProvider struct{ cfg *cfgpkg.Config }

func (p ambientConfigProvider) GetProjectConfig(context.Context, cfgpkg.ProjectRef) (*cfgpkg.Config, error) {
	return p.cfg, nil
}

// Capture the actual driver boundary AFTER all handler assembly/transforms,
// including system prompts and tool schemas, not just the memory formatter.
type ambientRequest struct {
	Prompts  []string
	Messages []message.Message
	Tools    []string
}

func (r ambientRequest) text(t *testing.T) string {
	t.Helper()
	b, err := json.Marshal(r)
	require.NoError(t, err)
	return string(b)
}

type ambientCaptureDriver struct {
	requestRecordingDriver
	captureMu sync.Mutex
	sent      []ambientRequest
	failFirst bool
}

func (d *ambientCaptureDriver) StreamResponse(_ context.Context, prompts []string, msgs []message.Message, offered []tools.Tool) <-chan llm.DriverEvent {
	req := ambientRequest{Prompts: append([]string(nil), prompts...), Messages: append([]message.Message(nil), msgs...)}
	for _, tool := range offered {
		schema, _ := json.Marshal(tool.ParamSchema())
		req.Tools = append(req.Tools, tool.Name()+"\n"+tool.Description()+"\n"+string(schema))
	}
	d.captureMu.Lock()
	d.sent = append(d.sent, req)
	fail := d.failFirst && len(d.sent) == 1
	d.captureMu.Unlock()
	ch := make(chan llm.DriverEvent, 1)
	if fail {
		ch <- llm.DriverEvent{Type: llm.EventError, Error: errors.New("http2: client connection lost")}
	} else {
		ch <- llm.DriverEvent{Type: llm.EventComplete, Response: &llm.DriverResponse{Content: "ok", FinishReason: message.FinishReasonEndTurn, Usage: llm.TokenUsage{TokenCount: 10}}}
	}
	close(ch)
	return ch
}
func emptyAmbientTools() *reliantv1.ToolsConfig {
	empty := func() *reliantv1.CelStringList {
		return &reliantv1.CelStringList{Value: &reliantv1.CelStringList_Literal{Literal: &reliantv1.StringList{}}}
	}
	return &reliantv1.ToolsConfig{PreloadedTools: empty(), LoadableTools: empty()}
}

// This additionally runs the real OpenAI adapter and captures its HTTP body,
// proving the policy survives provider message conversion, not only assembly.
func TestCallLLM_ExcludeAmbientMemoryOutgoingHTTP(t *testing.T) {
	for _, excluded := range []bool{false, true} {
		t.Run(fmt.Sprintf("excluded=%t", excluded), func(t *testing.T) {
			h := NewIdempotencyTestHelper(t)
			defer h.Cleanup()
			ctx := context.Background()
			project := h.CreateTestProject(ctx, uuid.NewString(), uuid.NewString())
			chat := h.CreateTestChat(ctx, uuid.NewString(), project.ID, project.UserID)
			h.CreateTestUserMessageWithText(ctx, chat.ID, chat.ID, approvedBriefCanary)
			bodies := make(chan string, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				bodies <- string(body)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer srv.Close()
			driver := openai.NewClient(llm.DriverOptions{Model: models.Model{ID: "gpt-4o", APIModel: "gpt-4o"}, ApiKey: "test-only", BaseURL: srv.URL, MaxTokens: 100})
			cfg := &cfgpkg.Config{GlobalMemoryMD: ambientGlobalCanary, ProjectMemoryMD: ambientProjectCanary, RepoMemories: map[string]string{"private-repo": ambientRepoCanary}, DaemonRuntimeType: "kata"}
			handler := NewCallLLMActivity(h.Repo(), nil, nil, ambientConfigProvider{cfg}, func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
				return driver, nil
			}, nil)
			input := callLLMInput(chat.ID, chat.ID, "mock-model")
			input.Node.GetCallLlm().ExcludeAmbientMemory = excluded
			input.Node.GetCallLlm().ToolsConfig = emptyAmbientTools()
			var out CallLLMOutput
			require.NoError(t, h.ExecuteActivity(handler.Execute, input, &out))
			select {
			case body := <-bodies:
				require.Contains(t, body, approvedBriefCanary)
				for _, canary := range []string{ambientGlobalCanary, ambientProjectCanary, ambientRepoCanary} {
					if excluded {
						require.NotContains(t, body, canary)
					} else {
						require.Contains(t, body, canary)
					}
				}
			default:
				t.Fatal("provider transport received no request")
			}
		})
	}
}

func TestCallLLM_ExcludeAmbientMemoryProviderBoundary(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		local, inherited, retry bool
	}{
		{name: "default retains memory"},
		{name: "call opts out", local: true},
		{name: "inherited cannot reenable with false", inherited: true},
		{name: "inherited survives temporal retry", inherited: true, retry: true},
		{name: "call survives temporal retry", local: true, retry: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := NewIdempotencyTestHelper(t)
			defer h.Cleanup()
			ctx := context.Background()
			project := h.CreateTestProject(ctx, uuid.NewString(), uuid.NewString())
			chat := h.CreateTestChat(ctx, uuid.NewString(), project.ID, project.UserID)
			h.CreateTestUserMessageWithText(ctx, chat.ID, chat.ID, approvedBriefCanary)
			cfg := &cfgpkg.Config{GlobalMemoryMD: ambientGlobalCanary, ProjectMemoryMD: ambientProjectCanary, RepoMemories: map[string]string{"private-repo": ambientRepoCanary}, DaemonRuntimeType: "kata"}
			driver := &ambientCaptureDriver{failFirst: tc.retry}
			handler := NewCallLLMActivity(h.Repo(), nil, nil, ambientConfigProvider{cfg}, func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
				return driver, nil
			}, nil)
			input := callLLMInput(chat.ID, chat.ID, "mock-model")
			input.Runtime.ExcludeAmbientMemory = tc.inherited
			input.Node.GetCallLlm().ExcludeAmbientMemory = tc.local
			input.Node.GetCallLlm().ToolsConfig = emptyAmbientTools()
			if tc.retry {
				var suite temporaltest.WorkflowTestSuite
				env := suite.NewTestWorkflowEnvironment()
				env.RegisterWorkflow(callLLMWithRetries)
				env.RegisterActivityWithOptions(handler.Execute, activity.RegisterOptions{Name: "CallLLM"})
				env.ExecuteWorkflow(callLLMWithRetries, input)
				require.NoError(t, env.GetWorkflowError())
			} else {
				var out CallLLMOutput
				require.NoError(t, h.ExecuteActivity(handler.Execute, input, &out))
			}
			want := 1
			if tc.retry {
				want = 2
			}
			require.Len(t, driver.sent, want)
			for _, req := range driver.sent {
				text := req.text(t)
				require.Contains(t, text, approvedBriefCanary)
				for _, canary := range []string{ambientGlobalCanary, ambientProjectCanary, ambientRepoCanary} {
					if tc.local || tc.inherited {
						require.NotContains(t, text, canary)
					} else {
						require.Contains(t, text, canary)
					}
				}
				require.Contains(t, text, "this daemon runs in a kata sandbox", "harness safety guidance is not ambient memory")
				require.Empty(t, req.Tools, "memory policy must not introduce tools")
			}
			if tc.retry {
				require.Equal(t, driver.sent[0], driver.sent[1], "retry must preserve the entire assembled request")
			}
		})
	}
}

// Simulate coarse recovery/stopped-agent resume by discarding every runtime
// input and constructing a fresh activity handler. The durable thread and its
// lineage, not a surviving in-memory tracker, must keep memories out.
func TestCallLLM_ExcludeAmbientMemoryDurableRecovery(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()
	project := h.CreateTestProject(ctx, uuid.NewString(), uuid.NewString())
	chat := h.CreateTestChat(ctx, uuid.NewString(), project.ID, project.UserID)
	h.CreateTestUserMessageWithText(ctx, chat.ID, chat.ID, approvedBriefCanary)
	cfg := &cfgpkg.Config{GlobalMemoryMD: ambientGlobalCanary, ProjectMemoryMD: ambientProjectCanary, RepoMemories: map[string]string{"private": ambientRepoCanary}}
	capture := &ambientCaptureDriver{}
	freshHandler := func() *CallLLMActivity {
		return NewCallLLMActivity(h.Repo(), nil, nil, ambientConfigProvider{cfg}, func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
			return capture, nil
		}, nil)
	}
	first := callLLMInput(chat.ID, chat.ID, "mock-model")
	first.Node.GetCallLlm().ExcludeAmbientMemory = true
	var out CallLLMOutput
	require.NoError(t, h.ExecuteActivity(freshHandler().Execute, first, &out))
	stored, err := h.Repo().GetThread(ctx, chat.ID)
	require.NoError(t, err)
	require.True(t, stored.ExcludeAmbientMemory)
	// Real coarse-recovery builder has no former runtime policy to recover;
	// even then the next actual provider request must consult durable lineage.
	resume := wfruntime.ResumeInputFromDurableState(ctx, h.Repo(), chat.ID, chat.ID)
	require.NotNil(t, resume)
	require.Empty(t, resume.AmbientMemoryExcluded)
	require.NoError(t, h.ExecuteActivity(freshHandler().Execute, callLLMInput(chat.ID, chat.ID, "mock-model"), &out))
	// A stopped child with false/default local policy resumes under an excluded
	// parent, even before that child's first successful provider call.
	parentID := chat.ID
	childID := uuid.NewString()
	_, err = h.Repo().CreateThread(ctx, &db.Thread{ID: childID, ChatID: chat.ID, ParentThreadID: &parentID, Status: db.ThreadStatusFailed})
	require.NoError(t, err)
	_, err = h.Repo().CreateContextWindow(ctx, &db.ContextWindow{ID: chat.ID + ":" + childID + ":0", ThreadID: childID, Sequence: 0})
	require.NoError(t, err)
	h.CreateTestUserMessageWithText(ctx, chat.ID, childID, approvedBriefCanary)
	require.NoError(t, h.ExecuteActivity(freshHandler().Execute, callLLMInput(chat.ID, childID, "mock-model"), &out))
	child, err := h.Repo().GetThread(ctx, childID)
	require.NoError(t, err)
	require.True(t, child.ExcludeAmbientMemory)
	require.Len(t, capture.sent, 3)
	for _, req := range capture.sent {
		text := req.text(t)
		require.Contains(t, text, approvedBriefCanary)
		for _, canary := range []string{ambientGlobalCanary, ambientProjectCanary, ambientRepoCanary} {
			require.NotContains(t, text, canary)
		}
	}
}

type failingAmbientPolicyStore struct{ CallLLMStore }

func (f failingAmbientPolicyStore) ResolveThreadAmbientMemoryExclusion(context.Context, string, string, bool) (bool, error) {
	return false, errors.New("policy storage unavailable")
}

func TestCallLLM_ExcludeAmbientMemoryFailsClosed(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()
	project := h.CreateTestProject(ctx, uuid.NewString(), uuid.NewString())
	chat := h.CreateTestChat(ctx, uuid.NewString(), project.ID, project.UserID)
	h.CreateTestUserMessageWithText(ctx, chat.ID, chat.ID, approvedBriefCanary)
	driver := &ambientCaptureDriver{}
	handler := NewCallLLMActivity(failingAmbientPolicyStore{h.Repo()}, nil, nil, ambientConfigProvider{&cfgpkg.Config{GlobalMemoryMD: ambientGlobalCanary}}, func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
		return driver, nil
	}, nil)
	var out CallLLMOutput
	err := h.ExecuteActivity(handler.Execute, callLLMInput(chat.ID, chat.ID, "mock-model"), &out)
	require.ErrorContains(t, err, "resolve ambient memory exclusion")
	require.Empty(t, driver.sent, "a failed policy read must never default to injecting memory")
}

// Existing empty tool/skill declarations are not a bootstrap-isolation API.
// Machine-less runs still see the configured skill catalog and generic harness
// instructions; an empty config removes the catalog, not the harness.
func TestCallLLM_ExcludeAmbientMemoryMinimalMachineLessContext(t *testing.T) {
	for _, catalog := range []bool{false, true} {
		name := "empty project config"
		if catalog {
			name = "skill catalog despite empty preload"
		}
		t.Run(name, func(t *testing.T) {
			h := NewIdempotencyTestHelper(t)
			defer h.Cleanup()
			ctx := context.Background()
			project := h.CreateTestProject(ctx, uuid.NewString(), uuid.NewString())
			chat := h.CreateTestChat(ctx, uuid.NewString(), project.ID, project.UserID)
			_, err := h.sqlDB.ExecContext(ctx, "UPDATE chats SET no_machine = true WHERE id = $1", chat.ID)
			require.NoError(t, err)
			h.CreateTestUserMessageWithText(ctx, chat.ID, chat.ID, approvedBriefCanary)
			cfg := &cfgpkg.Config{}
			if catalog {
				cfg.Skills = []cfgpkg.StoredSkill{{Name: "private", SkillPath: "private", Description: skillCatalogCanary, Body: skillBodyCanary}}
			}
			driver := &ambientCaptureDriver{}
			handler := NewCallLLMActivity(h.Repo(), nil, nil, ambientConfigProvider{cfg}, func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
				return driver, nil
			}, nil)
			input := callLLMInput(chat.ID, chat.ID, "mock-model")
			args := input.Node.GetCallLlm()
			args.ExcludeAmbientMemory = true
			args.ToolsConfig = emptyAmbientTools()
			args.Skills = &reliantv1.CelStringList{Value: &reliantv1.CelStringList_Literal{Literal: &reliantv1.StringList{}}}
			var out CallLLMOutput
			require.NoError(t, h.ExecuteActivity(handler.Execute, input, &out))
			require.Len(t, driver.sent, 1)
			req := driver.sent[0]
			require.Empty(t, req.Tools)
			text := req.text(t)
			require.Contains(t, text, approvedBriefCanary)
			require.Contains(t, text, "You are Reliant")
			require.Contains(t, text, "reliant_capabilities")
			require.Contains(t, text, "This run has no machine")
			require.NotContains(t, text, skillBodyCanary)
			require.Equal(t, catalog, strings.Contains(text, skillCatalogCanary))
		})
	}
}

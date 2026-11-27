// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/agentmailbox"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/chataccess/chataccesstest"
	"github.com/reliant-labs/reliant/internal/chatupdates"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
	"github.com/reliant-labs/reliant/internal/threads"
)

// The chat that made BranchChat fail in prod (trace
// 82a3f94880658230b94f738a9c7c066f) was an orchestrator with 20 plans and 116
// tasks, branched while its workflow was still writing to it. These tests hold
// the two properties that branch lacked: the copy costs the same number of
// statements however large the plan, and a branch of a chat that is being
// written to commits on its first attempt, with no serialization failure.

// branchCopySource is a chat to branch from: a worktree, a root thread and
// workflow, one message to branch at, and `plans` plans of `tasksPerPlan`
// tasks each, every fifth task a root and the rest children of the task
// before them.
type branchCopySource struct {
	chatID, threadID, messageID, worktreeID string
}

func seedBranchCopySource(t *testing.T, env *storetest.Env, ctx context.Context, plans, tasksPerPlan int) branchCopySource {
	t.Helper()
	now := time.Now().UTC()
	src := branchCopySource{chatID: uuid.NewString(), worktreeID: "wt-" + uuid.NewString()}
	src.threadID = src.chatID

	require.NoError(t, env.Repo.CreateWorktree(ctx, &db.Worktree{
		ID: src.worktreeID, Name: src.worktreeID, Path: t.TempDir(), Branch: "main",
		ProjectID: storetest.SeedProjectID, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	require.NoError(t, env.Repo.CreateChat(ctx, &db.Chat{
		ID: src.chatID, UserID: storetest.SeedUserID, Title: "Orchestrator", ProjectID: storetest.SeedProjectID,
		WorktreeID: &src.worktreeID, State: db.ChatStateIdle, CreatedAt: now, UpdatedAt: now,
	}))
	_, _, _, err := branchCopyThreads(env.Repo).CreateWorkflowWithThread(ctx, threads.CreateWorkflowWithThreadOpts{
		Workflow: &db.Workflow{
			ID: src.chatID, ChatID: src.chatID, WorkflowName: "builtin://agent",
			Thread: src.threadID, Status: db.Pending(), CreatedAt: now,
		},
		ThreadID: src.threadID,
		ChatID:   src.chatID,
	})
	require.NoError(t, err)
	msg, err := env.Repo.SaveMessageToThread(ctx, src.chatID, src.threadID, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER), "build it", &src.chatID, nil, nil)
	require.NoError(t, err)
	src.messageID = msg.ID

	// Plans are seeded in SQL, not row by row: the subject is the copy.
	_, err = env.SQL.ExecContext(ctx, `
INSERT INTO plans (id, thread_id, title, description, status, complexity, created_at, updated_at)
SELECT $1 || '-plan-' || p, $2, 'plan ' || p, 'about plan ' || p, $3, 2, now() - p * interval '1 minute', now()
  FROM generate_series(1, $4::int) p`,
		src.chatID, src.threadID, int32(db.PlanStatusCompleted), plans)
	require.NoError(t, err)
	_, err = env.SQL.ExecContext(ctx, `
INSERT INTO tasks (id, plan_id, parent_task_id, title, description, status, position, metadata, assignee, created_at, updated_at)
SELECT $1 || '-task-' || p || '-' || i, $1 || '-plan-' || p,
       CASE WHEN i % 5 = 0 THEN NULL ELSE $1 || '-task-' || p || '-' || (i - 1) END,
       'task ' || p || '-' || i, 'do ' || p || '-' || i, $2, i, '{"k":1}', 'agent', now(), now()
  FROM generate_series(1, $3::int) p, generate_series(0, $4::int - 1) i`,
		src.chatID, int32(db.TaskStatusCompleted), plans, tasksPerPlan)
	require.NoError(t, err)
	return src
}

func branchCopyThreads(repo *db.Repo) *threads.Service {
	return threads.New(threads.Deps{
		Repo: repo, Chats: chataccesstest.For(repo),
		Messages: store.NewMessageExtStore(repo.ORM()), Senders: store.NewThreadExtStore(repo.ORM()),
		// Branching must not signal workflows or cancel running tools. These
		// strict mocks fail on any unexpected call to either collaborator.
		Signaler: &threads.MockTemporalSignaler{}, ToolCanceler: &threads.MockToolCanceler{},
	})
}

// branchCopyService is the chat service over probe's pool, so the probe sees
// every statement BranchChat sends and nothing else does.
func branchCopyService(probe *storetest.TxProbe) *Service {
	repo, orm := probe.Repo, probe.ORM
	return &Service{
		deps: Deps{
			Repo: repo, Chats: chataccesstest.For(repo),
			Workflows:   store.NewWorkflowExtStore(orm),
			Worktrees:   store.NewWorktreeExtStore(orm),
			ProjectRows: store.NewProjectExtStore(orm), TriggerEvents: store.NewTriggerEventExtStore(orm),
			Questions: store.NewQuestionExtStore(orm), Spawns: store.NewToolCallExtStore(orm), Selections: store.NewChatSelectionExtStore(orm), Messages: store.NewMessageExtStore(orm), ToolBlocks: store.NewMessageContentBlockExtStore(orm), Senders: store.NewThreadExtStore(orm),
			Mailbox:       agentmailbox.New(agentmailbox.Deps{Store: store.NewAgentMessageExtStore(orm), Announcer: chatupdates.NewFromORM(orm, repo, nil)}),
			AgentMessages: store.NewAgentMessageExtStore(orm),
		},
		threads: branchCopyThreads(repo),
	}
}

// requireBranchCopiedPlans checks the branch holds a copy of every plan and
// task of the source: new ids, the same titles, descriptions, complexity and
// positions, statuses reset to pending, metadata and assignee left behind,
// and each task's parent the copy of its source parent.
func requireBranchCopiedPlans(t *testing.T, env *storetest.Env, ctx context.Context, sourceChatID, branchChatID string) (plans, tasks int) {
	t.Helper()
	type taskShape struct {
		description, parentTitle string
		position                 int
	}
	shapes := func(chatID string) (map[string]*db.Plan, map[string]taskShape, map[string]*db.Task) {
		planList, err := env.Repo.ListPlansByChatID(ctx, chatID)
		require.NoError(t, err)
		byTitle := map[string]*db.Plan{}
		taskShapes := map[string]taskShape{}
		taskByTitle := map[string]*db.Task{}
		for _, p := range planList {
			byTitle[p.Title] = p
			list, err := env.Repo.ListTasksByPlan(ctx, p.ID)
			require.NoError(t, err)
			titleByID := map[string]string{}
			for _, task := range list {
				titleByID[task.ID] = task.Title
			}
			for _, task := range list {
				shape := taskShape{position: task.Position}
				if task.Description != nil {
					shape.description = *task.Description
				}
				if task.ParentTaskID != nil {
					shape.parentTitle = titleByID[*task.ParentTaskID]
					require.NotEmpty(t, shape.parentTitle, "task %q's parent is not in its plan", task.Title)
				}
				taskShapes[p.Title+"/"+task.Title] = shape
				taskByTitle[task.Title] = task
			}
		}
		return byTitle, taskShapes, taskByTitle
	}

	srcPlans, srcTasks, srcTaskRows := shapes(sourceChatID)
	gotPlans, gotTasks, gotTaskRows := shapes(branchChatID)
	require.Len(t, gotPlans, len(srcPlans))
	require.Equal(t, srcTasks, gotTasks, "the branch's task trees must be the source's")
	for title, src := range srcPlans {
		got := gotPlans[title]
		require.NotNil(t, got, "plan %q not copied", title)
		require.NotEqual(t, src.ID, got.ID)
		require.Equal(t, branchChatID, got.ThreadID, "copies belong to the branch's root thread")
		require.Equal(t, int32(db.PlanStatusPending), got.Status)
		require.Equal(t, src.Description, got.Description)
		require.Equal(t, src.Complexity, got.Complexity)
	}
	for title, got := range gotTaskRows {
		require.NotEqual(t, srcTaskRows[title].ID, got.ID)
		require.Equal(t, int32(db.TaskStatusPending), got.Status)
		require.Nil(t, got.Metadata)
		require.Nil(t, got.Assignee)
	}
	return len(gotPlans), len(gotTaskRows)
}

func branchOf(t *testing.T, svc *Service, ctx context.Context, src branchCopySource) string {
	t.Helper()
	resp, err := svc.BranchChat(ctx, connect.NewRequest(&reliantv1.BranchChatRequest{
		ChatId:    src.chatID,
		MessageId: src.messageID,
	}))
	require.NoError(t, err)
	return resp.Msg.Chat.Id
}

// TestBranchChat_CopyOfALargeChatCostsWhatASmallOneDoes branches a chat of
// one plan and two tasks and one of 20 plans and 600 tasks, and holds the
// two to the same number of statements: the copy is one INSERT … SELECT,
// where the row-by-row copy sent a statement per plan, per plan's task list
// and per task (prod: 157 per attempt for 20 plans and 116 tasks; here 700
// statements against 60). It also holds the large branch's copy to the
// source.
func TestBranchChat_CopyOfALargeChatCostsWhatASmallOneDoes(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	probe := env.Probe(t)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, storetest.SeedUserID)
	svc := branchCopyService(probe)

	small := seedBranchCopySource(t, env, ctx, 1, 2)
	large := seedBranchCopySource(t, env, ctx, 20, 30)

	// One branch first, so neither measured one pays a cold cache.
	branchOf(t, svc, ctx, small)

	probe.Reset()
	smallBranch := branchOf(t, svc, ctx, small)
	smallStatements, smallLevels := probe.Statements(), probe.Isolations()

	probe.Reset()
	start := time.Now()
	largeBranch := branchOf(t, svc, ctx, large)
	elapsed := time.Since(start)
	largeStatements := probe.Statements()
	t.Logf("BranchChat of 20 plans / 600 tasks: %d statements, %s (1 plan / 2 tasks: %d statements)", largeStatements, elapsed, smallStatements)

	require.Equal(t, smallStatements, largeStatements, "a branch's statement count must not grow with the source's plans and tasks")
	require.Equal(t, smallLevels, probe.Isolations())
	for _, level := range probe.Isolations() {
		require.Equal(t, "read committed", level)
	}
	require.Empty(t, probe.SQLStates(), "no statement of the branch may fail")

	plans, tasks := requireBranchCopiedPlans(t, env, ctx, small.chatID, smallBranch)
	require.Equal(t, [2]int{1, 2}, [2]int{plans, tasks})
	plans, tasks = requireBranchCopiedPlans(t, env, ctx, large.chatID, largeBranch)
	require.Equal(t, [2]int{20, 600}, [2]int{plans, tasks})
}

// TestBranchChat_WhileTheSourceIsWrittenCommitsFirstTime branches a chat
// while a writer does what a running workflow does to it: starts child
// workflows (each bumps the shared worktree's activity_generation), saves
// messages to its thread, emits user updates (the user's
// update_stream_counters row) and moves its tasks' status. Under SERIALIZABLE
// each of those, landing after the branch's snapshot, failed the branch with
// SQLSTATE 40001 and spent a retry; READ COMMITTED waits for the row lock
// instead, so no statement of any branch fails and each commits on its first
// attempt.
func TestBranchChat_WhileTheSourceIsWrittenCommitsFirstTime(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	probe := env.Probe(t)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, storetest.SeedUserID)
	svc := branchCopyService(probe)
	src := seedBranchCopySource(t, env, ctx, 5, 20)
	sourceTasks, err := env.SQL.QueryContext(ctx, `SELECT id FROM tasks WHERE plan_id LIKE $1 || '-plan-%'`, src.chatID)
	require.NoError(t, err)
	var taskIDs []string
	for sourceTasks.Next() {
		var id string
		require.NoError(t, sourceTasks.Scan(&id))
		taskIDs = append(taskIDs, id)
	}
	require.NoError(t, sourceTasks.Err())
	require.NoError(t, sourceTasks.Close())

	// A branch with nothing else running: how many transactions one branch
	// begins (the branch, and the activity update its commit emits).
	probe.Reset()
	quietBranch := branchOf(t, svc, ctx, src)
	quietBegins := len(probe.Isolations())
	require.NotZero(t, quietBegins)

	// The writer runs on the env's pool, not the probe's, so the probe sees
	// only the branches' statements.
	writerThreads := branchCopyThreads(env.Repo)
	stop := make(chan struct{})
	var rounds atomic.Int64
	var wg sync.WaitGroup
	writerErr := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			childID := uuid.NewString()
			parent := src.threadID
			if _, _, _, err := writerThreads.CreateWorkflowWithThread(ctx, threads.CreateWorkflowWithThreadOpts{
				Workflow: &db.Workflow{
					ID: childID, ParentID: &src.chatID, ChatID: src.chatID, WorkflowName: "builtin://agent",
					Thread: childID, Status: db.Pending(), CreatedAt: time.Now().UTC(),
				},
				ThreadID:     childID,
				ChatID:       src.chatID,
				ParentThread: &parent,
				Origin:       db.ThreadOriginSpawn,
			}); err != nil {
				writerErr <- fmt.Errorf("start child workflow: %w", err)
				return
			}
			if _, err := env.Repo.SaveMessageToThread(ctx, src.chatID, src.threadID, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT), fmt.Sprintf("step %d", i), &src.chatID, nil, nil); err != nil {
				writerErr <- fmt.Errorf("save message: %w", err)
				return
			}
			if err := env.Repo.CreateUserUpdate(ctx, &db.UserUpdate{
				UserID: storetest.SeedUserID, ChatID: &src.chatID, UpdateType: db.UserUpdateChatActivityChanged,
				EntityType: db.EntityTypeChat, EntityID: src.chatID, Data: []byte(`{}`),
			}); err != nil {
				writerErr <- fmt.Errorf("user update: %w", err)
				return
			}
			if _, err := env.SQL.ExecContext(ctx, `UPDATE tasks SET status = $2, updated_at = now() WHERE id = $1`,
				taskIDs[i%len(taskIDs)], int32(db.TaskStatusInProgress)); err != nil {
				writerErr <- fmt.Errorf("move task: %w", err)
				return
			}
			rounds.Add(1)
		}
	}()
	t.Cleanup(func() {
		select {
		case <-stop:
		default:
			close(stop)
		}
		wg.Wait()
	})
	// The branches start once the writer is writing.
	for rounds.Load() == 0 {
		select {
		case err := <-writerErr:
			t.Fatalf("writer: %v", err)
		default:
			time.Sleep(time.Millisecond)
		}
	}

	probe.Reset()
	const branches = 5
	var branchIDs []string
	for range branches {
		branchIDs = append(branchIDs, branchOf(t, svc, ctx, src))
	}
	close(stop)
	wg.Wait()
	select {
	case err := <-writerErr:
		t.Fatalf("writer: %v", err)
	default:
	}
	t.Logf("%d branches alongside %d writer rounds", branches, rounds.Load())

	require.Empty(t, probe.SQLStates(), "no statement of a branch of a chat being written may fail (40001 is a serialization failure)")
	levels := probe.Isolations()
	require.Len(t, levels, branches*quietBegins, "each branch begins what a quiet one does: no attempt was retried")
	for _, level := range levels {
		require.Equal(t, "read committed", level)
	}
	for _, id := range append(branchIDs, quietBranch) {
		plans, tasks := requireBranchCopiedPlansCount(t, env, ctx, id)
		require.Equal(t, [2]int{5, 100}, [2]int{plans, tasks})
	}
}

// requireBranchCopiedPlansCount counts a branch's plans and tasks. The source
// moves under the concurrent test, so its copies are counted, not compared.
func requireBranchCopiedPlansCount(t *testing.T, env *storetest.Env, ctx context.Context, branchChatID string) (plans, tasks int) {
	t.Helper()
	list, err := env.Repo.ListPlansByChatID(ctx, branchChatID)
	require.NoError(t, err)
	for _, p := range list {
		ts, err := env.Repo.ListTasksByPlan(ctx, p.ID)
		require.NoError(t, err)
		tasks += len(ts)
	}
	return len(list), tasks
}

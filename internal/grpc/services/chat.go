// Copyright (c) 2025 Reliant Labs
package services

import (
	"strings"
	"sync"

	"go.temporal.io/sdk/client"

	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/streaming"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/workflow"
)

// ChatService implements the ChatService RPC handlers
type ChatService struct {
	reliantv1connect.UnimplementedChatServiceHandler
	database   db.Repository
	tempClient client.Client
	// runs owns pause/resume. Handlers ask it for an outcome and render that;
	// they hold no PauseService handle, so the lifecycle machinery has exactly
	// one door. See internal/runs.
	runs         *runs.Service
	threads      *threads.Service
	taskQueue    string
	streamingHub streaming.StreamingHub
	// daemonRouter reaches the user's filesystem, which the api-server cannot
	// see itself. Used to probe a project for existing code when a chat opens
	// (see internal/launch/greenfield.go). Optional: nil means the probe is
	// skipped.
	daemonRouter toolexec.DaemonRouter
	discussLocks sync.Map // per-chat lock to prevent concurrent discuss calls

	// launcher owns the start path — see internal/launch. It is shared with the
	// worker, which cannot import this package, so every helper StartChat and
	// SendMessage need lives there and this service calls through.
	launcherOnce sync.Once
	launcherImpl *launch.Launcher
}

// launcher returns this service's start-path launcher, building it on first use.
//
// It is built lazily rather than in NewChatService because tests construct
// ChatService as a struct literal (`&ChatService{database: repo}`) to exercise
// one method, and a launcher that only existed when the constructor ran would
// make every one of those a nil dereference. The dependencies it takes are the
// service's own fields, so a lazily built launcher and an eagerly built one
// cannot disagree.
func (s *ChatService) launcher() *launch.Launcher {
	s.launcherOnce.Do(func() {
		s.launcherImpl = launch.NewLauncher(s.database, s.tempClient, s.runs, s.taskQueue, s.daemonRouter)
	})
	return s.launcherImpl
}

// NewChatService creates a new ChatService
func NewChatService(database db.Repository, tempClient client.Client, pauseService *workflow.PauseService, taskQueue string, hub streaming.StreamingHub, daemonRouter toolexec.DaemonRouter) *ChatService {
	if strings.TrimSpace(taskQueue) == "" {
		taskQueue = workflow.SharedTaskQueue
	}

	return &ChatService{
		database:   database,
		tempClient: tempClient,
		runs:       runs.NewService(database, tempClient, pauseService),
		threads: threads.NewService(database,
			threads.WithTemporalSignaler(tempClient),
			threads.WithToolCanceler(daemonRouter),
		),
		taskQueue:    taskQueue,
		streamingHub: hub,
		daemonRouter: daemonRouter,
	}
}

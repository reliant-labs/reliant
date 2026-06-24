// Copyright (c) 2025 Reliant Labs
package services

import (
	"strings"
	"sync"

	"go.temporal.io/sdk/client"

	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/streaming"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/workflow"
)

// ChatService implements the ChatService RPC handlers
type ChatService struct {
	reliantv1connect.UnimplementedChatServiceHandler
	database           db.Repository
	tempClient         client.Client
	pauseService       *workflow.PauseService
	threads            *threads.Service
	taskQueue          string
	streamingHub       streaming.StreamingHub
	controlPlaneClient controlplane.Client
	discussLocks       sync.Map // per-chat lock to prevent concurrent discuss calls
}

// NewChatService creates a new ChatService
func NewChatService(database db.Repository, tempClient client.Client, pauseService *workflow.PauseService, taskQueue string, hub streaming.StreamingHub) *ChatService {
	if strings.TrimSpace(taskQueue) == "" {
		taskQueue = workflow.SharedTaskQueue
	}

	return &ChatService{
		database:           database,
		tempClient:         tempClient,
		pauseService:       pauseService,
		threads:            threads.NewService(database),
		taskQueue:          taskQueue,
		streamingHub:       hub,
		controlPlaneClient: controlplane.NewClient(""),
	}
}

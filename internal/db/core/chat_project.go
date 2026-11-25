// Copyright (c) 2025 Reliant Labs
package core

import (
	"errors"
	"time"
)

// ChatProject is a project attached to a chat beside its primary project
// (Chat.ProjectID, which is never a ChatProject). The chat's tools reach the
// attached project's main checkout through their `project` argument.
type ChatProject struct {
	ChatID    string `json:"chat_id"`
	ProjectID string `json:"project_id"`
	// DaemonID is the machine the attachment was checked against: it held an
	// installed checkout of the project when the project was attached. nil
	// once that machine is deleted. Tool calls resolve the checkout on the
	// machine they run on, which today must be the chat's own.
	DaemonID  *string   `json:"daemon_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// MaxChatProjects is how many projects may be attached to one chat, beside
// its primary.
const MaxChatProjects = 10

// ErrTooManyChatProjects is an attach to a chat that has MaxChatProjects
// attached projects already.
var ErrTooManyChatProjects = errors.New("a chat can have at most 10 attached projects")

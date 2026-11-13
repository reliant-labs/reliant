package embedded

import "context"

// CommandPublisher publishes commands.
type CommandPublisher interface {
	PublishCommand(ctx context.Context, workspaceID string, cmd string) error
}

// EventPublisher publishes events.
type EventPublisher interface {
	PublishEvent(ctx context.Context, event string) error
}

// Service composes CommandPublisher and EventPublisher with its own methods.
type Service interface {
	CommandPublisher
	EventPublisher
	Close() error
}

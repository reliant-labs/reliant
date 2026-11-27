package threads

import (
	"context"
	"testing"
)

type suppliedAmbientMemory struct {
	called bool
}

func (m *suppliedAmbientMemory) ResolveThreadAmbientMemoryExclusion(_ context.Context, _, _ string, requested bool) (bool, error) {
	m.called = true
	return requested, nil
}

func TestNewRetainsAmbientMemoryWithoutRepo(t *testing.T) {
	memory := &suppliedAmbientMemory{}
	svc := New(Deps{Repo: nil, Chats: nil, Messages: nil, Senders: nil, Signaler: nil, ToolCanceler: nil, AmbientMemory: memory})
	if svc.ambientMemory != memory {
		t.Fatal("New replaced the supplied ambient-memory implementation")
	}
	excluded, err := svc.ambientMemory.ResolveThreadAmbientMemoryExclusion(context.Background(), "chat", "thread", true)
	if err != nil || !excluded || !memory.called {
		t.Fatalf("supplied ambient memory was not reached: excluded=%v called=%v err=%v", excluded, memory.called, err)
	}
}

func TestNewWithoutRepoLeavesAmbientMemoryUnset(t *testing.T) {
	svc := New(Deps{Repo: nil, Chats: nil, Messages: nil, Senders: nil, Signaler: nil, ToolCanceler: nil})
	if svc.ambientMemory != nil {
		t.Fatal("New derived ambient memory without a repository reader")
	}
}

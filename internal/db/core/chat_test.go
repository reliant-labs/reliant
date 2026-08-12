package core

import "testing"

func TestChat_MainThreadID(t *testing.T) {
	workflowID := "wf-123"
	empty := ""

	tests := []struct {
		name string
		chat Chat
		want string
	}{
		{
			name: "workflow id set",
			chat: Chat{WorkflowID: &workflowID},
			want: "wf-123",
		},
		{
			name: "nil pointer",
			chat: Chat{WorkflowID: nil},
			want: "",
		},
		{
			name: "empty string pointer",
			chat: Chat{WorkflowID: &empty},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.chat.MainThreadID(); got != tt.want {
				t.Fatalf("MainThreadID() = %q, want %q", got, tt.want)
			}
		})
	}
}

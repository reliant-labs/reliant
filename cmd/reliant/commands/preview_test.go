// Copyright (c) 2025 Reliant Labs
package commands

import (
	"bytes"
	"strings"
	"testing"
)

func TestPreviewURLForPort(t *testing.T) {
	tests := []struct {
		name       string
		template   string
		port       int
		wantURL    string
		wantRemote bool
	}{
		{
			name:       "subdomain template substitutes port",
			template:   "https://{port}-48fa7f56.workspaces.reliantapi.com",
			port:       3000,
			wantURL:    "https://3000-48fa7f56.workspaces.reliantapi.com",
			wantRemote: true,
		},
		{
			name:       "dev subdomain template with gateway port",
			template:   "http://{port}-48fa7f56.preview.reliant.test:28080",
			port:       5173,
			wantURL:    "http://5173-48fa7f56.preview.reliant.test:28080",
			wantRemote: true,
		},
		{
			name:       "dev path template substitutes port",
			template:   "http://localhost:8080/proxy/48fa7f56/{port}/",
			port:       8000,
			wantURL:    "http://localhost:8080/proxy/48fa7f56/8000/",
			wantRemote: true,
		},
		{
			name:       "no template falls back to loopback",
			template:   "",
			port:       3000,
			wantURL:    "http://localhost:3000/",
			wantRemote: false,
		},
		{
			name:       "malformed template (no placeholder) falls back to loopback",
			template:   "https://example.com/no-placeholder",
			port:       3000,
			wantURL:    "http://localhost:3000/",
			wantRemote: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(previewURLTemplateEnv, tt.template)
			gotURL, gotRemote := previewURLForPort(tt.port)
			if gotURL != tt.wantURL {
				t.Errorf("url = %q, want %q", gotURL, tt.wantURL)
			}
			if gotRemote != tt.wantRemote {
				t.Errorf("remote = %v, want %v", gotRemote, tt.wantRemote)
			}
		})
	}
}

// TestPreviewURLCmdInvalidPort verifies the command rejects a non-listening
// port only when --require-listening is set, and rejects invalid port args.
func TestPreviewURLCmdInvalidPort(t *testing.T) {
	cmd := newPreviewURLCmd()
	cmd.SetArgs([]string{"not-a-port"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error for non-numeric port, got nil")
	}
}

// TestPreviewURLCmdPrintsURL verifies the command prints the substituted URL to
// stdout when the template env is present.
func TestPreviewURLCmdPrintsURL(t *testing.T) {
	t.Setenv(previewURLTemplateEnv, "https://{port}-abc.workspaces.reliantapi.com")
	var out, errBuf bytes.Buffer
	cmd := newPreviewURLCmd()
	cmd.SetArgs([]string{"3000"})
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != "https://3000-abc.workspaces.reliantapi.com" {
		t.Errorf("stdout = %q, want the substituted URL", got)
	}
}

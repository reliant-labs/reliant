package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Handler lines name the service, list only rpc handlers, and diff against
// the declared rpcs. On reliant `--grep ChatService` dropped 11 of 31 handlers
// (their lines carried only the bare method name), and `--kind handler`
// listed every *Service method — helpers, Name, Register — as a handler.
func handlerShapesFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range map[string]string{
		"proto/reliant/v1/chat.proto": `syntax = "proto3";
service ChatService {
  rpc SendMessage(A) returns (B);
  rpc ListMessages(A) returns (B);
  rpc ArchiveChat(A) returns (B);
  // A streaming rpc is an rpc: its handler is no "no declared service" method.
  rpc Watch(A) returns (stream B) {}
}
`,
		"proto/reliant/v1/legacy.proto": "syntax = \"proto3\";\n// forge:unmanaged\nservice LegacyService {\n  rpc Ping(A) returns (B);\n}\n",
		"internal/handlers/chat/service.go": `package chat

type Service struct{}

func (s *Service) Name() string { return "chat" }

func (s *Service) Register() {}

func (s *Service) buildPage() int { return 0 }
`,
		// A receiver not named s.
		"internal/handlers/chat/send.go":              "package chat\n\nfunc (svc *Service) SendMessage() error { return nil }\n\nfunc (svc *Service) Watch() error { return nil }\n",
		"internal/handlers/chat/rpc_list_messages.go": "package chat\n\n// forge:gen unwired-stub symbol=chat.ListMessages\nfunc (s *Service) ListMessages() error { return nil }\n",
		// A handler package no declared service resolves to.
		"internal/handlers/orphan/service.go": "package orphan\n\ntype Service struct{}\n\nfunc (s *Service) Exported() {}\n\nfunc (s *Service) hidden() {}\n",
	} {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)
	return dir
}

func TestCollectShapes_HandlersAreNamedByServiceAndAreRPCsOnly(t *testing.T) {
	dir := handlerShapesFixture(t)
	handlers := map[string]string{}
	for _, s := range collectShapes(dir) {
		if s.Kind == "handler" {
			handlers[s.Name] = s.Detail
		}
	}
	want := map[string]string{
		"ChatService.SendMessage":  "",
		"ChatService.ListMessages": "unwired-stub",
		"ChatService.Watch":        "",
		"Exported":                 "no declared service",
	}
	if len(handlers) != len(want) {
		t.Errorf("handlers = %v\nwant       %v", handlers, want)
	}
	for name, detail := range want {
		if got, ok := handlers[name]; !ok || got != detail {
			t.Errorf("handler %s: got %q (present=%v), want %q", name, got, ok, detail)
		}
	}
}

func TestShapes_ServiceFilterAndUnimplemented(t *testing.T) {
	dir := handlerShapesFixture(t)
	shapes := collectShapes(dir)

	var names []string
	for _, s := range shapesOfService(shapes, "Chat") {
		names = append(names, s.Kind+":"+s.Name)
	}
	got := strings.Join(names, ",")
	for _, w := range []string{"rpc:ChatService.SendMessage", "rpc:ChatService.ArchiveChat", "handler:ChatService.SendMessage"} {
		if !strings.Contains(got, w) {
			t.Errorf("--service Chat is missing %s: %s", w, got)
		}
	}
	if strings.Contains(got, "Legacy") || strings.Contains(got, "Exported") {
		t.Errorf("--service Chat kept another service's shapes: %s", got)
	}

	var b strings.Builder
	if err := printUnimplemented(&b, shapes); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, w := range []string{"ChatService.ArchiveChat", "no handler method", "ChatService.ListMessages", "unwired stub at", "2 declared rpc(s) without a handler"} {
		if !strings.Contains(out, w) {
			t.Errorf("--unimplemented does not say %q:\n%s", w, out)
		}
	}
	if strings.Contains(out, "SendMessage") || strings.Contains(out, "LegacyService") {
		t.Errorf("--unimplemented listed an implemented or unmanaged rpc:\n%s", out)
	}
}

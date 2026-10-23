//go:build e2e

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestE2EDigitNamedServicesAndEntitiesBuild scaffolds services and entities
// whose names protoc-gen-go, protoc-gen-connect-go and protoc-gen-es re-case
// — a lowercase letter after a digit, an underscore before one, a leading
// acronym — and requires the whole project to build, vet, pass its own
// tests against real postgres, regenerate idempotently, and type-check its
// frontend.
//
// Every one of these spellings broke a scaffold before forge derived the
// identifiers it shares with the generators by THEIR rules
// (internal/naming/generated.go):
//
//   - service alphav1connect: forge spelled
//     UnimplementedAlphav1connectServiceHandler; connect-go declares
//     UnimplementedAlphav1ConnectServiceHandler.
//   - entity Base64Item (field base64item): the Update op dereferenced
//     req.Base64item; protoc-gen-go declares Base64Item.
//   - entity Oauth2token: every pb type, RPC method and response field
//     (pb.CreateOauth2tokenRequest, …) was spelled from the proto name;
//     protoc-gen-go declares CreateOauth2TokenRequest.
//   - fields sha256sum / address_line_2: one conversion spelled both structs
//     one way; the pb message has Sha256Sum / AddressLine_2, forge's db row
//     Sha256sum / AddressLine2.
//   - rpc GetOauth2tokenStats: the handler stub must declare
//     GetOauth2TokenStats, and the next generate must see it as implemented.
//   - rpc LLMChat: connect-es names the client method lLMChat, not llmChat.
//
// node/npm are required via requireTool (skip on a laptop, FAIL in CI) for
// the frontend half, mirroring TestE2EScaffoldFrontendBuilds.
func TestE2EDigitNamedServicesAndEntitiesBuild(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	requireTool(t, "node", "npm")
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "digitapp",
		"--mod", "example.com/digitapp",
		"--service", "alphav1connect,oauth2,base64",
		"--frontend", "web",
	)
	projectDir := filepath.Join(dir, "digitapp")
	addCorpusForgePkgReplace(t, projectDir)

	appendProto := func(svc, rpcs, messages string) {
		t.Helper()
		path := filepath.Join(projectDir, "proto", "services", svc, "v1", svc+".proto")
		proto := readFileE2E(t, path)
		if rpcs != "" {
			const anchor = "  // TODO: Add your RPC methods here.\n"
			if !strings.Contains(proto, anchor) {
				t.Fatalf("%s: scaffolded service block has no %q to add rpcs at:\n%s", path, anchor, proto)
			}
			proto = strings.Replace(proto, anchor, anchor+rpcs, 1)
		}
		if err := os.WriteFile(path, []byte(proto+messages), 0o644); err != nil {
			t.Fatalf("author %s: %v", path, err)
		}
	}
	appendProto("base64", "", `
// forge:entity
message Base64Item {
  string id = 1;
  string name = 2;
  string sha256sum = 3;
  string address_line_2 = 4;
}
`)
	appendProto("oauth2", "  rpc GetOauth2tokenStats(GetOauth2tokenStatsRequest) returns (GetOauth2tokenStatsResponse) {}\n", `
// forge:entity
message Oauth2token {
  string id = 1;
  string label = 2;
}

message GetOauth2tokenStatsRequest {}
message GetOauth2tokenStatsResponse {
  int64 x509_count = 1;
}
`)
	appendProto("alphav1connect", "  rpc LLMChat(LLMChatRequest) returns (LLMChatResponse) {}\n", `
message LLMChatRequest {
  string prompt = 1;
}
message LLMChatResponse {
  string reply = 1;
}
`)

	runCmd(t, projectDir, forgeBin, "scaffold")

	// ── the identifiers the generators declare, spelled their way ──
	read := func(rel ...string) string {
		return readFileE2E(t, filepath.Join(append([]string{projectDir}, rel...)...))
	}
	expect := func(file, content string, wants ...string) {
		t.Helper()
		for _, w := range wants {
			if !strings.Contains(content, w) {
				t.Errorf("%s should contain %q; got:\n%s", file, w, content)
			}
		}
	}
	expect("alphav1connect/service.go", read("internal", "handlers", "alphav1connect", "service.go"),
		"alphav1connectv1connect.UnimplementedAlphav1ConnectServiceHandler",
		"alphav1connectv1connect.NewAlphav1ConnectServiceHandler(")
	expect("base64/handlers_crud_ops_gen.go", read("internal", "handlers", "base64", "handlers_crud_ops_gen.go"),
		"if req.Base64Item == nil",
		"m.Sha256Sum = e.Sha256sum",
		"m.AddressLine_2 = e.AddressLine2")
	expect("oauth2/handlers_crud_ops_gen.go", read("internal", "handlers", "oauth2", "handlers_crud_ops_gen.go"),
		"crud.UpdateOp[pb.UpdateOauth2TokenRequest, pb.UpdateOauth2TokenResponse, *db.Oauth2token]",
		"if req.Oauth2Token == nil")
	expect("oauth2/rpc_get_oauth2token_stats.go", read("internal", "handlers", "oauth2", "rpc_get_oauth2token_stats.go"),
		"func (s *Service) GetOauth2TokenStats(",
		"*connect.Request[pb.GetOauth2TokenStatsRequest]")

	// ── generate x2: green and byte-for-byte idempotent ──
	snaps := make([]map[string]string, 0, 2)
	for i := 0; i < 2; i++ {
		runCmd(t, projectDir, forgeBin, "generate")
		snaps = append(snaps, hashProjectTree(t, projectDir))
		runCmd(t, projectDir, "go", "build", "./...")
		runCmd(t, projectDir, "go", "vet", "./...")
	}
	if diff := diffTreeE2E(snaps[0], snaps[1]); diff != "" {
		t.Errorf("generate #2 is not idempotent vs #1 (file churn — e.g. a stub re-scaffolded under a second name):\n%s", diff)
	}

	// ── runtime: the born CRUD lifecycle tests, against real postgres ──
	runCmd(t, projectDir, "go", "test", "./internal/handlers/...")

	// ── frontend: the TS the generated pages and hooks reference ──
	webDir := filepath.Join(projectDir, "frontends", "web")
	expect("alphav1connect hooks", read("frontends", "web", "src", "hooks", "alphav1connect-service-hooks_gen.ts"),
		"client.lLMChat(req)")
	prebuildWebRuntimeE2E(t)
	runCmdTimeout(t, webDir, 5*time.Minute,
		"npm", "install", "--no-audit", "--no-fund", "--prefer-offline")
	runCmdTimeout(t, webDir, 3*time.Minute,
		"npx", "--no-install", "tsc", "--noEmit")
}

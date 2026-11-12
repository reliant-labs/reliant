package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

func TestMissingStubsReconcilesEquivalentProtoServiceSpelling(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "internal", "handlers", "api_key")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := `package api_key
import wire "example.com/app/gen/services/api_key/v1/api_keyv1connect"
// User-owned comment must stay byte-for-byte.
type Service struct { wire.UnimplementedAPIKeyServiceHandler; custom string }
func (s *Service) Register() { wire.NewAPIKeyServiceHandler(s) }
var route = wire.APIKeyServiceName
var unrelated = other.NewAPIKeyServiceHandler
`
	path := filepath.Join(dir, "service.go")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := ServiceDef{Name: "ApiKeyService", GoPackage: "example.com/app/gen/services/api_key/v1", PkgName: "api_keyv1"}
	if _, err := GenerateMissingHandlerStubs(svc, root, dir, nil, nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.ReplaceAll(src, "wire.UnimplementedAPIKeyServiceHandler", "wire.UnimplementedApiKeyServiceHandler")
	want = strings.ReplaceAll(want, "wire.NewAPIKeyServiceHandler", "wire.NewApiKeyServiceHandler")
	want = strings.ReplaceAll(want, "wire.APIKeyServiceName", "wire.ApiKeyServiceName")
	if string(got) != want {
		t.Fatalf("wire selectors must follow proto without rewriting user content:\n%s", got)
	}
	if _, err := GenerateMissingHandlerStubs(svc, root, dir, nil, nil); err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(path)
	if string(again) != want {
		t.Fatal("second reconciliation was not idempotent")
	}

	// Disowning is an absolute stop, even for wire selector reconciliation.
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cs := &checksums.FileChecksums{Disowned: map[string]checksums.DisownedEntry{"internal/handlers/api_key/service.go": {}}}
	if _, err := GenerateMissingHandlerStubs(svc, root, dir, nil, cs); err != nil {
		t.Fatal(err)
	}
	untouched, _ := os.ReadFile(path)
	if string(untouched) != src {
		t.Fatal("disowned service.go was changed")
	}
}

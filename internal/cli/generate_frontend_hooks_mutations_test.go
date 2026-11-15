package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
)

// TestGenerateFrontendHooks_MutationsMirrorTheHooks pins `<service>Mutations`,
// the writes for code outside React: one entry per mutation hook and none for
// a read, each invalidating exactly the key its hook invalidates (the entity
// scope for a CRUD write, the whole service for a bespoke one). A store that
// calls the helper must refresh the same reads the hook would; a helper whose
// key drifted from the hook's would leave them stale with nothing failing.
func TestGenerateFrontendHooks_MutationsMirrorTheHooks(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.ProjectConfig{
		Name:      "myapp",
		Frontends: []config.FrontendConfig{config.FrontendConfig{Name: "web", Type: "nextjs"}.WithDir("frontends/web")},
	}
	method := func(name string) codegen.Method {
		return codegen.Method{Name: name, InputType: name + "Request", OutputType: name + "Response"}
	}
	services := []codegen.ServiceDef{
		{
			Name:      "TaskService",
			ProtoFile: "proto/services/tasks/v1/tasks.proto",
			Methods: []codegen.Method{
				method("ListTasks"), method("GetTask"), method("CreateTask"),
				method("UpdateTask"), method("ArchiveEverything"),
			},
		},
		{
			Name:      "ReportService",
			ProtoFile: "proto/services/reports/v1/reports.proto",
			Methods:   []codegen.Method{method("GetReport"), method("ListReports")},
		},
	}
	if err := generateFrontendHooks(cfg, services, dir, nil); err != nil {
		t.Fatalf("generateFrontendHooks: %v", err)
	}
	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, "frontends", "web", "src", "hooks", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	s := read("task-service-hooks_gen.ts")
	hookKeys := map[string]string{}
	for _, m := range regexp.MustCompile(`export const use(\w+) = createMutationHook<[^>]*>\(\s*(taskServiceKeys\.\w+),`).FindAllStringSubmatch(s, -1) {
		hookKeys[m[1]] = m[2]
	}
	callKeys := map[string]string{}
	for _, m := range regexp.MustCompile(`\n  (\w+): createMutationCall<[^>]*>\(\s*(taskServiceKeys\.\w+),\s*\(req, signal\) => client\.(\w+)\(req, \{ signal \}\),`).FindAllStringSubmatch(s, -1) {
		if m[1] != m[3] {
			t.Errorf("%s calls client.%s", m[1], m[3])
		}
		callKeys[m[1]] = m[2]
	}
	want := map[string]string{
		"createTask":        "taskServiceKeys.task",
		"updateTask":        "taskServiceKeys.task",
		"archiveEverything": "taskServiceKeys.all",
	}
	if len(callKeys) != len(want) {
		t.Errorf("taskServiceMutations entries = %v, want one per mutation %v:\n%s", callKeys, want, s)
	}
	for name, key := range want {
		hookName := strings.ToUpper(name[:1]) + name[1:]
		if callKeys[name] != key || hookKeys[hookName] != key {
			t.Errorf("%s: helper invalidates %q, hook invalidates %q, want both %q", name, callKeys[name], hookKeys[hookName], key)
		}
	}
	for _, wantLine := range []string{
		"export const taskServiceMutations = createServiceMutations({",
		"  createMutationCall,\n",
		"  createServiceMutations,\n",
	} {
		if !strings.Contains(s, wantLine) {
			t.Errorf("missing %q:\n%s", wantLine, s)
		}
	}

	// A read-only service has no writes, so it gets no table and imports
	// neither factory (an unused import fails the project's eslint).
	r := read("report-service-hooks_gen.ts")
	for _, absent := range []string{"reportServiceMutations", "createMutationCall", "createServiceMutations"} {
		if strings.Contains(r, absent) {
			t.Errorf("read-only service emits %q:\n%s", absent, r)
		}
	}
}

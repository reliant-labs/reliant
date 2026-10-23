package generator

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"

	"github.com/reliant-labs/forge/internal/configproto"
	"github.com/reliant-labs/forge/internal/naming"
	"github.com/reliant-labs/forge/internal/templates"
)

// frontendConfigEnvVars duplicates a fact the template already states: the
// env_var of every field the scaffolded frontend config declares. The
// duplication is deliberate — the scaffold paths need the set BEFORE any
// proto has been compiled, so it cannot be read back from a descriptor —
// but a duplicated fact drifts unless something compares the two copies.
//
// This is that comparison. A field added to the template without adding it
// here would generate into config_gen.ts and stay unreadable by the
// templates; a name removed from the template but left here would render a
// frontend reading a key its config does not carry, which does not
// type-check.
func TestFrontendConfigTemplateMatchesDeclaredEnvVars(t *testing.T) {
	src, err := templates.ProjectTemplates().Render("frontend_config.proto.tmpl", frontendConfigProtoData{
		TemplateData: configproto.For("demo").TemplateData("example.com/demo"),
		Frontend:     "web",
		MessageName:  "WebConfig",
		APIPort:      8080,
	})
	if err != nil {
		t.Fatalf("render frontend_config.proto.tmpl: %v", err)
	}

	// Only FIELD declarations count. The template's prose mentions env var
	// names (NEXT_PUBLIC_BASE_PATH, deliberately excluded) that must not be
	// read as declarations, so the pattern matches the option block.
	re := regexp.MustCompile(`env_var:\s*"([A-Z0-9_]+)"`)
	var inTemplate []string
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		inTemplate = append(inTemplate, m[1])
	}
	if len(inTemplate) == 0 {
		t.Fatal("derived NO env_var from frontend_config.proto.tmpl — the pattern matched nothing, " +
			"so this test would pass vacuously; fix the pattern before trusting a green run")
	}

	got := append([]string(nil), inTemplate...)
	want := append([]string(nil), frontendConfigEnvVars...)
	sort.Strings(got)
	sort.Strings(want)

	if len(got) != len(want) {
		t.Fatalf("frontend_config.proto.tmpl declares %v; frontendConfigEnvVars lists %v — "+
			"the scaffold paths build their typed-config presence set from the Go list, so the two must agree",
			got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("template declares %q where frontendConfigEnvVars has %q", got[i], want[i])
		}
	}
}

// The presence set the scaffold hands the frontend templates must actually
// turn the typed-module branch on. A Bound=false value would render the
// build-time env-var form against a project that DOES declare a config —
// the exact split this change exists to close.
func TestScaffoldedFrontendTypedConfigEnablesTheTypedBranch(t *testing.T) {
	tc := ScaffoldedFrontendTypedConfig()
	if !tc.Bound {
		t.Fatal("ScaffoldedFrontendTypedConfig() is not Bound — every frontend template would render " +
			"its process.env form even though the scaffold declares a config message")
	}
	// This gates the one read site in session-provider.ts. A false here
	// silently degrades that read to `undefined` rather than failing.
	if !tc.HasMockAPI {
		t.Error("HasMockAPI is false — the mock auth provider could not be selected from config")
	}
}

// bufFileLowerSnakeCase is buf's STANDARD FILE_LOWER_SNAKE_CASE rule: a
// proto file's base name must be lower_snake_case. A hyphen fails it.
var bufFileLowerSnakeCase = regexp.MustCompile(`^[a-z][a-z0-9_]*\.proto$`)

// Every name forge accepts for a frontend must produce a proto file buf
// will lint. `forge scaffold frontend` permits hyphens (ValidateFrontendName
// rejects only a LEADING one), and hyphenated frontend names are ordinary —
// so the path builder has to fold them, not pass them through.
//
// A hyphen here is not a cosmetic lint failure. `buf lint` runs in the
// generated CI workflow, so a project that scaffolds `internal-console`
// gets a red build on its first push, and the only fix is renaming a
// frontend or hand-editing a forge-owned path.
func TestFrontendConfigProtoRelPathIsAlwaysLowerSnakeCase(t *testing.T) {
	for _, frontend := range []string{
		"web",
		"admin",
		"internal-console",
		"settings-web",
		"AdminPortal",
		"admin_portal",
	} {
		got := FrontendConfigProtoRelPath(configproto.For("demo"), frontend)
		base := filepath.Base(got)
		if !bufFileLowerSnakeCase.MatchString(base) {
			t.Errorf("FrontendConfigProtoRelPath(%q) = %q; base %q is not lower_snake_case.proto — "+
				"buf's STANDARD lint rejects it with `Filename %q should be lower_snake_case.proto`",
				frontend, got, base, base)
		}
	}
}

// The proto file name, the proto MESSAGE name, and the KCL identifier are
// three projections of one frontend name, emitted by three different
// generators that never see each other's output. They have to agree for a
// hyphenated name exactly as they already do for a single-word one.
func TestFrontendConfigNamesAgreeForHyphenatedFrontend(t *testing.T) {
	if got, want := filepath.Base(FrontendConfigProtoRelPath(configproto.For("demo"), "internal-console")), "internal_console_config.proto"; got != want {
		t.Errorf("proto file = %q, want %q", got, want)
	}
	if got, want := FrontendConfigMessageName("internal-console"), "InternalConsoleConfig"; got != want {
		t.Errorf("message name = %q, want %q", got, want)
	}
	if got, want := naming.KCLIdentifier("internal-console"), "internal_console"; got != want {
		t.Errorf("KCL identifier = %q, want %q", got, want)
	}
}

// WriteFrontendConfigProto must not clobber a config a project already has.
// The values in one (an issuer, a client id registered with an IdP) are not
// reconstructible from the scaffold, so overwriting is data loss.
func TestWriteFrontendConfigProtoLeavesExistingFileAlone(t *testing.T) {
	root := t.TempDir()
	rel := FrontendConfigProtoRelPath(configproto.For("demo"), "web")

	if err := WriteFrontendConfigProto(root, configproto.For("demo"), "example.com/demo", "web", 8080); err != nil {
		t.Fatalf("first write: %v", err)
	}
	first := readFile(t, filepath.Join(root, rel))

	// Simulate a user edit, then re-scaffold.
	edited := first + "\n// a user's own note\n"
	if err := os.WriteFile(filepath.Join(root, rel), []byte(edited), 0o644); err != nil {
		t.Fatalf("simulate user edit: %v", err)
	}

	if err := WriteFrontendConfigProto(root, configproto.For("demo"), "example.com/demo", "web", 8080); err != nil {
		t.Fatalf("second write: %v", err)
	}
	if got := readFile(t, filepath.Join(root, rel)); got != edited {
		t.Error("WriteFrontendConfigProto overwrote an existing config proto — a project's issuer and " +
			"client id are not reconstructible from the scaffold, so this is data loss")
	}
}

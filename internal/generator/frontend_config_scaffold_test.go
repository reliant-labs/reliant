package generator

import (
	"github.com/reliant-labs/forge/internal/configproto"

	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The frontend config system — the typed zod module (src/lib/config_gen.ts),
// the KCL schema (deploy/kcl/frontend_config_gen.k) and the per-env
// config.js assigning window.__FORGE_CONFIG__ — is keyed entirely on a
// message-level (forge.v1.frontend_config) annotation. All three generators
// are correct: given an annotated message they emit all three artifacts.
//
// Given NO annotated message they correctly emit NOTHING, and that is the
// state every scaffolded project shipped in: `forge project new X --frontend
// web` wrote a config proto declaring only AppConfig, so a fresh project got
// none of the system and its frontend kept reading build-time
// process.env.NEXT_PUBLIC_* — the exact shape the config system exists to
// replace, and one that cannot be promoted between environments because a
// bundler inlines it.
//
// Every prior verification of the config system passed because it HAND-WROTE
// the annotation. Nobody checked the scaffold default, so the feature was
// complete and unreachable at the same time. These tests check the scaffold
// default: they assert the annotation is DECLARED by scaffolding, which is
// what makes the rest of the system run without a hand edit.

// frontendConfigMessageRE finds the frontend-bound config message and
// captures the frontend name the annotation binds it to. Matching the
// ANNOTATION rather than a message name is deliberate: the binding is by
// annotation everywhere else in forge, so renaming the message must not
// break this test and must not silently re-point the config.
var frontendConfigMessageRE = regexp.MustCompile(
	`option\s*\(forge\.v1\.frontend_config\)\s*=\s*\{\s*frontend:\s*"([^"]+)"\s*\}`)

// TestScaffold_FrontendGetsAnnotatedConfigMessage is the gate. A project
// scaffolded WITH a frontend must declare that frontend's config message,
// carrying the annotation that activates the three projections.
func TestScaffold_FrontendGetsAnnotatedConfigMessage(t *testing.T) {
	if testing.Short() {
		t.Skip("scaffolds and generates a full project; runs in task test")
	}
	root := scaffoldWithFrontend(t, "cfgapp", "web")

	proto, path := readFrontendConfigProto(t, root, "web")

	m := frontendConfigMessageRE.FindStringSubmatch(proto)
	if m == nil {
		t.Fatalf("%s declares no (forge.v1.frontend_config) annotation — the scaffolded frontend "+
			"gets NO typed config module, NO KCL schema and NO runtime config.js, and its "+
			"templates keep reading build-time process.env.NEXT_PUBLIC_*", path)
	}
	if m[1] != "web" {
		t.Errorf("frontend config is bound to frontend %q, but the scaffolded frontend is %q — "+
			"generate refuses a config naming a frontend forge.yaml does not declare", m[1], "web")
	}
}

// TestScaffold_FrontendConfigDeclaresTheFieldsItsTemplatesRead is the
// substance behind the annotation. An annotated but empty message would
// satisfy the test above while leaving every read site undefined.
//
// The expected set is derived from what the scaffolded frontend templates
// actually read, not from a list someone remembered: each entry below is a
// NEXT_PUBLIC_* variable that appears in the shipped template tree and has a
// runtime meaning.
func TestScaffold_FrontendConfigDeclaresTheFieldsItsTemplatesRead(t *testing.T) {
	root := scaffoldWithFrontend(t, "fieldapp", "web")
	proto, path := readFrontendConfigProto(t, root, "web")

	// The env_var spelling is the ONE name shared by the proto, the KCL
	// projection, the runtime document and the generated TS module — so
	// asserting on it checks the whole chain agrees, not just that a
	// proto field exists.
	want := []string{
		"API_URL",
		"MOCK_API",
		"OTEL_ENDPOINT",
		"APP_VERSION",
	}
	for _, env := range want {
		if !strings.Contains(proto, `env_var: "`+env+`"`) {
			t.Errorf("%s declares no field with env_var %q — the scaffolded frontend reads it, "+
				"so the generated config module will not carry it", path, env)
		}
	}

	// The other half of the same statement: no OIDC value may reach a
	// browser. Sign-in is NATIVE — credentials go to this app's own API and
	// the server runs the whole OIDC flow — so the issuer, client id,
	// redirect URI, scopes and resource indicator are BACKEND config. They
	// are asserted absent rather than merely left out of `want` because a
	// value delivered to the bundle is a value some later code can use to
	// talk to the issuer directly, which is the browser-side flow this
	// design replaced.
	for _, env := range []string{
		"OIDC_ISSUER",
		"OIDC_CLIENT_ID",
		"OIDC_REDIRECT_URI",
		"OIDC_SCOPES",
		"OIDC_RESOURCE",
	} {
		if strings.Contains(proto, `env_var: "`+env+`"`) {
			t.Errorf("%s declares env_var %q — that value would be delivered to the browser in "+
				"config.js, but native sign-in means the browser never contacts the issuer; "+
				"OIDC settings belong on the backend's config message", path, env)
		}
	}
}

// TestScaffold_FrontendConfigOmitsBasePath pins a deliberate EXCLUSION, and
// pins it here because the omission looks like an oversight to anyone
// auditing the list above.
//
// NEXT_PUBLIC_BASE_PATH is genuinely build-time. next.config.ts reads it to
// place every emitted asset, so the prefix is baked into file paths a build
// has already written. A runtime document delivered to the browser cannot
// move files on disk — declaring it as runtime config would produce a value
// the app reads and cannot honor, which is worse than not having it.
func TestScaffold_FrontendConfigOmitsBasePath(t *testing.T) {
	root := scaffoldWithFrontend(t, "bpapp", "web")
	proto, path := readFrontendConfigProto(t, root, "web")

	if strings.Contains(proto, `env_var: "BASE_PATH"`) {
		t.Errorf("%s declares BASE_PATH as runtime config, but next.config.ts consumes it at BUILD "+
			"time to place emitted assets — a runtime document cannot relocate files a build "+
			"already wrote", path)
	}
}

// TestScaffold_FrontendReadsTypedModuleNotProcessEnv is the user-visible
// half. Declaring the config is pointless if the scaffolded frontend still
// reads process.env: the annotation would generate artifacts nothing loads.
//
// session-provider.ts is the file that carries the read: it selects mock
// vs. real-backend mode from MOCK_API, and a build-time-inlined value there
// splits that decision across two sources — the app in mock mode while this
// file believes it is driving a real backend, so scaffolded UI renders
// signed out.
//
// The auth modules named here are the ones the NATIVE flow ships.
// oidc-provider.ts is deliberately not among them: browser-side OIDC was
// deleted, and internal/templates/frontend_auth_flow_test.go asserts it
// stays deleted.
func TestScaffold_FrontendReadsTypedModuleNotProcessEnv(t *testing.T) {
	root := scaffoldWithFrontend(t, "typedapp", "web")

	rel := filepath.Join("frontends", "web", "src", "lib", "auth", "session-provider.ts")
	body := readFile(t, filepath.Join(root, rel))

	if !strings.Contains(body, `from "@/lib/config_gen"`) {
		t.Errorf("%s does not import the generated typed config module — it was scaffolded in its "+
			"build-time env-var form, so the config system's values never reach auth", rel)
	}
	if n := strings.Count(body, "process.env.NEXT_PUBLIC_MOCK_API"); n > 0 {
		t.Errorf("%s still reads process.env.NEXT_PUBLIC_MOCK_API in %d place(s) — that is inlined "+
			"at build time, so the bundle cannot be promoted between environments", rel, n)
	}

	// The two modules the native flow actually ships must exist, or the
	// assertion above could pass against a tree that has no sign-in at all.
	for _, name := range []string{"native-login.ts", "context.tsx"} {
		p := filepath.Join("frontends", "web", "src", "lib", "auth", name)
		if _, err := os.Stat(filepath.Join(root, p)); err != nil {
			t.Errorf("%s was not scaffolded (%v) — the native sign-in flow POSTs credentials to "+
				"this app's own API through it, so a tree without it cannot sign anyone in", p, err)
		}
	}

	// No OIDC values reach the browser at all. Grepping the whole auth
	// directory rather than one file is what makes this a statement about
	// the SHIPPED tree instead of about a file someone remembered to check.
	authDir := filepath.Join(root, "frontends", "web", "src", "lib", "auth")
	entries, err := os.ReadDir(authDir)
	if err != nil {
		t.Fatalf("read scaffolded auth dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("%s is empty — the scan below would report green having read nothing", authDir)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		src := readFile(t, filepath.Join(authDir, e.Name()))
		for _, key := range []string{"OIDC_ISSUER", "OIDC_CLIENT_ID", "OIDC_REDIRECT_URI", "OIDC_SCOPES", "OIDC_RESOURCE"} {
			if strings.Contains(src, key) {
				t.Errorf("src/lib/auth/%s references %s — the browser is not supposed to know the "+
					"issuer exists; handing it one is how a script-readable token comes back",
					e.Name(), key)
			}
		}
	}
}

// scaffoldWithFrontend generates a project that has a frontend, which is the
// configuration under test: the gap only exists on the frontend path.
func scaffoldWithFrontend(t *testing.T, name, frontend string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	g := NewProjectGenerator(name, root, "example.com/"+name)
	g.FrontendName = frontend
	if err := g.Generate(); err != nil {
		t.Fatalf("Generate(): %v", err)
	}
	return root
}

// readFrontendConfigProto returns the proto file declaring the frontend's
// config, and its project-relative path for failure messages.
//
// It looks for the per-frontend file FIRST and falls back to the shared
// config.proto, so this test pins the ANNOTATION being scaffolded rather
// than the file layout carrying it — a later layout change stays free.
func readFrontendConfigProto(t *testing.T, root, frontend string) (body, rel string) {
	t.Helper()
	candidates := []string{
		filepath.Join(filepath.FromSlash(configproto.ForProject(root).Dir()), frontend+"_config.proto"),
		filepath.FromSlash(configproto.ForProject(root).RootPath()),
	}
	for _, c := range candidates {
		data, err := os.ReadFile(filepath.Join(root, c))
		if err != nil {
			continue
		}
		if strings.Contains(string(data), "forge.v1.frontend_config") {
			return string(data), c
		}
		body, rel = string(data), c
	}
	if body == "" {
		t.Fatalf("no config proto found (looked for %v)", candidates)
	}
	return body, rel
}

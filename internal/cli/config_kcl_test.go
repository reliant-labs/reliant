package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
)

// TestLoadProjectConfigEnvMap_SensitiveRoutesToSecret is the end-to-end mission
// check for the two-channel config projection: a scaffolded project's dev
// config renders each field through config_gen.appConfigEnvMap — the
// EXACT source `forge env up` injects into the host env and a deploy projects into
// every workload's manifest — and each field lands on the RIGHT channel.
//
//   - DATABASE_URL is `sensitive`, so it projects a SECRET REFERENCE
//     (<project>-secrets / database_url), never an inline value. This is what
//     stops the rendered Deployment from carrying a database password as a
//     literal `value:` visible to anyone with repo or namespace read.
//   - ENVIRONMENT is ordinary config, so it still projects inline.
//
// The VALUE half lives in the gitignored `.env.dev` the same generate step
// scaffolds — asserted here too, because a Secret reference with nothing
// behind it is a dev loop that cannot boot.
func TestLoadProjectConfigEnvMap_SensitiveRoutesToSecret(t *testing.T) {
	if _, err := exec.LookPath("kcl"); err != nil {
		t.Skip("kcl not on PATH; skipping config projection render test")
	}
	tmp := t.TempDir()
	g := generator.NewProjectGenerator("cfgproj", tmp, "example.com/cfgproj")
	g.Kind = config.ProjectKindService
	g.ApplyKindFeatureDefaults(config.ProjectKindService)
	if err := g.Generate(); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	// Emit the REAL KCL-native config trio from the scaffold defaults: the
	// shared schema + projection, the dev per-env values instance, and the
	// gitignored dev secrets dotenv.
	var fields []codegen.ConfigField
	for _, m := range codegen.DefaultConfigMessages() {
		fields = append(fields, m.Fields...)
	}
	kclDirAbs := filepath.Join(tmp, "deploy", "kcl")
	if err := codegen.GenerateConfigNativeShared(fields, "cfgproj", tmp, kclDirAbs, nil); err != nil {
		t.Fatalf("GenerateConfigNativeShared: %v", err)
	}
	if _, err := codegen.GenerateConfigKScaffold(fields, "cfgproj", kclDirAbs, "dev"); err != nil {
		t.Fatalf("GenerateConfigKScaffold: %v", err)
	}
	if _, err := codegen.GenerateEnvSecretsScaffold(fields, "cfgproj", tmp, "dev"); err != nil {
		t.Fatalf("GenerateEnvSecretsScaffold: %v", err)
	}

	srcs, err := loadProjectConfigEnvMap(tmp, "dev")
	if err != nil {
		t.Fatalf("loadProjectConfigEnvMap: %v", err)
	}

	db, ok := srcs["DATABASE_URL"]
	if !ok {
		t.Fatalf("projection dropped DATABASE_URL entirely; got %#v", srcs)
	}
	if db.Value != nil {
		t.Errorf("DATABASE_URL projected an INLINE value %q — a sensitive field must route to "+
			"the Secret channel in every environment", *db.Value)
	}
	if db.Secret == nil || db.Secret.Name != "cfgproj-secrets" || db.Secret.Key != "database_url" || db.Secret.StoreKey != "DATABASE_URL" {
		t.Errorf("DATABASE_URL secret = %#v, want forge.SecretRef {name: cfgproj-secrets, key: database_url, store_key: DATABASE_URL}", db.Secret)
	}
	if env, ok := srcs["ENVIRONMENT"]; !ok || env.Value == nil || *env.Value != "development" {
		t.Errorf("ENVIRONMENT projection = %#v, want inline \"development\"", srcs["ENVIRONMENT"])
	}

	// The reference needs a SLOT to point at: the dev secret provider's store
	// lists every declared sensitive ref, keyed by env-var NAME. That store is
	// secrets/dev.yaml — NOT a dotenv, which forge lint rejects outright.
	store, err := os.ReadFile(filepath.Join(tmp, "secrets", "dev.yaml"))
	if err != nil {
		t.Fatalf("read scaffolded secrets/dev.yaml: %v", err)
	}
	// The slot is EMPTY, and that is not a broken dev loop: DATABASE_URL's
	// value is composed in deploy/kcl/dev/main.k from the port it resolves
	// there, and KCL env vars override this store on host launch. A DSN
	// written here would be a second declaration of the dev postgres port
	// that cannot track the first. See codegen.generateEnvSecretsBody.
	if !strings.Contains(string(store), `DATABASE_URL: ""`) {
		t.Errorf("secrets/dev.yaml missing the empty DATABASE_URL slot:\n%s", store)
	}
	if strings.Contains(string(store), "postgres://") {
		t.Errorf("secrets/dev.yaml carries a DSN — the dev port is KCL's fact, not this file's:\n%s", store)
	}
}

// The projection is `{str: str | forge.SecretRef}`: a bare string is a plain
// value, an object is a Secret reference, and any other shape is refused
// rather than silently read as "no value".
func TestKCLEnvSource_DecodesTheProjectionUnion(t *testing.T) {
	var got map[string]kclEnvSource
	if err := json.Unmarshal([]byte(`{
		"PORT": "8080",
		"EMPTY": "",
		"DATABASE_URL": {"name": "app-secrets", "key": "database_url", "store_key": "DATABASE_URL"},
		"ADMIN_PASSWORD": {"name": "app-secrets", "key": "admin_password", "optional": true}
	}`), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v := got["PORT"].Value; v == nil || *v != "8080" || got["PORT"].Secret != nil {
		t.Errorf("PORT = %+v, want the plain value 8080", got["PORT"])
	}
	if v := got["EMPTY"].Value; v == nil || *v != "" {
		t.Errorf("EMPTY = %+v, want an explicit empty value (not absent)", got["EMPTY"])
	}
	if s := got["DATABASE_URL"].Secret; s == nil || s.Name != "app-secrets" || s.Key != "database_url" || s.StoreKey != "DATABASE_URL" || got["DATABASE_URL"].Value != nil {
		t.Errorf("DATABASE_URL = %+v, want the Secret ref", got["DATABASE_URL"])
	}
	if s := got["ADMIN_PASSWORD"].Secret; s == nil || !s.Optional {
		t.Errorf("ADMIN_PASSWORD = %+v, want an optional Secret ref", got["ADMIN_PASSWORD"])
	}
	for _, bad := range []string{`{"X": 8080}`, `{"X": {"from_secret": {"name": "s"}}}`, `{"X": {"key": "k"}}`, `{"X": ["a"]}`} {
		if err := json.Unmarshal([]byte(bad), &got); err == nil {
			t.Errorf("decoded %s; want a refusal", bad)
		}
	}
}

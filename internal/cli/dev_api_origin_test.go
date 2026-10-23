package cli

import (
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/kclplugin"
	"github.com/reliant-labs/forge/internal/kclrender"
)

// TestDevFrontendReachesEveryService: in the dev loop, the ONE origin the
// frontend is given serves every service the project has.
//
// The frontend dials one base URL (its Connect transport has one), and sign-in
// sets an HttpOnly session cookie on that origin. The dev env used to run each
// service as its own `go run ./cmd/<p> <service>` host process on its own
// port, and the frontend's dev config resolved the API at the FIRST service's
// port key — a process that mounts that service alone. So a 3-service
// project's frontend reached one service in dev, the same defect #527 fixed
// for hosted envs.
//
// Asserted the way the browser would find it: the dev frontend's API_URL (the
// value config.js is rendered with, through the same port store `forge env
// up` arms) names a port, and the host process bound to that port must be the
// binary's `server` — every service on one Connect mux — with no other host
// service beside it that the frontend could never call.
func TestDevFrontendReachesEveryService(t *testing.T) {
	if testing.Short() {
		t.Skip("scaffolds and generates a full project; runs in task test")
	}
	kclplugin.Register()
	dir := t.TempDir()
	g := generator.NewProjectGenerator("acme", dir, "example.com/acme")
	g.Kind = config.ProjectKindService
	g.ApplyKindFeatureDefaults(config.ProjectKindService)
	g.ServiceName, g.AdditionalServices = "alpha", []string{"beta", "gamma"}
	g.FrontendName = "web"
	if err := g.Generate(); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	cfg, err := loadProjectConfigFrom(filepath.Join(dir, "forge.yaml"))
	if err != nil {
		t.Fatalf("load forge.yaml: %v", err)
	}
	cs, err := generator.LoadChecksums(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := generatePerEnvDeployConfig(dir, cfg, cs, nil); err != nil {
		t.Fatalf("generate per-env config: %v", err)
	}

	// `forge env up dev` arms the writable port store, so the env render and
	// the frontend-config render resolve every key against one record.
	t.Cleanup(kclplugin.ResetDefaultResolverForTest)
	restore := kclplugin.UsePortStore(filepath.Join(dir, ".forge", "ports-dev.json"))
	t.Cleanup(restore)

	raw, err := kclrender.Run(dir, filepath.Join(dir, "deploy", "kcl", "dev"), []string{"env=dev"})
	if err != nil {
		t.Fatalf("render dev: %v", err)
	}
	entities, err := parseKCLEntities(raw)
	if err != nil {
		t.Fatalf("decode dev: %v", err)
	}

	// The frontend's dev config, as `forge generate` projects it from the
	// scaffolded web_config.proto: the frontend_config_gen module and the dev
	// config.k instance whose api_url resolves the `<project>-dev-api` key.
	fc := identityFrontendConfig()
	kclDir := filepath.Join(dir, "deploy", "kcl")
	module, err := codegen.GenerateFrontendConfigKCL([]codegen.FrontendConfig{fc}, "acme")
	if err != nil {
		t.Fatal(err)
	}
	writeFrontendCfgFile(t, filepath.Join(kclDir, codegen.FrontendConfigModule+".k"), module)
	if _, err := codegen.EnsureFrontendConfigInstances([]codegen.FrontendConfig{fc}, kclDir, "dev", "acme"); err != nil {
		t.Fatal(err)
	}
	values, err := loadFrontendRuntimeConfig(dir, "dev", []codegen.FrontendConfig{fc})
	if err != nil {
		t.Fatalf("render the dev frontend config: %v", err)
	}
	apiURL, _ := values["web"]["API_URL"].(string)
	u, err := url.Parse(apiURL)
	if err != nil || u.Port() == "" {
		t.Fatalf("dev frontend API_URL = %q, want an origin with a port", apiURL)
	}
	apiPort, _ := strconv.Atoi(u.Port())

	var api *WorkloadEntity
	var hostServices []string
	for i, w := range entities.Workloads {
		if w.Runtime.Type != RuntimeHost || w.Kind != "service" {
			continue
		}
		hostServices = append(hostServices, w.Name)
		for _, p := range w.HostPorts() {
			if p == apiPort {
				api = &entities.Workloads[i]
			}
		}
	}
	if api == nil {
		t.Fatalf("dev frontend API_URL %s: no host service binds port %d (host services: %v)", apiURL, apiPort, hostServices)
	}
	if got := strings.Join(api.Spec.Args, " "); got != "server" {
		t.Errorf("dev frontend API_URL %s is %q, which runs %q — one service's subcommand mounts only that service, "+
			"so the frontend reaches one of alpha/beta/gamma; want the binary's `server`, which mounts them all", apiURL, api.Name, got)
	}
	if len(hostServices) != 1 {
		t.Errorf("dev runs host services %v — every one but %q is a port the frontend never dials", hostServices, api.Name)
	}
}

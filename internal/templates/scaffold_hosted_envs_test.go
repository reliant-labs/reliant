package templates_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/kclrender"
)

// hostedConfigGenStub is the pipeline-generated config projection a fresh
// scaffold's envs import, in the exact shape codegen.GenerateConfigKCL emits
// (see TestScaffoldedIngressEvaluates): a sensitive DATABASE_URL (and, in a
// dev env with a frontend, the login broker's IDP_BROKER_TOKEN) reaches only
// the workloads that list it in `config_secrets`.
const hostedConfigGenStub = `import forge

schema ConfigSecretRef:
    name: str
    key: str

schema AppConfig:
    port: int = 8080
    database_url: ConfigSecretRef = ConfigSecretRef { name = "app-secrets", key = "database_url" }
    idp_broker_token: ConfigSecretRef = ConfigSecretRef { name = "app-secrets", key = "idp_broker_token" }

APP_CONFIG_SENSITIVE_ENV: [str] = ["DATABASE_URL", "IDP_BROKER_TOKEN"]

appConfigEnvMap = lambda c: AppConfig, config_secrets: [str] -> {str: str | forge.SecretRef} {
    _sensitive: {str: forge.SecretRef} = {
        "DATABASE_URL" = forge.SecretRef {name = c.database_url.name, key = c.database_url.key, store_key = "DATABASE_URL"}
        "IDP_BROKER_TOKEN" = forge.SecretRef {name = c.idp_broker_token.name, key = c.idp_broker_token.key, store_key = "IDP_BROKER_TOKEN"}
    }
    assert all _n in config_secrets { _n in _sensitive }, "unknown config_secrets name"
    {
        "PORT" = str(c.port)
    } | {_k: _sensitive[_k] for _k in _sensitive if _k in config_secrets}
}
`

// scaffoldForRender generates a fresh service project and stubs the config
// trio `forge generate` would write, so every env can be rendered offline
// through forge's own evaluation seam.
func scaffoldForRender(t *testing.T, name string, services []string, frontend string) string {
	t.Helper()
	tmp := t.TempDir()
	g := generator.NewProjectGenerator(name, tmp, "github.com/acme/"+name)
	g.Kind = config.ProjectKindService
	g.ApplyKindFeatureDefaults(config.ProjectKindService)
	if len(services) > 0 {
		g.ServiceName, g.AdditionalServices = services[0], services[1:]
	}
	g.FrontendName = frontend
	if err := g.Generate(); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "deploy/kcl/config_gen.k"), []byte(hostedConfigGenStub+serverRootStub(t, "DATABASE_URL", "IDP_BROKER_TOKEN")), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, env := range []string{"dev", "staging", "prod"} {
		stub := "import config_gen\n\napp_config: config_gen.AppConfig = {\n}\n"
		if err := os.WriteFile(filepath.Join(tmp, "deploy/kcl", env, "config.k"), []byte(stub), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return tmp
}

// renderEnvOutput renders one env and returns its `output` contract.
func renderEnvOutput(t *testing.T, root, env string) map[string]any {
	t.Helper()
	out, err := kclrender.Run(root, filepath.Join(root, "deploy/kcl", env), []string{"env=" + env})
	if err != nil {
		t.Fatalf("render %s env: %v", env, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v\n%s", env, err, out)
	}
	parsed, ok := doc["output"].(map[string]any)
	if !ok {
		t.Fatalf("%s render has no `output` contract:\n%s", env, out)
	}
	return parsed
}

// TestFreshScaffoldRendersEveryEnvHosted is the guard for hosting being the
// default deploy target: a project straight out of `forge project new`
// renders EVERY env it was born with — dev locally, staging and prod on the
// forge control plane — with nothing left to fill in first.
//
// Before, staging and prod bound every workload to a cluster the author had
// to name, and the frontend to a REPLACE_ME bucket; the first deploy stopped
// at "declared context not in kubeconfig". Now each deployed env publishes
// every workload, a managed database, the secrets and the static site to the
// control plane, and the cluster/bucket binders sit beside them unused.
//
// Asserted on the RENDER, because that is what every forge command reads; a
// string match on the template would pass a file that does not evaluate.
func TestFreshScaffoldRendersEveryEnvHosted(t *testing.T) {
	if testing.Short() {
		t.Skip("scaffolds and generates a full project; runs in task test")
	}
	for _, tc := range []struct {
		name     string
		services []string
		frontend string
	}{
		{"two services and a frontend", []string{"orders", "billing"}, "web"},
		// The `forge project new` default: no service yet, no frontend.
		{"bare", nil, ""},
		// A site with no API yet: its runtime_config names no workload.
		{"frontend only", nil, "web"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := scaffoldForRender(t, "shop", tc.services, tc.frontend)

			if dev := renderEnvOutput(t, root, "dev"); dev["lifecycle"] != "local" {
				t.Errorf("dev must stay the local loop, got lifecycle %#v", dev["lifecycle"])
			}

			for _, env := range []string{"staging", "prod"} {
				raw, err := os.ReadFile(filepath.Join(root, "deploy/kcl", env, "main.k"))
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(raw), "REPLACE_ME") {
					t.Errorf("%s/main.k is born with a placeholder:\n%s", env, raw)
				}

				out := renderEnvOutput(t, root, env)
				if _, ok := out["control_plane"].(map[string]any); !ok {
					t.Errorf("%s: no control_plane — a hosted env publishes to one", env)
				}
				if sp, _ := out["secret_provider"].(map[string]any); sp["type"] != "hosted" {
					t.Errorf("%s: secret_provider = %#v, want the control plane's managed store", env, out["secret_provider"])
				}
				dbs, _ := out["databases"].([]any)
				if len(dbs) != 1 {
					t.Fatalf("%s: databases = %#v, want the one managed database", env, out["databases"])
				}
				if db, _ := dbs[0].(map[string]any); db["name"] != "shop" || runtimeType(db) != "hosted" {
					t.Errorf("%s: database = %#v, want `shop` on the hosted runtime", env, db)
				}

				workloads, _ := out["workloads"].([]any)
				// migrate, and the one API workload once there is a service
				// for it to run (TestHostedFrontendReachesEveryService).
				wantWorkloads := 1
				if len(tc.services) > 0 {
					wantWorkloads++
				}
				if len(workloads) != wantWorkloads {
					t.Fatalf("%s: %d workloads, want %d:\n%#v", env, len(workloads), wantWorkloads, workloads)
				}
				for _, w := range workloads {
					wm, _ := w.(map[string]any)
					if runtimeType(wm) != "hosted" {
						t.Errorf("%s: workload %v runs on %q, want hosted", env, wm["name"], runtimeType(wm))
					}
					// The platform admits only its own registry, so a hosted
					// image names no host (ADR-0003 F1).
					if img, _ := wm["image"].(string); img != "shop" {
						t.Errorf("%s: workload %v image = %q, want the bare project image", env, wm["name"], img)
					}
				}

				frontends, _ := out["frontends"].([]any)
				if tc.frontend == "" {
					if len(frontends) != 0 {
						t.Errorf("%s: no frontend was scaffolded, got %#v", env, frontends)
					}
					continue
				}
				if len(frontends) != 1 {
					t.Fatalf("%s: frontends = %#v, want the scaffolded one", env, frontends)
				}
				f, _ := frontends[0].(map[string]any)
				if runtimeType(f) != "hosted" || f["image"] != "web" || f["public_dir"] != "out" {
					t.Errorf("%s: frontend = %#v, want web on platform static hosting, bare image, out/", env, f)
				}
				spec, _ := f["runtime_config_spec"].(map[string]any)
				if len(tc.services) == 0 {
					// A site with no API names none, but a hosted frontend
					// ships browser telemetry to the platform's own-origin
					// ingest by default.
					if _, hasAPI := spec["API_URL"]; hasAPI || len(spec) != 1 {
						t.Errorf("%s: a site with no API names one: %#v", env, spec)
					}
					if ep, _ := spec["OTEL_ENDPOINT"].(map[string]any); ep["value"] != "/_otel" {
						t.Errorf("%s: a hosted frontend must default OTEL_ENDPOINT to /_otel, got %#v", env, spec)
					}
					continue
				}
				// API_URL is a reference the control plane resolves to the
				// hosted API's allocated URL.
				api, _ := spec["API_URL"].(map[string]any)
				ref, _ := api["workloadURL"].(map[string]any)
				if ref["name"] != "api" {
					t.Errorf("%s: frontend runtime_config API_URL = %#v, want a workloadURL to api", env, spec["API_URL"])
				}
			}
		})
	}
}

// TestHostedFrontendReachesEveryService: in a hosted env, the ONE origin the
// frontend is given serves every service the project has.
//
// One origin is not a detail of the transport that a per-service URL map
// could route around. The scaffolded frontend dials one base URL — a Connect
// call is `/<package>.<Service>/<Method>`, so one origin can serve them all —
// and sign-in answers with an HttpOnly session cookie the browser returns to
// that origin only. The hosted platform gives every workload its own
// hostname and routes no paths between them.
//
// So #518, which hosted each service as its own workload and pointed API_URL
// at the first, shipped a frontend that could call one of three services. The
// fix is the topology: the env runs the binary's `server` — every service on
// one Connect mux — as one workload, and API_URL names it. Asserted on the
// render: the referenced workload runs `server`, and no other hosted service
// exists for the browser to miss.
func TestHostedFrontendReachesEveryService(t *testing.T) {
	if testing.Short() {
		t.Skip("scaffolds and generates a full project; runs in task test")
	}
	root := scaffoldForRender(t, "shop", []string{"alpha", "beta", "gamma"}, "web")
	for _, env := range []string{"staging", "prod"} {
		out := renderEnvOutput(t, root, env)

		frontends, _ := out["frontends"].([]any)
		if len(frontends) != 1 {
			t.Fatalf("%s: frontends = %#v", env, frontends)
		}
		f, _ := frontends[0].(map[string]any)
		spec, _ := f["runtime_config_spec"].(map[string]any)
		apiURL, _ := spec["API_URL"].(map[string]any)
		ref, _ := apiURL["workloadURL"].(map[string]any)
		target, _ := ref["name"].(string)
		if target == "" {
			t.Fatalf("%s: the hosted frontend's API_URL references no workload: %#v", env, spec)
		}

		var hostedServices []string
		var api map[string]any
		for _, w := range out["workloads"].([]any) {
			wm := w.(map[string]any)
			if wm["kind"] == "service" && runtimeType(wm) == "hosted" {
				hostedServices = append(hostedServices, wm["name"].(string))
			}
			if wm["name"] == target {
				api = wm
			}
		}
		if api == nil {
			t.Fatalf("%s: API_URL names %q, which this env does not run", env, target)
		}
		spec, _ = api["spec"].(map[string]any)
		if args, _ := spec["args"].([]any); len(args) != 1 || args[0] != "server" {
			t.Errorf("%s: API_URL names %q, which runs %v — one service's subcommand mounts only that service, so the "+
				"frontend reaches one of alpha/beta/gamma; want the binary's `server`, which mounts them all", env, target, spec["args"])
		}
		if len(hostedServices) != 1 || hostedServices[0] != target {
			t.Errorf("%s: hosted services %v — every one but the API_URL target (%s) is a hostname the frontend never calls", env, hostedServices, target)
		}
		vars := map[string]map[string]any{}
		for _, e := range spec["env"].([]any) {
			em := e.(map[string]any)
			vars[em["name"].(string)] = em
		}
		if cors := vars["CORS_ORIGINS"]["workloadURL"]; cors == nil {
			t.Errorf("%s/%s: the API does not accept the site's browser calls (CORS_ORIGINS = %#v)", env, target, vars["CORS_ORIGINS"])
		}
		// Native sign-in answers with a session cookie set by the API's origin,
		// and the site is another origin: unless the API allows credentials,
		// the browser drops the cookie and every call after sign-in is 401.
		if got := vars["CORS_ALLOW_CREDENTIALS"]["value"]; got != "true" {
			t.Errorf("%s/%s: CORS_ALLOW_CREDENTIALS = %#v, want \"true\": the site's sign-in cookie never reaches the API", env, target, vars["CORS_ALLOW_CREDENTIALS"])
		}
		if db := vars["DATABASE_URL"]["databaseRef"]; db == nil {
			t.Errorf("%s/%s: the API does not read the managed database (DATABASE_URL = %#v)", env, target, vars["DATABASE_URL"])
		}
	}
}

// TestFreshOperatorRendersUntilOnlyTheClusterIsMissing: `forge scaffold
// operator` declares the operator before it has a CRD (`forge scaffold crd`
// is the next step), so its workload says `crds = []`. That declaration must
// render: in dev, where the operator runs on k3d, outright; in a hosted env,
// where it is bound `_on_cluster`, refused ONLY for the cluster the env has
// not declared — the one fix the author has to make there.
//
// The workload schema used to require at least one CRD of every operator, so
// the fresh declaration failed the render of EVERY env, dev included, with a
// schema error naming neither the operator nor the next step.
func TestFreshOperatorRendersUntilOnlyTheClusterIsMissing(t *testing.T) {
	if testing.Short() {
		t.Skip("scaffolds and generates a full project; runs in task test")
	}
	root := scaffoldForRender(t, "shop", []string{"orders"}, "")
	op := config.ComponentConfig{Name: "reaper", Kind: config.ComponentKindOperator, Group: "shop.io", Version: "v1alpha1"}
	workloads := filepath.Join(root, codegen.WorkloadsKCLRelPath)
	raw, err := os.ReadFile(workloads)
	if err != nil {
		t.Fatal(err)
	}
	stanza := codegen.WorkloadStanza("github.com/acme/shop", "shop", op)
	if !strings.Contains(stanza, "crds = []") {
		t.Fatalf("precondition: a fresh operator's stanza is not the CRD-less one:\n%s", stanza)
	}
	if err := os.WriteFile(workloads, []byte(string(raw)+"\n"+stanza), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, env := range []string{"dev", "staging", "prod"} {
		res, err := codegen.AppendEnvBinding(root, "shop", env, codegen.WorkloadKindOperator, op.Name)
		if err != nil || !res.Applied {
			t.Fatalf("bind reaper in %s: %+v, %v", env, res, err)
		}
	}

	dev := renderEnvOutput(t, root, "dev")
	var devOp bool
	for _, w := range dev["workloads"].([]any) {
		devOp = devOp || w.(map[string]any)["name"] == "reaper"
	}
	if !devOp {
		t.Errorf("dev renders no reaper workload: %#v", dev["workloads"])
	}

	for _, env := range []string{"staging", "prod"} {
		_, err := kclrender.Run(root, filepath.Join(root, "deploy/kcl", env), []string{"env=" + env})
		if err == nil {
			t.Fatalf("%s: an operator bound to an undeclared cluster rendered", env)
		}
		for _, want := range []string{"workload 'reaper' is bound to _on_cluster", "declares no `_cluster`", "the hosted platform refuses operators"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: the refusal does not say %q:\n%v", env, want, err)
			}
		}
		if strings.Contains(err.Error(), "CRD") {
			t.Errorf("%s: refused over the operator's CRDs, not the missing cluster:\n%v", env, err)
		}
	}
}

// runtimeType reads an entity's runtime: a workload or frontend carries the
// runtime object, a database entity just its type.
func runtimeType(entity map[string]any) string {
	if s, ok := entity["runtime"].(string); ok {
		return s
	}
	rt, _ := entity["runtime"].(map[string]any)
	s, _ := rt["type"].(string)
	return s
}

// TestScaffoldedClusterBinderRefusesUntilDeclared pins the behavior for a
// workload bound to the cluster binder of an env that declares no cluster —
// which is what `forge scaffold` writes for a kind the hosted platform
// refuses (a cron, an operator). It must fail the RENDER, naming the
// workload and the fix, rather than render an env that deploys nowhere.
// Declaring `_cluster` is the whole fix: the env then renders, mixed.
func TestScaffoldedClusterBinderRefusesUntilDeclared(t *testing.T) {
	if testing.Short() {
		t.Skip("scaffolds and generates a full project; runs in task test")
	}
	root := scaffoldForRender(t, "shop", []string{"orders"}, "")
	path := filepath.Join(root, "deploy/kcl/prod/main.k")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// The API — `server`, running orders — is the workload to rebind.
	src := strings.Replace(string(raw), "    _hosted(_api)\n", "    _on_cluster(_api)\n", 1)
	if src == string(raw) {
		t.Fatalf("precondition: prod/main.k does not bind _api `_hosted`:\n%s", raw)
	}
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = kclrender.Run(root, filepath.Join(root, "deploy/kcl/prod"), []string{"env=prod"})
	if err == nil {
		t.Fatal("an `_on_cluster` binding with no `_cluster` declared rendered")
	}
	for _, want := range []string{"workload 'api' is bound to _on_cluster", "declares no `_cluster`"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("render error does not say %q:\n%v", want, err)
		}
	}

	declared := strings.Replace(src, "\n_cluster = None\n",
		"\n_cluster = forge.ClusterTarget {cluster = \"gke_acme_prod\", connected_cluster = \"shop-prod\", namespace = \"shop-prod\", platform = \"amd64\"}\n", 1)
	if err := os.WriteFile(path, []byte(declared), 0o644); err != nil {
		t.Fatal(err)
	}
	out := renderEnvOutput(t, root, "prod")
	for _, w := range out["workloads"].([]any) {
		wm := w.(map[string]any)
		want := "hosted"
		if wm["name"] == "api" {
			want = "cluster"
		}
		if runtimeType(wm) != want {
			t.Errorf("workload %v runs on %q, want %s", wm["name"], runtimeType(wm), want)
		}
	}
}

// devWorkloadJSON renders the dev env and returns each workload, keyed by
// name, as its JSON text — enough to ask what a workload's env carries
// without pinning the render contract's exact nesting.
func devWorkloadJSON(t *testing.T, root string) map[string]string {
	t.Helper()
	out := renderEnvOutput(t, root, "dev")
	workloads, _ := out["workloads"].([]any)
	byName := map[string]string{}
	for _, w := range workloads {
		m, _ := w.(map[string]any)
		name, _ := m["name"].(string)
		raw, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		byName[name] = string(raw)
	}
	return byName
}

// TestDevSignInReachesTheAPI pins the two halves of the dev login broker's
// credential to one store. idp-provision mints the broker's token; the API
// is the one process that brokers sign-in. Before, the job printed the token
// to the terminal and the API's config_secrets named only DATABASE_URL, so
// even a token stored by hand never reached it: /auth/login was a 404 and the
// README's first sixty seconds ended at a sign-in page nobody could pass.
func TestDevSignInReachesTheAPI(t *testing.T) {
	root := scaffoldForRender(t, "shop", []string{"orders"}, "web")
	w := devWorkloadJSON(t, root)

	api, ok := w["api"]
	if !ok {
		t.Fatalf("dev renders no api workload: %v", w)
	}
	// The broker token is an IdentityRef lowered to the identity's output
	// that `forge env up` writes to the secret store under this name.
	if !strings.Contains(api, `"name":"IDP_BROKER_TOKEN","secretRef":{"key":"DEV_API_BROKER_TOKEN","name":"forge-identity-identity"}`) {
		t.Errorf("the dev API is not handed IDP_BROKER_TOKEN from the identity, so native sign-in never mounts:\n%s", api)
	}
	for _, env := range []string{"JWT_ISSUER", "JWT_AUDIENCE", "OIDC_CLIENT_ID", "OIDC_REDIRECT_URI", "IDP_BASE", "IDP_LOGIN_URI"} {
		if !strings.Contains(api, `"name":"`+env+`","secretRef":{"key":`) {
			t.Errorf("the dev API's %s is not filled from the identity:\n%s", env, api)
		}
	}
	if _, ok := w["idp-provision"]; ok {
		t.Errorf("dev still renders the retired idp-provision job: %v", w)
	}

	// A project with no frontend runs no dev IdP; its API has no broker.
	bare := devWorkloadJSON(t, scaffoldForRender(t, "shop", []string{"orders"}, ""))
	if strings.Contains(bare["api"], "IDP_BROKER_TOKEN") {
		t.Errorf("an API with no sign-in to broker was handed the broker token:\n%s", bare["api"])
	}
}

// TestDevObservabilityIsOnByDefaultAndDeliversTelemetry pins the three things
// that must agree for any data to arrive from a FRESH project with no edits:
// the clickstack compose service runs, it is handed the ports to publish, and
// the compose file publishes exactly those variables on loopback. The endpoint
// host processes export to is DERIVED by the typed observability declaration
// (Bundle.observability.local_port, the same variable that is the published
// port), so the two cannot disagree.
func TestDevObservabilityIsOnByDefaultAndDeliversTelemetry(t *testing.T) {
	root := scaffoldForRender(t, "shop", []string{"orders"}, "web")
	w := devWorkloadJSON(t, root)

	stack, ok := w["clickstack"]
	if !ok {
		t.Fatalf("a fresh dev env does not run clickstack: %v", w)
	}
	for _, v := range []string{"CLICKSTACK_OTLP_HTTP_PORT", "CLICKSTACK_OTLP_GRPC_PORT", "CLICKSTACK_UI_PORT"} {
		if !regexp.MustCompile(`"` + v + `":"[1-9]\d*"`).MatchString(stack) {
			t.Errorf("clickstack is not handed a real %s to publish:\n%s", v, stack)
		}
	}
	// The API's endpoint is the declaration's derivation of the SAME port the
	// clickstack service publishes, as a pair with its protocol.
	port := regexp.MustCompile(`"CLICKSTACK_OTLP_HTTP_PORT":"(\d+)"`).FindStringSubmatch(stack)[1]
	if !strings.Contains(w["api"], `"name":"OTEL_EXPORTER_OTLP_ENDPOINT","value":"http://127.0.0.1:`+port+`"`) ||
		!strings.Contains(w["api"], `"name":"OTEL_EXPORTER_OTLP_PROTOCOL","value":"http/protobuf"`) {
		t.Errorf("the API does not export to the port clickstack publishes (%s):\n%s", port, w["api"])
	}
	if strings.Contains(w["api"], "OTEL_LOGS_EXPORTER") {
		t.Errorf("the local collector tails the log files; OTLP logs must stay off:\n%s", w["api"])
	}

	compose, err := os.ReadFile(filepath.Join(root, "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"127.0.0.1:${CLICKSTACK_OTLP_HTTP_PORT:-4318}:4318"`,
		`"127.0.0.1:${CLICKSTACK_OTLP_GRPC_PORT:-4317}:4317"`,
		`"127.0.0.1:${CLICKSTACK_UI_PORT:-8180}:8080"`,
	} {
		if !strings.Contains(string(compose), want) {
			t.Errorf("docker-compose.yml clickstack service is missing %s", want)
		}
	}

	// The switch turns everything off together.
	mainK := filepath.Join(root, "deploy/kcl/dev/main.k")
	raw, err := os.ReadFile(mainK)
	if err != nil {
		t.Fatal(err)
	}
	off := strings.Replace(string(raw), "_observability = True", "_observability = False", 1)
	if off == string(raw) {
		t.Fatalf("dev/main.k has no `_observability = True` switch:\n%s", raw)
	}
	if err := os.WriteFile(mainK, []byte(off), 0o644); err != nil {
		t.Fatal(err)
	}
	offW := devWorkloadJSON(t, root)
	if _, ok := offW["clickstack"]; ok {
		t.Errorf("_observability = False still runs clickstack")
	}
	if strings.Contains(offW["api"], "OTEL_EXPORTER_OTLP") {
		t.Errorf("_observability = False still exports OTLP (a dangling endpoint):\n%s", offW["api"])
	}
}

// Every long-running host process gets its own pprof port. On the config
// default (127.0.0.1:6060) the second process on a machine loses the bind,
// and `forge env status` probed whichever process held it and reported THAT
// process's profiles as this app's.
func TestDevHostProcessesGetTheirOwnPprofPort(t *testing.T) {
	w := devWorkloadJSON(t, scaffoldForRender(t, "shop", []string{"orders"}, "web"))
	if !regexp.MustCompile(`"name":"PPROF_ADDR","value":"127\.0\.0\.1:\d+"`).MatchString(w["api"]) {
		t.Errorf("the dev API is not given its own loopback PPROF_ADDR:\n%s", w["api"])
	}
	if strings.Contains(w["migrate"], "PPROF_ADDR") {
		t.Errorf("a one-shot job was handed a pprof listener:\n%s", w["migrate"])
	}
}

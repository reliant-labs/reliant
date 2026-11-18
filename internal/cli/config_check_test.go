package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/forgecompat"
	"github.com/reliant-labs/forge/internal/goexec"
	"github.com/reliant-labs/forge/internal/templates"
	forgeconfig "github.com/reliant-labs/forge/pkg/config"
	deployv1alpha1 "github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// signerWorkload is a project-image worker on a cluster, shaped like
// control-plane's delivery-signer, with env as its rendered environment.
func signerWorkload(env ...deployv1alpha1.EnvVar) WorkloadEntity {
	return WorkloadEntity{
		Name:    "delivery-signer",
		Kind:    "worker",
		Image:   "control-plane",
		Build:   BuildConfigEntity{Type: "go", Go: &GoBuild{Cmd: "./cmd/control-plane", OutputName: "control-plane"}},
		Runtime: RuntimeEntity{Type: RuntimeCluster, Cluster: &ClusterRuntime{}},
		Spec:    deployv1alpha1.WorkloadSpec{Kind: deployv1alpha1.KindWorker, Args: []string{"delivery_signer"}, Env: env},
	}
}

func secretEnv(name string) deployv1alpha1.EnvVar {
	return deployv1alpha1.EnvVar{Name: name, SecretRef: &deployv1alpha1.SecretKeyRef{Name: "control-plane-internal", Key: name}}
}

func TestConfigCheckEnvIsTheRenderWithPlaceholders(t *testing.T) {
	w := signerWorkload(
		deployv1alpha1.EnvVar{Name: "DELIVERY_SIGNER_ENABLED", Value: "true"},
		secretEnv("INTERNAL_SERVICE_SECRET"),
		deployv1alpha1.EnvVar{Name: "DATABASE_URL", DatabaseRef: &deployv1alpha1.DatabaseRef{Name: "cp"}},
		deployv1alpha1.EnvVar{Name: "POD_NAME", FieldRef: &deployv1alpha1.FieldRef{FieldPath: "metadata.name"}},
	)
	w.Spec.Volumes = []deployv1alpha1.Volume{
		{Name: "cfg", MountPath: "/etc/cp", Source: deployv1alpha1.VolumeSource{Secret: &deployv1alpha1.SecretVolumeSource{Name: "cp-config"}}},
		{Name: "tmp", MountPath: "/tmp", Source: deployv1alpha1.VolumeSource{EmptyDir: &deployv1alpha1.EmptyDirVolumeSource{}}},
	}
	env := configCheckEnv(w.Spec)
	want := map[string]string{
		forgeconfig.CheckEnv:        "1",
		"DELIVERY_SIGNER_ENABLED":   "true",
		"INTERNAL_SERVICE_SECRET":   forgeconfig.CheckPlaceholder,
		"DATABASE_URL":              forgeconfig.CheckPlaceholder,
		"POD_NAME":                  forgeconfig.CheckPlaceholder,
		forgeconfig.CheckPresentEnv: "DATABASE_URL,INTERNAL_SERVICE_SECRET,POD_NAME",
		forgeconfig.CheckMountsEnv:  "/etc/cp",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("env[%s] = %q, want %q", k, env[k], v)
		}
	}
	if _, ok := env["IMAGE_BUILD_REGISTRY_BASE"]; ok {
		t.Error("a variable the render does not wire reached the check's env")
	}
	if len(env) != len(want) {
		t.Errorf("env has %d variables, want %d: %v", len(env), len(want), env)
	}

	op := deployv1alpha1.WorkloadSpec{Kind: deployv1alpha1.KindOperator}
	if got := configCheckEnv(op)["LEADER_ELECTION"]; got != "true" {
		t.Errorf("an operator's LEADER_ELECTION = %q, want the renderer's true", got)
	}
}

// The target runs the image the way the pod does. Kubernetes `command`
// replaces the image ENTRYPOINT and `args` its CMD; `docker run --entrypoint
// E image A...` is the same (an --entrypoint override also drops the image's
// CMD), and with no command `docker run image A...` keeps the ENTRYPOINT and
// replaces the CMD with args, or keeps both when args is empty too.
func TestPlanConfigCheckRunsTheWorkloadCommandAsThePodDoes(t *testing.T) {
	for _, tc := range []struct {
		name          string
		command, args []string
		entrypoint    string
		wantArgs      string
	}{
		{name: "image entrypoint and cmd", entrypoint: "", wantArgs: ""},
		{name: "args replace the image cmd", args: []string{"delivery_signer"}, wantArgs: "delivery_signer"},
		{name: "command alone", command: []string{"/app/control-plane"}, entrypoint: "/app/control-plane", wantArgs: ""},
		{name: "command and args", command: []string{"/app/control-plane", "--verbose"}, args: []string{"delivery_signer"},
			entrypoint: "/app/control-plane", wantArgs: "--verbose delivery_signer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := signerWorkload()
			w.Spec.Command, w.Spec.Args = tc.command, tc.args
			targets, _ := planConfigCheck(&KCLEntities{Workloads: []WorkloadEntity{w}}, "bin")
			if len(targets) != 1 {
				t.Fatalf("targets = %+v, want one", targets)
			}
			if got := targets[0]; got.Entrypoint != tc.entrypoint || strings.Join(got.Args, " ") != tc.wantArgs {
				t.Errorf("entrypoint %q args %q, want %q and %q", got.Entrypoint, got.Args, tc.entrypoint, tc.wantArgs)
			}
		})
	}
}

func TestPlanConfigCheckNamesEveryWorkloadItSkips(t *testing.T) {
	signer := signerWorkload()
	signer.Spec.Command = []string{"/app/control-plane", "--verbose"}
	host := signerWorkload()
	host.Name, host.Runtime = "api-dev", RuntimeEntity{Type: RuntimeHost, Host: &HostRuntime{}}
	tool := signerWorkload()
	tool.Name, tool.Kind = "migrate-tool", "tool"
	e := &KCLEntities{Workloads: []WorkloadEntity{
		signer, host, tool,
		{Name: "litellm", Kind: "service", Runtime: RuntimeEntity{Type: RuntimeCluster, Cluster: &ClusterRuntime{}}},
	}}
	targets, skips := planConfigCheck(e, "bin")
	if len(targets) != 1 || targets[0].Workload != "delivery-signer" {
		t.Fatalf("targets = %+v, want the signer alone", targets)
	}
	tg := targets[0]
	if tg.Binary != filepath.Join("bin", "control-plane") || tg.Entrypoint != "/app/control-plane" ||
		strings.Join(tg.Args, " ") != "--verbose delivery_signer" {
		t.Errorf("target = %+v, want bin/control-plane run as the workload's command and args", tg)
	}
	got := map[string]string{}
	for _, s := range skips {
		got[s.Workload] = s.Why
	}
	for name, why := range map[string]string{"api-dev": "host runtime", "migrate-tool": "tool", "litellm": "not built by forge"} {
		if !strings.Contains(got[name], why) {
			t.Errorf("skip of %s = %q, want it to say %q", name, got[name], why)
		}
	}
}

func TestConfigCheckGatesFollowTheRollout(t *testing.T) {
	cases := []struct {
		pin, mode string
		want      bool
	}{
		{"v1.0.15", configCheckAuto, false},
		{"v1.0.15-0.20261103112813-0dd8256deb65", "", false},
		{"v1.0.16-0.20261110000000-abcdef123456", "", false}, // a pseudo-version sorts before its release
		{"v1.0.16", configCheckAuto, true},
		{"v1.2.0", "", true},
		{"", "", true},
		{"0.0.0", "", true},
		{"v1.0.15", configCheckGate, true},
		{"v1.2.0", configCheckWarn, false},
	}
	for _, c := range cases {
		if got := configCheckGates(&config.ProjectConfig{ForgeVersion: c.pin}, c.mode); got != c.want {
			t.Errorf("pin %q mode %q: gates = %v, want %v", c.pin, c.mode, got, c.want)
		}
	}
	if !configCheckGates(&config.ProjectConfig{ModulePath: forgecompat.ModulePath, ForgeVersion: "v1.0.0"}, "") {
		t.Error("forge's own tree must gate")
	}
}

// stubConfigCheckRunner answers every workload with stdout.
func stubConfigCheckRunner(t *testing.T, stdout string) {
	prev := configCheckRunner
	configCheckRunner = func(context.Context, string, configCheckTarget) (string, string, error) { return stdout, "", nil }
	t.Cleanup(func() { configCheckRunner = prev })
}

// fakeProjectBinary writes bin/control-plane into a temp dir, with or
// without the check mode's marker.
func fakeProjectBinary(t *testing.T, marker bool) string {
	dir := t.TempDir()
	body := "ELF..."
	if marker {
		body += forgeconfig.CheckResultPrefix
	}
	if err := os.WriteFile(filepath.Join(dir, "control-plane"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestProjectConfigCheckWarnsBelowTheGateAndRefusesAtIt(t *testing.T) {
	stubConfigCheckRunner(t, forgeconfig.CheckResultPrefix+` {"ok":false,"problems":["required config field internal_service_secret is not set"]}`)
	e := &KCLEntities{Workloads: []WorkloadEntity{signerWorkload()}}
	dir := fakeProjectBinary(t, true)
	for _, c := range []struct {
		pin     string
		wantErr bool
	}{{"v1.0.15", false}, {"v1.0.16", true}} {
		var out bytes.Buffer
		chk := &projectConfigCheck{cfg: &config.ProjectConfig{ForgeVersion: c.pin}, entities: e, env: "prod", out: &out, outputDir: dir}
		err := chk.run(t.Context(), "control-plane:v1")
		if (err != nil) != c.wantErr {
			t.Fatalf("pin %s: err = %v, want error %v\n%s", c.pin, err, c.wantErr, out.String())
		}
		if !strings.Contains(out.String(), "✗ delivery-signer would refuse to start") ||
			!strings.Contains(out.String(), "internal_service_secret is not set") {
			t.Errorf("pin %s: output does not name the workload and its problem:\n%s", c.pin, out.String())
		}
		if !c.wantErr && !strings.Contains(out.String(), "gates from "+configCheckGatesFrom) {
			t.Errorf("pin %s: the warning does not say when it gates:\n%s", c.pin, out.String())
		}
	}
}

func TestProjectConfigCheckSkipsABinaryWithoutTheMode(t *testing.T) {
	stubConfigCheckRunner(t, "")
	var out bytes.Buffer
	chk := &projectConfigCheck{entities: &KCLEntities{Workloads: []WorkloadEntity{signerWorkload()}}, env: "prod", out: &out, outputDir: fakeProjectBinary(t, false)}
	if err := chk.run(t.Context(), "control-plane:v1"); err != nil {
		t.Fatalf("a binary without the mode failed the check: %v", err)
	}
	if !strings.Contains(out.String(), "delivery-signer: not checked: its binary predates the check mode") {
		t.Errorf("the skip is not named:\n%s", out.String())
	}
}

func TestProjectConfigCheckRefusesAGatedUnverifiedWorkload(t *testing.T) {
	stubConfigCheckRunner(t, "started\n")
	for _, c := range []struct {
		mode    string
		wantErr bool
	}{{configCheckGate, true}, {configCheckWarn, false}} {
		var out bytes.Buffer
		chk := &projectConfigCheck{entities: &KCLEntities{Workloads: []WorkloadEntity{signerWorkload()}}, env: "prod", mode: c.mode, out: &out, outputDir: fakeProjectBinary(t, true)}
		err := chk.run(t.Context(), "control-plane:v1")
		if (err != nil) != c.wantErr {
			t.Fatalf("mode %s: err = %v, want error %v\n%s", c.mode, err, c.wantErr, out.String())
		}
		if c.wantErr && !strings.Contains(err.Error(), "delivery-signer") {
			t.Errorf("mode %s: the refusal does not name the workload: %v", c.mode, err)
		}
		if !strings.Contains(out.String(), "? delivery-signer: unverified") {
			t.Errorf("mode %s: unverified is not reported:\n%s", c.mode, out.String())
		}
	}
}

func TestNewProjectConfigCheckRunsOnlyForAPushOfAnEnv(t *testing.T) {
	e := &KCLEntities{}
	for _, c := range []struct {
		opts buildOptions
		want bool
	}{
		{buildOptions{env: "prod", push: true}, true},
		{buildOptions{env: "prod", release: "v1.2.3"}, true},
		{buildOptions{env: "prod"}, false},
		{buildOptions{env: "prod", push: true, configCheck: configCheckOff}, false},
		{buildOptions{push: true}, false},
	} {
		if got := newProjectConfigCheck(nil, e, c.opts) != nil; got != c.want {
			t.Errorf("%+v: check = %v, want %v", c.opts, got, c.want)
		}
	}
}

// The two control-plane delivery-signer boot failures of 2026-11-03,
// reproduced end to end: a real forge-config binary (testdata/configcheck)
// run in its check mode with each failing env, and with the fixed one.
func TestConfigCheckCatchesBothDeliverySignerBootFailures(t *testing.T) {
	testkit.Slow(t, "builds the testdata/configcheck fixture binary with go build",
		testkit.Inputs("internal/cli/testdata/configcheck/**", "pkg/config/**"))

	bin := t.TempDir()
	build := exec.Command("go", "build", "-o", filepath.Join(bin, "control-plane"), "./testdata/configcheck")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build the fixture: %v\n%s", err, out)
	}
	prev := configCheckRunner
	configCheckRunner = hostConfigCheckRunner
	t.Cleanup(func() { configCheckRunner = prev })

	enabled := deployv1alpha1.EnvVar{Name: "DELIVERY_SIGNER_ENABLED", Value: "true"}
	cases := []struct {
		name string
		env  []deployv1alpha1.EnvVar
		want string // "" passes
	}{
		// cp #1886: the env never wires the control-plane-internal secret.
		{"unwired required secret", []deployv1alpha1.EnvVar{enabled, {Name: "IMAGE_BUILD_REGISTRY_BASE", Value: "registry.reliantapi.com"}},
			"required config field internal_service_secret is not set"},
		// cp #1890: the secret is wired, the registry base is left empty.
		{"empty registry base", []deployv1alpha1.EnvVar{enabled, secretEnv("INTERNAL_SERVICE_SECRET")},
			"delivery_signing.signer_enabled needs image_build.registry_base"},
		{"fixed", []deployv1alpha1.EnvVar{enabled, secretEnv("INTERNAL_SERVICE_SECRET"), {Name: "IMAGE_BUILD_REGISTRY_BASE", Value: "registry.reliantapi.com"}},
			""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out bytes.Buffer
			chk := &projectConfigCheck{
				cfg:      &config.ProjectConfig{ForgeVersion: configCheckGatesFrom},
				entities: &KCLEntities{Workloads: []WorkloadEntity{signerWorkload(c.env...)}},
				env:      "prod", out: &out, outputDir: bin,
			}
			err := chk.run(t.Context(), "control-plane:v1")
			if c.want == "" {
				if err != nil || !strings.Contains(out.String(), "✓ delivery-signer") {
					t.Fatalf("the fixed env failed the check: %v\n%s", err, out.String())
				}
				return
			}
			if err == nil || !strings.Contains(out.String(), c.want) {
				t.Fatalf("err = %v, want a refused push naming %q:\n%s", err, c.want, out.String())
			}
			if strings.Contains(out.String(), "started") {
				t.Fatalf("the process ran past its config load:\n%s", out.String())
			}
		})
	}
}

// A fresh service project's migration job — the scaffolded `db migrate up`,
// built from the cmd-tree-db.go.tmpl this forge writes — answers the check
// with its DSN wired, and is refused with it unwired. Before it took part,
// `db migrate` read DATABASE_URL directly and never reached a config load,
// so the check reported it unverified and a fresh project's first deploy
// (`project new --service X --claimable`, then `env deploy prod
// --claimable`) was refused before the push once the check gated. Built with
// no migrations embedded: the state every project starts in, before its
// first entity.
func TestConfigCheckVerifiesAFreshProjectsMigrationJob(t *testing.T) {
	testkit.Slow(t, "renders the scaffolded db.go into a module and builds it with go build",
		testkit.Inputs("internal/templates/project/cmd-tree-db.go.tmpl", "pkg/config/**"))

	bin := buildScaffoldedMigrator(t)
	prev := configCheckRunner
	configCheckRunner = hostConfigCheckRunner
	t.Cleanup(func() { configCheckRunner = prev })

	migrate := func(env ...deployv1alpha1.EnvVar) WorkloadEntity {
		return WorkloadEntity{
			Name:    "migrate",
			Kind:    "job",
			Image:   "demo",
			Build:   BuildConfigEntity{Type: "go", Go: &GoBuild{Cmd: "./cmd/demo", OutputName: "demo"}},
			Runtime: RuntimeEntity{Type: RuntimeHosted},
			Spec:    deployv1alpha1.WorkloadSpec{Kind: deployv1alpha1.KindJob, Args: []string{"db", "migrate", "up"}, Env: env},
		}
	}
	wired := deployv1alpha1.EnvVar{Name: "DATABASE_URL", DatabaseRef: &deployv1alpha1.DatabaseRef{Name: "demo"}}
	for _, c := range []struct {
		name string
		w    WorkloadEntity
		want string // "" passes
	}{
		{"database wired", migrate(wired), ""},
		{"database unwired", migrate(), "DATABASE_URL is not set"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var out bytes.Buffer
			chk := &projectConfigCheck{
				cfg:      &config.ProjectConfig{ForgeVersion: configCheckGatesFrom},
				entities: &KCLEntities{Workloads: []WorkloadEntity{c.w}},
				env:      "prod", out: &out, outputDir: bin,
			}
			err := chk.run(t.Context(), "demo:v1")
			if c.want == "" {
				if err != nil || !strings.Contains(out.String(), "✓ migrate") {
					t.Fatalf("a fresh project's wired migration job failed the check: %v\n%s", err, out.String())
				}
				return
			}
			if err == nil || !strings.Contains(out.String(), c.want) {
				t.Fatalf("err = %v, want a refused push naming %q:\n%s", err, c.want, out.String())
			}
		})
	}
}

// buildScaffoldedMigrator renders cmd-tree-db.go.tmpl into a minimal module
// shaped like a fresh project with no migrations (db/ embeds none; pkg/config
// registers the DSN flag), builds it against this forge checkout through a
// go.work, and returns the directory holding the binary, named "demo".
func buildScaffoldedMigrator(t *testing.T) string {
	t.Helper()
	forgeRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	src, err := templates.ProjectTemplates().Render("cmd-tree-db.go.tmpl", map[string]any{
		"Module": "example.com/demo", "Name": "demo", "HasMigrations": false,
	})
	if err != nil {
		t.Fatalf("render cmd-tree-db.go.tmpl: %v", err)
	}
	mod := t.TempDir()
	files := map[string]string{
		"go.mod":  "module example.com/demo\n\ngo 1.27.0\n",
		"go.work": "go 1.27.0\n\nuse (\n\t.\n\t" + forgeRoot + "\n)\n",
		"main.go": "package main\n\nimport \"example.com/demo/cmd\"\n\nfunc main() { cmd.Execute() }\n",
		"db/source.go": "package db\n\nimport \"io/fs\"\n\nconst SourceDir = \"migrations\"\n\nconst HasMigrations = false\n\n" +
			"func Source() fs.FS { return nil }\n",
		"pkg/config/config.go": "package config\n\nimport \"github.com/spf13/cobra\"\n\n" +
			"func RegisterFlags(cmd *cobra.Command) { cmd.PersistentFlags().String(\"database-url\", \"\", \"\") }\n",
		// The root also has a `server` that loads a typed config, as every
		// project's does: that is what links the check mode into the binary.
		"cmd/root.go": "package cmd\n\nimport (\n\t\"os\"\n\n\t\"github.com/spf13/cobra\"\n" +
			"\t\"google.golang.org/protobuf/types/known/emptypb\"\n\n\tforgeconfig \"github.com/reliant-labs/forge/pkg/config\"\n)\n\n" +
			"type Deps struct{}\n\nvar factories []func(Deps) *cobra.Command\n\n" +
			"func registerCommand(f func(Deps) *cobra.Command) { factories = append(factories, f) }\n\n" +
			"func Execute() {\n\troot := &cobra.Command{Use: \"demo\"}\n" +
			"\troot.AddCommand(&cobra.Command{Use: \"server\", RunE: func(cmd *cobra.Command, _ []string) error {\n" +
			"\t\treturn forgeconfig.Load(cmd, &emptypb.Empty{})\n\t}})\n" +
			"\tfor _, f := range factories {\n\t\troot.AddCommand(f(Deps{}))\n\t}\n" +
			"\tif root.Execute() != nil {\n\t\tos.Exit(1)\n\t}\n}\n",
		"cmd/db.go": string(src),
	}
	for rel, body := range files {
		p := filepath.Join(mod, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bin := t.TempDir()
	build := exec.Command("go", "build", "-o", filepath.Join(bin, "demo"), ".")
	build.Dir = mod
	build.Env = goexec.WithTrimpath(append(os.Environ(), "GOWORK="+filepath.Join(mod, "go.work"), "GOFLAGS=-mod=readonly"))
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build the scaffolded migrator: %v\n%s", err, out)
	}
	return bin
}

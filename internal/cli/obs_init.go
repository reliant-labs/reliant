package cli

// `forge obs init [env…]`: monitoring for an app forge never built or deploys.
//
// A team with an existing app — any language, its own CI and infrastructure —
// gets its telemetry, issues and alerts into Reliant with nothing but the
// standard OpenTelemetry variables. This verb does the forge side of that in
// one call:
//
//  1. requires sign-in (`forge login`, or a token): there is no anonymous
//     path for observability;
//  2. names the project (forge.yaml `name`, --project, the env documents, or
//     the directory) and declares each env that nothing declares yet in its
//     env document, deploy/envs/<env>.yaml — the minimal monitoring-only one
//     (docs/design/env-document.md §4), read through the env-declaration
//     seam every `forge obs` verb reads. An env forge.yaml `envs:` already
//     declares (the legacy shorthand) stays there; forge.yaml is never a
//     new env's home;
//  3. registers each env on the control plane as SELF_MANAGED (the platform
//     never deploys there);
//  4. mints a per-env telemetry ingest token (TelemetryTokenService), shown
//     once; and
//  5. prints the exact OTEL_* variables and the dashboard link.
//
// A `monitoring.endpoint` (a self-hosted OTLP backend) skips 1, 3 and 4: the
// app exports there and Reliant is not involved.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/mod/modfile"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/internal/envdoc"
	"github.com/reliant-labs/forge/internal/obsbackend"
)

const (
	obsInitSchema = "forge.obs.init/v1"
	// procCreateIngestToken and friends are control-plane TelemetryTokenService.
	procCreateIngestToken = "controlplane.v1.TelemetryTokenService/CreateIngestToken"
	procListIngestTokens  = "controlplane.v1.TelemetryTokenService/ListIngestTokens"
	procRotateIngestToken = "controlplane.v1.TelemetryTokenService/RotateIngestToken"
	procRevokeIngestToken = "controlplane.v1.TelemetryTokenService/RevokeIngestToken"
	// defaultIngestTokenName labels the token `forge obs init` mints.
	defaultIngestTokenName = "forge-obs-init"
	// otlpProtocol is the exporter protocol the public edge serves.
	otlpProtocol = "http/protobuf"
)

// obsInitOpts are `forge obs init`'s flags.
type obsInitOpts struct {
	service     string
	tokenName   string
	ttl         time.Duration
	org         string
	writeConfig bool
	noWrite     bool
	jsonOut     bool
}

// obsInitDoc is the --json document (contract: forge.obs.init/v1).
type obsInitDoc struct {
	Schema       string `json:"schema"`
	Project      string `json:"project"`
	OrgID        string `json:"org_id,omitempty"`
	ControlPlane string `json:"control_plane,omitempty"`
	ConsoleURL   string `json:"console_url,omitempty"`
	// WroteConfig reports that init wrote a declaration (an env document,
	// or forge.yaml for a legacy env); ConfigPath is the first file it wrote,
	// else the governing forge.yaml. WroteFiles lists every file written.
	WroteConfig bool         `json:"wrote_config"`
	ConfigPath  string       `json:"config_path,omitempty"`
	WroteFiles  []string     `json:"wrote_files,omitempty"`
	Envs        []obsInitEnv `json:"envs"`
}

// obsInitEnv is one env's result.
type obsInitEnv struct {
	Env           string `json:"env"`
	EnvironmentID string `json:"environment_id,omitempty"`
	// Declaration is the file that declares the env, relative to the
	// project: deploy/envs/<env>.yaml, or forge.yaml for a legacy env; ""
	// when only flags do.
	Declaration  string            `json:"declaration,omitempty"`
	Token        *obsInitToken     `json:"token,omitempty"`
	OTelEnv      map[string]string `json:"otel_env"`
	DashboardURL string            `json:"dashboard_url,omitempty"`
}

// obsInitToken is the minted token's metadata. The JWT is in OTelEnv only.
type obsInitToken struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	ExpiresAt time.Time `json:"expires_at"`
}

// obsSession is a signed-in connection to one project's control plane.
type obsSession struct {
	Caller       cloudCaller
	OrgID        string
	ControlPlane string
}

// obsInitDeps are the seams `forge obs init` reaches the world through.
type obsInitDeps struct {
	// Connect signs in to controlPlane for project, creating the hosted
	// project on first use when bind (a person's first `obs init`).
	Connect func(ctx context.Context, controlPlane, project, org string, bind bool) (obsSession, error)
	// Getwd is the directory a new forge.yaml would be written in.
	Getwd func() (string, error)
	// Confirm asks a yes/no question; ok=false when nobody can answer.
	Confirm func(question string) (yes, ok bool)
	Stdout  io.Writer
	Stderr  io.Writer
}

// obsInitDepsOverride replaces the real seams in tests.
var obsInitDepsOverride *obsInitDeps

func defaultObsInitDeps(cmd *cobra.Command) obsInitDeps {
	if o := obsInitDepsOverride; o != nil {
		return *o
	}
	return obsInitDeps{
		Connect: connectObsControlPlane,
		Getwd:   os.Getwd,
		Confirm: stdinConfirm(cmd.InOrStdin(), cmd.ErrOrStderr()),
		Stdout:  cmd.OutOrStdout(),
		Stderr:  cmd.ErrOrStderr(),
	}
}

func newObsInitCmd() *cobra.Command {
	var o obsInitOpts
	cmd := &cobra.Command{
		Use:   "init [env…]",
		Short: "Send an existing app's telemetry to Reliant: register envs, mint ingest tokens, print the OTEL_* variables",
		Long: `Monitoring for an app forge did not build and does not deploy — any language,
any CI, any infrastructure. Sign in first (` + "`forge login`" + `); there is no
anonymous path.

For each env, init registers it on the control plane (self-managed: the platform
never deploys there), mints a telemetry ingest token for it, and prints the
standard OpenTelemetry variables your app needs — nothing forge-specific:

  OTEL_EXPORTER_OTLP_ENDPOINT   the public OTLP edge
  OTEL_EXPORTER_OTLP_HEADERS    authorization=Bearer <the token>  (shown once: store it as a secret)
  OTEL_SERVICE_NAME             one per deployable (api, worker, web)
  OTEL_RESOURCE_ATTRIBUTES      deployment.environment.name=<env>,service.version=<your version>

It declares each env nothing declares yet in an env document,
deploy/envs/<env>.yaml (apiVersion forge.dev/env/v1: the project, the env and
where its telemetry goes; ` + "`forge project schema env`" + ` prints the schema). It needs
no forge.yaml. In a directory with no forge files it asks first (--write-config
to skip the question, --no-write to keep the directory untouched and pass
--project). An env document already there is read, never rewritten; an env
forge.yaml ` + "`envs:`" + ` declares (the legacy shorthand) stays in forge.yaml.

  forge login
  forge obs init prod staging
  forge obs status prod        # then: forge obs issues prod, forge obs logs prod
  forge obs init prod --json   # the same, as a document

Rotate or revoke a token with ` + "`forge obs tokens`" + `; route alerts with ` + "`forge obs receivers`" + `.
Language-by-language setup: forge skill load observability (section "Monitoring only, any language").`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runObsInit(cmd.Context(), defaultObsInitDeps(cmd), args, o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.service, "service", "", "fill OTEL_SERVICE_NAME with this service.name (default: a placeholder to fill per deployable)")
	f.StringVar(&o.tokenName, "token-name", defaultIngestTokenName, "the ingest token's label (ci, laptop, prod-k8s)")
	f.DurationVar(&o.ttl, "ttl", 0, "the ingest token's lifetime (default and maximum 90 days, at least 1h)")
	f.StringVar(&o.org, "org", "", "the organization a NEW project is created in (default: your only one)")
	f.BoolVar(&o.writeConfig, "write-config", false, "write the env document (deploy/envs/<env>.yaml) without asking")
	f.BoolVar(&o.noWrite, "no-write", false, "write no file: no env document, no forge.yaml (name the project with --project)")
	f.BoolVar(&o.jsonOut, "json", false, "print the result as a "+obsInitSchema+" document")
	return cmd
}

// runObsInit is the verb.
func runObsInit(ctx context.Context, d obsInitDeps, args []string, o obsInitOpts) error {
	if o.writeConfig && o.noWrite {
		return obsbackend.Wrong("--write-config and --no-write contradict each other")
	}
	cfgPath, cfg, err := initConfig()
	if err != nil && cfgPath != "" && !o.noWrite {
		// A Go repo's forge.yaml must name its module (config's rule); a
		// name-only one written by hand does not load. go.mod already says
		// it, so write it down rather than refuse.
		if mp := goModulePath(filepath.Dir(cfgPath)); mp != "" && strings.Contains(err.Error(), "module_path") {
			if _, uerr := upsertMonitoringConfig(cfgPath, "", nil, mp); uerr == nil {
				cfgPath, cfg, err = initConfig()
			}
		}
	}
	if err != nil {
		return err
	}
	projectDir := ""
	if cfgPath != "" {
		projectDir = filepath.Dir(cfgPath)
	} else if projectDir, err = d.Getwd(); err != nil {
		return err
	}
	project, err := initProjectName(cfg, d, projectDir)
	if err != nil {
		return err
	}
	envs, err := initEnvs(args, cfg, projectDir)
	if err != nil {
		return err
	}
	decls, err := initDeclarations(projectDir, cfg, envs)
	if err != nil {
		return err
	}
	var docs []envDocumentDeclaration
	for _, dl := range decls {
		if dl.source == envDeclDocument {
			docs = append(docs, dl.document)
		}
	}
	if len(docs) > 0 {
		if project, err = projectFromEnvDocuments(cfg, docs); err != nil {
			return err
		}
	}

	doc := obsInitDoc{Schema: obsInitSchema, Project: project, Envs: []obsInitEnv{}}
	written, err := initWriteDeclarations(d, cfgPath, projectDir, project, cfg, decls, o)
	if err != nil {
		return err
	}
	doc.WroteFiles = written
	doc.WroteConfig = len(written) > 0
	switch {
	case len(written) > 0:
		doc.ConfigPath = written[0]
	case cfgPath != "":
		doc.ConfigPath = cfgPath
	}
	if len(written) > 0 && cfgPath != "" {
		// Re-read what was written: the seam and every later verb read it.
		if _, cfg, err = initConfig(); err != nil {
			return err
		}
	}
	mon, err := initMonitoring(cfg, decls)
	if err != nil {
		return err
	}
	console := cloud.NewConsole(cloud.ConsoleBase(monitoringConsoleURL(mon)), project)
	declaredIn := map[string]string{}
	for _, dl := range decls {
		declaredIn[dl.env] = dl.path
	}

	if ep := monitoringEndpoint(mon); ep != "" {
		// A self-hosted backend: nothing to register, nothing to mint.
		for _, env := range envs {
			doc.Envs = append(doc.Envs, obsInitEnv{Env: env, Declaration: declaredIn[env], OTelEnv: otelEnv(ep, "", env, project, o.service)})
		}
		return writeObsInit(d, doc, o, ep)
	}

	cp := monitoringControlPlane(mon)
	sess, err := d.Connect(ctx, cp, project, o.org, true)
	if err != nil {
		return err
	}
	doc.OrgID, doc.ControlPlane, doc.ConsoleURL = sess.OrgID, cp, console.Home("")
	sess.ControlPlane = cp
	registered, err := registerMonitoredEnvs(ctx, sess, project, envs, console, o)
	if err != nil {
		return err
	}
	for i := range registered {
		registered[i].Declaration = declaredIn[registered[i].Env]
	}
	doc.Envs = append(doc.Envs, registered...)
	return writeObsInit(d, doc, o, "")
}

// registerMonitoredEnvs is the control-plane half of `forge obs init`, shared
// with the hosted obs_init tool: it registers each env as SELF_MANAGED, mints
// its ingest token and returns the OTEL_* variables an app sends with. It
// reads no forge.yaml and writes nothing locally.
func registerMonitoredEnvs(ctx context.Context, sess obsSession, project string, envs []string, console cloud.Console, o obsInitOpts) ([]obsInitEnv, error) {
	// A control plane that predates TelemetryTokenService cannot finish
	// init: refuse before registering any env on it, not after.
	if err := requireIngestTokens(ctx, sess); err != nil {
		return nil, err
	}
	out := make([]obsInitEnv, 0, len(envs))
	for _, env := range envs {
		id, _, err := deploytarget.EnsureHostedEnvironment(ctx, sess.Caller, deploytarget.HostedEnvRef{
			Project: project, Name: env, Kind: deploytarget.HostedEnvSelfManaged})
		if err != nil {
			return nil, obsbackend.MapCloudError(fmt.Errorf("register env %s: %w", env, err))
		}
		tok, err := createIngestToken(ctx, sess, id, o.tokenName, o.ttl)
		if err != nil {
			return nil, fmt.Errorf("env %s: %w", env, err)
		}
		if tok.OTLPEndpoint == "" {
			return nil, obsbackend.Undetermined("the control plane %s has no public OTLP endpoint, so an app outside it has nowhere to send; "+
				"its operator sets telemetry_token.local_telemetry_endpoint", sess.ControlPlane)
		}
		out = append(out, obsInitEnv{
			Env: env, EnvironmentID: id,
			Token:        &obsInitToken{ID: tok.Token.ID, Name: tok.Token.Name, ExpiresAt: tok.Token.ExpiresAt},
			OTelEnv:      otelEnv(tok.OTLPEndpoint, tok.OTLPHeaders, env, project, o.service),
			DashboardURL: console.Dashboards(env),
		})
	}
	return out, nil
}

// initConfig reads the governing forge.yaml: ("", nil, nil) when there is none.
func initConfig() (string, *config.ProjectConfig, error) {
	path, err := findProjectConfigFile()
	if errors.Is(err, ErrProjectConfigNotFound) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	cfg, err := loadProjectConfigFrom(path)
	if err != nil {
		return path, nil, err
	}
	return path, cfg, nil
}

// projectFromEnvDocuments is the project env documents name, which must be
// one project and agree with forge.yaml / --project when those name one.
func projectFromEnvDocuments(cfg *config.ProjectConfig, docs []envDocumentDeclaration) (string, error) {
	p := docs[0].Project
	for _, d := range docs[1:] {
		if d.Project != p {
			return "", obsbackend.Wrong("the env documents name two projects (%q and %q): an init is for one project", p, d.Project)
		}
	}
	if cfg != nil && cfg.Name != "" && cfg.Name != p {
		return "", obsbackend.Wrong("the env documents name project %q, but forge.yaml's name is %q", p, cfg.Name)
	}
	if flag := strings.TrimSpace(obsProjectFlag); flag != "" && flag != p {
		return "", obsbackend.Wrong("--project %q disagrees with the env documents' project %q", flag, p)
	}
	return p, nil
}

// initProjectName is --project, else forge.yaml `name`, else (no forge.yaml)
// the project the env documents under projectDir name, else the directory's
// name, made a valid project name.
func initProjectName(cfg *config.ProjectConfig, d obsInitDeps, projectDir string) (string, error) {
	flag := strings.TrimSpace(obsProjectFlag)
	if cfg != nil {
		if flag != "" && cfg.Name != "" && flag != cfg.Name {
			return "", obsbackend.Wrong("--project %q disagrees with forge.yaml's name %q", flag, cfg.Name)
		}
		if cfg.Name != "" {
			return cfg.Name, nil
		}
	}
	if flag != "" {
		if !validProjectName(flag) {
			return "", obsbackend.Wrong("--project %q is not a project name: lowercase letters, digits and '-', starting with a letter", flag)
		}
		return flag, nil
	}
	if p := envDocumentsProject(projectDir); p != "" {
		return p, nil
	}
	wd, err := d.Getwd()
	if err != nil {
		return "", err
	}
	if name := projectNameFromDir(filepath.Base(wd)); name != "" {
		return name, nil
	}
	return "", obsbackend.Wrong("name the project: forge obs init --project <name>")
}

var projectNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

func validProjectName(s string) bool { return projectNameRE.MatchString(s) }

// projectNameFromDir turns a directory name into a project name: lowercase,
// runs of anything else as one '-', trimmed.
func projectNameFromDir(dir string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(dir) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	name := strings.Trim(b.String(), "-")
	if len(name) > 63 {
		name = strings.Trim(name[:63], "-")
	}
	if !validProjectName(name) {
		return ""
	}
	return name
}

// initEnvs is the positional envs, else the envs that declare monitoring:
// forge.yaml `envs:` and the env documents with an observability block.
func initEnvs(args []string, cfg *config.ProjectConfig, projectDir string) ([]string, error) {
	envs := args
	if len(envs) == 0 {
		if cfg != nil {
			envs = append(envs, cfg.Envs...)
		}
		documented, _ := envdoc.List(projectDir)
		for _, env := range documented {
			if d, ok, err := readEnvDocumentDeclaration(projectDir, env); err == nil && ok && d.Monitoring != nil {
				envs = append(envs, env)
			}
		}
	}
	if len(envs) == 0 {
		return nil, obsbackend.Wrong("name the env(s) your app runs in: forge obs init prod [staging …]")
	}
	seen := map[string]bool{}
	var out []string
	for _, e := range envs {
		if !config.ValidEnvName(e) {
			return nil, obsbackend.Wrong("%q is not an env name: a lowercase DNS label (prod, staging, eu-west)", e)
		}
		if !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	return out, nil
}

// initDecl is where one env init touches is declared.
type initDecl struct {
	env string
	// source is envDeclDocument (deploy/envs/<env>.yaml), envDeclYAML (the
	// legacy forge.yaml `envs:`), or envDeclNone: nothing declares it yet,
	// and init writes its env document.
	source envDeclSource
	// document is the env document's declaration, when source is one.
	document envDocumentDeclaration
	// path is the declaring file, relative to the project; "" when nothing
	// declares the env (flags only).
	path string
}

// initDeclarations resolves each env's declaration through the seam. An env
// deploy/kcl declares is refused: forge env deploy wires its telemetry, and a
// second declaration would declare it twice. An env document that declares no
// observability is refused too: init would register an env whose declaration
// says nothing about where its telemetry goes.
func initDeclarations(projectDir string, cfg *config.ProjectConfig, envs []string) ([]initDecl, error) {
	out := make([]initDecl, 0, len(envs))
	for _, env := range envs {
		ed, err := declareEnv(projectDir, cfg, env)
		if err != nil {
			return nil, obsbackend.Wrong("%v", err)
		}
		dl := initDecl{env: env, source: ed.Source}
		switch ed.Source {
		case envDeclKCL:
			return nil, obsbackend.Wrong("env %s is declared in deploy/kcl/%s/main.k: `forge env deploy %s` wires its telemetry. "+
				"For a token an app outside forge's deploys sends with, use `forge obs tokens create %s`", env, env, env, env)
		case envDeclDocument:
			doc, _, err := readEnvDocumentDeclaration(projectDir, env)
			if err != nil {
				return nil, obsbackend.Wrong("%v", err)
			}
			dl.document, dl.path = doc, envDocumentRel(projectDir, env)
			if doc.Monitoring == nil {
				return nil, obsbackend.Wrong("env %s's document declares no observability: add `observability: {enabled: true, destination: {token_from_control_plane: true}}` to %s",
					env, dl.path)
			}
		case envDeclYAML:
			dl.path = defaultProjectConfigFile
		default:
			dl.source = envDeclNone
		}
		out = append(out, dl)
	}
	return out, nil
}

// initWriteDeclarations declares what is not declared yet, and returns the
// files it wrote. An env nothing declares gets its env document,
// deploy/envs/<env>.yaml (writeMonitoringDocument): forge.yaml stays project
// and tooling config. An env forge.yaml `envs:` already declares (the legacy
// shorthand) stays there: init only switches `monitoring:` on when the file
// lacks it, and never moves the env.
//
// It asks before writing into a directory with no forge files (no forge.yaml,
// no env document); --write-config skips the question and --no-write writes
// nothing.
func initWriteDeclarations(d obsInitDeps, cfgPath, projectDir, project string, cfg *config.ProjectConfig, decls []initDecl, o obsInitOpts) ([]string, error) {
	var legacy []string
	var fresh []*initDecl
	for i := range decls {
		switch decls[i].source {
		case envDeclYAML:
			legacy = append(legacy, decls[i].env)
		case envDeclNone:
			fresh = append(fresh, &decls[i])
		}
	}
	flagProject := strings.TrimSpace(obsProjectFlag) != ""
	if o.noWrite {
		if len(fresh) > 0 && cfgPath == "" && !flagProject && envDocumentsProject(projectDir) == "" {
			return nil, obsbackend.Wrong("with --no-write and no forge files, name the project: forge obs init --project <name> …")
		}
		return nil, nil
	}
	var written []string
	if len(legacy) > 0 {
		changed, err := upsertMonitoringConfig(cfgPath, project, legacy, goModulePath(projectDir))
		if err != nil {
			return nil, err
		}
		if changed {
			written = append(written, cfgPath)
		}
	}
	if len(fresh) == 0 {
		return written, nil
	}
	paths := make([]string, len(fresh))
	for i, dl := range fresh {
		paths[i] = filepath.Join(projectDir, envdoc.Dir, dl.env+".yaml")
	}
	if existing, _ := envdoc.List(projectDir); cfgPath == "" && len(existing) == 0 && !o.writeConfig {
		yes, ok := d.Confirm(fmt.Sprintf("No forge files here. Write %s, declaring the env (project: %s, monitoring on)?", strings.Join(paths, ", "), project))
		switch {
		case yes:
		case flagProject:
			// --project names the project for this run; nothing is written.
			return written, nil
		case !ok:
			return nil, obsbackend.Wrong("no forge files here, and nobody to ask: pass --write-config to write the env document (%s), or --project <name> --no-write",
				filepath.ToSlash(filepath.Join(envdoc.Dir, fresh[0].env+".yaml")))
		default:
			return nil, obsbackend.Wrong("not writing the env document: name the project with --project <name> (and --no-write)")
		}
	}
	mon := newDocumentMonitoring(cfg)
	for i, dl := range fresh {
		if err := writeMonitoringDocument(paths[i], project, dl.env, mon); err != nil {
			return nil, err
		}
		written = append(written, paths[i])
		// Read it back through the loader: what init wrote is what every
		// later verb reads, and it must validate.
		doc, ok, err := readEnvDocumentDeclaration(projectDir, dl.env)
		if err != nil || !ok {
			return nil, fmt.Errorf("the env document init wrote does not load (a forge bug): %s: %v", paths[i], err)
		}
		dl.source, dl.document, dl.path = envDeclDocument, doc, envDocumentRel(projectDir, dl.env)
	}
	// A forge.yaml with no `name:` would hide the documents' project from
	// every verb (forge.yaml names the project when there is one).
	if cfg != nil && cfg.Name == "" {
		changed, err := upsertMonitoringConfig(cfgPath, project, nil, "")
		if err != nil {
			return nil, err
		}
		if changed {
			written = append(written, cfgPath)
		}
	}
	return written, nil
}

// newDocumentMonitoring is the monitoring a new env document declares: what
// forge.yaml's legacy `monitoring:` says (so a project's envs agree on where
// telemetry goes), else Reliant's control plane; --control-plane overrides.
func newDocumentMonitoring(cfg *config.ProjectConfig) config.MonitoringConfig {
	m := config.MonitoringConfig{}
	if cfg != nil && cfg.Monitoring != nil {
		m = *cfg.Monitoring
	}
	m.Enabled = true
	if cp := strings.TrimSpace(obsControlPlaneFlag); cp != "" {
		m.ControlPlane = cp
	}
	return m
}

// writeMonitoringDocument writes the minimal monitoring-only env document
// (docs/design/env-document.md §4) to path: the project, the env, its control
// plane and where its telemetry goes. A self-hosted endpoint is written as the
// destination; otherwise the telemetry goes to the control plane, which mints
// its token.
func writeMonitoringDocument(path, project, env string, m config.MonitoringConfig) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# The %s env's declaration, written by `forge obs init`: an env document,\n", env)
	b.WriteString("# validated against the env schema (`forge project schema env`). It grows with\n")
	b.WriteString("# the adoption: add `workloads:` to record deploys (forge skill load deploy/record).\n")
	fmt.Fprintf(&b, "# yaml-language-server: $schema=%s\n", envSchemaID)
	// Quoted: a project or env named `on`, `2026` or `true` is a valid name
	// that a plain YAML scalar would turn into a boolean or a number.
	fmt.Fprintf(&b, "apiVersion: %s\nproject: %s\nenv: %s\n", envdoc.APIVersion, strconv.Quote(project), strconv.Quote(env))
	if ep := monitoringEndpoint(&m); ep != "" {
		fmt.Fprintf(&b, "observability:\n  enabled: true\n  destination:\n    endpoint: %s\n", strconv.Quote(ep))
	} else {
		if cp := strings.TrimSpace(m.ControlPlane); cp != "" {
			fmt.Fprintf(&b, "control_plane:\n  endpoint: %s\n", strconv.Quote(strings.TrimRight(cp, "/")))
		} else {
			b.WriteString("control_plane: {}  # Reliant's control plane\n")
		}
		b.WriteString("observability:\n  enabled: true\n  destination: {token_from_control_plane: true}\n")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // deploy/envs is a checked-in project directory
		return err
	}
	return atomicfile.WriteFile(path, []byte(b.String()), 0o644) //nolint:gosec // an env document is a checked-in project file
}

// initMonitoring is the monitoring the envs of one init share: one control
// plane (or one self-hosted endpoint) per init, since init signs in once.
// --control-plane overrides every env's.
func initMonitoring(cfg *config.ProjectConfig, decls []initDecl) (*config.MonitoringConfig, error) {
	var mon *config.MonitoringConfig
	first := ""
	for _, dl := range decls {
		var m *config.MonitoringConfig
		if dl.source == envDeclDocument {
			m = dl.document.Monitoring
		} else if cfg != nil {
			m = cfg.Monitoring
		}
		if cp := strings.TrimSpace(obsControlPlaneFlag); cp != "" {
			c := config.MonitoringConfig{Enabled: true}
			if m != nil {
				c = *m
			}
			c.ControlPlane = cp
			m = &c
		}
		if first == "" {
			mon, first = m, dl.env
			continue
		}
		if !sameTelemetryDestination(m, mon) {
			return nil, obsbackend.Wrong("envs %s and %s send telemetry to different places (%s and %s): init them separately",
				first, dl.env, monitoringPlace(mon), monitoringPlace(m))
		}
	}
	// console_url is forge.yaml's alone.
	if cfg != nil && cfg.Monitoring != nil && cfg.Monitoring.ConsoleURL != "" && (mon == nil || mon.ConsoleURL == "") {
		c := config.MonitoringConfig{Enabled: true}
		if mon != nil {
			c = *mon
		}
		c.ConsoleURL = cfg.Monitoring.ConsoleURL
		mon = &c
	}
	return mon, nil
}

// sameTelemetryDestination reports whether a and b send telemetry to one
// place. A self-hosted endpoint is the destination when either names one, so
// two declarations of one endpoint agree whatever control plane each also
// names (the endpoint path never signs in); only two control-plane
// destinations compare their control planes.
func sameTelemetryDestination(a, b *config.MonitoringConfig) bool {
	ea, eb := monitoringEndpoint(a), monitoringEndpoint(b)
	if ea != "" || eb != "" {
		return ea == eb
	}
	return monitoringControlPlane(a) == monitoringControlPlane(b)
}

// monitoringPlace names where a monitoring declaration's telemetry goes.
func monitoringPlace(m *config.MonitoringConfig) string {
	if ep := monitoringEndpoint(m); ep != "" {
		return ep
	}
	return monitoringControlPlane(m)
}

// goModulePath is dir/go.mod's module path, "" when there is none.
func goModulePath(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return ""
	}
	return modfile.ModulePath(data)
}

// upsertMonitoringConfig adds envs to forge.yaml `envs:` and switches
// `monitoring:` on when absent, editing the YAML tree so the author's comments
// and order survive. A missing file is created with `name:` first. modulePath,
// when set, is written as `module_path:` if the file has none (a Go repo's
// forge.yaml must name its module). nil envs touches neither envs nor
// monitoring.
func upsertMonitoringConfig(path, project string, envs []string, modulePath string) (bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		raw = []byte("name: " + project + "\n")
	} else if err != nil {
		return false, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return false, fmt.Errorf("%s: the top level is not a mapping", path)
	}
	changed := errors.Is(err, os.ErrNotExist)
	value := func(key string) *yaml.Node {
		for i := 0; i+1 < len(root.Content); i += 2 {
			if root.Content[i].Value == key {
				return root.Content[i+1]
			}
		}
		return nil
	}
	set := func(key string, v *yaml.Node) {
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, v)
	}
	if value("name") == nil && project != "" {
		root.Content = append([]*yaml.Node{{Kind: yaml.ScalarNode, Tag: "!!str", Value: "name"},
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: project}}, root.Content...)
		changed = true
	}
	if modulePath != "" && value("module_path") == nil {
		set("module_path", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: modulePath})
		changed = true
	}
	if envs == nil {
		return writeYAMLDoc(path, &doc, changed)
	}
	seq := value("envs")
	if seq == nil {
		seq = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
		set("envs", seq)
	}
	if seq.Kind != yaml.SequenceNode {
		return false, fmt.Errorf("%s: envs is not a list", path)
	}
	have := map[string]bool{}
	for _, n := range seq.Content {
		have[n.Value] = true
	}
	for _, e := range envs {
		if !have[e] {
			seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: e})
			changed = true
		}
	}
	if value("monitoring") == nil {
		set("monitoring", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"})
		changed = true
	}
	return writeYAMLDoc(path, &doc, changed)
}

// writeYAMLDoc writes doc to path when changed.
func writeYAMLDoc(path string, doc *yaml.Node, changed bool) (bool, error) {
	if !changed {
		return false, nil
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return false, err
	}
	if err := atomicfile.WriteFile(path, out, 0o644); err != nil { //nolint:gosec // forge.yaml is a checked-in project file
		return false, err
	}
	return true, nil
}

// monitoringEndpoint is a self-hosted OTLP endpoint, "" for Reliant's.
func monitoringEndpoint(m *config.MonitoringConfig) string {
	if m == nil {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(m.Endpoint), "/")
}

// monitoringConsoleURL is forge.yaml's console_url, "" when unset.
func monitoringConsoleURL(m *config.MonitoringConfig) string {
	if m == nil {
		return ""
	}
	return m.ConsoleURL
}

// otelEnv is the standard OpenTelemetry environment an app needs: the
// exporter, its auth, and the resource attributes forge's own apps carry
// (kcl/render.k), so the app's telemetry reads exactly like a forge-built
// one's.
func otelEnv(endpoint, headers, env, project, service string) map[string]string {
	if service == "" {
		service = "<your service name>"
	}
	out := map[string]string{
		"OTEL_EXPORTER_OTLP_ENDPOINT": endpoint,
		"OTEL_EXPORTER_OTLP_PROTOCOL": otlpProtocol,
		"OTEL_SERVICE_NAME":           service,
		"OTEL_RESOURCE_ATTRIBUTES": "deployment.environment.name=" + env + ",service.namespace=" + project +
			",service.version=<your version, e.g. $GIT_SHA>",
	}
	if headers != "" {
		out["OTEL_EXPORTER_OTLP_HEADERS"] = headers
	}
	return out
}

// writeObsInit prints the document, or the human view of it.
func writeObsInit(d obsInitDeps, doc obsInitDoc, o obsInitOpts, selfHosted string) error {
	if o.jsonOut {
		enc := json.NewEncoder(d.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(doc)
	}
	w := d.Stdout
	for _, p := range doc.WroteFiles {
		fmt.Fprintf(w, "wrote %s (the env's declaration, monitoring on) — commit it\n", p)
	}
	if len(doc.WroteFiles) > 0 {
		fmt.Fprintln(w)
	}
	for _, e := range doc.Envs {
		if selfHosted != "" {
			fmt.Fprintf(w, "%s: exporting to your OTLP backend %s (no token: forge mints none for a self-hosted backend)\n", e.Env, selfHosted)
		} else {
			fmt.Fprintf(w, "%s: registered on %s as project %s (environment %s)\n", e.Env, doc.ControlPlane, doc.Project, e.EnvironmentID)
			fmt.Fprintf(w, "  ingest token %q (%s), expires %s — printed ONCE: store the header below as a secret\n",
				e.Token.Name, e.Token.ID, e.Token.ExpiresAt.Format("2006-01-02"))
			fmt.Fprintf(w, "  nothing renews it (90 days at most): before it expires, `forge obs tokens rotate %s --grace 24h` and roll the new header out\n",
				e.Token.ID)
		}
		fmt.Fprintln(w)
		keys := make([]string, 0, len(e.OTelEnv))
		for k := range e.OTelEnv {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "  export %s=%s\n", k, envQuote(e.OTelEnv[k]))
		}
		fmt.Fprintln(w)
		if e.DashboardURL != "" {
			fmt.Fprintf(w, "  dashboards: %s\n\n", e.DashboardURL)
		}
	}
	fmt.Fprintln(w, "Set OTEL_SERVICE_NAME per deployable and service.version to the version you deploy (a git SHA):")
	fmt.Fprintln(w, "`forge obs check` judges each new service.version against the one before it, even with no deploy recorded.")
	if selfHosted == "" && len(doc.Envs) > 0 {
		env := doc.Envs[0].Env
		fmt.Fprintf(w, "\nnext: forge obs status %s · forge obs issues %s · forge obs receivers create … (alerts to Slack, PagerDuty, a webhook)\n", env, env)
	}
	fmt.Fprintln(w, "per language: forge skill load observability (\"Monitoring only, any language\")")
	return nil
}

// envQuote single-quotes v for a POSIX shell when it needs it.
func envQuote(v string) string {
	if v != "" && strings.IndexFunc(v, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./:=,@", r))
	}) < 0 {
		return v
	}
	return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
}

// stdinConfirm asks on errOut and reads y/n from in, when in is a terminal.
func stdinConfirm(in io.Reader, errOut io.Writer) func(string) (bool, bool) {
	return func(q string) (bool, bool) {
		f, ok := in.(*os.File)
		if !ok || !term.IsTerminal(int(f.Fd())) { //nolint:gosec // a file descriptor fits in int
			return false, false
		}
		fmt.Fprintf(errOut, "%s [Y/n] ", q)
		line, _ := bufio.NewReader(in).ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "", "y", "yes":
			return true, true
		}
		return false, true
	}
}

// ── the control plane ───────────────────────────────────────────────────

// wireIngestToken is TelemetryTokenService's IngestToken.
type wireIngestToken struct {
	ID            string     `json:"id"`
	OrgID         string     `json:"orgId"`
	EnvironmentID string     `json:"environmentId"`
	Name          string     `json:"name"`
	CreatedBy     string     `json:"createdBy"`
	IssuedAt      time.Time  `json:"issuedAt"`
	ExpiresAt     time.Time  `json:"expiresAt"`
	RevokedAt     *time.Time `json:"revokedAt,omitempty"`
	RevokedReason string     `json:"revokedReason,omitempty"`
	ReplacedBy    string     `json:"replacedBy,omitempty"`
}

// mintedIngestToken is Create/RotateIngestToken's answer.
type mintedIngestToken struct {
	Token        wireIngestToken  `json:"token"`
	JWT          string           `json:"jwt"`
	OTLPHeaders  string           `json:"otlpHeaders"`
	OTLPEndpoint string           `json:"otlpEndpoint"`
	Previous     *wireIngestToken `json:"previous,omitempty"`
}

// createIngestToken mints a token for environmentID.
func createIngestToken(ctx context.Context, s obsSession, environmentID, name string, ttl time.Duration) (mintedIngestToken, error) {
	req := map[string]any{"orgId": s.OrgID, "environmentId": environmentID, "name": name}
	if ttl > 0 {
		req["ttl"] = fmt.Sprintf("%ds", int64(ttl.Seconds()))
	}
	var out mintedIngestToken
	if err := s.Caller.Call(ctx, procCreateIngestToken, req, &out); err != nil {
		return out, ingestTokenErr(err)
	}
	return out, nil
}

// requireIngestTokens asks whether the session's control plane serves
// TelemetryTokenService, with a read-only ListIngestTokens. Only "it does
// not" (Unimplemented, or a 404 from a server with no such route) refuses:
// any other answer means the service is there, and the mint that follows
// reports its own errors.
func requireIngestTokens(ctx context.Context, s obsSession) error {
	var out struct {
		Tokens []wireIngestToken `json:"tokens"`
	}
	err := s.Caller.Call(ctx, procListIngestTokens, map[string]any{"orgId": s.OrgID}, &out)
	var ce *cloud.Error
	if errors.As(err, &ce) && (ce.Code == cloud.CodeUnimplemented || ce.HTTPStatus == 404) {
		return ingestTokenErr(err)
	}
	return nil
}

// ingestTokenErr explains the refusals a person can act on.
func ingestTokenErr(err error) error {
	var ce *cloud.Error
	if errors.As(err, &ce) {
		switch {
		case ce.Code == cloud.CodeUnimplemented || ce.HTTPStatus == 404:
			return obsbackend.Undetermined("this control plane has no telemetry ingest tokens yet (TelemetryTokenService): it needs a newer control plane").Cause(err)
		case ce.Code == cloud.CodePermissionDenied:
			return obsbackend.Wrong("minting an ingest token needs a person who is a member of the organization holding the obs:write permission "+
				"(owners and admins hold it); ask an owner to grant it: %v", err)
		}
	}
	return obsbackend.MapCloudError(err)
}

// connectObsControlPlane signs in to controlPlane for project: the
// credential (flag-free: env var, `forge login`, or the host's helper), the
// hosted project (created on a person's first use when bind), and the org.
// No credential is a refusal naming `forge login`: monitoring has no
// anonymous path.
func connectObsControlPlane(ctx context.Context, controlPlane, project, org string, bind bool) (obsSession, error) {
	ep, err := cloud.ResolveEndpoint("", &cloud.Declaration{Endpoint: controlPlane})
	if err != nil {
		return obsSession{}, err
	}
	ep.ProjectName = project
	if pid := boundProjectID(ep); pid != "" {
		ep.ProjectID = pid
	}
	cred, err := cloud.ResolveCredential("", ep)
	if err != nil {
		return obsSession{}, obsbackend.Wrong("forge obs needs you signed in to %s (monitoring has no anonymous path): %v", ep.URL, err).
			With(obsbackend.Cmd("forge", "login", "--endpoint", ep.URL))
	}
	if cred.Kind == "human" && bind && ep.ProjectID == "" {
		req := map[string]any{"name": project, "create": true}
		if org != "" {
			req["orgId"] = org
		}
		var reply struct {
			ProjectID string `json:"projectId"`
			OrgID     string `json:"orgId"`
		}
		if err := cloud.NewClient(ep, cred).Call(ctx, "controlplane.v1.DeployService/ResolveHostedProject", req, &reply); err != nil {
			return obsSession{}, obsbackend.MapCloudError(fmt.Errorf("create or resolve project %s: %w", project, err))
		}
		ep.ProjectID = reply.ProjectID
	} else if org != "" {
		return obsSession{}, obsbackend.Wrong("--org only chooses where a NEW project is created; project %s already resolves through your credential", project)
	}
	client := cloud.NewClient(ep, cred)
	orgID, err := cloud.ResolveOrganization(ctx, client)
	if err != nil {
		return obsSession{}, obsbackend.MapCloudError(err)
	}
	return obsSession{Caller: client, OrgID: orgID, ControlPlane: ep.URL}, nil
}

// boundProjectID is the project id .forge/hosted-projects.json bound for ep,
// "" when there is none (or no forge.yaml to hold it).
func boundProjectID(ep cloud.Endpoint) string {
	bound, err := projectEndpoint("", &cloud.Declaration{Endpoint: ep.URL})
	if err != nil || bound.ProjectName != ep.ProjectName {
		return ""
	}
	return bound.ProjectID
}

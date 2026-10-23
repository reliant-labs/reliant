package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/frontenddeps"
	"github.com/reliant-labs/forge/internal/nodeengine"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// runBufGenerateGo runs buf generate using the project's buf.gen.yaml for Go stubs.
func runBufGenerateGo(projectDir string) error {
	// Vendor forge/v1/forge.proto and buf/validate/validate.proto if the
	// project's protos import them, so buf resolves the annotations offline.
	if err := ensureVendoredProtos(projectDir); err != nil {
		return fmt.Errorf("vendor annotation protos: %w", err)
	}

	// Create a default buf.gen.yaml only if one doesn't exist
	if _, err := os.Stat(filepath.Join(projectDir, "buf.gen.yaml")); os.IsNotExist(err) {
		if err := writeDefaultBufGenYaml(projectDir); err != nil {
			return fmt.Errorf("failed to create buf.gen.yaml: %w", err)
		}
		fmt.Println("📝 Generated default buf.gen.yaml")
	}

	// The vendored annotation protos are imports only; a project's own
	// template that would compile them to Go is refused before buf writes a
	// second registration of their descriptors.
	if err := checkGoTemplateSkipsVendoredProtos(projectDir, "buf.gen.yaml", nil); err != nil {
		return err
	}

	cmd := exec.Command("buf", "generate")
	cmd.Dir = projectDir
	if err := withPinnedTools(cmd, projectDir); err != nil {
		return err
	}
	// After withPinnedTools: the pre-flight resolves plugins on the PATH
	// buf will actually get. See generate_buf_preflight.go.
	if err := preflightBufTemplate(projectDir, "buf.gen.yaml", nil, cmd.Env); err != nil {
		return err
	}
	fmt.Println("🔨 Running buf generate (Go stubs)...")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("buf generate failed: %w", err)
	}

	fmt.Println("  ✅ Go protobuf + Connect stubs generated")
	return nil
}

// bufGenGoInputs is the `inputs:` block of every Go buf template forge
// writes: all of proto/, except the vendored annotation protos, which are
// COMPILE inputs only. Their Go ships with forge/pkg/forgepb and the
// protovalidate runtime; a local copy would register the descriptor twice and
// panic every binary at init (checkGoTemplateSkipsVendoredProtos refuses a
// template that would). internal/templates/project/buf.gen.yaml spells the
// same block.
const bufGenGoInputs = `inputs:
  - directory: proto
    exclude_paths:
      # Vendored annotation protos are COMPILE inputs only — never
      # code-generated (a local copy would double-register the descriptor).
      - proto/forge/v1
      - proto/buf/validate
`

// writeDefaultBufGenYaml writes a standard buf.gen.yaml with Go plugins.
// Defaults to `local:` plugins (resolved from $PATH) so anonymous users
// can `forge generate` without `buf registry login`. Users who want
// BSR-hosted plugins can edit the file to `remote:` (see file comments).
func writeDefaultBufGenYaml(projectDir string) error {
	config := `version: v2
# Default: local plugins resolved from $PATH. Anonymous users can
# ` + "`forge generate`" + ` without touching the BSR. Run ` + "`forge tools install`" + `
# (or ` + "`go install`" + ` the two binaries listed below) to put them on PATH.
#
# To opt back into BSR-hosted plugins, replace ` + "`local:`" + ` with ` + "`remote:`" + `
# and the binary name with the BSR plugin path, e.g.
#   - remote: buf.build/protocolbuffers/go
#   - remote: buf.build/connectrpc/go
#
# OpenAPI specs: set ` + "`api.openapi: true`" + ` in forge.yaml. Forge invokes
# protoc-gen-connect-openapi via a synthesized template; you do NOT
# need to add it here. Install with:
#   ` + openAPIPluginInstallCmd + `
` + bufGenGoInputs + `plugins:
  - local: protoc-gen-go
    out: gen
    opt:
      - paths=source_relative
  - local: protoc-gen-connect-go
    out: gen
    opt:
      - paths=source_relative
`
	return atomicfile.WriteFile(filepath.Join(projectDir, "buf.gen.yaml"), []byte(config), 0644)
}

// runBufGenerateTypeScript runs buf generate for TypeScript stubs in a Next.js frontend.
// It runs buf from the project root to avoid picking up node_modules proto files,
// using --path flags to scope generation and --template to point at the frontend's buf.gen.yaml.
//
// When the project opted into the pnpm-workspace layout
// (frontend.workspaces: true), TS stubs go to the shared
// packages/api/src/gen/ once via runBufGenerateTypeScriptWorkspace and
// the per-frontend buf step is short-circuited. The per-frontend code
// path below is preserved unchanged for the default layout so existing
// snapshot tests stay stable.
func runBufGenerateTypeScript(fe config.FrontendConfig, cfg *config.ProjectConfig, projectDir string) error {
	if cfg != nil && cfg.IsFrontendWorkspacesEnabled() {
		return runBufGenerateTypeScriptWorkspace(cfg, projectDir)
	}
	// This site FAILS LOUDLY where the other emitters skip, and that is
	// deliberate: the error is what tells a user their `path:` is wrong
	// before `buf generate` quietly produces no stubs at all. So !ok
	// returns an error rather than a silent continue — collapsing it into
	// a skip would convert a clear diagnostic into the silence it exists
	// to prevent.
	feDir, ok := fe.Dir(projectDir)
	if !ok {
		return fmt.Errorf("frontend %s has no directory in this project: its path is outside "+
			"the project root or its code comes from another repository", fe.Name)
	}

	absFeDir := filepath.Join(projectDir, feDir)
	if !dirExists(absFeDir) {
		return fmt.Errorf("frontend directory %s not found", feDir)
	}

	// One writer per stub directory: a project whose own template writes
	// this frontend's src/gen keeps it. See generate_buf_ts_writers.go.
	owner, err := decideTSStubOwnership(projectDir, fe.Name, feDir)
	if err != nil {
		return err
	}
	if owner != tsStubsForge {
		return nil
	}

	fmt.Printf("🔨 Generating TypeScript stubs for %s...\n", fe.Name)

	// Ensure the frontend has a buf.gen.yaml with out: relative to project root.
	// Default to the local TypeScript plugin (./<feDir>/node_modules/.bin/protoc-gen-es)
	// so anonymous users can `forge generate` without `buf registry login`. The
	// path is relative to where `buf generate` runs (project root via --template),
	// not relative to this YAML file. Mirrors the template at
	// internal/templates/frontend/{nextjs,react-native}/buf.gen.yaml.tmpl.
	feBufGen := filepath.Join(absFeDir, "buf.gen.yaml")
	if _, err := os.Stat(feBufGen); os.IsNotExist(err) {
		// include_imports must be a plugin-level field in buf.gen.yaml v2,
		// not an `opt:` entry — protoc-gen-es rejects unknown opts.
		feSlash := filepath.ToSlash(feDir)
		tsConfig := fmt.Sprintf(`version: v2
# Local TypeScript plugin (no BSR auth needed). Run 'npm install' in
# %s/ before 'forge generate' so node_modules/.bin/protoc-gen-es exists.
# To switch to BSR-hosted plugin, replace 'local:' line with:
#   - remote: buf.build/bufbuild/es
plugins:
  - local: ./%s/node_modules/.bin/protoc-gen-es
    out: %s/src/gen
    include_imports: true
    opt:
      - target=ts
      - import_extension=.js
`, feSlash, feSlash, feSlash)
		if err := atomicfile.WriteFile(feBufGen, []byte(tsConfig), 0644); err != nil {
			return fmt.Errorf("failed to write TypeScript buf config: %w", err)
		}
	}

	// Build command: run from project root, use --template with relative path to frontend's buf.gen.yaml
	templatePath := filepath.Join(feDir, "buf.gen.yaml")
	template := templatePath
	// inlineTemplate is the retargeted body when template carries one
	// instead of a path.
	var inlineTemplate []byte

	// Verify the local TS plugin is on disk before invoking buf — otherwise
	// buf emits a confusing "fork/exec: no such file" error.
	//
	// This FAILS the run rather than skipping. A skip here is not survivable
	// even though it looks like the polite option: the hooks and mocks steps
	// downstream emit TypeScript importing `@/gen/<svc>/v1/<svc>_pb` for
	// every service, and those modules are exactly what this step writes. Skip
	// it and `forge generate` exits 0 having produced a frontend that cannot
	// compile — the diagnostic then surfaces at `next build`, usually in
	// another repo, blamed on whichever service was added most recently
	// rather than on the missing node_modules that actually caused it.
	if usesLocalTSPlugin(feBufGen) {
		pluginRel, ok := resolveLocalTSPluginRel(projectDir, feDir)
		if !ok {
			// Install the frontend's dependencies rather than asking the
			// user to. See ensureTSPluginInstalled.
			if err := ensureTSPluginInstalled(fe.Name, projectDir, feDir); err != nil {
				return err
			}
			pluginRel, ok = resolveLocalTSPluginRel(projectDir, feDir)
		}
		if !ok {
			return fmt.Errorf("%s: @bufbuild/protoc-gen-es is not installed, so no TypeScript "+
				"stubs can be generated — run `npm install` in %s and re-run generate "+
				"(continuing would emit hooks and mocks importing _pb modules that do not exist)",
				fe.Name, feDir)
		}
		if err := ensureTSPluginMatchesLock(fe.Name, projectDir, feDir, pluginRel); err != nil {
			return err
		}
		// Point THIS RUN at wherever the plugin actually is, by handing buf
		// the retargeted template as inline data. The committed buf.gen.yaml
		// is never rewritten: the right path depends on the generating
		// machine's node_modules layout (a dev build hoists it to the
		// project root under forge's web-runtime bridge; CI's `npm ci` puts
		// it in the frontend), so writing it back made every dev regenerate
		// commit a path CI's regenerate then reverted — a verify-generated
		// failure caused by nothing but where npm put a directory.
		body, err := os.ReadFile(feBufGen)
		if err != nil {
			return fmt.Errorf("read %s: %w", feBufGen, err)
		}
		if retargeted, changed := retargetedTSTemplate(body, pluginRel); changed {
			template = string(retargeted)
			inlineTemplate = retargeted
		}
	}

	// Every local plugin of the template buf is about to get — the
	// retargeted one when the step above rewrote it — resolved before buf
	// runs. See generate_buf_preflight.go.
	env := withNodeNoDeprecation()
	if err := preflightBufTemplate(projectDir, filepath.ToSlash(templatePath), inlineTemplate, env); err != nil {
		return err
	}

	args := []string{"generate", "--template", template}

	// Include every proto/<sub>/ with .proto files so pack-emitted services
	// (e.g. proto/audit/ from audit-log) participate in TypeScript codegen,
	// not just the canonical proto/services + proto/api pair.
	for _, p := range discoverProtoSubdirs(projectDir) {
		args = append(args, "--path", p)
	}

	cmd := exec.Command("buf", args...)
	cmd.Dir = projectDir
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("TypeScript generation failed for %s: %w", fe.Name, err)
	}

	fmt.Printf("  ✅ TypeScript stubs generated for %s\n", fe.Name)
	return nil
}

// runBufGenerateTypeScriptWorkspace runs buf generate for TypeScript
// stubs in pnpm-workspaces mode. Output goes to packages/api/src/gen
// once; the per-frontend buf step is skipped because each frontend now
// consumes the shared workspace package via `"@<scope>/api": "workspace:*"`.
//
// The TS plugin is sourced from the first frontend's node_modules/.bin/
// to avoid making packages/api/ itself a Node project with its own
// installed plugin. That keeps the workspace package free of build-time
// dependencies — pnpm install once at the workspace root populates
// every node_modules/.bin/ in lockstep.
func runBufGenerateTypeScriptWorkspace(cfg *config.ProjectConfig, projectDir string) error {
	// Pick the first nextjs / react-native / vite-spa frontend to source
	// the protoc-gen-es plugin from. We prefer nextjs > vite-spa > react-
	// native because the web-target frontends are likeliest to already
	// have node_modules populated in dev.
	var pluginFrontend *config.FrontendConfig
	priority := map[string]int{"nextjs": 1, "vite-spa": 2, "react-native": 3}
	bestRank := 99
	for i := range cfg.Frontends {
		fe := cfg.Frontends[i]
		rank, ok := priority[strings.ToLower(fe.Type)]
		if !ok {
			continue
		}
		if rank < bestRank {
			bestRank = rank
			pluginFrontend = &cfg.Frontends[i]
		}
	}
	if pluginFrontend == nil {
		// No TS-capable frontend yet — nothing to generate. forge scaffold
		// frontend will rerun this on a future generate cycle.
		return nil
	}

	feDir, ok := pluginFrontend.Dir(projectDir)
	if !ok {
		return fmt.Errorf("frontend %s has no directory in this project to resolve the "+
			"TypeScript plugin from", pluginFrontend.Name)
	}
	absFeDir := filepath.Join(projectDir, feDir)

	// Ensure the workspace buf.gen.yaml exists at packages/api/buf.gen.yaml.
	// We write it relative to project root so `out: packages/api/src/gen`
	// resolves correctly when buf runs from projectDir.
	bufGenPath := filepath.Join(projectDir, "packages", "api", "buf.gen.yaml")
	if _, err := os.Stat(bufGenPath); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(bufGenPath), 0o755); err != nil {
			return fmt.Errorf("create packages/api dir: %w", err)
		}
		feSlash := filepath.ToSlash(feDir)
		body := fmt.Sprintf(`version: v2
# Local TypeScript plugin (no BSR auth needed). Sourced from
# %s/node_modules/.bin/ — the workspace-layout convention picks one
# frontend to install protoc-gen-es; all frontends consume the
# generated output via the @<scope>/api workspace package.
#
# To switch to BSR-hosted plugin, replace 'local:' with
#   - remote: buf.build/bufbuild/es
plugins:
  - local: ./%s/node_modules/.bin/protoc-gen-es
    out: packages/api/src/gen
    include_imports: true
    opt:
      - target=ts
      - import_extension=.js
`, feSlash, feSlash)
		if err := atomicfile.WriteFile(bufGenPath, []byte(body), 0o644); err != nil {
			return fmt.Errorf("write packages/api/buf.gen.yaml: %w", err)
		}
	}

	// Verify the local TS plugin exists before invoking buf — same
	// pre-flight, and the same hard failure, as the per-frontend path. See
	// runBufGenerateTypeScript for why a skip here is not survivable.
	if usesLocalTSPlugin(bufGenPath) {
		pluginPath := filepath.Join(absFeDir, "node_modules", ".bin", "protoc-gen-es")
		_, statErr := os.Stat(pluginPath)
		if os.IsNotExist(statErr) {
			// Same install-first policy as the per-frontend path. A
			// workspace install at the frontend reconciles the hoisted
			// root tree too, since npm resolves workspace members from it.
			if instErr := ensureTSPluginInstalled(pluginFrontend.Name, projectDir, feDir); instErr != nil {
				return instErr
			}
			_, statErr = os.Stat(pluginPath)
		}
		if os.IsNotExist(statErr) {
			return fmt.Errorf("workspace TS gen: @bufbuild/protoc-gen-es is not installed, so no "+
				"TypeScript stubs can be generated — run `npm install` (or `pnpm install`) at the "+
				"project root and re-run generate (continuing would emit hooks and mocks importing "+
				"_pb modules that do not exist); looked in %s", pluginPath)
		}
	}

	if usesLocalTSPlugin(bufGenPath) {
		rel := "./" + filepath.ToSlash(filepath.Join(feDir, "node_modules", ".bin", "protoc-gen-es"))
		if err := ensureTSPluginMatchesLock(pluginFrontend.Name, projectDir, feDir, rel); err != nil {
			return err
		}
	}

	env := withNodeNoDeprecation()
	if err := preflightBufTemplate(projectDir, "packages/api/buf.gen.yaml", nil, env); err != nil {
		return err
	}

	fmt.Println("🔨 Generating TypeScript stubs into packages/api/src/gen...")
	relativeTemplate := filepath.Join("packages", "api", "buf.gen.yaml")
	args := []string{"generate", "--template", relativeTemplate}
	for _, p := range discoverProtoSubdirs(projectDir) {
		args = append(args, "--path", p)
	}

	cmd := exec.Command("buf", args...)
	cmd.Dir = projectDir
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("workspace TypeScript generation failed: %w", err)
	}
	fmt.Println("  ✅ TypeScript stubs generated into packages/api/src/gen")
	return nil
}

// ensureTSPluginInstalled installs a frontend's node_modules so that
// protoc-gen-es exists, when `forge generate` finds it missing.
//
// INSTALLING is the policy here, not warning and not failing first, and it
// follows the convention forge already applies at every other point where a
// missing toolchain would stop a run: `forge env up` installs a frontend's deps
// before the dev loop (ensureFrontendDeps), and `forge scaffold frontend`
// installs them the moment the directory exists (runFrontendNpmInstall). A
// generate that needs the plugin is the same situation, and a user who has to
// be told to run `npm ci` before re-running generate has been handed a step
// forge could have taken.
//
// `npm ci` is preferred because a lockfile is the exact thing this needs to
// reproduce, and it is what CI runs; without a lockfile there is nothing for
// `ci` to honour, so we fall back to `npm install`. "Without a lockfile" means
// at the root npm actually installs at, which is not the frontend when a
// workspace root adopts it — see frontenddeps.NpmInstallVerb. A failure returns an error
// naming the frontend, the directory, and the command — the run still fails,
// because the alternative is the silent skip this whole path exists to
// prevent.
//
// FORGE_SKIP_NPM_INSTALL=1 short-circuits the install, matching the seam
// runFrontendNpmInstall already established for tests that care about the
// generate logic rather than about node_modules. The caller re-resolves the
// plugin afterwards and produces the actionable error if it is still absent,
// so skipping here never converts a failure into a success.
func ensureTSPluginInstalled(feName, projectDir, feDir string) error {
	absFeDir := filepath.Join(projectDir, feDir)
	if _, err := os.Stat(filepath.Join(absFeDir, "package.json")); err != nil {
		// Not a node project (or no manifest) — nothing to install. The
		// caller's re-resolve turns this into the actionable error.
		return nil
	}
	if os.Getenv("FORGE_SKIP_NPM_INSTALL") != "" {
		return nil
	}

	if err := nodeengine.Check(absFeDir); err != nil {
		return fmt.Errorf("%s: @bufbuild/protoc-gen-es is not installed, and npm was not run: %w", feName, err)
	}
	verb := frontenddeps.NpmInstallVerb(absFeDir)
	fmt.Printf("📦 %s: node_modules missing — running `npm %s` in %s so protoc-gen-es exists\n",
		feName, verb, feDir)

	cmd := exec.Command("npm", verb, "--no-audit", "--no-fund")
	cmd.Dir = absFeDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: @bufbuild/protoc-gen-es is not installed and `npm %s` in %s failed: %w "+
			"— run it manually to see the full output, then re-run generate "+
			"(continuing would emit hooks and mocks importing _pb modules that do not exist)",
			feName, verb, feDir, err)
	}
	return nil
}

// withNodeNoDeprecation returns the current environment with
// --no-deprecation merged into NODE_OPTIONS (preserving any options the
// caller already set). buf runs the TypeScript plugin (protoc-gen-es)
// as a node script, and node's deprecation chatter — a DEP warning plus
// "(Use `node --trace-warnings ...`)" once per plugin process — is pure
// noise in `forge generate` output. Only deprecation WARNINGS are
// silenced; real plugin failures still fail the buf run and stream to
// stderr untouched.
func withNodeNoDeprecation() []string {
	env := os.Environ()
	const flag = "--no-deprecation"
	for i, kv := range env {
		v, ok := strings.CutPrefix(kv, "NODE_OPTIONS=")
		if !ok {
			continue
		}
		if !strings.Contains(v, flag) {
			env[i] = "NODE_OPTIONS=" + strings.TrimSpace(v+" "+flag)
		}
		return env
	}
	return append(env, "NODE_OPTIONS="+flag)
}

// usesLocalTSPlugin reports whether the frontend buf.gen.yaml at path uses
// a `local:` plugin entry that points at protoc-gen-es (the default since
// the BSR removal). Best-effort — if the file is unreadable we assume yes
// to err on the side of running the existence check.
// resolveLocalTSPluginRel finds the protoc-gen-es binary for one frontend and
// returns its path RELATIVE TO THE PROJECT ROOT, which is where forge invokes
// `buf generate --template`. Reports false when no copy is installed anywhere.
//
// Two locations, in the order npm itself would resolve them:
//
//  1. frontends/<name>/node_modules/.bin — an ordinary standalone frontend.
//  2. <project>/node_modules/.bin — a WORKSPACE install. Forge's dev bridge
//     writes a gitignored workspace root (see
//     internal/generator/frontend_webruntime_devlink.go), and npm hoists every
//     member's dependencies to the root, so the frontend-local directory does
//     not exist at all in a bridged project.
//
// Looking in only the first place is what made a bridged project generate NO
// TypeScript stubs: the plugin check above reported "not installed yet",
// `buf generate` never ran for the frontend, and src/gen/ stayed empty. The
// symptom surfaced much later and far away, as tsc reporting TS2307 "Cannot
// find module '@/gen/services/<svc>/v1/<svc>_pb'" against files forge had just
// generated — a missing-module error for code the generator was silently
// skipped for, which reads as a codegen bug rather than a missing plugin.
func resolveLocalTSPluginRel(projectDir, feDir string) (string, bool) {
	candidates := []string{
		filepath.Join(feDir, "node_modules", ".bin", "protoc-gen-es"),
		filepath.Join("node_modules", ".bin", "protoc-gen-es"),
	}
	for _, rel := range candidates {
		if _, err := os.Stat(filepath.Join(projectDir, rel)); err == nil {
			return "./" + filepath.ToSlash(rel), true
		}
	}
	return "", false
}

// localTSPluginLineRe matches the `- local: …protoc-gen-es` line in a
// buf.gen.yaml, capturing the leading punctuation so a rewrite preserves the
// file's own indentation.
var localTSPluginLineRe = regexp.MustCompile(`(?m)^(\s*-\s*local:\s*)\S*protoc-gen-es\s*$`)

// retargetedTSTemplate returns body with its local protoc-gen-es path set to
// want, and whether that changed anything. It is pure: the result is passed to
// `buf generate --template` as inline data, and the committed file is left as
// its author wrote it. A plugin line it cannot recognise is one a user has
// restructured; forge leaves that alone.
func retargetedTSTemplate(body []byte, want string) ([]byte, bool) {
	if localTSPluginLineRe.FindIndex(body) == nil {
		return body, false
	}
	updated := localTSPluginLineRe.ReplaceAll(body, []byte("${1}"+want))
	return updated, !bytes.Equal(updated, body)
}

func usesLocalTSPlugin(bufGenPath string) bool {
	data, err := os.ReadFile(bufGenPath)
	if err != nil {
		return true
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		// Match e.g. "- local: ./frontends/web/node_modules/.bin/protoc-gen-es"
		if strings.HasPrefix(trimmed, "- local:") && strings.Contains(trimmed, "protoc-gen-es") {
			return true
		}
		if strings.HasPrefix(trimmed, "- remote:") {
			return false
		}
	}
	return false
}

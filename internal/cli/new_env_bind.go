package cli

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/internal/naming"
)

// envBinders maps a `--bind <workload>=<target>` target onto the binder a
// scaffolded env declares for it (deploy/kcl/cloud/main.k.tmpl). The binder
// is what applies the runtime; a derived env that has been restructured and
// no longer declares it fails the render with KCL naming the lambda, rather
// than forge guessing at a runtime literal.
var envBinders = map[string]string{
	"hosted":  "_hosted",
	"cluster": "_on_cluster",
}

// frontendBinders is the same map for a FRONTEND's binding line: a frontend
// binds a runtime exactly as a workload does (ADR 0002 §6), through its own
// binders, since its runtimes differ (no cluster; a bucket instead).
var frontendBinders = map[string]string{
	"hosted": "_hosted_frontend",
	"bucket": "_on_bucket",
}

// bindingLine matches one scaffolded workload binding, capturing the binder
// and the bound reference: `<binder>(wl.<ident>)` for a workload workloads.k
// declares, or `<binder>(_<ident>)` for one the env declares itself — a
// hosted env's `_api`, which `--bind api=cluster` rebinds.
func bindingLine(ident string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^(\s*)(_[a-z_]+)\((wl\.` + regexp.QuoteMeta(ident) + `|_` + regexp.QuoteMeta(ident) + `)\)`)
}

// frontendBindingLine matches one scaffolded frontend binding,
// `<binder>(_<ident>_frontend)`.
func frontendBindingLine(ident string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^(\s*)(_[a-z_]+)\(_` + regexp.QuoteMeta(ident) + `_frontend\)`)
}

// bindTargetKnown reports whether target names a binder for a workload or a
// frontend. Which one applies is decided against the env file, by the line
// the name binds.
func bindTargetKnown(target string) bool {
	_, w := envBinders[target]
	_, f := frontendBinders[target]
	return w || f
}

// bindUsage is the one spelling of the --bind grammar every refusal names.
const bindUsage = "write it as <workload>=hosted|cluster, or <frontend>=hosted|bucket"

var (
	bundleProjectLine = regexp.MustCompile(`(?m)^(\s+)project = .*$`)
	controlPlaneField = regexp.MustCompile(`(?m)^\s+control_plane\s*=`)
)

// validateEnvBinds checks every `--bind` before anything is written, so a
// typo never leaves a half-made env directory behind.
func validateEnvBinds(binds []string) error {
	for _, b := range binds {
		name, target, ok := strings.Cut(b, "=")
		if !ok || name == "" || !bindTargetKnown(target) {
			return cliutil.UserErr("forge env new", fmt.Sprintf("--bind %q", b), "", bindUsage)
		}
	}
	return nil
}

// bindEnvContent validates and transforms the entire binding set before writing.
func bindEnvContent(projectDir, env, content string, binds []string) (string, error) {
	// The project name tells the env's own API workload (the project
	// binary's `server`) from any other; an unreadable forge.yaml only costs
	// the more specific refusal below.
	projectName := ""
	if cfg, err := loadProjectConfigFrom(filepath.Join(projectDir, defaultProjectConfigFile)); err == nil {
		projectName = cfg.Name
	}
	hosted := false
	for _, b := range binds {
		name, target, ok := strings.Cut(b, "=")
		if !ok || name == "" || !bindTargetKnown(target) {
			return "", cliutil.UserErr("forge env new", fmt.Sprintf("--bind %q", b), "", bindUsage)
		}
		rebound, err := rebindOne(content, projectName, env, b, naming.KCLIdentifier(name), target)
		if err != nil {
			return "", err
		}
		content = rebound
		hosted = hosted || target == "hosted"
	}
	if hosted && !controlPlaneField.MatchString(content) {
		loc := bundleProjectLine.FindStringSubmatchIndex(content)
		if loc == nil {
			return "", cliutil.UserErr("forge env new",
				fmt.Sprintf("deploy/kcl/%s/main.k binds something hosted but has no Bundle `project = ` line to add control_plane after", env),
				"", "add `control_plane = forge.ControlPlane {}` to the env's Bundle by hand")
		}
		indent := content[loc[2]:loc[3]]
		decl := "\n" + indent + "# Where the hosted-bound workloads and frontends are published: Reliant cloud (set\n" +
			indent + "# `endpoint` for another control plane). There is no organization to declare: forge\n" +
			indent + "# reads it from the credential, and pushes images and the config bundle to\n" +
			indent + "# <registry_host>/<your org>/<project>.\n" +
			indent + "control_plane = forge.ControlPlane {}"
		content = content[:loc[1]] + decl + content[loc[1]:]
	}
	return content, nil
}

// rebindOne rewrites the ONE binding line that binds ident — a workload's or
// a frontend's — to target's binder. Exactly one of the two may match: a name
// is unique across an env's workloads and frontends (the Bundle refuses a
// duplicate), so a hit on both, or on neither, is a file forge will not guess
// about.
func rebindOne(content, projectName, env, bind, ident, target string) (string, error) {
	wl, fe := bindingLine(ident), frontendBindingLine(ident)
	wlHits, feHits := len(wl.FindAllStringIndex(content, -1)), len(fe.FindAllStringIndex(content, -1))
	switch {
	case wlHits == 1 && feHits == 0:
		binder, ok := envBinders[target]
		if !ok {
			return "", cliutil.UserErr("forge env new",
				fmt.Sprintf("--bind %s: %s is a workload, which binds hosted or cluster", bind, ident), "", bindUsage)
		}
		return wl.ReplaceAllString(content, "${1}"+binder+"(${3})"), nil
	case feHits == 1 && wlHits == 0:
		binder, ok := frontendBinders[target]
		if !ok {
			return "", cliutil.UserErr("forge env new",
				fmt.Sprintf("--bind %s: %s is a frontend, which binds hosted or bucket", bind, ident), "", bindUsage)
		}
		return fe.ReplaceAllString(content, "${1}"+binder+"(_"+ident+"_frontend)"), nil
	}
	// A hosted env runs its services and workers in its one API workload,
	// so a service has no line of its own there: the API is what moves.
	if api, ok := codegen.EnvServerWorkload(content, projectName); ok && wlHits == 0 && feHits == 0 {
		return "", cliutil.UserErr("forge env new",
			fmt.Sprintf("--bind %s: in deploy/kcl/%s/main.k, %s runs inside %s — the binary's `server`, which serves every service at the one origin the frontend calls — so it has no binding line of its own", bind, env, ident, api),
			"", fmt.Sprintf("rebind the whole API with --bind %s=%s, or split %s out by hand: its own `_<binder>(wl.%s)` line, and taking it out of `server`", strings.TrimPrefix(api, "_"), target, ident, ident))
	}
	return "", cliutil.UserErr("forge env new",
		fmt.Sprintf("--bind %s: deploy/kcl/%s/main.k has no single `_<binder>(wl.%s)` or `_<binder>(_%s_frontend)` line to rebind", bind, env, ident, ident),
		"", "bind it by hand in the env's `_workloads` or `frontends` list")
}

// checkHostedAdmissible runs the hosted deploy path's own admission plan over
// what a render publishes to a control plane, offline. nil when the env
// publishes nothing.
func checkHostedAdmissible(name string, raw []byte) error {
	e, err := parseKCLEntities(raw)
	if err != nil {
		return cliutil.WrapUserErr("forge env new --check", fmt.Sprintf("decode the %s render", name), "", "", err)
	}
	if !e.HasHosted() {
		return nil
	}
	group, err := buildHostedGroup(name, e)
	if err != nil {
		return cliutil.WrapUserErr("forge env new --check",
			fmt.Sprintf("env %q cannot be published to its control plane", name), "",
			"fix the declaration the error names", err)
	}
	if group == nil {
		return nil
	}
	if _, err := deploytarget.PreflightHosted(*group); err != nil {
		return cliutil.WrapUserErr("forge env new --check",
			fmt.Sprintf("the control plane would refuse env %q", name), "",
			"each refusal names the workload and the field; drop the field, or bind that workload to a cluster you operate",
			err)
	}
	return nil
}

package scaffold

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/naming"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// declareFrontendInKCL adds a freshly-scaffolded frontend to every
// environment's deploy/kcl/<env>/main.k, the same declaration
// `forge project new --frontend` renders from the env templates.
//
// It exists because the declaration is load-bearing, not decorative. In the
// dev env the frontend's port is a KCL fact (`plugin.resolve_port`), and
// `forge env up` reads the frontend set — its path, its dev_runner, and above
// all its port — from the render. A frontend present only in forge.yaml was
// bridged in with forge.yaml's literal `port:` (3000 by default), so the port
// preflight probed 3000, found it taken, and refused to start: the frontend
// the user had just added could not be run. In staging/prod the declaration
// is where its runtime is bound, and a frontend missing from it is one
// that ships nowhere without anyone having decided that.
//
// The edit is two insertions per env and never a rewrite:
//
//   - a named frontend declaration (and dev port when not pinned) before
//     the bundle, independent of database declarations;
//   - every env: a single binder(reference) line in `frontends += [...]`,
//     composing with existing frontends and rebindable by `env new --bind`.
//
// FAILURE IS ADVISORY, like declareWorkloadInKCL: the frontend's code is
// already written. An env file that is missing, already names the frontend,
// or has been restructured past the point where the anchors are unambiguous
// is left untouched and the stanza is printed for the user to place.
func declareFrontendInKCL(root, projectName, frontendName, kclType string, pinnedPort int, routes []string) {
	DeclareFrontendInKCL(root, projectName, frontendName, kclType, pinnedPort, routes)
}

// DeclareFrontendInKCL gives later frontends the same bindings as initial ones.
func DeclareFrontendInKCL(root, projectName, frontendName, kclType string, pinnedPort int, routes []string) {
	envs, err := os.ReadDir(filepath.Join(root, "deploy", "kcl"))
	if err != nil {
		return // no deploy/ tree (e.g. --disable deploy): nothing to declare into
	}
	for _, env := range envs {
		if !env.IsDir() {
			continue
		}
		rel := filepath.Join("deploy", "kcl", env.Name(), "main.k")
		path := filepath.Join(root, rel)
		raw, err := os.ReadFile(path)
		if err != nil {
			continue // not an env directory
		}
		binding := frontendBindingFor(string(raw), env.Name() == codegen.DevEnvName)
		updated, status := spliceFrontendIntoEnvKCL(string(raw), projectName, env.Name(), frontendName, kclType, binding, pinnedPort, routes)
		switch status {
		case frontendKCLApplied:
			if err := atomicfile.WriteFile(path, []byte(updated), 0o644); err != nil {
				fmt.Printf("\n⚠️  could not update %s: %v\n\n%s\n", rel, err,
					frontendKCLStanzaHint(projectName, env.Name(), frontendName, kclType, binding, pinnedPort, routes))
				continue
			}
			fmt.Printf("   - %s (frontend '%s' declared)\n", rel, frontendName)
			if binding == frontendHosted && !hostedCORSOrigin.MatchString(codegen.StripKCLProse(string(raw))) {
				fmt.Printf("     ↳ hosted: a service answers this site's browser calls only once its CORS_ORIGINS names it — add\n"+
					"       `CORS_ORIGINS = forge.WorkloadURL {workload = %q}, CORS_ALLOW_CREDENTIALS = \"true\"` to the `_hosted` binder's env in %s\n", frontendName, rel)
			}
		case frontendKCLAlreadyDeclared:
			// Quiet: an earlier run, or the user, already declared it.
		case frontendKCLNoAnchor:
			fmt.Printf("\n📝 %s\n", frontendKCLStanzaHint(projectName, env.Name(), frontendName, kclType, binding, pinnedPort, routes))
		}
	}
}

type frontendKCLStatus int

const (
	frontendKCLApplied frontendKCLStatus = iota
	frontendKCLAlreadyDeclared
	frontendKCLNoAnchor
)

// bundleSecretProviderLine is the `secret_provider = ` entry of the env's
// bundle literal — the one field every scaffolded env declares, at
// four-space indentation, directly inside `_bundle = forge.Bundle {`. It
// anchors the frontends insertion.
var bundleSecretProviderLine = regexp.MustCompile(`(?m)^    secret_provider = `)

// frontendDeclaredIn reports whether content already declares a
// forge.Frontend named name. A frontend name is unique within an env, so a
// `name = "<name>"` line inside any forge.Frontend literal is enough — the
// same looseness workloadDeclaredIn accepts, for the same reason: a false
// "already declared" costs a printed hint, a false "not declared" costs a
// duplicate that KCL would reject.
func frontendDeclaredIn(content, name string) bool {
	re := regexp.MustCompile(`(?s)forge\.Frontend\s*\{[^}]*?name\s*=\s*"` + regexp.QuoteMeta(name) + `"`)
	return re.MatchString(content)
}

// frontendPortIdent is the KCL variable holding a frontend's dev port.
func frontendPortIdent(frontendName string) string {
	return "_" + naming.KCLIdentifier(frontendName) + "_frontend_port"
}

// frontendBinding is how an env binds a frontend it declares.
type frontendBinding int

const (
	// frontendOnHost: the dev server (the dev env).
	frontendOnHost frontendBinding = iota
	// frontendHosted: the env's own `_hosted_frontend` binder — platform
	// static hosting, the way a hosted env (the scaffold default) binds the
	// frontend it was born with.
	frontendHosted
	// frontendOnBucket: the author's own bucket, for an env with no hosted
	// binder (scaffolded before hosting was the default).
	frontendOnBucket
)

// hostedFrontendBinder is the declaration of a hosted env's frontend binder.
var hostedFrontendBinder = regexp.MustCompile(`(?m)^_hosted_frontend\s*=\s*lambda\b`)

// frontendBindingFor picks how env binds a new frontend, from what the env
// file already declares.
func frontendBindingFor(content string, dev bool) frontendBinding {
	switch {
	case dev:
		return frontendOnHost
	case hostedFrontendBinder.MatchString(codegen.StripKCLProse(content)):
		return frontendHosted
	}
	return frontendOnBucket
}

func frontendKCLDeclaration(frontendName, kclType string, binding frontendBinding, pinnedPort int, routes []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "_%s_frontend = forge.Frontend {\n", naming.KCLIdentifier(frontendName))
	// Every frontend binds a runtime (ADR 0002 §6): the dev server in dev;
	// in a hosted env the env's own `_hosted_frontend` binder (platform
	// static hosting, bare image, API_URL resolved by the control plane);
	// otherwise the author's own bucket, whose name forge does not guess —
	// bucket names are global. `forge env new --check` refuses that
	// placeholder until it is filled.
	fmt.Fprintf(&b, "        name = %q\n", frontendName)
	fmt.Fprintf(&b, "        path = %q\n", "frontends/"+frontendName)
	// Declared whenever it is not the schema default: the type is what keeps
	// a Vite or Expo frontend from being generated as a Next.js one.
	if kclType != "" && kclType != "nextjs" {
		fmt.Fprintf(&b, "        type = %q\n", kclType)
	}
	switch {
	case binding == frontendOnHost && pinnedPort > 0:
		fmt.Fprintf(&b, "        port = %d\n", pinnedPort)
	case binding == frontendOnHost:
		fmt.Fprintf(&b, "        port = %s\n", frontendPortIdent(frontendName))
	}
	// The route allowlist is a property of the frontend, not of an env, so
	// every env's declaration carries it: an env that omitted it would
	// regenerate the full CRUD set the allowlist exists to prevent.
	if len(routes) > 0 {
		quoted := make([]string, len(routes))
		for i, r := range routes {
			quoted[i] = fmt.Sprintf("%q", r)
		}
		fmt.Fprintf(&b, "        routes = [%s]\n", strings.Join(quoted, ", "))
	}
	if binding != frontendOnHost {
		b.WriteString("        public_dir = \"out\"\n")
	}
	b.WriteString("}\n")
	return strings.ReplaceAll(b.String(), "\n        ", "\n    ")
}

func frontendKCLEntry(frontendName, _ string, binding frontendBinding, _ int, _ []string) string {
	binder := "_on_host_frontend"
	switch binding {
	case frontendHosted:
		binder = "_hosted_frontend"
	case frontendOnBucket:
		binder = "_on_bucket"
	}
	return fmt.Sprintf("    frontends += [\n        %s(_%s_frontend)\n    ]\n", binder, naming.KCLIdentifier(frontendName))
}

func frontendKCLPortDecl(projectName, env, frontendName string) string {
	return fmt.Sprintf("# The %s frontend's dev-server port. resolve_port takes 3000 when free,\n"+
		"# steps to the next free port when not, and remembers the answer — so a\n"+
		"# frontend added next to anything already on 3000 still starts.\n"+
		"%s = plugin.resolve_port(%q, 3000)\n\n",
		frontendName, frontendPortIdent(frontendName), projectName+"-"+env+"-"+frontendName)
}

// spliceFrontendIntoEnvKCL is the pure core of declareFrontendInKCL.
func spliceFrontendIntoEnvKCL(content, projectName, env, frontendName, kclType string, binding frontendBinding, pinnedPort int, routes []string) (string, frontendKCLStatus) {
	if frontendDeclaredIn(content, frontendName) {
		return content, frontendKCLAlreadyDeclared
	}
	jobs := bundleSecretProviderLine.FindAllStringIndex(content, -1)
	if len(jobs) != 1 {
		return content, frontendKCLNoAnchor
	}
	bundles := regexp.MustCompile(`(?m)^_bundle = forge\.Bundle \{`).FindAllStringIndex(content, -1)
	if len(bundles) != 1 || bundles[0][0] > jobs[0][0] {
		return content, frontendKCLNoAnchor
	}
	declaration := frontendKCLDeclaration(frontendName, kclType, binding, pinnedPort, routes)
	if binding == frontendOnHost {
		if pinnedPort <= 0 {
			declaration = frontendKCLPortDecl(projectName, env, frontendName) + declaration
		}
		if !strings.Contains(content, "_on_host_frontend = lambda") {
			declaration += "_on_host_frontend = lambda f: forge.Frontend -> forge.Frontend {\n    f | {runtime = forge.OnHost {}}\n}\n"
		}
	}
	if binding == frontendOnBucket && !strings.Contains(content, "_on_bucket = lambda") {
		declaration += "_on_bucket = lambda f: forge.Frontend -> forge.Frontend {\n    f | {runtime = forge.OnBucket {bucket = \"REPLACE_ME_BUCKET\"}}\n}\n"
	}
	// Insert from the end backwards so the earlier offset stays valid.
	out := content[:jobs[0][0]] + frontendKCLEntry(frontendName, kclType, binding, pinnedPort, routes) + content[jobs[0][0]:]
	at := bundles[0][0]
	out = out[:at] + declaration + "\n" + out[at:]
	return out, frontendKCLApplied
}

// hostedCORSOrigin is a hosted env already naming a site its services
// accept browser calls from.
var hostedCORSOrigin = regexp.MustCompile(`CORS_ORIGINS\s*=\s*forge\.WorkloadURL`)

func frontendKCLStanzaHint(projectName, env, frontendName, kclType string, binding frontendBinding, pinnedPort int, routes []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Declare frontend '%s' in deploy/kcl/%s/main.k by hand (forge could not place it unambiguously):\n\n", frontendName, env)
	if binding == frontendOnHost && pinnedPort <= 0 {
		b.WriteString(frontendKCLPortDecl(projectName, env, frontendName))
	}
	b.WriteString(frontendKCLDeclaration(frontendName, kclType, binding, pinnedPort, routes))
	b.WriteString("    # inside the bundle, alongside workloads:\n")
	b.WriteString(frontendKCLEntry(frontendName, kclType, binding, pinnedPort, routes))
	return b.String()
}

// kclFrontendType is the KCL spelling of a scaffold frontend type
// (forge.Frontend.type: nextjs | vite | rn).
func kclFrontendType(frontendType string) string {
	switch frontendType {
	case "vite-spa":
		return "vite"
	case "react-native":
		return "rn"
	}
	return "nextjs"
}

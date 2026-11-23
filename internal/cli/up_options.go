package cli

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/reliant-labs/forge/internal/kcloptions"
)

// The `-D name=value` render options a `forge env up` was invoked with.
//
// forge relays these to KCL and does NOT interpret them. It never knows what
// an option means and never acts on one itself — the env's KCL reads
// `option("<name>")` and decides. That is the whole contract: forge owns the
// transport, the project owns the meaning. The one exception is transport,
// not meaning: a value for an option the KCL declares `type="bool"` is
// normalized to the spelling KCL reads as a bool, or refused
// (normalizeBoolOption), because KCL reads any other string as False.
//
// `forge env up`, `forge env build` and `forge env render` bind them; `forge
// env render` also passes them to its manifest render (withRenderOptionKV), so
// its preview of an option is the whole env under it. `forge env deploy`
// deliberately does not: a caller-supplied option that reshapes a cluster
// render is how a prod apply stops being reproducible from the repo alone.
//
// Threaded as a process-global set once before the first render — the same
// shape internal/devstack uses for the git facts, and for the same reason:
// ~20 render call sites, one per-command fact. The zero value emits nothing,
// so every command that never sets it renders byte-identically.
var (
	renderOptsMu sync.RWMutex
	renderOpts   []string // "name=<quoted value>" bindings, render-ready
)

// setRenderOptions records the bindings for this process's subsequent renders.
func setRenderOptions(dArgs []string) {
	renderOptsMu.Lock()
	renderOpts = dArgs
	renderOptsMu.Unlock()
}

// activeRenderOptionDArgs is what the render paths call. nil when no -D was
// passed.
func activeRenderOptionDArgs() []string {
	renderOptsMu.RLock()
	defer renderOptsMu.RUnlock()
	return renderOpts
}

// parseRenderOptions turns raw `-D name=value` flag values into KCL bindings.
//
// The value is taken verbatim and wrapped in a KCL string literal. Quoting is
// not cosmetic: an unquoted binding is parsed as a KCL literal, so `-D
// port=8080` would arrive as an int and `-D tags={"a":"b"}` as a dict. Forcing
// str keeps forge out of the business of guessing the type — the KCL author
// declared it and can convert. (Same coercion fix image_tag and the devstack
// git facts already carry.)
func parseRenderOptions(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	seen := make(map[string]string, len(values))
	names := make([]string, 0, len(values))
	for _, raw := range values {
		idx := strings.Index(raw, "=")
		if idx < 0 {
			return nil, fmt.Errorf("-D %s: expected name=value", raw)
		}
		name := strings.TrimSpace(raw[:idx])
		value := raw[idx+1:] // verbatim: spaces may be meaningful to the project
		if name == "" {
			return nil, fmt.Errorf("-D %s: missing option name before '='", raw)
		}
		if why, reserved := kcloptions.Reserved[name]; reserved {
			return nil, fmt.Errorf(
				"-D %s: %q is set by forge, not by you — %s", raw, name, why)
		}
		if prev, dup := seen[name]; dup {
			return nil, fmt.Errorf("-D: %q given twice (%q then %q); pick one", name, prev, value)
		}
		seen[name] = value
		names = append(names, name)
	}

	sort.Strings(names) // deterministic bindings → byte-identical repeat renders
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, name+"="+strconv.Quote(seen[name]))
	}
	return out, nil
}

// withRenderOptionKV adds parsed -D bindings ("name=<quoted value>", as
// parseRenderOptions returns them) to a manifest render's key/value
// bindings, which cluster.RenderManifests passes to KCL verbatim. A -D
// binding wins over a same-named config key: it is the more specific ask.
func withRenderOptionKV(kv map[string]string, dArgs []string) map[string]string {
	if len(dArgs) == 0 {
		return kv
	}
	out := make(map[string]string, len(kv)+len(dArgs))
	for k, v := range kv {
		out[k] = v
	}
	for _, d := range dArgs {
		if name, value, ok := strings.Cut(d, "="); ok {
			out[name] = value
		}
	}
	return out
}

// resolveRenderOptions is the one path every command that binds -D takes
// (`forge env up`, `forge env build`/`forge build`, `forge env render`): parse,
// check against what the env's KCL declares, and return the render-ready
// bindings.
//
// Parse runs FIRST: it rejects malformed values and forge-reserved names,
// which deserve their own message ("image_tag is set by forge") rather than
// the declared-name check's "no such option" — image_tag is a real option,
// just not yours. The bindings come from the CHECKED values, so a bool
// option's value reaches KCL in the one spelling KCL reads as true.
func resolveRenderOptions(projectDir, envName string, values []string) ([]string, error) {
	if _, err := parseRenderOptions(values); err != nil {
		return nil, err
	}
	checked, err := validateRenderOptions(projectDir, envName, values)
	if err != nil {
		return nil, err
	}
	return parseRenderOptions(checked)
}

// validateRenderOptions checks the -D values against the options the env's
// KCL actually declares, and returns them ready to bind:
//
//   - a name the env never reads fails, so a typo fails before the render
//     instead of silently doing nothing (an unread option is not an error in
//     KCL — it is just unread);
//   - a value for an option declared `type="bool"` is normalized to KCL's
//     spelling, or refused (see normalizeBoolOption).
//
// Degrades PERMISSIVELY: when discovery can't see the project's options at all
// it returns the values unchanged rather than rejecting. A caller who passed a
// real option must not be blocked because forge failed to parse the KCL — the
// render is about to happen anyway and will report any genuine problem with a
// better message.
func validateRenderOptions(projectDir, envName string, values []string) ([]string, error) {
	if len(values) == 0 {
		return values, nil
	}
	declared, discoverable, err := kcloptions.Discover(projectDir, envName)
	if err != nil || !discoverable {
		return values, nil
	}
	return checkRenderOptions(envName, declared, values)
}

// checkRenderOptions is validateRenderOptions over an already-discovered
// option list: the declared-name check, then the bool normalization.
func checkRenderOptions(envName string, declared []kcloptions.Option, values []string) ([]string, error) {
	byName := make(map[string]kcloptions.Option, len(declared))
	for _, o := range declared {
		byName[o.Name] = o
	}

	var unknown []string
	for _, raw := range values {
		name := strings.TrimSpace(raw)
		if idx := strings.Index(raw, "="); idx >= 0 {
			name = strings.TrimSpace(raw[:idx])
		}
		if _, ok := byName[name]; name != "" && !ok {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		msg := fmt.Sprintf("-D: deploy/kcl/%s/ declares no option named %s",
			envName, quoteJoin(unknown))
		if len(declared) == 0 {
			return nil, fmt.Errorf("%s (it declares none; add `option(\"<name>\")` to its KCL)", msg)
		}
		names := make([]string, 0, len(declared))
		for _, o := range declared {
			names = append(names, o.Name)
		}
		return nil, fmt.Errorf("%s; it declares %s (see `forge env options %s`)",
			msg, quoteJoin(names), envName)
	}

	out := make([]string, 0, len(values))
	for _, raw := range values {
		name, value, ok := strings.Cut(raw, "=")
		name = strings.TrimSpace(name)
		if !ok || !strings.EqualFold(strings.TrimSpace(byName[name].Type), "bool") {
			out = append(out, raw)
			continue
		}
		canonical, err := normalizeBoolOption(envName, name, value)
		if err != nil {
			return nil, err
		}
		out = append(out, name+"="+canonical)
	}
	return out, nil
}

// normalizeBoolOption maps a -D value for an option the env declares
// `type="bool"` to "true" or "false".
//
// Every -D value is bound as a KCL string (parseRenderOptions), and KCL's
// option(type="bool") converts a string to True only when it is exactly
// "true" or "True"; anything else — "1", "TRUE", "yes" — is silently False.
// So `-D debug_image=1` used to build the non-debug image with no word said.
// The spellings a person means as a bool are mapped to the one KCL reads, and
// anything else is refused: a value that cannot be read as a bool is a
// mistake, and binding it would be the same silent False.
func normalizeBoolOption(envName, name, value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "1", "yes":
		return "true", nil
	case "false", "0", "no":
		return "false", nil
	}
	return "", fmt.Errorf(
		"-D %s=%s: deploy/kcl/%s/ declares %q as a bool option; give true or false (1/0 and yes/no, any case, are accepted too)",
		name, value, envName, name)
}

func quoteJoin(items []string) string {
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = strconv.Quote(s)
	}
	return strings.Join(quoted, ", ")
}

// refuseDeployRenderOptions refuses `forge env deploy -D`.
//
// A deploy renders from the repo alone: the release's bundle, the plan, the
// hub's apply and the manifests this process applies are all one render, and a
// release records no render options. A `-D` that reached only some of those
// renders (the build and entity render saw it, the manifest render did not)
// would ship a release that disagrees with its own manifests, and the flag
// used to be accepted and silently ignored by the manifest render (SC7
// rehearsal, D4). Honouring it everywhere would make an apply unreproducible
// from the repo, so it is refused, with the places it does work.
func refuseDeployRenderOptions(env string, values []string) error {
	if len(values) == 0 {
		return nil
	}
	return fmt.Errorf("forge env deploy does not take -D (%s): a deploy renders from the repo alone, and a release records no render options, so the bundle and the applied manifests could not agree on it.\n"+
		"  To change what the env renders, declare the value in deploy/kcl/%s/ and commit it.\n"+
		"  -D is for 'forge env up %s -D ...' and 'forge env build %s -D ...', and 'forge env render %s -D ...' previews it (see 'forge env options %s')",
		strings.Join(values, " "), env, env, env, env, env)
}

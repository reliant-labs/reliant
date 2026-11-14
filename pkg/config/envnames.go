package config

// The env layer's NAMES: the variable a config field is read from, and the
// aliases it falls back to ((forge.v1.config).env_var_aliases).
//
// A field reads env_var first, then each alias in the order declared; the
// first one set wins, and a variable set to "" is unset (lookupEnv), so an
// empty primary falls through to the next name exactly as an absent one
// does. That ordering is what a hand-written fallback chain did — `if v :=
// os.Getenv(A); v != "" { … } else if v := os.Getenv(B); …` — and an alias
// exists so a project can delete that chain without breaking whoever still
// sets B.
//
// A DEPRECATED alias prints one line to stderr at load, whether it supplied
// the value or an earlier name shadowed it: either way the setter exporting
// it has a rename to make, and the process start is the only place that
// setter's operator looks. A permanent alias (NODE_ENV) is silent.
//
// The notice goes to the command's stderr (cmd.ErrOrStderr, so a test or an
// embedding program can capture it) rather than a logger: config loads
// before the logger exists, because the logger is built from the config.

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/reliant-labs/forge/pkg/forgepb"
)

// envNames returns the variables a field's env layer reads, in precedence
// order: env_var, then each alias as declared.
func envNames(opt *forgepb.ConfigFieldOptions) []string {
	var names []string
	if v := opt.GetEnvVar(); v != "" {
		names = append(names, v)
	}
	for _, a := range opt.GetEnvVarAliases() {
		if a.GetName() != "" {
			names = append(names, a.GetName())
		}
	}
	return names
}

// lookupFieldEnv resolves a field's env layer: the first of its names set to
// something other than "". name is the variable that supplied the value.
//
// An alias carrying `values` supplies the field's value mapped from its own,
// and only for a value the map names: any other value is another tool's
// business, so it reads as unset and the next name is tried (aliasValue).
func lookupFieldEnv(opt *forgepb.ConfigFieldOptions, env environ) (name, value string, ok bool) {
	if n := opt.GetEnvVar(); n != "" {
		if v, set := env.lookupEnv(n); set {
			return n, v, true
		}
	}
	for _, a := range opt.GetEnvVarAliases() {
		if v, set := aliasValue(a, env); set {
			return a.GetName(), v, true
		}
	}
	return "", "", false
}

// aliasValue is what one alias supplies: its value, or for a value-mapped
// alias the field value its value maps to; ok is false when it supplies
// nothing (unset, "", or a value the map does not name).
func aliasValue(a *forgepb.EnvVarAlias, env environ) (string, bool) {
	if a.GetName() == "" {
		return "", false
	}
	v, set := env.lookupEnv(a.GetName())
	if !set {
		return "", false
	}
	if m := a.GetValues(); len(m) > 0 {
		mapped, known := m[v]
		return mapped, known
	}
	return v, true
}

// aliasLabel names an alias for help text and errors: the variable, or for a
// value-mapped one the settings it honours ("DEBUG=1|true").
func aliasLabel(a *forgepb.EnvVarAlias) string {
	m := a.GetValues()
	if len(m) == 0 {
		return a.GetName()
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return a.GetName() + "=" + strings.Join(keys, "|")
}

// noticeDeprecatedAliases writes one line to w for each deprecated alias of
// the field that is set. supplier is the variable the env layer read the
// value from ("" when none was set).
func noticeDeprecatedAliases(w io.Writer, fd protoreflect.FieldDescriptor, opt *forgepb.ConfigFieldOptions, supplier string, env environ) {
	primary := opt.GetEnvVar()
	for _, a := range opt.GetEnvVarAliases() {
		name := a.GetName()
		if !a.GetDeprecated() || name == "" {
			continue
		}
		if _, set := aliasValue(a, env); !set {
			continue // unset, or a value-mapped alias set to a value it does not honour
		}
		if name == supplier {
			fmt.Fprintf(w, "warning: %s is a deprecated name for %s (config field %s); set %s instead\n",
				name, primary, fd.Name(), primary)
			continue
		}
		fmt.Fprintf(w, "warning: %s is a deprecated name for %s (config field %s) and is ignored, because %s is set; unset %s\n",
			name, primary, fd.Name(), supplier, name)
	}
}

// noticeWriter is where load-time notices go: the command's stderr, or the
// process's when LoadInto runs without a command.
func noticeWriter(cmd *cobra.Command) io.Writer {
	if cmd != nil {
		return cmd.ErrOrStderr()
	}
	return os.Stderr
}

// envSourcesPhrase names the variables a field reads, for an error message:
// "DAEMON_LISTEN_PORT", or "DAEMON_LISTEN_PORT or TOOLS_DAEMON_PORT".
func envSourcesPhrase(opt *forgepb.ConfigFieldOptions) string {
	var names []string
	if v := opt.GetEnvVar(); v != "" {
		names = append(names, v)
	}
	for _, a := range opt.GetEnvVarAliases() {
		if a.GetName() != "" {
			names = append(names, aliasLabel(a))
		}
	}
	return strings.Join(names, " or ")
}

// emptyEnvNote is the clause a required-field error adds when any of the
// field's variables is present but empty, so nobody goes looking for a
// variable that is in fact set. "" when none is.
func emptyEnvNote(opt *forgepb.ConfigFieldOptions, env environ) string {
	var empty []string
	for _, n := range envNames(opt) {
		if env.envSetButEmpty(n) {
			empty = append(empty, n)
		}
	}
	switch len(empty) {
	case 0:
		return ""
	case 1:
		return fmt.Sprintf(" — %s is set but empty, and an empty env var counts as unset", empty[0])
	default:
		return fmt.Sprintf(" — %s are set but empty, and an empty env var counts as unset", strings.Join(empty, ", "))
	}
}

// flagUsage is a flag's help text: the field's description, then the env
// vars that also set it. Deprecated aliases are left out — help is where a
// new setter learns the name to use, and that is never the deprecated one.
func flagUsage(opt *forgepb.ConfigFieldOptions) string {
	names := []string{}
	if v := opt.GetEnvVar(); v != "" {
		names = append(names, v)
	}
	for _, a := range opt.GetEnvVarAliases() {
		if a.GetName() != "" && !a.GetDeprecated() {
			names = append(names, aliasLabel(a))
		}
	}
	desc := opt.GetDescription()
	if len(names) == 0 {
		return desc
	}
	env := "[env: " + strings.Join(names, ", ") + "]"
	if desc == "" {
		return env
	}
	return desc + " " + env
}

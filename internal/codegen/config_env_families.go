package codegen

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Env families ((forge.v1.config).env_prefix / env_suffix): a
// map<string, scalar> field loaded from every variable named
// <prefix><KEY><suffix>. The runtime loader (forge/pkg/config envfamily.go)
// reads them; this refuses, at generate time, a family that cannot mean what
// it says and two names one process could not tell apart.

var envNameTailPattern = regexp.MustCompile(`^[A-Za-z0-9_]*$`)

// familyScalarTypes are the map value kinds a family's values parse as —
// the scalars the runtime loader parses from a string.
var familyScalarTypes = map[string]bool{
	"string": true, "bytes": true, "bool": true, "float": true, "double": true,
	"int32": true, "int64": true, "sint32": true, "sint64": true, "sfixed32": true, "sfixed64": true,
	"uint32": true, "uint64": true, "fixed32": true, "fixed64": true,
}

// familyPatternOf renders a family's names: "APP_OAUTH_<KEY>_CLIENT_ID".
func familyPatternOf(f ConfigField) string { return f.EnvPrefix + "<KEY>" + f.EnvSuffix }

// familyClaims reports whether name is one of the family's variables.
func familyClaims(f ConfigField, name string) bool {
	return strings.HasPrefix(name, f.EnvPrefix) && strings.HasSuffix(name, f.EnvSuffix) &&
		len(name) > len(f.EnvPrefix)+len(f.EnvSuffix)
}

// familiesOverlap reports whether some variable name belongs to both
// families: one prefix extends the other AND one suffix extends the other.
func familiesOverlap(a, b ConfigField) bool {
	prefixes := strings.HasPrefix(a.EnvPrefix, b.EnvPrefix) || strings.HasPrefix(b.EnvPrefix, a.EnvPrefix)
	suffixes := strings.HasSuffix(a.EnvSuffix, b.EnvSuffix) || strings.HasSuffix(b.EnvSuffix, a.EnvSuffix)
	return prefixes && suffixes
}

// ValidateConfigEnvFamilies refuses every env family that cannot be loaded as
// declared, and every pair of names one process could not tell apart:
//
//   - env_suffix without env_prefix; a prefix or suffix that is not part of
//     an environment variable name;
//   - a family on a field that is not a map<string, scalar>;
//   - a family that also declares env_var, env_var_aliases, flag or
//     default_value (a family's names are its pattern; it has no flag form,
//     and an open-ended map has no default);
//   - a family on a frontend config, which reads no environment;
//   - two families of one process that could claim the same variable, and a
//     family that claims another field's env_var or alias.
//
// Every offender is reported, as ValidateConfigEnvAliases does.
func ValidateConfigEnvFamilies(messages []ConfigMessage) error {
	byName := make(map[string]*ConfigMessage, len(messages))
	composed := map[string]bool{}
	frontendOwned := map[string]string{}
	for i := range messages {
		m := &messages[i]
		byName[m.Name] = m
		for _, f := range m.Fields {
			if f.MessageType != "" {
				composed[f.MessageType] = true
				if m.Frontend != "" {
					frontendOwned[f.MessageType] = m.Frontend
				}
			}
		}
		if m.Frontend != "" {
			frontendOwned[m.Name] = m.Frontend
		}
	}

	var problems []string
	seen := map[string]bool{}
	refuse := func(p string) {
		if !seen[p] {
			seen[p] = true
			problems = append(problems, p)
		}
	}

	for _, m := range messages {
		for _, f := range m.Fields {
			if f.EnvPrefix == "" && f.EnvSuffix == "" {
				continue
			}
			for _, p := range familyShapeProblems(m, f, frontendOwned) {
				refuse(p)
			}
		}
	}

	for _, surface := range configEnvSurfaces(messages, byName, composed) {
		for _, p := range familyCollisions(surface) {
			refuse(p)
		}
	}

	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	var b strings.Builder
	fmt.Fprintf(&b, "%d (forge.v1.config).env_prefix problem(s):\n", len(problems))
	for _, p := range problems {
		fmt.Fprintf(&b, "  - %s\n", p)
	}
	b.WriteString("An env family loads every variable named <env_prefix><KEY><env_suffix> into a map; see ConfigFieldOptions.env_prefix in forge/v1/forge.proto.")
	return fmt.Errorf("%s", b.String())
}

// familyShapeProblems explains each way one field's env family cannot be
// loaded as declared.
func familyShapeProblems(m ConfigMessage, f ConfigField, frontendOwned map[string]string) []string {
	where := m.Name + "." + f.Name
	if f.ProtoFile != "" {
		where += " (" + f.ProtoFile + ")"
	}
	var out []string
	switch {
	case f.EnvPrefix == "":
		return []string{fmt.Sprintf("%s: env_suffix without env_prefix. A family is <env_prefix><KEY><env_suffix>; set env_prefix.", where)}
	case !envVarNamePattern.MatchString(f.EnvPrefix):
		out = append(out, fmt.Sprintf("%s: env_prefix %q does not begin an environment variable name (letters, digits and underscores, not starting with a digit).", where, f.EnvPrefix))
	case !envNameTailPattern.MatchString(f.EnvSuffix):
		out = append(out, fmt.Sprintf("%s: env_suffix %q is not part of an environment variable name (letters, digits and underscores).", where, f.EnvSuffix))
	}
	if fe, ok := frontendOwned[m.Name]; ok {
		out = append(out, fmt.Sprintf("%s: a frontend config field takes no env_prefix — frontend %q reads its values from the rendered config document, not from an environment.", where, fe))
	}
	if f.MapValueType == "" || !familyScalarTypes[f.MapValueType] {
		out = append(out, fmt.Sprintf("%s: env_prefix needs a map<string, V> field with a scalar V (string, bool, a number); each variable is one entry. A set the proto can name is fields.", where))
	}
	var also []string
	if f.EnvVar != "" {
		also = append(also, "env_var")
	}
	if len(f.EnvVarAliases) > 0 {
		also = append(also, "env_var_aliases")
	}
	if f.Flag != "" {
		also = append(also, "flag")
	}
	if f.DefaultValue != "" {
		also = append(also, "default_value")
	}
	if len(also) > 0 {
		out = append(out, fmt.Sprintf("%s: an env_prefix family takes no %s: its names are its pattern %s, it has no flag form, and an open-ended map has no default. Remove them.",
			where, strings.Join(also, " or "), familyPatternOf(f)))
	}
	return out
}

// familyCollisions lists, for one process's leaves, every pair of families
// that could claim the same variable and every family that claims another
// field's env_var or alias.
func familyCollisions(surface envSurface) []string {
	var families, plain []envLeaf
	for _, leaf := range surface.leaves {
		if leaf.field.EnvPrefix != "" {
			families = append(families, leaf)
		} else {
			plain = append(plain, leaf)
		}
	}
	var out []string
	for i, a := range families {
		for _, b := range families[i+1:] {
			if familiesOverlap(a.field, b.field) {
				out = append(out, fmt.Sprintf("%s reads two env families that can claim the same variable: %s (%s) and %s (%s). Give them prefixes or suffixes neither extends.",
					surface.name, familyPatternOf(a.field), a.path, familyPatternOf(b.field), b.path))
			}
		}
		for _, p := range plain {
			names := []string{p.field.EnvVar}
			for _, al := range p.field.EnvVarAliases {
				names = append(names, al.Name)
			}
			for _, n := range names {
				if n != "" && familyClaims(a.field, n) {
					out = append(out, fmt.Sprintf("%s reads %s as a name of %s, and the env family %s (%s) claims it too. Rename one.",
						surface.name, n, p.path, familyPatternOf(a.field), a.path))
				}
			}
		}
	}
	return out
}

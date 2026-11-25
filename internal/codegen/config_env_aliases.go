package codegen

// Generate-time checks for (forge.v1.config).env_var_aliases.
//
// An alias is a fallback NAME for a field's env_var, read only by the
// runtime loader (forge/pkg/config, envnames.go). Nothing else projects it,
// so the only way an alias can go wrong is by naming something that already
// means something else: another field's env_var or alias in the same
// process, or the field's own env_var. The loader would then read one
// variable into two fields, or a setter of one field would silently set
// another, and nothing at runtime could tell which the author meant. So
// `forge generate` refuses those, with every offender listed at once.

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// envVarNamePattern is the shape of a portable environment variable name
// (POSIX: letters, digits and underscores, not starting with a digit).
var envVarNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// envNameUse is one place a process reads an env var name from.
type envNameUse struct {
	field string // Message.path, e.g. "ReliantConfig.control_plane.url"
	file  string // the proto declaring the field, when known
	alias bool   // false: the field's env_var; true: one of its aliases
}

func (u envNameUse) String() string {
	kind := "env_var"
	if u.alias {
		kind = "alias"
	}
	if u.file != "" {
		return fmt.Sprintf("%s of %s (%s)", kind, u.field, u.file)
	}
	return fmt.Sprintf("%s of %s", kind, u.field)
}

// ValidateConfigEnvAliases refuses every env_var_aliases entry that cannot
// mean what it says:
//
//   - its name is empty or not an env var name;
//   - the field has no env_var — an alias is a fallback FOR env_var;
//   - it repeats the field's own env_var, or another alias of the field;
//   - it names a variable another field of the same process reads, as an
//     env_var or an alias. A process is a binary's root config, a command
//     config together with its binary's root config (the process loads
//     both), or the project-global config (every unbound root message);
//   - the field belongs to a frontend config, whose values come from the
//     rendered config document, never from an environment.
//
// Two fields sharing an env_var with no alias involved is not this check's
// business: ValidateBinaryConfigBindings already refuses the root/command
// case, and the rest predates aliases.
func ValidateConfigEnvAliases(messages []ConfigMessage) error {
	byName := make(map[string]*ConfigMessage, len(messages))
	composed := map[string]bool{}
	for i := range messages {
		byName[messages[i].Name] = &messages[i]
		for _, f := range messages[i].Fields {
			if f.MessageType != "" {
				composed[f.MessageType] = true
			}
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

	// The field's own shape, once per declaration.
	frontendOwned := map[string]string{} // message name -> frontend that loads it
	for _, m := range messages {
		if m.Frontend == "" {
			continue
		}
		frontendOwned[m.Name] = m.Frontend
		for _, f := range m.Fields {
			if f.MessageType != "" {
				frontendOwned[f.MessageType] = m.Frontend
			}
		}
	}
	for _, m := range messages {
		for _, f := range m.Fields {
			if len(f.EnvVarAliases) == 0 {
				continue
			}
			where := m.Name + "." + f.Name
			if f.ProtoFile != "" {
				where += " (" + f.ProtoFile + ")"
			}
			if fe, ok := frontendOwned[m.Name]; ok {
				refuse(fmt.Sprintf("%s: a frontend config field takes no env_var_aliases — frontend %q reads its values from the rendered config document, not from an environment. Remove them.", where, fe))
				continue
			}
			if f.EnvVar == "" {
				refuse(fmt.Sprintf("%s: env_var_aliases without an env_var. An alias is a fallback for env_var: set env_var to the name new setters should use.", where))
			}
			own := map[string]bool{}
			for _, a := range f.EnvVarAliases {
				switch {
				case !envVarNamePattern.MatchString(a.Name):
					refuse(fmt.Sprintf("%s: alias %q is not an environment variable name (letters, digits and underscores, not starting with a digit).", where, a.Name))
				case a.Name == f.EnvVar:
					refuse(fmt.Sprintf("%s: alias %s is the field's own env_var. Remove it.", where, a.Name))
				case own[a.Name]:
					refuse(fmt.Sprintf("%s: alias %s is listed twice. Remove one.", where, a.Name))
				}
				own[a.Name] = true
				for _, p := range aliasValueProblems(f, a) {
					refuse(fmt.Sprintf("%s: alias %s %s", where, a.Name, p))
				}
			}
		}
	}

	// Collisions, per process.
	for _, surface := range configEnvSurfaces(messages, byName, composed) {
		uses := map[string][]envNameUse{}
		var names []string
		add := func(name string, u envNameUse) {
			if name == "" || !envVarNamePattern.MatchString(name) {
				return
			}
			if _, ok := uses[name]; !ok {
				names = append(names, name)
			}
			for _, prior := range uses[name] {
				if prior.field == u.field {
					return // the field's own repeat, refused above
				}
			}
			uses[name] = append(uses[name], u)
		}
		for _, leaf := range surface.leaves {
			add(leaf.field.EnvVar, envNameUse{field: leaf.path, file: leaf.field.ProtoFile})
			for _, a := range leaf.field.EnvVarAliases {
				if a.Name == leaf.field.EnvVar {
					continue // refused above as the field's own env_var
				}
				add(a.Name, envNameUse{field: leaf.path, file: leaf.field.ProtoFile, alias: true})
			}
		}
		for _, name := range names {
			us := uses[name]
			if len(us) < 2 || !anyAlias(us) {
				continue
			}
			var parts []string
			for _, u := range us {
				parts = append(parts, u.String())
			}
			refuse(fmt.Sprintf("%s reads %s as the %s. The loader would read one variable into %d fields, and a setter of one would silently set the others. Give each field names of its own.",
				surface.name, name, strings.Join(parts, ", and as the "), len(us)))
		}
	}

	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	var b strings.Builder
	fmt.Fprintf(&b, "%d (forge.v1.config).env_var_aliases problem(s):\n", len(problems))
	for _, p := range problems {
		b.WriteString("  - " + p + "\n")
	}
	b.WriteString("An alias is an extra name the runtime loader reads a field from when its env_var is unset; see ConfigFieldOptions.env_var_aliases in forge/v1/forge.proto.")
	return fmt.Errorf("%s", b.String())
}

func anyAlias(us []envNameUse) bool {
	for _, u := range us {
		if u.alias {
			return true
		}
	}
	return false
}

// envSurface is the set of config leaves one process reads.
type envSurface struct {
	name   string // "the `reliant server api` process", for the message
	leaves []envLeaf
}

type envLeaf struct {
	path  string
	field ConfigField
}

// configEnvSurfaces lists the leaf sets within which no two fields may read
// one variable: the project-global config (every root message bound to no
// binary and no frontend), each binary-bound config alone, and each PAIR of
// one binary's configs that some process loads together — its root config
// and any other, or a subtree config and a command or subtree config its
// scope meets (sharedProcessPath). A clash involves two fields, so pairs
// cover every process however many configs it loads, without inventing a
// union of configs no single process loads. Frontend configs read no
// environment and are skipped.
func configEnvSurfaces(messages []ConfigMessage, byName map[string]*ConfigMessage, composed map[string]bool) []envSurface {
	var out []envSurface
	var global envSurface
	global.name = "The project config"
	var bound []*ConfigMessage
	for i := range messages {
		m := &messages[i]
		switch {
		case m.Frontend != "":
			continue
		case m.Binary == "":
			if composed[m.Name] {
				continue // a block: reached through whatever composes it
			}
			global.leaves = append(global.leaves, configLeaves(m, byName)...)
		default:
			bound = append(bound, m)
			name := fmt.Sprintf("The %#q process", m.ProcessName())
			if len(m.Subtrees) > 0 {
				name = "Every process of " + strings.TrimPrefix(scopePhrase(m), "the ")
			}
			out = append(out, envSurface{name: name, leaves: configLeaves(m, byName)})
		}
	}
	for i, a := range bound {
		for _, b := range bound[i+1:] {
			process, meet := sharedProcessPath(a, b)
			if !meet {
				continue
			}
			name := strings.TrimSpace(a.Binary + " " + strings.Join(process, " "))
			out = append(out, envSurface{
				name:   fmt.Sprintf("The %#q process", name),
				leaves: append(configLeaves(a, byName), configLeaves(b, byName)...),
			})
		}
	}
	if len(global.leaves) > 0 {
		out = append([]envSurface{global}, out...)
	}
	return out
}

// configLeaves walks m and every block it composes, at any depth (the
// runtime loader descends without limit), returning each scalar leaf with its
// path. A block that composes itself, directly or not, is walked once.
func configLeaves(m *ConfigMessage, byName map[string]*ConfigMessage) []envLeaf {
	var out []envLeaf
	var walk func(msg *ConfigMessage, prefix string, onPath map[string]bool)
	walk = func(msg *ConfigMessage, prefix string, onPath map[string]bool) {
		if onPath[msg.Name] {
			return
		}
		onPath[msg.Name] = true
		defer delete(onPath, msg.Name)
		for _, f := range msg.Fields {
			if f.MessageType != "" {
				if bm, ok := byName[f.MessageType]; ok {
					walk(bm, prefix+f.Name+".", onPath)
				}
				continue
			}
			out = append(out, envLeaf{path: prefix + f.Name, field: f})
		}
	}
	walk(m, m.Name+".", map[string]bool{})
	return out
}

// aliasValueProblems explains each way a value-mapped alias's map cannot be
// honoured: an empty key (an empty variable is unset, so it never matches),
// or a mapped value the field itself would refuse — outside its
// allowed_values, or not parseable as its kind. The loader would otherwise
// discover the latter at the first start that sets the alias.
func aliasValueProblems(f ConfigField, a EnvVarAlias) []string {
	keys := make([]string, 0, len(a.Values))
	for k := range a.Values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		v := a.Values[k]
		if k == "" {
			out = append(out, "maps the empty value, which never matches: an empty variable is unset. Remove it.")
			continue
		}
		if len(f.AllowedValues) > 0 && v != "" && !slices.Contains(f.AllowedValues, v) {
			out = append(out, fmt.Sprintf("maps %q to %q, which is not one of the field's allowed_values %v.", k, v, f.AllowedValues))
			continue
		}
		if err := checkScalarValue(f.ProtoType, v); err != nil {
			out = append(out, fmt.Sprintf("maps %q to %q, which is not a valid %s: %v.", k, v, f.ProtoType, err))
		}
	}
	return out
}

// checkScalarValue reports whether raw parses as a config value of a scalar
// proto kind, the way the runtime loader parses it. Kinds it does not know
// (messages such as Duration, enums) are not checked here.
func checkScalarValue(protoType, raw string) error {
	var err error
	switch protoType {
	case "int32", "sint32", "sfixed32":
		_, err = strconv.ParseInt(raw, 10, 32)
	case "int64", "sint64", "sfixed64":
		_, err = strconv.ParseInt(raw, 10, 64)
	case "uint32", "fixed32":
		_, err = strconv.ParseUint(raw, 10, 32)
	case "uint64", "fixed64":
		_, err = strconv.ParseUint(raw, 10, 64)
	case "bool":
		_, err = strconv.ParseBool(raw)
	case "float":
		_, err = strconv.ParseFloat(raw, 32)
	case "double":
		_, err = strconv.ParseFloat(raw, 64)
	}
	return err
}

package codegen

import (
	"fmt"
	"sort"
	"strings"
)

// RequiredWhenKey is one resolved (forge.v1.config).required_when condition
// of a frontend config field: the field is required when the document's Key
// holds Equals. Key is the condition field's document key (its env_var
// spelling), which is what every reader of the document — config_gen.ts, the
// KCL check, a cross-repo render — can see.
type RequiredWhenKey struct {
	Key    string `json:"key"`
	Equals string `json:"equals"`
}

// frontendRequiredWhen resolves f's conditions against fc's own fields. It
// assumes ValidateFrontendConfigs has accepted fc: a condition naming no
// sibling is dropped rather than reported here.
func frontendRequiredWhen(fc FrontendConfig, f ConfigField) []RequiredWhenKey {
	if len(f.RequiredWhen) == 0 {
		return nil
	}
	byName := make(map[string]ConfigField, len(fc.Fields))
	for _, g := range fc.Fields {
		byName[g.Name] = g
	}
	var out []RequiredWhenKey
	for _, c := range f.RequiredWhen {
		g, ok := byName[c.Field]
		if !ok {
			continue
		}
		out = append(out, RequiredWhenKey{Key: tsConfigKeyRaw(g), Equals: c.Equals})
	}
	return out
}

// frontendFieldRequired reports whether f is required unconditionally in the
// document: required with no default (a default always satisfies a plain
// `required`, as the loader and the typed KCL schema both treat it).
func frontendFieldRequired(f ConfigField) bool {
	return f.Required && f.DefaultValue == ""
}

// requiredWhenProblems is ValidateFrontendConfigs' required_when half: every
// misuse of the annotation, one line each. A frontend config is validated
// FLATTENED (its composed blocks' leaves included), because that is the
// message whose document the conditions are judged against; a binary config
// carrying it is refused outright.
func requiredWhenProblems(messages []ConfigMessage) []string {
	var problems []string

	// Which messages reach a frontend document, and which reach anything
	// else. A block composed only onto frontend configs may carry the
	// annotation (it is judged in each composing frontend's document); one
	// also composed onto a binary config may not.
	composedByFrontend := map[string]bool{}
	composedByOther := map[string]bool{}
	for _, m := range messages {
		for _, f := range m.Fields {
			if f.ProtoType == "message" && f.MessageType != "" {
				if m.Frontend != "" {
					composedByFrontend[f.MessageType] = true
				} else {
					composedByOther[f.MessageType] = true
				}
			}
		}
	}
	for _, m := range messages {
		frontendOnly := m.Frontend != "" || (composedByFrontend[m.Name] && !composedByOther[m.Name])
		for _, f := range m.Fields {
			if len(f.RequiredWhen) == 0 || frontendOnly {
				continue
			}
			problems = append(problems, fmt.Sprintf(
				"%s.%s: required_when is for a (forge.v1.frontend_config) message, whose values arrive in one document every check can see; "+
					"a binary config's value is layered from defaults, a config file, env and flags. Use `required: true`, or check the mode in code",
				m.Name, f.Name))
		}
	}

	for _, fc := range FrontendConfigsFromMessages(messages) {
		byName := make(map[string]ConfigField, len(fc.Fields))
		for _, f := range fc.Fields {
			byName[f.Name] = f
		}
		for _, f := range fc.Fields {
			where := fc.MessageName + "." + f.Name
			if len(f.RequiredWhen) > 0 && f.Required {
				problems = append(problems, fmt.Sprintf(
					"%s: sets both `required: true` and required_when; a required field is required in every mode — drop one", where))
			}
			for _, c := range f.RequiredWhen {
				g, ok := byName[c.Field]
				switch {
				case c.Field == "":
					problems = append(problems, fmt.Sprintf("%s: a required_when condition names no field", where))
				case c.Field == f.Name:
					problems = append(problems, fmt.Sprintf("%s: required_when names the field itself", where))
				case !ok:
					problems = append(problems, fmt.Sprintf(
						"%s: required_when names %q, which frontend %q's config does not declare (fields: %s)",
						where, c.Field, fc.Frontend, strings.Join(configLeafNames(fc.Fields), ", ")))
				case g.ProtoType != "string" || g.MessageType != "":
					problems = append(problems, fmt.Sprintf(
						"%s: required_when names %q, a %s field; a condition compares a string field's value", where, c.Field, g.ProtoType))
				case c.Equals == "":
					problems = append(problems, fmt.Sprintf(
						"%s: required_when on %q has no `equals`; an empty value is the unset field, which is the mode the default serves", where, c.Field))
				case len(g.AllowedValues) > 0 && !containsString(g.AllowedValues, c.Equals):
					problems = append(problems, fmt.Sprintf(
						"%s: required_when on %q equals %q, which is outside its allowed_values [%s], so the condition can never hold",
						where, c.Field, c.Equals, strings.Join(g.AllowedValues, ", ")))
				}
			}
		}
	}
	return problems
}

func configLeafNames(fields []ConfigField) []string {
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f.MessageType == "" {
			out = append(out, f.Name)
		}
	}
	sort.Strings(out)
	return out
}

// frontendConfigHasRequiredWhen reports whether any leaf of fc carries
// (forge.v1.config).required_when, i.e. whether the generated module needs
// missingRequiredConfig. A config without one keeps its module unchanged.
func frontendConfigHasRequiredWhen(fc FrontendConfig) bool {
	for _, f := range fc.Fields {
		if f.MessageType == "" && len(f.RequiredWhen) > 0 {
			return true
		}
	}
	return false
}

// requiredWhenTS emits config_gen.ts's mode-dependent required keys and
// missingRequiredConfig, or "" when fc declares none.
//
// They are judged on the RAW document, before zod applies defaults: the
// condition names a mode the default is wrong for (a managed deployment
// whose default is a self-hosted value), so a key the document leaves to its
// default — or sets to exactly the default — is missing.
func requiredWhenTS(fc FrontendConfig) string {
	if !frontendConfigHasRequiredWhen(fc) {
		return ""
	}
	var rows strings.Builder
	for _, f := range fc.Fields {
		if f.MessageType != "" || len(f.RequiredWhen) == 0 {
			continue
		}
		conds := frontendRequiredWhen(fc, f)
		parts := make([]string, 0, len(conds))
		for _, c := range conds {
			parts = append(parts, fmt.Sprintf("{ key: %q, equals: %q }", c.Key, c.Equals))
		}
		def := ""
		if f.DefaultValue != "" {
			def = fmt.Sprintf(" default: %q,", f.DefaultValue)
		}
		fmt.Fprintf(&rows, "  { key: %q,%s when: [%s] },\n", tsConfigKeyRaw(f), def, strings.Join(parts, ", "))
	}
	return strings.Replace(requiredWhenTSTemplate, "{{ROWS}}", rows.String(), 1)
}

const requiredWhenTSTemplate = `/** A key the document must carry in the mode ` + "`when`" + ` names, and does not. */
export interface MissingConfigKey {
  key: string;
  when: { key: string; equals: string };
}

// (forge.v1.config).required_when, per key: required, with a value other
// than its default, when any of its conditions holds.
const requiredWhen: ReadonlyArray<{
  key: string;
  default?: string;
  when: ReadonlyArray<{ key: string; equals: string }>;
}> = [
{{ROWS}}];

function documentValue(raw: Record<string, unknown>, key: string): string | undefined {
  const v = raw[key];
  if (v === undefined || v === null) return undefined;
  const s = String(v).trim();
  return s === "" ? undefined : s;
}

/**
 * The keys the document must carry in the mode it selects, and does not.
 * loadConfig() throws on any; an app that renders its own boot error calls
 * this first. Judged on the raw document: a default does not count.
 */
export function missingRequiredConfig(raw: Record<string, unknown> = rawConfig()): MissingConfigKey[] {
  const out: MissingConfigKey[] = [];
  for (const r of requiredWhen) {
    for (const c of r.when) {
      if (documentValue(raw, c.key) !== c.equals) continue;
      const v = documentValue(raw, r.key);
      if (v === undefined || v === r.default) out.push({ key: r.key, when: c });
      break;
    }
  }
  return out;
}

`

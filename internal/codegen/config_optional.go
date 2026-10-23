package codegen

import (
	"fmt"
	"sort"
	"strings"
)

// Optionality rules for config fields.
//
// Forge has two ways to say a field may be absent and one to say it must be
// present, and the interesting case is the SENSITIVE field — because for
// those, "absent" is checked long before the process starts.
//
// A sensitive field projects to a secret_ref, and `forge env up` / `forge env
// deploy` validate every declared ref against the environment's store before
// anything runs (secrets.ValidateDeclaredRefs). That pre-flight is the point:
// the alternative is discovering a missing production credential at runtime,
// from a stack trace, which is exactly what it was built to prevent.
//
// So a declared secret with no value is an ERROR BY DEFAULT — and `optional:
// true` is the single, explicit way for an author to say the absence is
// intended. It is opt-in rather than inferred: forge cannot tell a credential
// nobody has configured YET from one this environment deliberately leaves
// unset, and guessing wrong in the permissive direction turns the check off
// precisely where it matters.
//
// "Declared" is per WORKLOAD: a ref exists only for a sensitive field the
// workload names in its config_secrets. So none of this is a runtime check —
// at runtime a field must have a value only when it says `required: true`,
// sensitive or not (forge.proto's ConfigFieldOptions.optional has why).

// ContradictoryOptionality names one field that claims to be both required
// and optional.
type ContradictoryOptionality struct {
	Message string // config message declaring the field
	Field   string // proto field name
	EnvVar  string // the field's env_var, when it declares one
}

// ValidateConfigOptionality refuses a field annotated `required: true` AND
// `optional: true`.
//
// The two are direct contradictions — one says startup fails without a value,
// the other says absence is fine — and forge refuses rather than picking a
// winner. Either precedence would be a silent behaviour that an author has to
// learn from source: resolving toward `required` makes `optional` decorative
// on the field that most needs it to work, and resolving toward `optional`
// silently disables a check someone asked for. A proto author who writes both
// has not decided yet, and the fix is one line either way.
//
// It reports EVERY offender rather than the first, for the reason
// ValidateFrontendConfigs does: these arrive in batches when a config block is
// split or copied, and fixing them one generate-run at a time is miserable.
func ValidateConfigOptionality(messages []ConfigMessage) error {
	var bad []ContradictoryOptionality
	for i := range messages {
		m := &messages[i]
		for _, f := range m.Fields {
			if f.Required && f.Optional {
				bad = append(bad, ContradictoryOptionality{
					Message: m.Name,
					Field:   f.Name,
					EnvVar:  f.EnvVar,
				})
			}
		}
	}
	if len(bad) == 0 {
		return nil
	}
	sort.Slice(bad, func(i, j int) bool {
		if bad[i].Message != bad[j].Message {
			return bad[i].Message < bad[j].Message
		}
		return bad[i].Field < bad[j].Field
	})

	var b strings.Builder
	fmt.Fprintf(&b, "%d config field(s) are annotated both `required: true` and `optional: true`:\n", len(bad))
	for _, f := range bad {
		env := f.EnvVar
		if env == "" {
			env = "(no env_var)"
		}
		fmt.Fprintf(&b, "    %s.%s   [%s]\n", f.Message, f.Field, env)
	}
	b.WriteString("\nThese say opposite things: `required` fails startup when the value is\n")
	b.WriteString("missing, `optional` declares the absence intended. Keep exactly one.\n")
	b.WriteString("For a `sensitive` field, `optional: true` is also what exempts it from\n")
	b.WriteString("the secret-store pre-flight — see ValidateConfigOptionality.")
	return fmt.Errorf("%s", b.String())
}

// OptionalSecretEnvVars returns the env-var names of every sensitive field
// marked optional, across all messages.
//
// This is the seam the secret pre-flight consumes: it works in terms of env
// names (that is what a secret_ref carries by the time it reaches the store),
// so the proto-level annotation is reduced to a name set here rather than
// teaching the secrets package about config messages. The dependency runs one
// way — secrets knows nothing about proto.
//
// Non-sensitive optional fields are deliberately NOT included. `optional` on
// an ordinary field already means "no value needed" everywhere it matters,
// and nothing pre-flights it; returning it here would only invite a caller to
// think this set means something broader than it does.
func OptionalSecretEnvVars(messages []ConfigMessage) []string {
	seen := map[string]bool{}
	var out []string
	for i := range messages {
		for _, f := range messages[i].Fields {
			if !f.Sensitive || !f.Optional || f.EnvVar == "" || seen[f.EnvVar] {
				continue
			}
			seen[f.EnvVar] = true
			out = append(out, f.EnvVar)
		}
	}
	sort.Strings(out)
	return out
}

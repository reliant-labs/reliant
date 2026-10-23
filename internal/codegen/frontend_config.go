package codegen

import (
	"fmt"
	"sort"
	"strings"
)

// FrontendConfig is one frontend's resolved config surface: the message
// bound to it by (forge.v1.frontend_config).frontend, flattened to the leaf
// fields its bundle actually reads.
//
// It is the browser twin of BinaryConfig and is built the same way — own
// leaves plus the leaves of every composed block — so a shared definition
// (the OIDC issuer both halves need) is declared once and projected into
// both the binary's env and the frontend's runtime config.
type FrontendConfig struct {
	// Frontend is the forge.yaml frontends[].name this config belongs to.
	Frontend string
	// MessageName is the proto message carrying the annotation. It names
	// the generated KCL schema, so it appears in per-env config.k.
	MessageName string
	// Fields are the leaves the bundle reads, in proto declaration order:
	// own fields first, then each composed block's leaves.
	Fields []ConfigField
}

// FrontendConfigsFromMessages collects every frontend-bound config message,
// flattening composed blocks exactly as ConfigFieldsForBinary does for a
// binary. Output is sorted by frontend name so generated artifacts are
// deterministic regardless of proto declaration order.
//
// Callers MUST run ValidateFrontendConfigs first (or use it as a gate in
// the generate pipeline): this function does not itself refuse a sensitive
// field, because reporting every offender at once is more useful than
// failing on the first.
func FrontendConfigsFromMessages(messages []ConfigMessage) []FrontendConfig {
	byName := make(map[string]*ConfigMessage, len(messages))
	for i := range messages {
		byName[messages[i].Name] = &messages[i]
	}

	var out []FrontendConfig
	for i := range messages {
		m := &messages[i]
		if m.Frontend == "" {
			continue
		}
		fc := FrontendConfig{Frontend: m.Frontend, MessageName: m.Name}
		for _, f := range m.Fields {
			// A message-typed field naming another config message is a
			// composed block — the SHARED half. Its leaves keep their own
			// annotations, mirroring the per-binary flattening so a fact
			// declared once lands in both projections.
			if f.ProtoType == "message" && f.MessageType != "" {
				bm, known := byName[f.MessageType]
				if !known {
					continue
				}
				for _, bf := range bm.Fields {
					if bf.ProtoType == "message" && bf.MessageType != "" {
						continue // one nesting level, as everywhere else
					}
					fc.Fields = append(fc.Fields, bf)
				}
				continue
			}
			fc.Fields = append(fc.Fields, f)
		}
		out = append(out, fc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Frontend < out[j].Frontend })
	return out
}

// SensitiveFrontendField names one refusal: a field marked sensitive that
// reaches a frontend, and the message it was declared on (which may be a
// shared block rather than the annotated message itself — naming the
// DECLARATION site is what makes the error actionable).
type SensitiveFrontendField struct {
	Frontend    string // forge.yaml frontend name
	ConfigName  string // the frontend-bound message
	DeclaredOn  string // message the field is declared on (block or config)
	FieldName   string // proto field name
	ViaBlock    string // composing field name when reached through a block
	EnvVarOrNil string // the field's env_var, when it declares one
}

// ValidateFrontendConfigs refuses a `sensitive` field that reaches a
// frontend config.
//
// This is the highest-value rule in the frontend config system, and it is
// an ERROR rather than a warning on purpose. Everything a browser reads is
// public: a frontend's config is delivered to the client, so a secret in it
// is a published secret the moment the artifact ships. A warning would be
// read after the fact — and a leaked credential cannot be un-leaked, so the
// only useful moment to refuse is before the value is ever written into a
// bundle, a config.js, or a rendered KCL plan.
//
// It reports EVERY offender rather than the first, because a config split
// gone wrong tends to produce several at once and fixing them one
// generate-run at a time is miserable.
//
// The message is a runbook: it names the field, where it was declared
// (following composition into shared blocks, which is where this mistake
// actually happens — a BaseConfig holding both an issuer and a client
// secret, composed onto a frontend), and what to do about it.
func ValidateFrontendConfigs(messages []ConfigMessage) error {
	byName := make(map[string]*ConfigMessage, len(messages))
	for i := range messages {
		byName[messages[i].Name] = &messages[i]
	}

	var bad []SensitiveFrontendField
	for i := range messages {
		m := &messages[i]
		if m.Frontend == "" {
			continue
		}
		for _, f := range m.Fields {
			if f.ProtoType == "message" && f.MessageType != "" {
				bm, known := byName[f.MessageType]
				if !known {
					continue
				}
				for _, bf := range bm.Fields {
					if bf.ProtoType == "message" && bf.MessageType != "" {
						continue
					}
					if bf.Sensitive {
						bad = append(bad, SensitiveFrontendField{
							Frontend:    m.Frontend,
							ConfigName:  m.Name,
							DeclaredOn:  bm.Name,
							FieldName:   bf.Name,
							ViaBlock:    f.Name,
							EnvVarOrNil: bf.EnvVar,
						})
					}
				}
				continue
			}
			if f.Sensitive {
				bad = append(bad, SensitiveFrontendField{
					Frontend:    m.Frontend,
					ConfigName:  m.Name,
					DeclaredOn:  m.Name,
					FieldName:   f.Name,
					EnvVarOrNil: f.EnvVar,
				})
			}
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("%s", formatSensitiveFrontendRefusal(bad))
}

// formatSensitiveFrontendRefusal renders the runbook. Kept separate so the
// wording is testable without building a descriptor set.
func formatSensitiveFrontendRefusal(bad []SensitiveFrontendField) string {
	var b strings.Builder

	plural := "a sensitive field"
	if len(bad) > 1 {
		plural = fmt.Sprintf("%d sensitive fields", len(bad))
	}
	fmt.Fprintf(&b, "frontend config carries %s\n\n", plural)

	b.WriteString("Everything a frontend config carries is PUBLIC. It is delivered to the\n")
	b.WriteString("browser and readable by anyone who opens devtools — there is no browser\n")
	b.WriteString("equivalent of a Kubernetes Secret. forge refuses at generate time rather\n")
	b.WriteString("than warning, because a secret that reaches a built artifact has already\n")
	b.WriteString("been published and cannot be withdrawn.\n\n")

	for _, f := range bad {
		if f.ViaBlock != "" {
			fmt.Fprintf(&b, "  • %s.%s (sensitive) reaches frontend %q\n",
				f.DeclaredOn, f.FieldName, f.Frontend)
			fmt.Fprintf(&b, "    via %s.%s — a shared block composed onto a frontend config\n",
				f.ConfigName, f.ViaBlock)
		} else {
			fmt.Fprintf(&b, "  • %s.%s (sensitive) is declared directly on frontend %q's config\n",
				f.ConfigName, f.FieldName, f.Frontend)
		}
		if f.EnvVarOrNil != "" {
			fmt.Fprintf(&b, "    env_var: %s\n", f.EnvVarOrNil)
		}
	}

	b.WriteString("\nFix — pick the one that matches what the browser actually needs:\n\n")
	b.WriteString("  1. The browser does NOT need this value (the usual case).\n")
	b.WriteString("     Move the field to a message bound to a BINARY\n")
	b.WriteString("     (option (forge.v1.binary_config) = {binary: \"server\"};) and let the\n")
	b.WriteString("     backend hold it. If it is currently on a SHARED block composed onto\n")
	b.WriteString("     both halves, split that block: the public facts (issuer, client id)\n")
	b.WriteString("     stay shared, the secret moves to the binary-only message.\n\n")
	b.WriteString("  2. The browser needs the CAPABILITY the secret grants.\n")
	b.WriteString("     It needs a token minted by the backend, not the credential itself.\n")
	b.WriteString("     Add an RPC that performs the privileged call server-side.\n\n")
	b.WriteString("  3. The value is not actually secret and was mis-annotated.\n")
	b.WriteString("     Drop `sensitive: true` from its (forge.v1.config) options.\n\n")
	b.WriteString("Then re-run `forge generate`.")

	return b.String()
}

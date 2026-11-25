package codegen

import (
	"fmt"
	"strconv"
	"strings"
)

// Env families in the KCL projection (config_gen.k).
//
// A family ((forge.v1.config).env_prefix / env_suffix) is a map<string, V>
// field the runtime loader fills from every variable named
// <prefix><KEY><suffix>. Its keys are an open set the proto cannot name, so
// it projects as a KCL map keyed by KEY rather than as one member per
// variable:
//
//	map<string, string> oauth_client_ids     -> oauth_client_ids: {str: str} = {}
//	map<string, string> oauth_client_secrets -> oauth_client_secrets: {str: ConfigSecretRef} = {}
//	  (sensitive: true)
//
// and the env projection expands each entry to one variable, exactly as a
// scalar field of the same sensitivity projects:
//
//   - a non-sensitive family's entries are inline values, projected for every
//     caller (APP_OAUTH_GITHUB_CLIENT_ID = "abc");
//   - a sensitive family's entries are forge.SecretRefs, one per KEY, each
//     with store_key = the variable's own name — so a cluster workload reads
//     its own secretKeyRef and a hosted one its own managedSecret — and they
//     are projected only for a caller whose config_secrets names the family
//     by its PATTERN, "APP_OAUTH_<KEY>_CLIENT_SECRET". The pattern is the
//     family's one name: a workload reads the whole family or none of it,
//     which is what the loader does too (it reads every variable under the
//     prefix).
//
// Naming the pattern rather than each expanded variable keeps every list a
// static string set — the <Schema>_SENSITIVE_ENV constants, the process
// selector's routing and ROOTS' reach are all decided before an env's values
// exist.

// familySecretName is a sensitive family's name in config_secrets and the
// sensitive-env constants: its pattern, APP_OAUTH_<KEY>_CLIENT_SECRET.
func familySecretName(f ConfigField) string { return familyPatternOf(f) }

// familyValueField is the scalar a family's map values are, as a ConfigField
// the scalar emitters understand.
func familyValueField(f ConfigField) ConfigField {
	return ConfigField{Name: f.Name, KCLBlock: f.KCLBlock, ProtoType: f.MapValueType}
}

// kclFamilyValueType is the KCL type of a family's map values: a
// ConfigSecretRef for a sensitive family (a reference per KEY, never a
// value), else the value scalar's KCL type.
func kclFamilyValueType(f ConfigField) string {
	if f.Sensitive {
		return "ConfigSecretRef"
	}
	return kclTypeForProtoConfig(familyValueField(f))
}

// kclFamilyNameExpr is the KCL expression naming the variable of the entry
// whose key is held in keyVar: "APP_OAUTH_" + _k + "_CLIENT_ID".
func kclFamilyNameExpr(f ConfigField, keyVar string) string {
	expr := strconv.Quote(f.EnvPrefix) + " + " + keyVar
	if f.EnvSuffix != "" {
		expr += " + " + strconv.Quote(f.EnvSuffix)
	}
	return expr
}

// kclFamilyEntries is the KCL dict comprehension expanding family f, read off
// receiver recv, into env-map entries keyed by variable name.
func kclFamilyEntries(f ConfigField, recv string) string {
	name := kclFamilyNameExpr(f, "_k")
	src := recv + "." + f.KCLPath()
	if !f.Sensitive {
		return fmt.Sprintf("{%s: %s for _k, _v in %s}", name, kclValueExpr(familyValueField(f), "_v"), src)
	}
	opt := ""
	if f.Optional {
		opt = ", optional = True"
	}
	return fmt.Sprintf("{%s: forge.SecretRef {name = _v.name, key = _v.key, store_key = %s%s} for _k, _v in %s}", name, name, opt, src)
}

// familyCheckConds are the schema check lines refusing an entry a family
// cannot carry to the process:
//
//   - a KEY that is not letters, digits and underscores. The variable is
//     <prefix><KEY><suffix>, so any other character makes a name a shell
//     cannot export, or one the KEY's own suffix match misreads;
//   - an empty value of a string family. The loader reads "" as unset (as for
//     every variable), so the entry would render and never load.
func familyCheckConds(fields []ConfigField) []string {
	var conds []string
	for _, f := range fields {
		if f.EnvPrefix == "" {
			continue
		}
		conds = append(conds, "        all _k in "+f.Name+` { regex.match(_k, r"`+familyKeyPattern+`") }, "`+f.Name+
			`: ${[_k for _k in `+f.Name+` if not regex.match(_k, r'`+familyKeyPattern+`')]} cannot be a KEY of `+familyPatternOf(f)+
			` — a KEY is letters, digits and underscores"`+"\n")
		if !f.Sensitive && (f.MapValueType == "string" || f.MapValueType == "bytes") {
			conds = append(conds, fmt.Sprintf(
				"        all _k, _v in %s { _v != \"\" }, %s\n",
				f.Name, emptyFamilyValueMessage(f.Name)))
		}
	}
	return conds
}

// emptyFamilyValueMessage is the KCL string literal (interpolating the
// offending keys) a string family's empty-value check fails with.
func emptyFamilyValueMessage(field string) string {
	return `"` + field + `: ${[_k for _k, _v in ` + field + ` if _v == '']} set to \"\" cannot reach the process: an empty variable counts as unset — remove the entry instead"`
}

// familyKeyPattern is a family KEY: what may stand between prefix and suffix
// in an environment variable name.
const familyKeyPattern = `^[A-Za-z0-9_]+$`

// hasEnvFamily reports whether any field is an env family — whether the
// generated module needs the regex import its key checks use.
func hasEnvFamily(fieldSets ...[]ConfigField) bool {
	for _, fields := range fieldSets {
		for _, f := range fields {
			if f.EnvPrefix != "" {
				return true
			}
		}
	}
	return false
}

// configModuleImports is config_gen.k's import block: forge, for the
// projection's SecretRef, and regex when a family's key checks need it.
func configModuleImports(fieldSets ...[]ConfigField) string {
	var b strings.Builder
	b.WriteString("import forge\n")
	if hasEnvFamily(fieldSets...) {
		b.WriteString("import regex\n")
	}
	b.WriteString("\n")
	return b.String()
}

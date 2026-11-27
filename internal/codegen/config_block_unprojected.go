package codegen

import (
	"fmt"
	"sort"
	"strings"
)

// Config blocks nested more than one level deep.
//
// The KCL projection expands one level of composed blocks
// (flattenWithBlocks): `AppConfig.stripe.secret_key` is projected, but a block
// inside a block — `ServerProcessConfig.client_versions.daemon.min_version` —
// is not. The Go loader still reads it, so the field exists at run time and is
// missing from config_gen.k, its env map and every root's reach: a deployer
// cannot set it through config.k. That used to happen without a word, which is
// how a field moved into a deeper block disappeared from KCL unnoticed (forge
// bugs 938f0340, b0872b84). UnprojectedNestedConfigBlocks names each such
// block, so generate can say so.

// UnprojectedNestedConfigBlocks describes every config block the KCL
// projection drops because it is nested more than one block deep in a backend
// config message, with the env vars of the leaves it holds. Sorted, one entry
// per dropped block; empty when nothing is dropped. Frontend configs are
// projected by their own emitter and are not considered.
func UnprojectedNestedConfigBlocks(messages []ConfigMessage) []string {
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
	seen := map[string]bool{}
	var out []string
	for _, m := range messages {
		// Only a message the projection starts from: a block is reported
		// from the root that composes it, not again as a root of its own.
		if m.Frontend != "" || composed[m.Name] {
			continue
		}
		for _, f := range m.Fields {
			block, ok := byName[f.MessageType]
			if !ok || f.MessageType == "" {
				continue
			}
			for _, bf := range block.Fields {
				inner, ok := byName[bf.MessageType]
				if !ok || bf.MessageType == "" {
					continue
				}
				path := m.Name + "." + f.Name + "." + bf.Name
				if seen[path] {
					continue
				}
				seen[path] = true
				envs := nestedLeafEnvVars(inner, byName, map[string]bool{block.Name: true})
				what := "its fields"
				if len(envs) > 0 {
					what = "its fields (" + strings.Join(envs, ", ") + ")"
				}
				out = append(out, fmt.Sprintf("%s (%s, inside the %s block) is nested two blocks deep, so deploy/kcl/config_gen.k does not project it: %s cannot be set from config.k and are missing from its env map and root reach. forge projects one level of config blocks; move the fields up into %s or into a block %s composes directly",
					path, bf.MessageType, f.Name, what, block.Name, m.Name))
			}
		}
	}
	sort.Strings(out)
	return out
}

// nestedLeafEnvVars is every env var of m's leaves, at any depth below it,
// sorted. visiting guards against a block that composes itself.
func nestedLeafEnvVars(m *ConfigMessage, byName map[string]*ConfigMessage, visiting map[string]bool) []string {
	if visiting[m.Name] {
		return nil
	}
	visiting[m.Name] = true
	defer delete(visiting, m.Name)
	var envs []string
	for _, f := range m.Fields {
		if inner, ok := byName[f.MessageType]; ok && f.MessageType != "" {
			envs = append(envs, nestedLeafEnvVars(inner, byName, visiting)...)
			continue
		}
		switch {
		case f.EnvVar != "":
			envs = append(envs, f.EnvVar)
		case f.EnvPrefix != "":
			envs = append(envs, f.EnvPrefix+"<KEY>"+f.EnvSuffix)
		}
	}
	sort.Strings(envs)
	return envs
}

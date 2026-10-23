package codegen

// Composed config blocks in the KCL projection.
//
// A config block is its own namespace in proto: `StaticSiteConfig.base_domain`
// and `SimpleBackendConfig.base_domain` are two different fields, bound to two
// different env vars, and the Go loader resolves each through its own message.
// Nothing about declaring both is a proto error.
//
// The KCL projection used to FLATTEN every block's leaves into the one
// `schema AppConfig` ("nested schemas are not yet projected"), so two blocks
// declaring the same leaf name arrived as two declarations of one field. The
// first mitigation qualified contested names (`simple_backend_base_domain`),
// which meant the key an operator typed in config.k depended on whether some
// OTHER block happened to declare the same leaf — adding a field to one block
// could rename a field in another.
//
// Blocks are now projected as NESTED schemas, which is what they are:
//
//	schema AppConfigStripe:            # one schema per composed block
//	    secret_key: ConfigSecretRef = …
//	schema AppConfig:
//	    stripe: AppConfigStripe = AppConfigStripe {}
//
// and config.k authors a leaf by its path, `stripe.secret_key = …` (KCL merges
// a path key into the nested schema's defaults). The namespace the proto has is
// the namespace KCL has, so no leaf name can collide across blocks and none is
// ever renamed. The flattener's job is only to hand the emitters one ordered
// field list in which every block leaf carries its KCLBlock.

// FlattenBlockLeaves projects rootMessage's fields into the field list the KCL
// emitters consume: its own scalar leaves, plus one level of the leaves of
// every config block it composes, each tagged with the block it belongs to
// (ConfigField.KCLBlock). Names are never rewritten.
func FlattenBlockLeaves(messages []ConfigMessage, rootMessage string) []ConfigField {
	byName := make(map[string]*ConfigMessage, len(messages))
	for i := range messages {
		byName[messages[i].Name] = &messages[i]
	}

	var root *ConfigMessage
	for i := range messages {
		if messages[i].Name == rootMessage {
			root = &messages[i]
			break
		}
	}
	if root == nil {
		return nil
	}

	return flattenWithBlocks(root.Fields, byName)
}

// FlattenFieldsWithBlocks is FlattenBlockLeaves for a caller that already holds
// the field list — the per-binary and multi-root-message emitters, which select
// their fields by a rule of their own before flattening.
func FlattenFieldsWithBlocks(fields []ConfigField, messages []ConfigMessage) []ConfigField {
	byName := make(map[string]*ConfigMessage, len(messages))
	for i := range messages {
		byName[messages[i].Name] = &messages[i]
	}
	return flattenWithBlocks(fields, byName)
}

// flattenWithBlocks is the shared core: it walks one field list and expands
// each composed block one level, tagging every expanded leaf with its block.
// The block-reference field itself is dropped — the nested schema IS its
// projection. A reference to an unknown message is dropped too (nothing to
// project), exactly as before.
//
// An env family (env_prefix) is dropped as well: these lists feed the KCL
// schema and env projection, which have no shape for an open-ended set of
// variables, so a family's variables are set on a workload's env directly
// (ConfigFieldOptions.env_prefix in forge.proto).
func flattenWithBlocks(fields []ConfigField, byName map[string]*ConfigMessage) []ConfigField {
	var out []ConfigField
	for _, f := range fields {
		if f.EnvPrefix != "" {
			continue
		}
		if f.MessageType == "" {
			out = append(out, f)
			continue
		}
		bm, known := byName[f.MessageType]
		if !known {
			continue
		}
		for _, bf := range bm.Fields {
			if bf.MessageType != "" || bf.EnvPrefix != "" {
				continue // one nesting level, as elsewhere; families are not projected
			}
			bf.KCLBlock = f.Name
			bf.KCLBlockType = f.MessageType
			out = append(out, bf)
		}
	}
	return out
}

package wfcel

import (
	"strings"
	"unicode"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// TypeRegistry provides CEL-aware type information for all workflow message types.
// It maps node type strings (e.g., "call_llm") to their args and output message descriptors.
type TypeRegistry struct {
	// nodeArgs maps node type string → args message descriptor
	nodeArgs map[string]protoreflect.MessageDescriptor
	// nodeOutputs maps node type string → output message descriptor
	nodeOutputs map[string]protoreflect.MessageDescriptor
}

// NewTypeRegistry creates a TypeRegistry by inspecting proto descriptors.
// It dynamically discovers both args and output types from the proto file descriptor
// that contains V2Node. Args are read from the V2Node.args oneof, and outputs are
// discovered by finding V2*Output messages and matching them to node types via
// PascalCase→snake_case conversion.
func NewTypeRegistry() *TypeRegistry {
	r := &TypeRegistry{
		nodeArgs:    make(map[string]protoreflect.MessageDescriptor),
		nodeOutputs: make(map[string]protoreflect.MessageDescriptor),
	}

	// Build node type → args descriptor from V2Node.args oneof.
	nodeDesc := (&reliantv1.Node{}).ProtoReflect().Descriptor()
	argsOneof := nodeDesc.Oneofs().ByName("args")
	if argsOneof != nil {
		for i := 0; i < argsOneof.Fields().Len(); i++ {
			fd := argsOneof.Fields().Get(i)
			// The oneof field name is the node type (e.g., "call_llm", "run", "loop").
			nodeType := string(fd.Name())
			if fd.Kind() == protoreflect.MessageKind {
				r.nodeArgs[nodeType] = fd.Message()
			}
		}
	}

	// Build node type → output descriptor dynamically from the proto file.
	// Output messages follow the convention V2{PascalCase}Output where PascalCase
	// maps to the snake_case node type. We iterate all messages in the same file
	// as V2Node and match them to known node types.
	outputsBySnake := discoverOutputTypes(nodeDesc.ParentFile())
	for nodeType := range r.nodeArgs {
		if md := matchOutputType(nodeType, outputsBySnake); md != nil {
			r.nodeOutputs[nodeType] = md
		}
	}

	return r
}

// matchOutputType finds the best-matching output descriptor for a node type.
// It tries an exact match first, then progressively strips trailing _word
// segments to handle cases like node type "save_message_node" matching output
// "V2SaveMessageOutput" (snake key "save_message").
func matchOutputType(nodeType string, outputs map[string]protoreflect.MessageDescriptor) protoreflect.MessageDescriptor {
	// Exact match.
	if md, ok := outputs[nodeType]; ok {
		return md
	}
	// Try stripping trailing _word segments.
	candidate := nodeType
	for {
		idx := strings.LastIndex(candidate, "_")
		if idx <= 0 {
			break
		}
		candidate = candidate[:idx]
		if md, ok := outputs[candidate]; ok {
			return md
		}
	}
	return nil
}

// discoverOutputTypes scans all top-level messages in a proto file descriptor
// and returns a map from snake_case node type to message descriptor for every
// message matching either *Output or V2*Output naming conventions.
//
// For example, CallLLMOutput / V2CallLLMOutput → "call_llm", RunOutput / V2RunOutput → "run".
func discoverOutputTypes(fd protoreflect.FileDescriptor) map[string]protoreflect.MessageDescriptor {
	result := make(map[string]protoreflect.MessageDescriptor)
	msgs := fd.Messages()
	for i := 0; i < msgs.Len(); i++ {
		md := msgs.Get(i)
		name := string(md.Name())
		if !strings.HasSuffix(name, "Output") {
			continue
		}

		// Strip optional "V2" prefix, then strip "Output" suffix to get PascalCase core.
		base := strings.TrimPrefix(name, "V2")
		if !strings.HasSuffix(base, "Output") {
			continue
		}
		core := base[:len(base)-6] // e.g. "CallLLM", "Run", "SaveMessage"
		if core == "" {
			continue
		}
		snake := pascalToSnake(core)
		result[snake] = md
	}
	return result
}

// pascalToSnake converts a PascalCase (or mixed-case with acronyms) string
// to snake_case. It handles consecutive uppercase runs as acronyms:
// "CallLLM" → "call_llm", "SaveMessage" → "save_message",
// "CreateWorktree" → "create_worktree", "ExecuteTools" → "execute_tools".
func pascalToSnake(s string) string {
	var b strings.Builder
	runes := []rune(s)
	for i, r := range runes {
		if unicode.IsUpper(r) {
			if i > 0 {
				// Insert underscore before an uppercase letter if:
				// - previous char is lowercase, OR
				// - this starts a new word (next char is lowercase) in an acronym run
				prevLower := unicode.IsLower(runes[i-1])
				nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
				if prevLower || (nextLower && unicode.IsUpper(runes[i-1])) {
					b.WriteRune('_')
				}
			}
			b.WriteRune(unicode.ToLower(r))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ArgsForNodeType returns the args message descriptor for a node type.
func (r *TypeRegistry) ArgsForNodeType(nodeType string) (protoreflect.MessageDescriptor, bool) {
	md, ok := r.nodeArgs[nodeType]
	return md, ok
}

// OutputForNodeType returns the output message descriptor for a node type.
func (r *TypeRegistry) OutputForNodeType(nodeType string) (protoreflect.MessageDescriptor, bool) {
	md, ok := r.nodeOutputs[nodeType]
	return md, ok
}

// FieldsForNodeType returns FieldInfo for the args of a node type.
func (r *TypeRegistry) FieldsForNodeType(nodeType string) []FieldInfo {
	md, ok := r.nodeArgs[nodeType]
	if !ok {
		return nil
	}
	return ExtractFieldInfo(md)
}

// OutputFieldsForNodeType returns FieldInfo for the output of a node type.
func (r *TypeRegistry) OutputFieldsForNodeType(nodeType string) []FieldInfo {
	md, ok := r.nodeOutputs[nodeType]
	if !ok {
		return nil
	}
	return ExtractFieldInfo(md)
}

// NodeTypes returns all registered node type names.
func (r *TypeRegistry) NodeTypes() []string {
	types := make([]string, 0, len(r.nodeArgs))
	for t := range r.nodeArgs {
		types = append(types, t)
	}
	return types
}

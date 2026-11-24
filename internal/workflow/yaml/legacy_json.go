package wfyaml

import (
	"encoding/json"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// HasLegacyWorkflowJSON identifies explicit authoring syntax before a tolerant
// protobuf decode can discard it. Unknown additive keys alone are not legacy
// syntax. The bindings are shared with the YAML parser, so new node arguments
// do not require a second list here.
func HasLegacyWorkflowJSON(data []byte) (bool, error) {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return false, err
	}
	return hasLegacyJSON(raw, (&reliantv1.Workflow{}).ProtoReflect().Descriptor()), nil
}

func hasLegacyJSON(raw any, md protoreflect.MessageDescriptor) bool {
	obj, ok := raw.(map[string]any)
	if !ok {
		return false
	}
	for key, value := range obj {
		fd := md.Fields().ByJSONName(key)
		if fd == nil {
			fd = md.Fields().ByName(protoreflect.Name(key))
		}
		if fd == nil {
			switch md.FullName() {
			case "reliant.v1.Workflow":
				if key == "sequence" {
					return true
				}
			case "reliant.v1.Node":
				nodeType, _ := obj["type"].(string)
				binding, known := generatedNodeBindingForType(nodeType)
				if known {
					_, arg := binding.argFieldKeys[key]
					if key == "args" || arg {
						return true
					}
				}
			}
			continue
		}
		// Authoring JSON permits scalar CEL wrappers and string-or-list
		// routing fields; protobuf JSON requires their message/list shape.
		if fd.IsList() && fd.Kind() == protoreflect.StringKind {
			if _, scalar := value.(string); scalar {
				return true
			}
		}
		if msg := fd.Message(); msg != nil {
			if _, scalar := value.(string); scalar && (msg.Name() == "DirectCelBool" || msg.Name() == "CelString" || msg.Name() == "CelBool" || msg.Name() == "CelInt" || msg.Name() == "CelDouble") {
				return true
			}
		}
		if fd.Message() == nil || fd.IsMap() {
			continue
		}
		if fd.IsList() {
			items, _ := value.([]any)
			for _, item := range items {
				if hasLegacyJSON(item, fd.Message()) {
					return true
				}
			}
		} else if hasLegacyJSON(value, fd.Message()) {
			return true
		}
	}
	return false
}

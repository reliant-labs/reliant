package wfyaml

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"google.golang.org/protobuf/proto"
)

func TestGeneratedNodeBindingsCoverAllNodeArgsOneofFields(t *testing.T) {
	nodeDescriptor := (&reliantv1.Node{}).ProtoReflect().Descriptor()
	argsOneof := nodeDescriptor.Oneofs().ByName("args")
	if argsOneof == nil {
		t.Fatal("node descriptor missing args oneof")
	}

	seenNodeTypes := map[string]struct{}{}
	fields := argsOneof.Fields()
	for i := 0; i < fields.Len(); i++ {
		oneofField := fields.Get(i)
		argsMessage := oneofField.Message()
		if argsMessage == nil {
			t.Fatalf("args oneof field %q has no message descriptor", oneofField.Name())
		}
		messageOptions := argsMessage.Options()
		if messageOptions == nil {
			t.Fatalf("args message %q has no options", argsMessage.FullName())
		}
		extension := proto.GetExtension(messageOptions, reliantv1.E_NodeMeta)
		meta, ok := extension.(*reliantv1.NodeMeta)
		if !ok || meta == nil || meta.NodeType == "" {
			t.Fatalf("args message %q missing valid NodeMeta.node_type", argsMessage.FullName())
		}
		nodeType := meta.NodeType
		seenNodeTypes[nodeType] = struct{}{}

		binding, bindingExists := generatedNodeBindingForType(nodeType)
		if !bindingExists {
			t.Fatalf("generated binding missing for node type %q", nodeType)
		}
		if binding.oneofFieldName != oneofField.Name() {
			t.Fatalf("binding oneof mismatch for node type %q: got %q want %q", nodeType, binding.oneofFieldName, oneofField.Name())
		}
		if binding.isStructural != meta.IsStructural {
			t.Fatalf("binding structural mismatch for node type %q: got %v want %v", nodeType, binding.isStructural, meta.IsStructural)
		}

		for fieldIdx := 0; fieldIdx < argsMessage.Fields().Len(); fieldIdx++ {
			argKey := string(argsMessage.Fields().Get(fieldIdx).Name())
			if _, hasKey := binding.argFieldKeys[argKey]; !hasKey {
				t.Fatalf("binding missing arg key %q for node type %q", argKey, nodeType)
			}
		}
	}

	for generatedNodeType := range generatedNodeBindingsByType {
		if _, exists := seenNodeTypes[generatedNodeType]; !exists {
			t.Fatalf("generated node binding has stale node type %q not present in proto args oneof", generatedNodeType)
		}
	}
}

func TestGeneratedCoreKeysAreDescriptorDerived(t *testing.T) {
	nodeDescriptor := (&reliantv1.Node{}).ProtoReflect().Descriptor()
	edgeDescriptor := (&reliantv1.Edge{}).ProtoReflect().Descriptor()
	edgeCaseDescriptor := (&reliantv1.EdgeCase{}).ProtoReflect().Descriptor()

	if yamlKeyID != string(nodeDescriptor.Fields().ByName("id").Name()) {
		t.Fatalf("yamlKeyID drifted from proto descriptor")
	}
	if yamlKeyType != string(nodeDescriptor.Fields().ByName("type").Name()) {
		t.Fatalf("yamlKeyType drifted from proto descriptor")
	}
	if yamlKeyCondition != string(nodeDescriptor.Fields().ByName("condition").Name()) {
		t.Fatalf("yamlKeyCondition drifted from proto descriptor")
	}
	if yamlKeyTimeout != string(nodeDescriptor.Fields().ByName("timeout").Name()) {
		t.Fatalf("yamlKeyTimeout drifted from proto descriptor")
	}
	if yamlKeySaveMessage != string(nodeDescriptor.Fields().ByName("save_message").Name()) {
		t.Fatalf("yamlKeySaveMessage drifted from proto descriptor")
	}
	if yamlKeyDaemon != string(nodeDescriptor.Fields().ByName("daemon").Name()) {
		t.Fatalf("yamlKeyDaemon drifted from proto descriptor")
	}
	if yamlKeyArgs != string(nodeDescriptor.Oneofs().ByName("args").Name()) {
		t.Fatalf("yamlKeyArgs drifted from proto descriptor")
	}

	if yamlKeyFrom != string(edgeDescriptor.Fields().ByName("from").Name()) {
		t.Fatalf("yamlKeyFrom drifted from proto descriptor")
	}
	if yamlKeyCases != string(edgeDescriptor.Fields().ByName("cases").Name()) {
		t.Fatalf("yamlKeyCases drifted from proto descriptor")
	}
	if yamlKeyDefault != string(edgeDescriptor.Fields().ByName("default").Name()) {
		t.Fatalf("yamlKeyDefault drifted from proto descriptor")
	}

	if yamlKeyTo != string(edgeCaseDescriptor.Fields().ByName("to").Name()) {
		t.Fatalf("yamlKeyTo drifted from proto descriptor")
	}
	if yamlKeyLabel != string(edgeCaseDescriptor.Fields().ByName("label").Name()) {
		t.Fatalf("yamlKeyLabel drifted from proto descriptor")
	}
}

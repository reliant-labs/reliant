package model

import (
	"sync"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"google.golang.org/protobuf/proto"
)

var (
	discoveredMetas     map[string]*reliantv1.NodeMeta
	discoveredMetasOnce sync.Once
)

// DiscoverNodeMetas reads NodeMeta annotations from all args messages
// in the V2Node.args oneof, returning a map of node_type -> NodeMeta.
func DiscoverNodeMetas() map[string]*reliantv1.NodeMeta {
	discoveredMetasOnce.Do(func() {
		discoveredMetas = discoverNodeMetasFromDescriptor()
	})
	return discoveredMetas
}

func discoverNodeMetasFromDescriptor() map[string]*reliantv1.NodeMeta {
	result := make(map[string]*reliantv1.NodeMeta)

	// Get the V2Node message descriptor.
	nodeDesc := (&reliantv1.Node{}).ProtoReflect().Descriptor()

	// Find the "args" oneof.
	oneofs := nodeDesc.Oneofs()
	argsOneof := oneofs.ByName("args")
	if argsOneof == nil {
		return result
	}

	// Iterate all fields in the oneof.
	fields := argsOneof.Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		msgDesc := fd.Message()
		if msgDesc == nil {
			continue
		}

		// Check for the node_meta extension on the message options.
		opts := msgDesc.Options()
		if opts == nil {
			continue
		}
		ext := proto.GetExtension(opts, reliantv1.E_NodeMeta)
		if ext == nil {
			continue
		}
		meta, ok := ext.(*reliantv1.NodeMeta)
		if !ok || meta == nil || meta.NodeType == "" {
			continue
		}

		result[meta.NodeType] = meta
	}

	return result
}

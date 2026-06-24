package wfyaml

import (
	"fmt"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"gopkg.in/yaml.v3"
)

// ParseDraftWorkflow parses workflow draft YAML into a Workflow proto.
//
// Drafts produced by legacy UI paths may place activity-node arg fields at the
// node top level instead of under "args". Before parsing, this function
// normalizes those fields into the activity node's args mapping by using proto
// descriptors for the node type, avoiding duplicated stringly key ownership in
// service layers.
func ParseDraftWorkflow(data []byte) (*reliantv1.Workflow, error) {
	normalizedYAML, err := normalizeDraftWorkflowYAML(data)
	if err != nil {
		return nil, err
	}
	return ParseWorkflow(normalizedYAML)
}

func normalizeDraftWorkflowYAML(data []byte) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, fmt.Errorf("v2yaml: expected document node")
	}

	workflowNode := doc.Content[0]
	nodesNode := getYAMLField(workflowNode, "nodes")
	if nodesNode != nil {
		normalizeDraftNodeList(nodesNode)
	}

	normalized, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, err
	}
	return normalized, nil
}

func normalizeDraftNodeList(nodesNode *yaml.Node) {
	if nodesNode == nil || nodesNode.Kind != yaml.SequenceNode {
		return
	}
	for _, item := range nodesNode.Content {
		normalizeDraftNode(item)
	}
}

func normalizeDraftNode(node *yaml.Node) {
	if node == nil || node.Kind != yaml.MappingNode {
		return
	}

	nodeType := getYAMLFieldString(node, yamlKeyType)
	if nodeType == "" {
		normalizeDraftInlineWorkflow(node)
		return
	}

	binding, ok := generatedNodeBindingForType(nodeType)
	if ok && !binding.isStructural {
		moveTopLevelActivityArgsIntoArgs(node, getActivityArgsFieldSet(nodeType))
	}

	normalizeDraftInlineWorkflow(node)
}

func normalizeDraftInlineWorkflow(node *yaml.Node) {
	inlineNode := getYAMLField(node, "inline")
	if inlineNode == nil || inlineNode.Kind != yaml.MappingNode {
		return
	}
	inlineNodes := getYAMLField(inlineNode, "nodes")
	normalizeDraftNodeList(inlineNodes)
}

func moveTopLevelActivityArgsIntoArgs(node *yaml.Node, activityArgsFields map[string]bool) {
	if len(activityArgsFields) == 0 {
		return
	}

	argsNode := getYAMLField(node, yamlKeyArgs)
	if argsNode != nil && argsNode.Kind != yaml.MappingNode {
		// Let downstream typed parsing return a clear args type error.
		return
	}

	movedArgs := make(map[string]*yaml.Node)
	updatedContent := make([]*yaml.Node, 0, len(node.Content))
	for i := 0; i < len(node.Content); i += 2 {
		keyNode := node.Content[i]
		valueNode := node.Content[i+1]
		key := keyNode.Value

		if key == yamlKeyArgs {
			updatedContent = append(updatedContent, keyNode, valueNode)
			continue
		}
		if activityArgsFields[key] {
			movedArgs[key] = valueNode
			continue
		}
		updatedContent = append(updatedContent, keyNode, valueNode)
	}

	if len(movedArgs) == 0 {
		node.Content = updatedContent
		return
	}

	if argsNode == nil {
		argsNode = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		updatedContent = append(updatedContent, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: yamlKeyArgs}, argsNode)
	}

	for key, valueNode := range movedArgs {
		setYAMLMappingFieldIfMissing(argsNode, key, valueNode)
	}

	node.Content = updatedContent
}

func setYAMLMappingFieldIfMissing(mappingNode *yaml.Node, key string, value *yaml.Node) {
	if mappingNode == nil || mappingNode.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i < len(mappingNode.Content); i += 2 {
		if mappingNode.Content[i].Value == key {
			return
		}
	}
	mappingNode.Content = append(mappingNode.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		value,
	)
}

func getActivityArgsFieldSet(nodeType string) map[string]bool {
	binding, ok := generatedNodeBindingForType(nodeType)
	if !ok {
		return nil
	}
	fieldSet := make(map[string]bool, len(binding.argFieldKeys))
	for key := range binding.argFieldKeys {
		fieldSet[key] = true
	}
	return fieldSet
}

// Copyright (c) 2025 Reliant Labs
package validation

import (
	"reflect"
	"sort"
	"strings"

	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
)

// =============================================================================
// WORKFLOW TYPE PROVIDER
// =============================================================================

// workflowTypeProvider implements the types.Provider interface to provide
// custom type information for workflow-specific CEL namespaces.
type workflowTypeProvider struct {
	base    types.Provider
	typeCtx *WorkflowTypeContext
}

func newWorkflowTypeProvider(base types.Provider, typeCtx *WorkflowTypeContext) *workflowTypeProvider {
	return &workflowTypeProvider{
		base:    base,
		typeCtx: typeCtx,
	}
}

// =============================================================================
// PROVIDER INTERFACE IMPLEMENTATION
// =============================================================================

func (p *workflowTypeProvider) FindStructType(structType string) (*types.Type, bool) {
	switch structType {
	case "nodes":
		return types.NewTypeTypeWithParam(types.NewObjectType("nodes")), true
	case "inputs":
		return types.NewTypeTypeWithParam(types.NewObjectType("inputs")), true
	case "outputs":
		return types.NewTypeTypeWithParam(types.NewObjectType("outputs")), true
	case "output":
		if p.typeCtx != nil && p.typeCtx.CurrentNodeOutputType != nil {
			return types.NewTypeTypeWithParam(p.typeCtx.CurrentNodeOutputType), true
		}
		return nil, false
	}

	// Synthetic node output types (node_output.call_llm, etc.)
	if strings.HasPrefix(structType, "node_output.") {
		return types.NewTypeTypeWithParam(types.NewObjectType(structType)), true
	}

	if p.typeCtx != nil && strings.HasPrefix(structType, "inputs.") {
		if p.isSyntheticInputType(structType) {
			return types.NewObjectType(structType), true
		}
	}

	return p.base.FindStructType(structType)
}

func (p *workflowTypeProvider) FindStructFieldNames(structType string) ([]string, bool) {
	if p.typeCtx == nil {
		return p.base.FindStructFieldNames(structType)
	}

	switch structType {
	case "nodes":
		nodeIDs := make([]string, 0, len(p.typeCtx.NodeTypes))
		for nodeID := range p.typeCtx.NodeTypes {
			nodeIDs = append(nodeIDs, nodeID)
		}
		sort.Strings(nodeIDs)
		return nodeIDs, true

	case "inputs":
		inputNames := make([]string, 0, len(p.typeCtx.InputFields)+len(p.typeCtx.InputGroups))
		for name := range p.typeCtx.InputFields {
			inputNames = append(inputNames, name)
		}
		for name := range p.typeCtx.InputGroups {
			inputNames = append(inputNames, name)
		}
		sort.Strings(inputNames)
		return inputNames, true

	case "outputs":
		if p.typeCtx.OutputFields != nil {
			outputNames := make([]string, 0, len(p.typeCtx.OutputFields))
			for name := range p.typeCtx.OutputFields {
				outputNames = append(outputNames, name)
			}
			sort.Strings(outputNames)
			return outputNames, true
		}
	case "output":
		return p.findCurrentOutputFieldNames()
	}

	if strings.HasPrefix(structType, "inputs.") {
		if fieldNames := p.getNestedInputFieldNames(structType); fieldNames != nil {
			return fieldNames, true
		}
	}

	// Resolve synthetic node output types: "node_output.{nodeID}" and
	// nested message types like "node_output.{nodeID}.{fieldName}".
	if strings.HasPrefix(structType, "node_output.") {
		rest := strings.TrimPrefix(structType, "node_output.")
		// Check for nested message type: node_output.{nodeID}.{fieldName}
		if idx := strings.IndexByte(rest, '.'); idx >= 0 {
			nodeID := rest[:idx]
			fieldName := rest[idx+1:]
			if nodeFields, ok := p.typeCtx.NodeOutputs[nodeID]; ok {
				if fi, ok := nodeFields[fieldName]; ok && len(fi.Properties) > 0 {
					names := make([]string, 0, len(fi.Properties))
					for name := range fi.Properties {
						names = append(names, name)
					}
					sort.Strings(names)
					return names, true
				}
			}
		} else {
			// Top-level node output: node_output.{nodeID}
			nodeID := rest
			if fields, ok := p.typeCtx.NodeOutputs[nodeID]; ok {
				fieldNames := make([]string, 0, len(fields))
				for name := range fields {
					fieldNames = append(fieldNames, name)
				}
				sort.Strings(fieldNames)
				return fieldNames, true
			}
		}
	}

	return p.base.FindStructFieldNames(structType)
}

func (p *workflowTypeProvider) FindStructFieldType(structType, fieldName string) (*types.FieldType, bool) {
	if p.typeCtx == nil {
		return p.base.FindStructFieldType(structType, fieldName)
	}

	switch structType {
	case "nodes":
		return p.findNodeFieldType(fieldName)
	case "inputs":
		return p.findInputFieldType(fieldName)
	case "outputs":
		return p.findOutputFieldType(fieldName)
	case "output":
		return p.findCurrentOutputFieldType(fieldName)
	}

	if strings.HasPrefix(structType, "inputs.") {
		if fieldType, ok := p.findNestedInputFieldType(structType, fieldName); ok {
			return fieldType, true
		}
		if p.isAdditionalPropertiesAllowed(structType) {
			return &types.FieldType{Type: types.DynType}, true
		}
		return nil, false
	}

	// Resolve synthetic node output types: "node_output.{nodeID}" and
	// nested types like "node_output.{nodeID}.{fieldName}".
	if strings.HasPrefix(structType, "node_output.") {
		rest := strings.TrimPrefix(structType, "node_output.")
		// Check for nested message type: node_output.{nodeID}.{fieldName}
		if idx := strings.IndexByte(rest, '.'); idx >= 0 {
			nodeID := rest[:idx]
			parentField := rest[idx+1:]
			if nodeFields, ok := p.typeCtx.NodeOutputs[nodeID]; ok {
				if fi, ok := nodeFields[parentField]; ok {
					if len(fi.Properties) > 0 {
						if subField, ok := fi.Properties[fieldName]; ok {
							celType := fieldInfoToCELType(subField)
							return &types.FieldType{Type: celType}, true
						}
						if fi.AdditionalPropertiesAllowed || fi.IsDynamic {
							return &types.FieldType{Type: types.DynType}, true
						}
						return nil, false
					}
					if fi.IsDynamic {
						return &types.FieldType{Type: types.DynType}, true
					}
					return nil, false
				}
			}
			return nil, false
		}

		// Top-level node output: node_output.{nodeID}
		nodeID := rest
		if nodeOutputFields, ok := p.typeCtx.NodeOutputs[nodeID]; ok {
			if fieldInfo, ok := nodeOutputFields[fieldName]; ok {
				// If this field has nested properties, return an object type
				// so sub-field access is resolved through the provider.
				if len(fieldInfo.Properties) > 0 {
					objType := types.NewObjectType(structType + "." + fieldName)
					return &types.FieldType{Type: objType}, true
				}
				celType := fieldInfoToCELType(fieldInfo)
				return &types.FieldType{Type: celType}, true
			}
			// Unknown field: allow dyn for node types that can have runtime-extended
			// outputs (structured output from response tools), reject for others.
			if p.nodeCanHaveExtendedOutputs(nodeID) {
				return &types.FieldType{Type: types.DynType}, true
			}
			return nil, false
		}
	}

	return p.base.FindStructFieldType(structType, fieldName)
}

func (p *workflowTypeProvider) findNodeFieldType(nodeID string) (*types.FieldType, bool) {
	nodeType, exists := p.typeCtx.NodeTypes[nodeID]
	if !exists {
		return nil, false
	}

	celType := getNodeOutputCELType(p.typeCtx.Registry, nodeType, nodeID)
	if celType == nil {
		return &types.FieldType{Type: types.DynType}, true
	}

	return &types.FieldType{Type: celType}, true
}

func (p *workflowTypeProvider) findInputFieldType(inputName string) (*types.FieldType, bool) {
	fieldInfo, exists := p.typeCtx.InputFields[inputName]
	if !exists {
		if _, ok := p.typeCtx.InputGroups[inputName]; ok {
			return &types.FieldType{Type: types.NewObjectType("inputs." + inputName)}, true
		}
		return nil, false
	}

	if len(fieldInfo.Properties) > 0 {
		return &types.FieldType{Type: types.NewObjectType("inputs." + inputName)}, true
	}

	celType := fieldInfoToCELType(fieldInfo)
	return &types.FieldType{Type: celType}, true
}

func (p *workflowTypeProvider) findOutputFieldType(outputName string) (*types.FieldType, bool) {
	if p.typeCtx.OutputFields == nil {
		return &types.FieldType{Type: types.DynType}, true
	}

	fieldInfo, exists := p.typeCtx.OutputFields[outputName]
	if !exists {
		return nil, false
	}

	celType := fieldInfoToCELType(fieldInfo)
	return &types.FieldType{Type: celType}, true
}

func (p *workflowTypeProvider) findCurrentOutputFieldType(fieldName string) (*types.FieldType, bool) {
	if p.typeCtx == nil || p.typeCtx.CurrentNodeID == "" {
		return &types.FieldType{Type: types.DynType}, true
	}

	if nodeOutputFields, ok := p.typeCtx.NodeOutputs[p.typeCtx.CurrentNodeID]; ok {
		if fieldInfo, ok := nodeOutputFields[fieldName]; ok {
			if len(fieldInfo.Properties) > 0 {
				objType := types.NewObjectType("node_output." + p.typeCtx.CurrentNodeID + "." + fieldName)
				return &types.FieldType{Type: objType}, true
			}
			celType := fieldInfoToCELType(fieldInfo)
			return &types.FieldType{Type: celType}, true
		}
		// Unknown field: allow dyn for node types that can have runtime-extended outputs.
		if p.nodeCanHaveExtendedOutputs(p.typeCtx.CurrentNodeID) {
			return &types.FieldType{Type: types.DynType}, true
		}
		return nil, false
	}

	return &types.FieldType{Type: types.DynType}, true
}

func (p *workflowTypeProvider) findCurrentOutputFieldNames() ([]string, bool) {
	if p.typeCtx == nil || p.typeCtx.CurrentNodeID == "" {
		return nil, false
	}

	if nodeOutputFields, ok := p.typeCtx.NodeOutputs[p.typeCtx.CurrentNodeID]; ok {
		fieldNames := make([]string, 0, len(nodeOutputFields))
		for name := range nodeOutputFields {
			fieldNames = append(fieldNames, name)
		}
		sort.Strings(fieldNames)
		return fieldNames, true
	}

	return nil, false
}

func (p *workflowTypeProvider) EnumValue(enumName string) ref.Val {
	return p.base.EnumValue(enumName)
}

func (p *workflowTypeProvider) FindIdent(identName string) (ref.Val, bool) {
	return p.base.FindIdent(identName)
}

func (p *workflowTypeProvider) NewValue(structType string, fields map[string]ref.Val) ref.Val {
	return p.base.NewValue(structType, fields)
}

// =============================================================================
// TYPE CONVERSION HELPERS
// =============================================================================

// fieldInfoToCELType converts a FieldInfo to a CEL type.
func fieldInfoToCELType(info *FieldInfo) *types.Type {
	if info == nil {
		return types.DynType
	}
	if info.IsDynamic {
		return types.DynType
	}
	return kindToCELType(info.Kind)
}

// kindToCELType converts a Go reflect.Kind to the corresponding CEL type.
func kindToCELType(kind reflect.Kind) *types.Type {
	switch kind {
	case reflect.String:
		return types.StringType
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return types.IntType
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return types.UintType
	case reflect.Float32, reflect.Float64:
		return types.DoubleType
	case reflect.Bool:
		return types.BoolType
	case reflect.Slice, reflect.Array:
		return types.NewListType(types.DynType)
	case reflect.Map:
		return types.NewMapType(types.StringType, types.DynType)
	case reflect.Struct:
		return types.DynType
	case reflect.Interface:
		return types.DynType
	default:
		return types.DynType
	}
}

// =============================================================================
// NESTED INPUT TYPE HELPERS
// =============================================================================

func (p *workflowTypeProvider) isSyntheticInputType(typeName string) bool {
	if p.typeCtx == nil {
		return false
	}

	path := strings.TrimPrefix(typeName, "inputs.")
	if path == "" || path == typeName {
		return false
	}

	parts := strings.Split(path, ".")
	if len(parts) == 0 {
		return false
	}

	firstPart := parts[0]

	if groupFields, ok := p.typeCtx.InputGroups[firstPart]; ok {
		if len(parts) == 1 {
			return true
		}
		return p.hasNestedProperties(groupFields, parts[1:])
	}

	if fieldInfo, ok := p.typeCtx.InputFields[firstPart]; ok {
		if len(fieldInfo.Properties) > 0 {
			if len(parts) == 1 {
				return true
			}
			return p.hasNestedProperties(fieldInfo.Properties, parts[1:])
		}
	}

	return false
}

func (p *workflowTypeProvider) hasNestedProperties(properties map[string]*FieldInfo, pathParts []string) bool {
	if len(pathParts) == 0 || properties == nil {
		return false
	}

	fieldInfo, ok := properties[pathParts[0]]
	if !ok {
		return false
	}

	if len(pathParts) == 1 {
		return len(fieldInfo.Properties) > 0
	}

	if fieldInfo.Properties == nil {
		return false
	}
	return p.hasNestedProperties(fieldInfo.Properties, pathParts[1:])
}

func (p *workflowTypeProvider) getNestedInputFieldNames(structType string) []string {
	if p.typeCtx == nil {
		return nil
	}

	path := strings.TrimPrefix(structType, "inputs.")
	if path == "" || path == structType {
		return nil
	}

	parts := strings.Split(path, ".")
	if len(parts) == 0 {
		return nil
	}

	firstPart := parts[0]

	if groupFields, ok := p.typeCtx.InputGroups[firstPart]; ok {
		if len(parts) == 1 {
			names := make([]string, 0, len(groupFields))
			for name := range groupFields {
				names = append(names, name)
			}
			sort.Strings(names)
			return names
		}
		return p.getNestedPropertyNames(groupFields, parts[1:])
	}

	if fieldInfo, ok := p.typeCtx.InputFields[firstPart]; ok {
		if fieldInfo.Properties != nil {
			if len(parts) == 1 {
				names := make([]string, 0, len(fieldInfo.Properties))
				for name := range fieldInfo.Properties {
					names = append(names, name)
				}
				sort.Strings(names)
				return names
			}
			return p.getNestedPropertyNames(fieldInfo.Properties, parts[1:])
		}
	}

	return nil
}

func (p *workflowTypeProvider) getNestedPropertyNames(properties map[string]*FieldInfo, pathParts []string) []string {
	if len(pathParts) == 0 || properties == nil {
		return nil
	}

	fieldInfo, ok := properties[pathParts[0]]
	if !ok || fieldInfo.Properties == nil {
		return nil
	}

	if len(pathParts) == 1 {
		names := make([]string, 0, len(fieldInfo.Properties))
		for name := range fieldInfo.Properties {
			names = append(names, name)
		}
		sort.Strings(names)
		return names
	}

	return p.getNestedPropertyNames(fieldInfo.Properties, pathParts[1:])
}

func (p *workflowTypeProvider) findNestedInputFieldType(structType, fieldName string) (*types.FieldType, bool) {
	if p.typeCtx == nil {
		return nil, false
	}

	path := strings.TrimPrefix(structType, "inputs.")
	if path == "" || path == structType {
		return nil, false
	}

	parts := strings.Split(path, ".")
	if len(parts) == 0 {
		return nil, false
	}

	firstPart := parts[0]

	if groupFields, ok := p.typeCtx.InputGroups[firstPart]; ok {
		if len(parts) == 1 {
			return p.fieldInfoToFieldType(groupFields, fieldName, structType)
		}
		return p.findNestedPropertyType(groupFields, parts[1:], fieldName, structType)
	}

	if fieldInfo, ok := p.typeCtx.InputFields[firstPart]; ok {
		if fieldInfo.Properties != nil {
			if len(parts) == 1 {
				return p.fieldInfoToFieldType(fieldInfo.Properties, fieldName, structType)
			}
			return p.findNestedPropertyType(fieldInfo.Properties, parts[1:], fieldName, structType)
		}
	}

	return nil, false
}

func (p *workflowTypeProvider) findNestedPropertyType(properties map[string]*FieldInfo, pathParts []string, fieldName, parentType string) (*types.FieldType, bool) {
	if len(pathParts) == 0 || properties == nil {
		return nil, false
	}

	fieldInfo, ok := properties[pathParts[0]]
	if !ok || fieldInfo.Properties == nil {
		return nil, false
	}

	if len(pathParts) == 1 {
		return p.fieldInfoToFieldType(fieldInfo.Properties, fieldName, parentType)
	}

	return p.findNestedPropertyType(fieldInfo.Properties, pathParts[1:], fieldName, parentType)
}

func (p *workflowTypeProvider) fieldInfoToFieldType(properties map[string]*FieldInfo, fieldName, parentType string) (*types.FieldType, bool) {
	fieldInfo, ok := properties[fieldName]
	if !ok {
		return nil, false
	}

	if len(fieldInfo.Properties) > 0 {
		return &types.FieldType{Type: types.NewObjectType(parentType + "." + fieldName)}, true
	}

	celType := fieldInfoToCELType(fieldInfo)
	return &types.FieldType{Type: celType}, true
}

func (p *workflowTypeProvider) isAdditionalPropertiesAllowed(structType string) bool {
	if p.typeCtx == nil {
		return false
	}

	path := strings.TrimPrefix(structType, "inputs.")
	if path == "" || path == structType {
		return false
	}

	parts := strings.Split(path, ".")
	if len(parts) == 0 {
		return false
	}

	firstPart := parts[0]

	if _, ok := p.typeCtx.InputGroups[firstPart]; ok {
		return false
	}

	fieldInfo, ok := p.typeCtx.InputFields[firstPart]
	if !ok {
		return false
	}

	if len(parts) == 1 {
		return fieldInfo.AdditionalPropertiesAllowed
	}

	return p.checkNestedAdditionalProperties(fieldInfo.Properties, parts[1:])
}

func (p *workflowTypeProvider) checkNestedAdditionalProperties(properties map[string]*FieldInfo, pathParts []string) bool {
	if len(pathParts) == 0 || properties == nil {
		return false
	}

	fieldInfo, ok := properties[pathParts[0]]
	if !ok {
		return false
	}

	if len(pathParts) == 1 {
		return fieldInfo.AdditionalPropertiesAllowed
	}

	if fieldInfo.Properties == nil {
		return false
	}
	return p.checkNestedAdditionalProperties(fieldInfo.Properties, pathParts[1:])
}

// nodeCanHaveExtendedOutputs returns true if the node has runtime-extended
// outputs beyond the proto schema (e.g., structured output from response tools).
// For these nodes, unknown field access returns dyn instead of a compile error.
func (p *workflowTypeProvider) nodeCanHaveExtendedOutputs(nodeID string) bool {
	if p.typeCtx == nil {
		return false
	}
	return p.typeCtx.NodesWithExtendedOutputs[nodeID]
}

// Ensure workflowTypeProvider implements types.Provider at compile time.
var _ types.Provider = (*workflowTypeProvider)(nil)

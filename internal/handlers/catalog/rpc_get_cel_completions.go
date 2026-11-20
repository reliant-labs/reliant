package catalog

import (
	"context"
	"sort"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/reference"
)

// GetCELCompletions returns CEL expression completion data for the workflow builder.
// The response is computed once and cached since the data is static.
func (s *Service) GetCELCompletions(
	ctx context.Context,
	req *reliantv1.GetCELCompletionsRequest,
) (*reliantv1.GetCELCompletionsResponse, error) {
	s.celCompletionsOnce.Do(func() {
		s.celCompletionsResp = s.buildCELCompletionsResponse()
	})
	return s.celCompletionsResp, nil
}

// buildCELCompletionsResponse assembles CEL completion data from reference and type registry.
func (s *Service) buildCELCompletionsResponse() *reliantv1.GetCELCompletionsResponse {
	resp := &reliantv1.GetCELCompletionsResponse{}

	// 1. Map reference.CELNamespaces → CELNamespaceInfo protos
	for _, ns := range reference.CELNamespaces {
		fields := make([]*reliantv1.CELFieldInfo, 0, len(ns.Fields))
		for _, f := range ns.Fields {
			fields = append(fields, &reliantv1.CELFieldInfo{
				Name:        f.Name,
				Type:        f.Type,
				Description: f.Description,
			})
		}
		resp.Namespaces = append(resp.Namespaces, &reliantv1.CELNamespaceInfo{
			Name:        ns.Name,
			Description: ns.Description,
			IsDynamic:   ns.IsDynamic,
			Fields:      fields,
		})
	}

	// 2. Map reference.CELFunctions → CELFunctionInfo protos
	for _, fn := range reference.CELFunctions {
		// Determine if this is a member function from the signature pattern.
		// Member functions have signatures like "string.func(...)" or "list.func(...)"
		isMember := strings.Contains(fn.Signature, ".")
		resp.Functions = append(resp.Functions, &reliantv1.CELFunctionInfo{
			Name:        fn.Name,
			Signature:   fn.Signature,
			Description: fn.Description,
			Example:     fn.Example,
			IsMember:    isMember,
		})
	}

	// 3. Map reference.CELHelperTypes → CELHelperTypeInfo protos
	for _, ht := range reference.CELHelperTypes {
		fields := make([]*reliantv1.CELFieldInfo, 0, len(ht.Fields))
		for _, f := range ht.Fields {
			fields = append(fields, &reliantv1.CELFieldInfo{
				Name:        f.Name,
				Type:        f.Type,
				Description: f.Description,
			})
		}
		resp.HelperTypes = append(resp.HelperTypes, &reliantv1.CELHelperTypeInfo{
			Name:        ht.Name,
			Description: ht.Description,
			AccessPath:  ht.AccessPath,
			Fields:      fields,
		})
	}

	// 4. Build node output schemas from the type registry
	registry := wfcel.NewTypeRegistry()
	nodeTypes := registry.NodeTypes()
	sort.Strings(nodeTypes)

	for _, nodeType := range nodeTypes {
		outputFields := registry.OutputFieldsForNodeType(nodeType)
		if len(outputFields) == 0 {
			continue
		}

		fields := make([]*reliantv1.CELFieldInfo, 0, len(outputFields))
		for _, f := range outputFields {
			fields = append(fields, &reliantv1.CELFieldInfo{
				Name:        f.Name,
				Type:        f.Type,
				Description: f.Description,
			})
		}

		// Get display name from reference.GetNodeType if available
		displayName := nodeType
		if info, ok := reference.GetNodeType(nodeType); ok && info.Name != "" {
			displayName = info.Name
		}

		resp.NodeOutputSchemas = append(resp.NodeOutputSchemas, &reliantv1.CELNodeOutputSchema{
			NodeType:    nodeType,
			DisplayName: displayName,
			Fields:      fields,
		})
	}

	return resp
}

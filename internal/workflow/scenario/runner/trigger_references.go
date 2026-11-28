// Copyright (c) 2025 Reliant Labs
package runner

import (
	"strings"
	"sync"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/operators"
	"cel.dev/cel-go/common/types"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/structpb"
)

var triggerReferenceEnv = sync.OnceValues(func() (*cel.Env, error) {
	return cel.NewEnv(cel.OptionalTypes())
})

// triggerPayloadReferences reports statically named paths, not which branches
// executed. Unknown computed keys and invalid expressions are not guesses.
func triggerPayloadReferences(wf *reliantv1.Workflow, fired string) map[string]bool {
	fields := make(map[string]bool)
	env, err := triggerReferenceEnv()
	if err != nil {
		return fields
	}
	read := func(text string, raw bool) {
		exprs, err := wfcel.TemplateExpressions(text, raw)
		if err != nil {
			return
		}
		for _, expr := range exprs {
			parsed, issues := env.Parse(expr)
			if issues.Err() != nil {
				continue
			}
			root := ast.NavigateAST(parsed.NativeRep())
			for _, ident := range ast.MatchDescendants(root, func(e ast.NavigableExpr) bool {
				return e.Kind() == ast.IdentKind && e.AsIdent() == string(wfcel.CELTrigger)
			}) {
				if triggerShadowed(ident) {
					continue
				}
				var path []string
				for at := ident; ; {
					parent, ok := at.Parent()
					if !ok {
						break
					}
					key, ok := selectedKey(parent, at)
					if !ok {
						break
					}
					path = append(path, key)
					at = parent
				}
				if len(path) > 1 && path[0] == "payload" {
					// Traversing a sample-filled object also reads that object.
					for i := 2; i <= len(path); i++ {
						fields[strings.Join(path[1:i], ".")] = true
					}
				}
			}
		}
	}
	walkWarningCEL(wf.ProtoReflect(), read)
	for _, trigger := range wf.GetTriggers() {
		if trigger.GetName() != fired {
			continue
		}
		read(trigger.GetFilter(), true)
		read(trigger.GetPrompt(), false)
		read(trigger.GetDedupeKey(), false)
		for _, value := range trigger.GetInputs() {
			read(value, false)
		}
		break
	}
	return fields
}

func selectedKey(parent, child ast.NavigableExpr) (string, bool) {
	switch parent.Kind() {
	case ast.SelectKind:
		selectExpr := parent.AsSelect()
		if selectExpr.Operand().ID() == child.ID() {
			return selectExpr.FieldName(), true
		}
	case ast.CallKind:
		call := parent.AsCall()
		switch call.FunctionName() {
		case operators.Index, operators.OptIndex, operators.OptSelect:
			args := call.Args()
			if len(args) == 2 && args[0].ID() == child.ID() && args[1].Kind() == ast.LiteralKind {
				key, ok := args[1].AsLiteral().(types.String)
				return string(key), ok && !strings.Contains(string(key), ".")
			}
		}
	}
	return "", false
}

func triggerShadowed(ident ast.NavigableExpr) bool {
	for child := ident; ; {
		parent, ok := child.Parent()
		if !ok {
			return false
		}
		if parent.Kind() == ast.ComprehensionKind {
			c := parent.AsComprehension()
			inLoop := child.ID() == c.LoopCondition().ID() || child.ID() == c.LoopStep().ID()
			name := ident.AsIdent()
			if (inLoop && (name == c.IterVar() || name == c.IterVar2() || name == c.AccuVar())) ||
				(child.ID() == c.Result().ID() && name == c.AccuVar()) {
				return true
			}
		}
		child = parent
	}
}

// Only typed executable fields are candidates. In particular, neither proto
// descriptions nor arbitrary google.protobuf.Value configuration imply CEL.
func walkWarningCEL(m protoreflect.Message, read func(string, bool)) {
	name := m.Descriptor().Name()
	switch v := m.Interface().(type) {
	case *reliantv1.DirectCelBool:
		read(v.GetExpr(), true)
		return
	case *reliantv1.EdgeCase:
		read(v.GetCondition(), true)
		return
	case *reliantv1.CelString:
		// The resolver also evaluates template-bearing literal CelStrings at
		// sites like message saving and thread injection.
		read(v.GetExpr(), false)
		read(v.GetLiteral(), false)
		return
	case *reliantv1.CelBool, *reliantv1.CelDouble, *reliantv1.CelInt, *reliantv1.CelStringList,
		*reliantv1.CelModelSelector, *reliantv1.CelDaemonSelector, *reliantv1.BoundParam:
		read(m.Get(m.Descriptor().Fields().ByName("expr")).String(), false)
		return
	case *reliantv1.WorkflowTrigger, *structpb.Struct, *structpb.ListValue, *structpb.Value:
		return // triggers and expression-valued maps have their own rules
	}
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch {
		case fd.IsMap():
			v.Map().Range(func(_ protoreflect.MapKey, value protoreflect.Value) bool {
				if warningExpressionMap(name, fd.Name()) {
					if fd.MapValue().Kind() == protoreflect.StringKind {
						read(value.String(), false)
					} else {
						readWarningValue(value.Message().Interface().(*structpb.Value), read)
					}
				} else if fd.MapValue().Kind() == protoreflect.MessageKind {
					walkWarningCEL(value.Message(), read)
				}
				return true
			})
		case fd.IsList() && fd.Kind() == protoreflect.MessageKind:
			for i := 0; i < v.List().Len(); i++ {
				walkWarningCEL(v.List().Get(i).Message(), read)
			}
		case !fd.IsList() && fd.Kind() == protoreflect.MessageKind:
			walkWarningCEL(v.Message(), read)
		case name == "LoopArgs" && fd.Name() == "key":
			read(v.String(), true)
		case name == "WorkflowConcurrency" && fd.Name() == "key":
			read(v.String(), false)
		}
		return true
	})
}

func warningExpressionMap(message, field protoreflect.Name) bool {
	switch message {
	case "Workflow", "RouterArgs", "ComputeArgs":
		return field == "outputs"
	case "LoopArgs", "SubWorkflowArgs":
		return field == "args"
	case "ActionArgs":
		return field == "with"
	case "InvokeToolArgs":
		return field == "params"
	default:
		return false
	}
}

func readWarningValue(v *structpb.Value, read func(string, bool)) {
	switch value := v.GetKind().(type) {
	case *structpb.Value_StringValue:
		read(value.StringValue, false)
	case *structpb.Value_ListValue:
		for _, item := range value.ListValue.GetValues() {
			readWarningValue(item, read)
		}
	case *structpb.Value_StructValue:
		for _, item := range value.StructValue.GetFields() {
			readWarningValue(item, read)
		}
	}
}

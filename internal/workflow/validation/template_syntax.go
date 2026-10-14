// Copyright (c) 2025 Reliant Labs
package validation

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/google/cel-go/cel"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
)

// Template syntax is checked once, for every template-bearing field, before any
// semantic check. A {{ that never closes, or an expression that does not parse,
// is an error naming the field and the position inside it. Neither is ever
// allowed to fall back to literal text: the runtime would then send the source
// of the template to a model or a shell instead of its value.
//
// This pass is the only reporter of CEL syntax errors. The semantic compile
// pass drops them (isCELSyntaxIssue) so a typo is reported once, with a
// position, instead of twice.

// celWrapperTypes are the proto wrappers whose `expr` arm holds a {{ }} template.
var celWrapperTypes = map[protoreflect.FullName]bool{
	"reliant.v1.CelString":         true,
	"reliant.v1.CelBool":           true,
	"reliant.v1.CelDouble":         true,
	"reliant.v1.CelInt":            true,
	"reliant.v1.CelStringList":     true,
	"reliant.v1.CelModelSelector":  true,
	"reliant.v1.CelDaemonSelector": true,
}

// plainStringTemplates are template fields the proto declares as a plain
// string rather than a Cel wrapper, so a walk over the wrappers cannot see them.
var plainStringTemplates = map[protoreflect.FullName]map[string]bool{
	"reliant.v1.LoopArgs":            {"key": true},
	"reliant.v1.CallLLMMessageInput": {"role": true, "content": true},
}

const (
	directCelBoolType protoreflect.FullName = "reliant.v1.DirectCelBool"
	valueType         protoreflect.FullName = "google.protobuf.Value"
	nodeType          protoreflect.FullName = "reliant.v1.Node"
	workflowType      protoreflect.FullName = "reliant.v1.Workflow"
	edgeCaseType      protoreflect.FullName = "reliant.v1.EdgeCase"
)

var (
	parseEnvOnce sync.Once
	parseEnv     *cel.Env
	parseEnvErr  error
)

// celParseEnv parses with the workflow environment so every macro and the
// optional-field syntax are known; only syntax is checked here.
func celParseEnv() (*cel.Env, error) {
	parseEnvOnce.Do(func() {
		parseEnv, parseEnvErr = wfcel.NewEnv(wfcel.EdgeConditionCELEnvConfig())
	})
	return parseEnv, parseEnvErr
}

// isCELSyntaxIssue reports whether a CEL issue is a parse error, which
// validateTemplateSyntax has already reported with a position.
func isCELSyntaxIssue(message string) bool {
	return strings.Contains(message, "Syntax error")
}

type templateSyntaxWalker struct {
	result *Result
	env    *cel.Env
}

// validateTemplateSyntax checks the template syntax of a whole workflow,
// inline bodies included.
func validateTemplateSyntax(wf *reliantv1.Workflow, result *Result) {
	env, err := celParseEnv()
	if err != nil {
		result.AddError(CategoryCELSyntax, nil, "", fmt.Sprintf("cannot build the CEL parser: %v", err))
		return
	}
	w := &templateSyntaxWalker{result: result, env: env}
	w.workflow(wf.ProtoReflect(), []string{wf.GetName()})
}

func (w *templateSyntaxWalker) workflow(m protoreflect.Message, path []string) {
	fields := m.Descriptor().Fields()

	if nodes := m.Get(fields.ByName("nodes")).List(); nodes.Len() > 0 {
		for i := 0; i < nodes.Len(); i++ {
			node := nodes.Get(i).Message()
			id := node.Get(node.Descriptor().Fields().ByName("id")).String()
			w.message(node, appendPath(path, "nodes", fmt.Sprintf("[%d](%s)", i, id)))
		}
	}
	if edges := m.Get(fields.ByName("edges")).List(); edges.Len() > 0 {
		for i := 0; i < edges.Len(); i++ {
			w.message(edges.Get(i).Message(), appendPath(path, "edges", fmt.Sprintf("[%d]", i)))
		}
	}

	// Workflow outputs are templates keyed by output name.
	outputs := m.Get(fields.ByName("outputs")).Map()
	var names []string
	outputs.Range(func(k protoreflect.MapKey, _ protoreflect.Value) bool {
		names = append(names, k.String())
		return true
	})
	sort.Strings(names)
	for _, name := range names {
		w.template(outputs.Get(protoreflect.ValueOfString(name).MapKey()).String(), appendPath(path, "outputs", name))
	}
}

// message walks one proto message, checking every template-bearing field
// under path.
func (w *templateSyntaxWalker) message(m protoreflect.Message, path []string) {
	md := m.Descriptor()
	switch {
	case celWrapperTypes[md.FullName()]:
		if exprFD := md.Fields().ByName("expr"); exprFD != nil && m.Has(exprFD) {
			w.template(m.Get(exprFD).String(), path)
		}
		return
	case md.FullName() == directCelBoolType:
		w.rawCEL(m.Get(md.Fields().ByName("expr")).String(), path)
		return
	case md.FullName() == edgeCaseType:
		if cond := m.Get(md.Fields().ByName("condition")).String(); cond != "" {
			w.rawCEL(cond, appendPath(path, "condition"))
		}
		w.messageFields(m, path, "condition")
		return
	case md.FullName() == valueType:
		w.value(m.Interface().(*structpb.Value), path)
		return
	}
	w.messageFields(m, path, "")
}

func (w *templateSyntaxWalker) messageFields(m protoreflect.Message, path []string, skip string) {
	md := m.Descriptor()
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		name := string(fd.Name())
		if name == skip {
			return true
		}
		fieldPath := path
		// The args oneof is not a name a workflow author writes.
		if !(md.FullName() == nodeType && fd.ContainingOneof() != nil && fd.ContainingOneof().Name() == "args") {
			fieldPath = appendPath(path, name)
		}
		switch {
		case fd.IsMap():
			w.mapField(fd, v.Map(), fieldPath)
		case fd.IsList():
			list := v.List()
			for i := 0; i < list.Len(); i++ {
				if fd.Kind() == protoreflect.MessageKind {
					w.child(list.Get(i).Message(), appendPath(fieldPath, fmt.Sprintf("[%d]", i)))
				}
			}
		case fd.Kind() == protoreflect.MessageKind:
			w.child(v.Message(), fieldPath)
		case fd.Kind() == protoreflect.StringKind && plainStringTemplates[md.FullName()][name]:
			w.template(v.String(), fieldPath)
		}
		return true
	})
}

func (w *templateSyntaxWalker) child(m protoreflect.Message, path []string) {
	if m.Descriptor().FullName() == workflowType {
		w.workflow(m, path)
		return
	}
	w.message(m, path)
}

func (w *templateSyntaxWalker) mapField(fd protoreflect.FieldDescriptor, mp protoreflect.Map, path []string) {
	if fd.MapValue().Kind() != protoreflect.MessageKind {
		return
	}
	var keys []string
	mp.Range(func(k protoreflect.MapKey, _ protoreflect.Value) bool {
		keys = append(keys, k.String())
		return true
	})
	sort.Strings(keys)
	for _, key := range keys {
		w.child(mp.Get(protoreflect.ValueOfString(key).MapKey()).Message(), appendPath(path, key))
	}
}

// value checks the strings inside a google.protobuf.Value (workflow args, a
// response schema), which may carry templates anywhere in the tree.
func (w *templateSyntaxWalker) value(v *structpb.Value, path []string) {
	switch k := v.GetKind().(type) {
	case *structpb.Value_StringValue:
		if wfcel.ContainsTemplate(k.StringValue) {
			w.template(k.StringValue, path)
		}
	case *structpb.Value_StructValue:
		keys := make([]string, 0, len(k.StructValue.GetFields()))
		for key := range k.StructValue.GetFields() {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			w.value(k.StructValue.GetFields()[key], appendPath(path, key))
		}
	case *structpb.Value_ListValue:
		for i, item := range k.ListValue.GetValues() {
			w.value(item, appendPath(path, fmt.Sprintf("[%d]", i)))
		}
	}
}

// template checks one {{ }} template value: that it scans, and that each
// expression parses.
func (w *templateSyntaxWalker) template(value string, path []string) {
	if !wfcel.ContainsTemplate(value) {
		return
	}
	matches, err := wfcel.ScanTemplate(value)
	if err != nil {
		var synErr *wfcel.TemplateSyntaxError
		if errors.As(err, &synErr) {
			w.result.Add(&Error{
				Severity: SeverityError,
				Category: CategoryCELSyntax,
				Path:     path,
				Message:  fmt.Sprintf("%s (line %d, column %d)", synErr.Message, synErr.Line, synErr.Column),
				Suggestion: "every {{ needs a closing }}; braces and quotes inside the expression must balance, " +
					"and a literal {{ is written as an expression: {{ '{{' }}",
			})
		} else {
			w.result.AddError(CategoryCELSyntax, path, "", err.Error())
		}
		return
	}
	for _, m := range matches {
		if m.Expr == "" {
			w.result.Add(&Error{
				Severity:   SeverityError,
				Category:   CategoryCELSyntax,
				Path:       path,
				Message:    fmt.Sprintf("empty expression {{ }} %s", positionIn(value, m.Start)),
				Suggestion: "put an expression between {{ and }}, or remove the braces",
			})
			continue
		}
		w.parse(m.Expr, value, m.ExprStart, path)
	}
}

// rawCEL checks a field that is CEL with no {{ }} (a condition, a while).
// A value wrapped in {{ }} is reported by rejectTemplateDelimitersInRawCEL.
func (w *templateSyntaxWalker) rawCEL(expr string, path []string) {
	if strings.TrimSpace(expr) == "" {
		return
	}
	if _, wrapped := wfcel.RawCELTemplateDelimiter(expr); wrapped {
		return
	}
	w.parse(expr, expr, 0, path)
}

// parse reports a syntax error in expr, which starts at exprStart within value.
func (w *templateSyntaxWalker) parse(expr, value string, exprStart int, path []string) {
	_, issues := w.env.Parse(expr)
	if issues == nil || issues.Err() == nil {
		return
	}
	for _, issue := range issues.Errors() {
		line, col := absolutePosition(value, exprStart, issue.Location.Line(), issue.Location.Column())
		w.result.Add(&Error{
			Severity:   SeverityError,
			Category:   CategoryCELSyntax,
			Path:       path,
			Message:    fmt.Sprintf("CEL syntax error at line %d, column %d: %s", line, col, firstLine(issue.Message)),
			Suggestion: "the expression is: " + oneLine(expr),
		})
	}
}

// absolutePosition maps a position inside an expression (1-based line, 0-based
// column, as CEL reports it) to a 1-based line and column of the whole value.
func absolutePosition(value string, exprStart, exprLine, exprCol int) (line, col int) {
	startLine, startCol := lineColumn(value, exprStart)
	if exprLine <= 1 {
		return startLine, startCol + exprCol
	}
	return startLine + exprLine - 1, exprCol + 1
}

// lineColumn is the 1-based line and column of byte offset in s.
func lineColumn(s string, offset int) (line, col int) {
	line, col = 1, 1
	for i := 0; i < offset && i < len(s); i++ {
		if s[i] == '\n' {
			line++
			col = 1
		} else {
			col++
		}
	}
	return line, col
}

func positionIn(s string, offset int) string {
	line, col := lineColumn(s, offset)
	return fmt.Sprintf("(line %d, column %d)", line, col)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func oneLine(s string) string {
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

func appendPath(path []string, parts ...string) []string {
	return append(append(make([]string, 0, len(path)+len(parts)), path...), parts...)
}

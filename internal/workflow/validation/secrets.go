package validation

import (
	"fmt"
	"sort"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/stepsecrets"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/structpb"
)

// secretsNamespace is the identifier a placeholder reads. It is not a CEL
// namespace: the runtime turns an authored {{ secrets.NAME }} hole into a marker
// before any expression is evaluated, so the identifier appearing anywhere the
// engine would see it is always a mistake.
const secretsNamespace wfcel.CELNamespace = "secrets"

// reservedSecretNames cannot name a secret because a run step exports every
// declared secret as an environment variable of the same name.
var reservedSecretNames = map[string]bool{
	"PATH": true, "HOME": true, "SHELL": true, "USER": true, "LOGNAME": true, "PWD": true, "OLDPWD": true,
	"IFS": true, "ENV": true, "BASH_ENV": true, "TMPDIR": true, "TERM": true, "LANG": true,
	"LD_PRELOAD": true, "LD_LIBRARY_PATH": true, "DYLD_INSERT_LIBRARIES": true, "DYLD_LIBRARY_PATH": true,
	"GH_TOKEN": true, "GITHUB_TOKEN": true, "SSH_AUTH_SOCK": true,
}

// validateSecrets enforces the secret rules for one workflow file.
//
// A secret is usable by any step that does not hand its text to a model:
//
//	action  with.*   (an http/request@1 url, headers and body, or an integration input)
//	run     command (every declared secret is also exported to it as an
//	        environment variable of the same name)
//
// It is an error in everything a model reads or writes (call_llm, router, agent
// and sub-agent prompts and messages, save_message, tool configs, response
// tools), in CEL that decides or computes (compute, conditions, while/key/items,
// edges, workflow outputs), in every other node's arguments, in invoke_tool
// (every tool a workflow can name today hands its parameters to a model or a
// chat), and in the declaration itself. Once a value is in a prompt it has gone
// to the model provider, and masking cannot recall it.
//
// Only a whole {{ secrets.NAME }} hole is a placeholder. A hole that reads
// `secrets` any other way (secrets['A'], secrets.A.upperAscii(), a
// concatenation) is refused: it could not be substituted, so it must not run as
// CEL.
func validateSecrets(wf *reliantv1.Workflow, result *Result) {
	declared := wf.GetSecrets()
	names := make([]string, 0, len(declared))
	for name := range declared {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		path := []string{wf.GetName(), "secrets", name}
		upper := strings.ToUpper(name)
		switch {
		case !stepsecrets.NamePattern.MatchString(name):
			result.AddError(CategoryStructure, path, "", "secret names are letters, digits and underscores, starting with a letter or underscore")
		case reservedSecretNames[upper] || strings.HasPrefix(upper, "RELIANT_"):
			result.AddError(CategoryStructure, path, "", fmt.Sprintf("%q cannot be a secret's name: a run step exports every declared secret as an environment variable of the same name", name))
		}
		switch env := declared[name].GetEnvironment(); {
		case !model.CelStringIsSet(env):
			result.AddError(CategoryStructure, path, "environment", `required: "<forge project>/<env name>" or an environment id`)
		case model.CelStringIsExpr(env):
			validateSecretEnvironmentScope(model.CelStringRaw(env), path, result)
		}
	}

	used := map[string]bool{}
	check := func(text string, path []string) { validateSecretHoles(text, declared, used, path, result) }

	var walkBody func(w *reliantv1.Workflow, base []string, root bool)
	walkBody = func(w *reliantv1.Workflow, base []string, root bool) {
		if !root && len(w.GetSecrets()) > 0 {
			result.AddError(CategoryStructure, base, "secrets", "secrets are declared once, at the top level of the workflow file; an inline body uses them, and a workflow loaded by ref declares its own")
		}
		walkWorkflowNodes(w, base, func(node *reliantv1.Node, nodePath []string) {
			for _, inline := range []*reliantv1.Workflow{node.GetWorkflow().GetInline(), node.GetLoop().GetInline()} {
				if inline != nil && len(inline.GetSecrets()) > 0 {
					result.AddError(CategoryStructure, joinPath(nodePath, "inline"), "secrets", "secrets are declared once, at the top level of the workflow file; an inline body uses them, and a workflow loaded by ref declares its own")
				}
			}
			switch {
			case node.GetAction() != nil:
				with := node.GetAction().GetWith()
				for _, k := range sortedValueKeys(with) {
					eachString(with[k], joinPath(nodePath, "with", k), check)
				}
			case node.GetRun() != nil:
				check(model.CelStringRaw(node.GetRun().GetCommand()), joinPath(nodePath, "command"))
			}
		})
	}
	walkBody(wf, []string{wf.GetName()}, true)
	if h := wf.GetOnFailure(); h != nil {
		walkBody(h, []string{wf.GetName(), "on_failure"}, false)
	}

	// A run step exports every declared secret, so with one present none is
	// unused.
	if !workflowHasRunStep(wf) {
		for _, name := range names {
			if !used[name] {
				result.Add(&Error{
					Severity: SeverityWarning, Category: CategoryStructure,
					Path:    []string{wf.GetName(), "secrets", name},
					Message: fmt.Sprintf("secret %q is declared but no step uses it", name),
				})
			}
		}
	}

	// Everything else: any read of `secrets` outside an allowed field is an error.
	rejectSecretsOutsideAllowed(wf.ProtoReflect(), []string{wf.GetName()}, result)
}

func workflowHasRunStep(wf *reliantv1.Workflow) bool {
	found := false
	walkWorkflowNodes(wf, nil, func(node *reliantv1.Node, _ []string) {
		if node.GetRun() != nil {
			found = true
		}
	})
	return found
}

// validateSecretEnvironmentScope refuses an environment expression that reads
// anything but inputs: it is evaluated when the step starts, in the node's own
// scope, and a value that depends on a prior node's output could be steered by
// data.
func validateSecretEnvironmentScope(raw string, path []string, result *Result) {
	others := []wfcel.CELNamespace{wfcel.CELNodes, wfcel.CELIter, wfcel.CELOutput, wfcel.CELOutputs, wfcel.CELTrigger, wfcel.CELFailure, secretsNamespace}
	if refs, err := wfcel.TemplateReferencesAny(raw, others); err == nil && refs {
		result.AddError(CategoryStructure, path, "environment", "a secret's environment may be an expression over inputs only")
	}
}

// validateSecretHoles checks the placeholders in one allowed field: each hole
// that reads `secrets` is exactly {{ secrets.NAME }} and names a declared secret.
func validateSecretHoles(text string, declared map[string]*reliantv1.SecretDeclaration, used map[string]bool, path []string, result *Result) {
	if !strings.Contains(text, "{{") {
		return
	}
	matches, err := wfcel.ScanTemplate(text)
	if err != nil {
		return // reported by the template-syntax layer
	}
	for _, m := range matches {
		if name, ok := stepsecrets.PlainName(m.Expr); ok {
			if _, isDeclared := declared[name]; !isDeclared {
				result.Add(&Error{
					Severity: SeverityError, Category: CategoryStructure, Path: path,
					Message:    fmt.Sprintf("secrets.%s is not declared", name),
					Suggestion: fmt.Sprintf("declare it at the top of the workflow:\nsecrets:\n  %s:\n    environment: <forge project>/<env name>", name),
				})
				continue
			}
			used[name] = true
			continue
		}
		if refs, err := wfcel.ReferencesOf([]string{m.Expr}, secretsNamespace); err == nil && refs.Referenced {
			result.Add(&Error{
				Severity: SeverityError, Category: CategoryStructure, Path: path,
				Message:    "a secret is used as a whole {{ secrets.NAME }} hole; it cannot be indexed, passed to a function or combined in an expression",
				Suggestion: "write the surrounding text outside the hole: \"Bearer {{ secrets.NAME }}\"",
			})
		}
	}
}

func eachString(v *structpb.Value, path []string, fn func(string, []string)) {
	switch k := v.GetKind().(type) {
	case *structpb.Value_StringValue:
		fn(k.StringValue, path)
	case *structpb.Value_StructValue:
		keys := make([]string, 0, len(k.StructValue.GetFields()))
		for key := range k.StructValue.GetFields() {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			eachString(k.StructValue.GetFields()[key], joinPath(path, key), fn)
		}
	case *structpb.Value_ListValue:
		for i, item := range k.ListValue.GetValues() {
			eachString(item, joinPath(path, fmt.Sprintf("[%d]", i)), fn)
		}
	}
}

// secretsAllowedHere reports whether a field is one a step may read a secret in
// (an action's `with`, a run's `command`), or one to skip
// (Node.secret_refs is the parser's own record of the declarations).
func secretsAllowedHere(md protoreflect.MessageDescriptor, field protoreflect.Name) (allowed, skip bool) {
	switch md.FullName() {
	case "reliant.v1.ActionArgs":
		return field == "with", false
	case "reliant.v1.RunArgs":
		return field == "command", false
	case "reliant.v1.Node":
		return false, field == "secret_refs"
	}
	return false, false
}

// rejectSecretsOutsideAllowed walks every field of the workflow and reports a
// read of `secrets` in any that is not an allowed one.
func rejectSecretsOutsideAllowed(m protoreflect.Message, path []string, result *Result) {
	md := m.Descriptor()
	rawCEL := md.FullName() == directCelBoolType
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if allowed, skip := secretsAllowedHere(md, fd.Name()); allowed || skip {
			return true
		}
		fieldPath := appendPath(path, string(fd.Name()))
		switch {
		case fd.IsMap():
			keys := make([]string, 0, v.Map().Len())
			v.Map().Range(func(k protoreflect.MapKey, _ protoreflect.Value) bool {
				keys = append(keys, k.String())
				return true
			})
			sort.Strings(keys)
			for _, key := range keys {
				mv := v.Map().Get(protoreflect.ValueOfString(key).MapKey())
				switch fd.MapValue().Kind() {
				case protoreflect.MessageKind:
					walkSecretsMessage(mv.Message(), appendPath(fieldPath, key), result)
				case protoreflect.StringKind:
					checkSecretsString(mv.String(), false, appendPath(fieldPath, key), result)
				}
			}
		case fd.IsList():
			list := v.List()
			for i := 0; i < list.Len(); i++ {
				p := appendPath(fieldPath, fmt.Sprintf("[%d]", i))
				switch fd.Kind() {
				case protoreflect.MessageKind:
					walkSecretsMessage(list.Get(i).Message(), p, result)
				case protoreflect.StringKind:
					checkSecretsString(list.Get(i).String(), false, p, result)
				}
			}
		case fd.Kind() == protoreflect.MessageKind:
			walkSecretsMessage(v.Message(), fieldPath, result)
		case fd.Kind() == protoreflect.StringKind:
			raw := rawCEL || string(fd.Name()) == "condition" || string(fd.Name()) == "while"
			checkSecretsString(v.String(), raw, fieldPath, result)
		}
		return true
	})
}

func walkSecretsMessage(m protoreflect.Message, path []string, result *Result) {
	if m.Descriptor().FullName() == valueType {
		eachString(m.Interface().(*structpb.Value), path, func(s string, p []string) { checkSecretsString(s, false, p, result) })
		return
	}
	rejectSecretsOutsideAllowed(m, path, result)
}

// checkSecretsString reports a read of `secrets` in s, a field that may not read
// one. A template is read for its {{ }} holes; a raw CEL field is read whole.
func checkSecretsString(s string, rawCEL bool, path []string, result *Result) {
	var hit bool
	var err error
	switch {
	case strings.Contains(s, "{{"):
		hit, err = wfcel.TemplateReferencesAny(s, []wfcel.CELNamespace{secretsNamespace})
	case rawCEL && strings.TrimSpace(s) != "":
		var refs wfcel.NamespaceRefs
		refs, err = wfcel.ReferencesOf([]string{s}, secretsNamespace)
		hit = refs.Referenced
	}
	// A string that does not parse is reported by the template-syntax layer.
	if err != nil || !hit {
		return
	}
	result.Add(&Error{
		Severity: SeverityError, Category: CategoryStructure,
		Path:       path,
		Message:    "secrets.NAME may not be used here: this field is read or written by a model, or decides what the workflow does, and a secret that reaches a prompt has gone to the model provider",
		Suggestion: "use a secret only in an action's inputs (url, headers, body) or a run step's command (or read the exported $NAME in it); the step's output is masked",
	})
}

package validation

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"golang.org/x/net/http/httpguts"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
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

// validateSecrets enforces the secret rules for one workflow file.
//
// A secret travels only in a credential slot, never in text a step can print or
// return. There are two slots:
//
//	action http/request@1  with.headers.<Name>: "{{ secrets.NAME }}", or a
//	                       literal prefix and then the hole ("Bearer {{ secrets.NAME }}")
//	action (any)           credential: {secret: NAME}, applied through the
//	                       integration's api_key auth method
//
// A hole anywhere else is an error: an http url, query or body, any other
// action's inputs, a run command (a command can print its own environment), and
// everything a model reads or writes, decides on or computes with. The
// activity enforces the same rule again at run time.
//
// Only a whole {{ secrets.NAME }} hole is a placeholder. A hole that reads
// `secrets` any other way (secrets['A'], secrets.A.upperAscii(), a
// concatenation) is refused.
func validateSecrets(wf *reliantv1.Workflow, result *Result) {
	declared := wf.GetSecrets()
	names := make([]string, 0, len(declared))
	for name := range declared {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		path := []string{wf.GetName(), "secrets", name}
		if !stepsecrets.NamePattern.MatchString(name) {
			result.AddError(CategoryStructure, path, "", "secret names are letters, digits and underscores, starting with a letter or underscore")
		}
		switch env := declared[name].GetEnvironment(); {
		case !model.CelStringIsSet(env):
			result.AddError(CategoryStructure, path, "environment", `required: "<forge project>/<env name>" or an environment id`)
		case model.CelStringIsExpr(env):
			validateSecretEnvironmentScope(model.CelStringRaw(env), path, result)
		}
	}

	used := map[string]bool{}
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
			if args := node.GetAction(); args != nil {
				validateActionSecrets(args, declared, used, nodePath, result)
			}
		})
	}
	walkBody(wf, []string{wf.GetName()}, true)
	if h := wf.GetOnFailure(); h != nil {
		walkBody(h, []string{wf.GetName(), "on_failure"}, false)
	}

	for _, name := range names {
		if !used[name] {
			result.Add(&Error{
				Severity: SeverityWarning, Category: CategoryStructure,
				Path:    []string{wf.GetName(), "secrets", name},
				Message: fmt.Sprintf("secret %q is declared but no step uses it", name),
			})
		}
	}

	// Everything else: any read of `secrets` outside an action is an error.
	rejectSecretsOutsideAllowed(wf.ProtoReflect(), []string{wf.GetName()}, result)
}

// httpRequestUses is the one action whose headers are credential slots: the
// generic HTTP request, whose author picks the host and the header its API
// reads a key from.
const httpRequestUses = "http/request@1"

// validateActionSecrets checks an action's two slots, and refuses a hole in
// every other input it has.
func validateActionSecrets(args *reliantv1.ActionArgs, declared map[string]*reliantv1.SecretDeclaration, used map[string]bool, nodePath []string, result *Result) {
	uses := strings.TrimSpace(model.CelStringRaw(args.GetUses()))
	isHTTP := !model.CelStringIsExpr(args.GetUses()) && uses == httpRequestUses
	slots := 0
	with := args.GetWith()
	for _, k := range sortedValueKeys(with) {
		if k == "headers" && isHTTP && with[k].GetStructValue() != nil {
			fields := with[k].GetStructValue().GetFields()
			for _, h := range sortedValueKeys(fields) {
				path := joinPath(nodePath, "with", "headers", h)
				text := fields[h].GetStringValue()
				if _, isString := fields[h].GetKind().(*structpb.Value_StringValue); !isString || !strings.Contains(text, "{{") {
					eachString(fields[h], path, func(s string, p []string) { checkSecretsString(s, false, p, result) })
					continue
				}
				if name, ok := validateHeaderSlot(h, text, path, result); ok {
					slots++
					markDeclared(name, declared, used, path, result)
				}
			}
			continue
		}
		eachString(with[k], joinPath(nodePath, "with", k), func(s string, p []string) { checkSecretsString(s, false, p, result) })
	}

	cred := args.GetCredential()
	if cred == nil {
		return
	}
	path := joinPath(nodePath, "credential")
	name := cred.GetSecret()
	if !stepsecrets.NamePattern.MatchString(name) {
		result.AddError(CategoryStructure, path, "secret", "credential.secret is the name of a declared secret, not a value or an expression")
		return
	}
	markDeclared(name, declared, used, path, result)
	switch {
	case model.CelStringIsSet(args.GetConnection()):
		result.AddError(CategoryStructure, path, "", "credential and connection are mutually exclusive: authenticate with one")
	case args.GetHostedUse() != nil:
		result.AddError(CategoryStructure, path, "", "credential and hosted_use are mutually exclusive")
	case slots > 0:
		result.AddError(CategoryStructure, path, "", "a secret header and a credential are mutually exclusive: authenticate with one")
	case model.CelStringIsExpr(args.GetUses()):
		result.AddError(CategoryStructure, path, "", "a credential needs a fixed uses: the integration decides where the key is sent")
	default:
		resolved, err := catalog.MustBuiltin().Resolve(uses)
		if err != nil {
			return // reported by validateActionNode
		}
		if _, _, err := httpaction.APIKeyPlacement(resolved.Manifest); err != nil {
			result.AddError(CategoryStructure, path, "", err.Error())
		}
	}
}

// validateHeaderSlot checks that a header value is one credential slot: a
// literal prefix and one {{ secrets.NAME }} hole that ends the value.
func validateHeaderSlot(header, text string, path []string, result *Result) (string, bool) {
	matches, err := wfcel.ScanTemplate(text)
	if err != nil {
		return "", false // reported by the template-syntax layer
	}
	reads := false
	for _, m := range matches {
		if _, ok := stepsecrets.PlainName(m.Expr); ok {
			reads = true
			continue
		}
		if refs, err := wfcel.ReferencesOf([]string{m.Expr}, secretsNamespace); err == nil && refs.Referenced {
			reads = true
		}
	}
	if !reads {
		return "", false // an ordinary templated header
	}
	name, plain := "", false
	if len(matches) == 1 {
		name, plain = stepsecrets.PlainName(matches[0].Expr)
	}
	switch {
	case !plain || matches[0].End != len(text) || strings.Contains(text[:matches[0].Start], "{{"):
		result.Add(&Error{
			Severity: SeverityError, Category: CategoryStructure, Path: path,
			Message:    "a secret header's value is one {{ secrets.NAME }} hole at its end, after only literal text",
			Suggestion: `write it as "Bearer {{ secrets.NAME }}" or "{{ secrets.NAME }}"`,
		})
		return "", false
	case !secretHeaderAllowed(header):
		result.AddError(CategoryStructure, path, "", fmt.Sprintf("header %s cannot carry a secret", header))
		return "", false
	}
	return name, true
}

func markDeclared(name string, declared map[string]*reliantv1.SecretDeclaration, used map[string]bool, path []string, result *Result) {
	if _, ok := declared[name]; !ok {
		result.Add(&Error{
			Severity: SeverityError, Category: CategoryStructure, Path: path,
			Message:    fmt.Sprintf("secrets.%s is not declared", name),
			Suggestion: fmt.Sprintf("declare it at the top of the workflow:\nsecrets:\n  %s:\n    environment: <forge project>/<env name>", name),
		})
		return
	}
	used[name] = true
}

// secretHeaderAllowed mirrors the activity's rule: the transport's own headers
// and the body's description are never a credential slot.
func secretHeaderAllowed(name string) bool {
	canonical := http.CanonicalHeaderKey(name)
	switch canonical {
	case "Host", "Content-Length", "Content-Type", "Content-Encoding", "Transfer-Encoding",
		"Connection", "Upgrade", "Te", "Trailer", "Keep-Alive":
		return false
	}
	return httpguts.ValidHeaderFieldName(name) && !strings.HasPrefix(canonical, "Proxy-")
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

// secretsAllowedHere reports whether a field is checked by
// validateActionSecrets instead (an action's `with` and `credential`), or one to
// skip (Node.secret_refs is the parser's own record of the declarations).
func secretsAllowedHere(md protoreflect.MessageDescriptor, field protoreflect.Name) (allowed, skip bool) {
	switch md.FullName() {
	case "reliant.v1.ActionArgs":
		return field == "with" || field == "credential", false
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
		Message:    "secrets.NAME may not be used here: a secret travels only in a credential slot, never in text a step or a model can read, print or return",
		Suggestion: "use it as an http/request@1 header value (\"Authorization\": \"Bearer {{ secrets.NAME }}\") or as an action's credential: {secret: NAME}",
	})
}

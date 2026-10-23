// Package cli — `forge api` command surface.
//
// `forge api curl <service.method>` prints a copy-pasteable curl invocation
// for a Connect RPC endpoint. Connect handlers already speak plain HTTP/1.1
// POST with application/json — no gRPC tooling needed — but the URL shape
// and Content-Type rules are undocumented in most projects. This command
// removes the discovery friction: look up the method's input message in
// forge_descriptor.json and emit a curl command with a request-body skeleton
// populated from zero values for each field, addressed to the conventional
// local listener (--port overrides).
//
// Streaming RPCs are flagged but still printed — the body shape is the same;
// only the Content-Type changes to application/connect+json. We surface the
// difference rather than reject the command so users can hit streaming
// endpoints too (curl will get the first frame; this is enough to verify
// reachability and auth).
package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/codegen"
)

// newAPICmd is the parent for `forge api ...` verbs. Today the only verb is
// `curl`; future verbs (e.g. `forge api list`, `forge api schema`) hang off
// the same parent so the namespace stays cohesive.
func newAPICmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "api",
		Short: "Inspect and exercise Connect RPC endpoints over plain HTTP+JSON",
		Long: `Connect handlers accept plain HTTP/1.1 POST requests with
Content-Type: application/json — no gRPC tooling required.

Sub-commands surface that capability for ad-hoc debugging from the shell.`,
	}
	cmd.AddCommand(newAPICurlCmd())
	return cmdutil.StrictGroup(cmd)
}

// newAPICurlCmd implements `forge api curl <service.method>`. The command is
// read-only and side-effect free: it never executes the curl, only prints it.
func newAPICurlCmd() *cobra.Command {
	var (
		port      int
		body      string
		host      string
		authToken string
	)
	cmd := &cobra.Command{
		Use:   "curl <service.method>",
		Short: "Print a copy-pasteable curl command for a Connect RPC method",
		Long: `Print a curl invocation that exercises a Connect RPC endpoint over
plain HTTP+JSON. The URL is derived from the proto package + service + method
name; the request body is a zero-value skeleton populated from the method's
input message fields.

Arguments:
  <service.method>   Fully-qualified service and method, e.g. "users.v1.UserService.GetUser".
                     Short form is also accepted: "UserService.GetUser" matches the unique
                     service of that name across all proto packages.

Examples:
  forge api curl users.v1.UserService.GetUser
  forge api curl UserService.GetUser --port 9090
  forge api curl users.v1.UserService.CreateUser --body '{"name":"alice"}'

The command never executes — it only prints. Pipe to ` + "`sh`" + ` if you want to run it,
or paste into a debugger / Postman / HTTPie session.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			projectDir, err := findProjectRoot()
			if err != nil || projectDir == "" {
				return cliutil.UserErr("forge api curl",
					"could not find forge.yaml in current directory or any parent",
					"",
					"run from inside a forge project, or `cd` to one first")
			}
			out, err := buildCurlCommand(projectDir, args[0], curlOptions{
				port:      port,
				body:      body,
				host:      host,
				authToken: authToken,
			})
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), out)
			return nil
		},
	}
	cmd.Flags().IntVar(&port, "port", 0, "Port the app listens on (default 8080; set per-env in deploy/kcl)")
	cmd.Flags().StringVar(&body, "body", "", "Request body JSON (default: zero-value skeleton from proto fields)")
	cmd.Flags().StringVar(&host, "host", "localhost", "Host name to embed in the URL")
	cmd.Flags().StringVar(&authToken, "auth-token", "", "Bearer token to inline (default: a $TOKEN placeholder on RPCs that require auth)")
	return cmd
}

// curlOptions captures the flag-tunable inputs to buildCurlCommand.
// Bundled as a struct so the signature stays stable as we add knobs
// (e.g. --auth-token) without churn at every call site.
type curlOptions struct {
	port      int
	body      string
	host      string
	authToken string
}

// buildCurlCommand is the pure function under the cobra command. It loads
// forge.yaml + forge_descriptor.json, resolves the target service+method,
// and renders a curl invocation as a single string.
//
// Returned errors are user-facing (via cliutil.UserErr): they identify the
// command, what failed, and a one-line fix. Internal errors (missing
// descriptor file, unreadable forge.yaml) are also surfaced through the
// same wrapper so the CLI boundary stays uniform.
func buildCurlCommand(projectDir, target string, opts curlOptions) (string, error) {
	desc, err := loadForgeDescriptor(projectDir)
	if err != nil {
		return "", cliutil.UserErr("forge api curl",
			fmt.Sprintf("could not read forge_descriptor.json: %v", err),
			"gen/forge_descriptor.json",
			"run `forge generate` to produce the proto descriptor, then retry")
	}
	if desc == nil || len(desc.Services) == 0 {
		return "", cliutil.UserErr("forge api curl",
			"no services found in forge_descriptor.json",
			"gen/forge_descriptor.json",
			"run `forge generate` after declaring at least one service in proto/")
	}

	svc, method, err := resolveServiceMethod(desc.Services, target)
	if err != nil {
		return "", err
	}

	port := opts.port
	if port == 0 {
		port = defaultServePort
	}

	bodyJSON := opts.body
	if bodyJSON == "" {
		bodyJSON = buildZeroBody(svc, method)
	}

	host := opts.host
	if host == "" {
		host = "localhost"
	}

	url := fmt.Sprintf("http://%s:%d/%s.%s/%s", host, port, svc.Package, svc.Name, method.Name)

	contentType := "application/json"
	streamingNote := ""
	if method.ClientStreaming || method.ServerStreaming {
		contentType = "application/connect+json"
		streamingNote = "\n# Note: this RPC is streaming — curl will only send/receive the first frame."
	}

	// The descriptor already knows whether the interceptor will turn this
	// caller away (AuthRequired mirrors the proto's auth_required, which is
	// fail-closed by default). Emitting a curl that is guaranteed to 401 and
	// saying nothing is the difference between a command you can paste and a
	// command you have to debug.
	authHeader := ""
	authNote := ""
	switch {
	case opts.authToken != "":
		authHeader = fmt.Sprintf("\n  -H 'Authorization: Bearer %s' \\", opts.authToken)
	case method.AuthRequired:
		authHeader = "\n  -H \"Authorization: Bearer $TOKEN\" \\"
		authNote = "\n# This RPC requires authentication (auth_required). Set $TOKEN first —\n# `forge skill load auth/dev-loop` covers minting one locally, or pass --auth-token."
	}

	// Single-line-per-segment, copy-pasteable. Keep the body inline (the
	// skeleton is small) so users can edit it in place before sending.
	curl := fmt.Sprintf(`curl -X POST \
  -H 'Content-Type: %s' \%s
  -d %s \
  %s`,
		contentType,
		authHeader,
		shellQuoteSingle(bodyJSON),
		url,
	)
	if authNote != "" {
		curl += authNote
	}
	// The dev loop binds a KERNEL-ASSIGNED port at launch (so several stacks
	// coexist on one host) and never persists it — it exists in the `forge env up`
	// banner and nowhere on disk. So this default is right for a deployed env
	// and wrong for the local stack the user most likely wants to hit, and
	// saying so is cheaper than the round-trip through a connection refused.
	if opts.port == 0 {
		curl += fmt.Sprintf(
			"\n# Port %d is the AppConfig default. `forge env up` binds an ephemeral port instead —\n# take it from the launch banner (or `forge env status <env>`) and pass --port.",
			defaultServePort)
	}
	if streamingNote != "" {
		curl += streamingNote
	}
	return curl, nil
}

// resolveServiceMethod accepts either "<pkg>.<Service>.<Method>" (fully
// qualified) or "<Service>.<Method>" (short form). The short form is
// rejected when more than one service across proto packages shares the
// name — the user must disambiguate by qualifying.
func resolveServiceMethod(services []codegen.ServiceDef, target string) (codegen.ServiceDef, codegen.Method, error) {
	parts := strings.Split(target, ".")
	if len(parts) < 2 {
		return codegen.ServiceDef{}, codegen.Method{}, cliutil.UserErr("forge api curl",
			fmt.Sprintf("invalid target %q (need at least Service.Method)", target),
			"",
			"call as `forge api curl <pkg>.<Service>.<Method>` or `forge api curl <Service>.<Method>`")
	}

	methodName := parts[len(parts)-1]
	serviceName := parts[len(parts)-2]
	// Anything before the service name is the proto package (may be empty).
	pkgPrefix := strings.Join(parts[:len(parts)-2], ".")

	var candidates []codegen.ServiceDef
	for _, s := range services {
		if s.Name != serviceName {
			continue
		}
		if pkgPrefix != "" && s.Package != pkgPrefix {
			continue
		}
		candidates = append(candidates, s)
	}

	if len(candidates) == 0 {
		return codegen.ServiceDef{}, codegen.Method{}, cliutil.UserErr("forge api curl",
			fmt.Sprintf("no service %q found in proto descriptors", serviceName),
			"",
			fmt.Sprintf("available services: %s", availableServicesHint(services)))
	}
	if len(candidates) > 1 {
		// Multiple services share the (unqualified) name — the user passed
		// the short form against an ambiguous catalog. Surface every
		// fully-qualified option so the next attempt is unambiguous.
		var qualified []string
		for _, s := range candidates {
			qualified = append(qualified, s.Package+"."+s.Name)
		}
		sort.Strings(qualified)
		return codegen.ServiceDef{}, codegen.Method{}, cliutil.UserErr("forge api curl",
			fmt.Sprintf("service name %q is ambiguous — declared in %d packages", serviceName, len(candidates)),
			"",
			fmt.Sprintf("qualify the package, e.g. `forge api curl %s.%s`", qualified[0], methodName))
	}

	svc := candidates[0]
	for _, m := range svc.Methods {
		if m.Name == methodName {
			return svc, m, nil
		}
	}

	var available []string
	for _, m := range svc.Methods {
		available = append(available, m.Name)
	}
	sort.Strings(available)
	if len(available) == 0 {
		return codegen.ServiceDef{}, codegen.Method{}, cliutil.UserErr("forge api curl",
			fmt.Sprintf("method %q not found on %s.%s (service has no methods)", methodName, svc.Package, svc.Name),
			"",
			"declare an rpc in the .proto file and run `forge generate`")
	}
	return codegen.ServiceDef{}, codegen.Method{}, cliutil.UserErr("forge api curl",
		fmt.Sprintf("method %q not found on %s.%s", methodName, svc.Package, svc.Name),
		"",
		fmt.Sprintf("available methods: %s", strings.Join(available, ", ")))
}

// availableServicesHint returns a short comma-separated list of qualified
// service names, used in the "not found" error to guide the next attempt.
// Truncates beyond a small threshold so the error stays readable.
func availableServicesHint(services []codegen.ServiceDef) string {
	const limit = 5
	names := make([]string, 0, len(services))
	for _, s := range services {
		names = append(names, s.Package+"."+s.Name)
	}
	sort.Strings(names)
	if len(names) <= limit {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:limit], ", ") + fmt.Sprintf(", … (%d more)", len(names)-limit)
}

// defaultServePort is the port a scaffolded binary listens on: every service
// in a binary mounts onto the SAME Connect mux and the process listens once,
// on AppConfig.port (env PORT, default 8080).
//
// There is no per-service port to discover. A port is a DEPLOY fact that lives
// in KCL (deploy/kcl/<env>/config.k), so nothing forge can introspect from the
// proto descriptor or the owned-file tree carries one. Commands that need a URL
// assume this default and let --port override it.
const defaultServePort = 8080

// buildZeroBody renders a JSON object containing each field of the
// method's input message at its proto zero value. The result is small and
// always valid JSON — just enough to make the request parse server-side so
// the user can iterate on the values.
//
// We use the ProtoJSON convention: field names stay snake_case (Connect's
// codec accepts both snake_case and camelCase by default, but snake_case
// matches the proto definition, which is what users tend to recognise).
//
// Input messages we don't have field data for (e.g. google.protobuf.Empty,
// or cross-file references the descriptor didn't capture) render as `{}`
// — empty but valid JSON, which is what those methods accept.
func buildZeroBody(svc codegen.ServiceDef, method codegen.Method) string {
	if method.IsInputEmpty() {
		return "{}"
	}
	fields, ok := svc.Messages[method.InputType]
	if !ok || len(fields) == 0 {
		// Method input is a message we don't have field defs for — the
		// most common cause is a cross-file message reference the
		// descriptor didn't capture. Returning {} keeps the command
		// useful: the request will parse, the user just edits in their
		// own values.
		return "{}"
	}

	obj := make(map[string]any, len(fields))
	for _, f := range fields {
		obj[f.Name] = zeroValueFor(f.ProtoType)
	}

	// json.Marshal sorts keys alphabetically by default; we want to
	// preserve proto declaration order so the skeleton matches the proto
	// file's reading order. Manual render keeps the dependency surface
	// zero (no yaml/json3rd-party deps).
	return renderJSONInOrder(obj, fieldOrder(fields))
}

// fieldOrder returns the proto-declared order of field names for a message.
func fieldOrder(fields []codegen.MessageFieldDef) []string {
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, f.Name)
	}
	return out
}

// renderJSONInOrder renders obj as a JSON object iterating keys in `order`.
// We marshal each value with encoding/json so escaping is correct; we hand-
// concatenate the surrounding `{...}` because the stdlib sorts map keys.
func renderJSONInOrder(obj map[string]any, order []string) string {
	if len(order) == 0 {
		return "{}"
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range order {
		if i > 0 {
			b.WriteByte(',')
		}
		// Key — always a string.
		kb, _ := json.Marshal(k)
		b.Write(kb)
		b.WriteByte(':')
		vb, err := json.Marshal(obj[k])
		if err != nil {
			vb = []byte("null")
		}
		b.Write(vb)
	}
	b.WriteByte('}')
	return b.String()
}

// zeroValueFor returns the proto zero value for a field type. We use Go
// types that json.Marshal will render correctly: "" for string and bytes,
// 0 for numeric, false for bool, nil for message/enum/map (rendered as
// null, which ProtoJSON accepts for those).
//
// For message / enum / map we deliberately emit null rather than a nested
// stub: a recursive walk would balloon the skeleton for deep message graphs
// and risks infinite loops on self-referential types. null is valid
// ProtoJSON for any nullable field and a clear "fill me in" signal.
//
// The SCALAR half is derived: the kind is projected to its Go type through
// codegen's closed table and the JSON zero follows from that type's family,
// so the ten integer widths cannot be named four-of-ten here the way
// kclTypeForProtoConfig once named them. What made that defect invisible is
// exactly the shape below's `default` used to have — an unnamed scalar took
// the non-scalar answer, and a `bytes` field rendered as null is
// indistinguishable from a message field rendered as null.
func zeroValueFor(protoType string) any {
	goType, ok := codegen.ProtoScalarGoType(protoType)
	if !ok {
		// message, enum, map, a well-known type name: not a scalar, and
		// null is the ProtoJSON value for all of them.
		return nil
	}
	switch goType {
	case "bool":
		return false
	case "string":
		return ""
	case "[]byte":
		// ProtoJSON encodes bytes as base64 text; "" is the empty value.
		return ""
	case "int32", "int64", "uint32", "uint64":
		return 0
	case "float32", "float64":
		return 0.0
	}
	// A proto scalar whose Go type names no family above — the vocabulary
	// grew and this projection did not. null would parse, so nothing
	// downstream would ever report it.
	panic("cli: no JSON zero value for proto scalar kind " + protoType)
}

// shellQuoteSingle wraps a string in single quotes for shell embedding,
// escaping any embedded single quotes. We use single quotes for the curl
// -d argument so $-expansion doesn't fire on the JSON body.
//
// Escape sequence inside single quotes: close the quote (`'`), emit an
// escaped quote (`\'`), reopen the quote (`'`). The shell concatenates
// adjacent quoted strings.
func shellQuoteSingle(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

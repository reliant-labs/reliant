// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/reliant-labs/forge/pkg/forgepb"
)

var updateServerRoleSurface = flag.Bool("update-server-role-surface", false,
	"rewrite testdata/server_roles/*.golden from the current tree")

// The server roles' configuration surface, pinned as golden files so a
// reshape of proto/reliant/config/v1 that moves fields between messages must leave
// what each process reads — and what it does with it — byte for byte the same.
//
//   - <role>.surface.golden is the role's env and flag census: every variable
//     (with its aliases, flag, default and validation) of every config the
//     role's command loads, found from the binary_config bindings rather than
//     named here, and every flag the command accepts. A field moving from one
//     message to another leaves it unchanged; a variable or flag gained or
//     lost shows in its diff.
//   - <role>.options.golden is the Options the role hands its package, loaded
//     from serverRoleSurfaceEnv — every variable set to a non-default value.
//     It must never change in a config-only reshape.
//
// Regenerate with: go test ./cmd/reliant/cmd -run TestServerRoleSurface -update-server-role-surface
func TestServerRoleSurface(t *testing.T) {
	isolateServerRoleSurfaceEnv(t)
	for name, value := range serverRoleSurfaceEnv {
		t.Setenv(name, value)
	}
	for _, role := range serverRoles {
		name := role.path[len(role.path)-1]
		t.Run(name, func(t *testing.T) {
			cmd, _, err := NewRootCmd().Find(role.path)
			require.NoError(t, err)
			checkGolden(t, name+".surface.golden", serverRoleSurface(t, cmd, role.path))

			opts, err := role.load(t)
			require.NoError(t, err)
			var b strings.Builder
			dumpValue(&b, reflect.ValueOf(comparableOptions(opts)), "")
			checkGolden(t, name+".options.golden", b.String())
		})
	}
}

// comparableOptions is opts with its func values replaced by what they
// answer, since a func prints as an address.
func comparableOptions(opts any) any {
	clients := func(lookup func(string) (string, string)) map[string][2]string {
		out := map[string][2]string{}
		for _, id := range []string{"github", "slack", "gmail"} {
			clientID, secret := lookup(id)
			out[id] = [2]string{clientID, secret}
		}
		return out
	}
	switch o := opts.(type) {
	case apiOptions:
		got := clients(o.Webhooks.OAuthClients)
		o.Webhooks.OAuthClients = nil
		return []any{o, got}
	case workerOptions:
		got := clients(o.Webhooks.OAuthClients)
		o.Webhooks.OAuthClients = nil
		return []any{o, got}
	case gatewayRole:
		return o.options()
	}
	return opts
}

// serverRoleSurfaceEnv sets every variable a server role reads to a valid
// value that is not its default, so the Options golden shows each one landing.
var serverRoleSurfaceEnv = map[string]string{
	// root config (ReliantConfig). RELIANT_ENV is left unset: loading it
	// configures internal/runenv process-wide.
	"RELIANT_LOG_LEVEL": "warn",
	"LOG_FORMAT":        "json",

	// database
	"DATABASE_URL":                   "postgres://reliant:pw@db.surface.test:5432/reliant",
	"DATABASE_DIRECT_URL":            "postgres://reliant:pw@db-direct.surface.test:5432/reliant",
	"DATABASE_DRIVER":                "postgres",
	"RELIANT_DB_MAX_OPEN_CONNS":      "41",
	"RELIANT_DB_MAX_IDLE_CONNS":      "17",
	"DB_CONNECT_TIMEOUT":             "47s",
	"RELIANT_DB_SCHEMA_WAIT_SECONDS": "123",

	// nats, temporal, streaming
	"NATS_URL":           "nats://nats.surface.test:4223",
	"NATS_USER":          "nats-user",
	"NATS_PASSWORD":      "nats-pass",
	"TEMPORAL_HOST":      "temporal.surface.test",
	"TEMPORAL_PORT":      "7299",
	"TEMPORAL_NAMESPACE": "surface",
	"STREAMING_DRIVER":   "nats",

	// tls, drain, ports, process
	"TLS_CERT_FILE":     "/tls/surface.crt",
	"TLS_KEY_FILE":      "/tls/surface.key",
	"DISABLE_TLS":       "true",
	"PRE_STOP_DELAY":    "11s",
	"SHUTDOWN_TIMEOUT":  "43s",
	"GRPC_PORT":         "19091",
	"HEALTH_PORT":       "18082",
	"PPROF_PORT":        "16061",
	"PPROF_ADDR":        "127.0.0.1:16062",
	"BIND_ADDRESS":      "127.0.0.2",
	"DATA_DIR":          "/surface/data",
	"TOOLS_DAEMON_PORT": "19191",
	"LIVENESS_PATHS":    "/live,/health",
	"READINESS_PATHS":   "/ready,/ready2",

	// control plane
	"RELIANT_CONTROL_PLANE_URL": "http://cp.surface.test:8080",
	"INTERNAL_SERVICE_SECRET":   "cp-secret",

	// network
	"RELIANT_ALLOW_PRIVATE_EGRESS": "true",

	// api only
	"ALLOWED_EMAIL_DOMAINS":             "a.surface.test, b.surface.test",
	"CORS_ALLOWED_ORIGINS":              "http://one.surface.test, http://two.surface.test",
	"AUTH_MODE":                         "apikey",
	"AUTH_API_KEY":                      "api-key",
	"AUTH_REQUIRE_EMAIL_VERIFIED_CLAIM": "true",
	"JWT_PUBLIC_KEY":                    "-----BEGIN PUBLIC KEY-----surface",
	"JWT_PUBLIC_KEY_FILE":               "/keys/jwt.pem",
	"RELIANT_JWKS_URL":                  "https://jwks.surface.test/keys",
	"MAX_UPLOAD_SIZE":                   "4242",
	"MCP_OAUTH_ISSUERS":                 "https://iss1.surface.test, https://iss2.surface.test",
	"RELIANT_AUTHORIZATION_SERVER":      " https://as.surface.test ",
	"RELIANT_AUTH_URL":                  "https://auth.surface.test",
	"RELIANT_AUTH_KEY":                  "sb_publishable_surface",
	"RELIANT_DISABLE_FORGE_API":         "true",
	"RELIANT_PAYLOAD_BLOB_GC_HORIZON":   "99h",
	"RELIANT_WORKTREE_AUTO_ARCHIVE":     "on",

	// worker only
	"RELIANT_WORKER_MAX_CONCURRENT_WORKFLOW_TASKS": "7",
	"RELIANT_WORKER_WORKFLOW_TASK_POLLERS":         "3",
	"RELIANT_WORKER_EGRESS_REPLICAS":               "2",
	"RELIANT_WORKER_EGRESS_USER_PER_MINUTE":        "11",
	"RELIANT_WORKER_EGRESS_USER_BURST":             "12",
	"RELIANT_WORKER_EGRESS_USER_MAX_CONCURRENT":    "15",
	"RELIANT_WORKER_EGRESS_HOST_MAX_CONCURRENT":    "17",
	"RELIANT_WORKER_EGRESS_TURN_MAX_PARALLEL":      "18",

	// integrations
	"PUBLIC_URL":                         " https://public.surface.test ",
	"RELIANT_VAULT_KEY":                  "v1:c3VyZmFjZS12YXVsdC1rZXktMzItYnl0ZXMtbG9uZyE=",
	"RELIANT_SLACK_SIGNING_SECRET":       "slack-signing",
	"RELIANT_GITHUB_WEBHOOK_SECRET":      "gh-webhook",
	"RELIANT_GITHUB_APP_SLUG":            " surface-app ",
	"RELIANT_GITHUB_BOT_USER_ID":         "4242",
	"RELIANT_GITHUB_BOT_LOGIN":           "surface-bot[bot]",
	"RELIANT_TEST_INTEGRATION_SECRET":    "test-integration",
	"RELIANT_OAUTH_GITHUB_CLIENT_ID":     "gh-client",
	"RELIANT_OAUTH_GITHUB_CLIENT_SECRET": "gh-secret",
	"RELIANT_OAUTH_SLACK_CLIENT_ID":      "slack-client",

	// observability, analytics
	"OTEL_EXPORTER_OTLP_ENDPOINT": "http://otel.surface.test:4318",
	"OTEL_ENABLED":                "true",
	"PROMETHEUS_ENABLED":          "false",
	"ENVIRONMENT":                 "surface",
	"RELIANT_ANALYTICS_DISABLED":  "true",

	// llm
	"RELIANT_API_BASE_URL":                       "https://llm.surface.test",
	"VERTEXAI_PROJECT":                           "surface-project",
	"VERTEXAI_LOCATION":                          "europe-west4",
	"RELIANT_LLM_STREAM_IDLE_TIMEOUT":            "91s",
	"RELIANT_LLM_STREAM_CONTENT_STALL_TIMEOUT":   "6m",
	"RELIANT_LLM_STREAM_PROGRESS_TIMEOUT":        "11m",
	"RELIANT_LLM_STREAM_MID_BLOCK_STALL_TIMEOUT": "3m",
	"RELIANT_DEBUG_OPENAI_RESPONSES_INPUT":       "true",
	"RELIANT_DEBUG_OPENAI_TOOL_SCHEMAS":          "true",

	// preview
	"PROXY_HOST":            "proxy.surface.test",
	"WORKSPACE_BASE_DOMAIN": "ws.surface.test",
	"WORKSPACE_URL_SCHEME":  "http",
	"WORKSPACE_URL_PORT":    "8443",
}

// isolateServerRoleSurfaceEnv unsets every variable any reliant config
// declares, so the census and the Options depend on serverRoleSurfaceEnv alone.
func isolateServerRoleSurfaceEnv(t *testing.T) {
	t.Helper()
	var prefixes []string
	for _, msg := range reliantConfigMessages() {
		for _, v := range configVars(msg.Fields()) {
			if v.prefix != "" {
				prefixes = append(prefixes, v.prefix)
				continue
			}
			for _, name := range append([]string{v.env}, v.aliases...) {
				t.Setenv(name, "")
				require.NoError(t, os.Unsetenv(name))
			}
		}
	}
	t.Setenv("RELIANT_CONFIG", "")
	require.NoError(t, os.Unsetenv("RELIANT_CONFIG"))
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		for _, p := range prefixes {
			if strings.HasPrefix(name, p) {
				t.Setenv(name, "")
				require.NoError(t, os.Unsetenv(name))
			}
		}
	}
}

// serverRoleSurface renders the census of what the command at path reads.
func serverRoleSurface(t *testing.T, cmd *cobra.Command, path []string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("# env: every variable of every config `reliant " + strings.Join(path, " ") + "` loads\n")
	var lines []string
	for _, msg := range reliantConfigMessages() {
		if !loadedBy(msg, path) {
			continue
		}
		for _, v := range configVars(msg.Fields()) {
			lines = append(lines, v.String())
		}
	}
	sort.Strings(lines)
	require.Equal(t, len(lines), len(slices.Compact(slices.Clone(lines))), "a variable is declared twice")
	b.WriteString(strings.Join(lines, "\n"))
	b.WriteString("\n\n# flags: every flag the command accepts\n")
	lines = nil
	add := func(f *pflag.Flag) {
		lines = append(lines, fmt.Sprintf("--%s shorthand=%q type=%s default=%q", f.Name, f.Shorthand, f.Value.Type(), f.DefValue))
	}
	cmd.InheritedFlags().VisitAll(add)
	cmd.LocalFlags().VisitAll(add)
	sort.Strings(lines)
	b.WriteString(strings.Join(slices.Compact(lines), "\n"))
	b.WriteString("\n")
	return b.String()
}

// reliantConfigMessages is every message in reliant.config.v1 bound to the reliant
// binary.
func reliantConfigMessages() []protoreflect.MessageDescriptor {
	var out []protoreflect.MessageDescriptor
	protoregistry.GlobalFiles.RangeFilesByPackage("reliant.config.v1", func(fd protoreflect.FileDescriptor) bool {
		for i := 0; i < fd.Messages().Len(); i++ {
			if b := binaryConfig(fd.Messages().Get(i)); b != nil && b.GetBinary() == ServiceName {
				out = append(out, fd.Messages().Get(i))
			}
		}
		return true
	})
	sort.Slice(out, func(i, j int) bool { return out[i].FullName() < out[j].FullName() })
	return out
}

func binaryConfig(md protoreflect.MessageDescriptor) *forgepb.BinaryConfigOptions {
	opts := md.Options()
	if opts == nil || !proto.HasExtension(opts, forgepb.E_BinaryConfig) {
		return nil
	}
	return proto.GetExtension(opts, forgepb.E_BinaryConfig).(*forgepb.BinaryConfigOptions)
}

// loadedBy reports whether the process running path loads msg: the root
// config, a subtree config over a prefix of path, or path's own config.
func loadedBy(msg protoreflect.MessageDescriptor, path []string) bool {
	b := binaryConfig(msg)
	if len(b.GetCommand()) > 0 {
		return slices.Equal(b.GetCommand(), path)
	}
	if len(b.GetSubtrees()) == 0 {
		return true
	}
	for _, s := range b.GetSubtrees() {
		if len(s.GetPath()) <= len(path) && slices.Equal(s.GetPath(), path[:len(s.GetPath())]) {
			return true
		}
	}
	return false
}

// configVar is one config leaf as an operator sets it.
type configVar struct {
	env, prefix, suffix string
	aliases             []string
	opts                *forgepb.ConfigFieldOptions
	kind                string
}

func (v configVar) String() string {
	name := v.env
	if v.prefix != "" {
		name = v.prefix + "*" + v.suffix
	}
	var aliases []string
	for _, a := range v.opts.GetEnvVarAliases() {
		aliases = append(aliases, fmt.Sprintf("%s(deprecated=%t values=%v)", a.GetName(), a.GetDeprecated(), a.GetValues()))
	}
	return fmt.Sprintf("%s kind=%s flag=%q default=%q required=%t optional=%t sensitive=%t allowed=%v aliases=%v",
		name, v.kind, v.opts.GetFlag(), v.opts.GetDefaultValue(), v.opts.GetRequired(), v.opts.GetOptional(),
		v.opts.GetSensitive(), v.opts.GetAllowedValues(), aliases)
}

// configVars walks fields, descending into composed blocks: a message field
// with no config option of its own.
func configVars(fields protoreflect.FieldDescriptors) []configVar {
	var out []configVar
	for i := 0; i < fields.Len(); i++ {
		f := fields.Get(i)
		opts, _ := proto.GetExtension(f.Options(), forgepb.E_Config).(*forgepb.ConfigFieldOptions)
		if opts.GetEnvVar() == "" && opts.GetEnvPrefix() == "" {
			if f.Kind() == protoreflect.MessageKind && !f.IsMap() {
				out = append(out, configVars(f.Message().Fields())...)
			}
			continue
		}
		kind := f.Kind().String()
		if f.Kind() == protoreflect.MessageKind {
			kind = string(f.Message().FullName())
		}
		if f.IsList() {
			kind = "repeated " + kind
		}
		if f.IsMap() {
			kind = "map"
		}
		v := configVar{env: opts.GetEnvVar(), prefix: opts.GetEnvPrefix(), suffix: opts.GetEnvSuffix(), opts: opts, kind: kind}
		for _, a := range opts.GetEnvVarAliases() {
			v.aliases = append(v.aliases, a.GetName())
		}
		out = append(out, v)
	}
	return out
}

// dumpValue renders v deterministically, unexported fields included: every
// field by name, map keys sorted, durations as durations, no addresses.
func dumpValue(b *strings.Builder, v reflect.Value, indent string) {
	switch v.Kind() {
	case reflect.Invalid:
		b.WriteString("<nil>")
	case reflect.Interface, reflect.Pointer:
		if v.IsNil() {
			b.WriteString("<nil>")
			return
		}
		dumpValue(b, v.Elem(), indent)
	case reflect.Struct:
		b.WriteString(v.Type().String() + " {\n")
		for i := 0; i < v.NumField(); i++ {
			b.WriteString(indent + "  " + v.Type().Field(i).Name + ": ")
			dumpValue(b, v.Field(i), indent+"  ")
			b.WriteString("\n")
		}
		b.WriteString(indent + "}")
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			b.WriteString("<nil>")
			return
		}
		b.WriteString("[\n")
		for i := 0; i < v.Len(); i++ {
			b.WriteString(indent + "  ")
			dumpValue(b, v.Index(i), indent+"  ")
			b.WriteString("\n")
		}
		b.WriteString(indent + "]")
	case reflect.Map:
		if v.IsNil() {
			b.WriteString("<nil>")
			return
		}
		keys := v.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return fmt.Sprint(keys[i]) < fmt.Sprint(keys[j]) })
		b.WriteString("{\n")
		for _, k := range keys {
			b.WriteString(indent + "  " + fmt.Sprint(k) + ": ")
			dumpValue(b, v.MapIndex(k), indent+"  ")
			b.WriteString("\n")
		}
		b.WriteString(indent + "}")
	case reflect.Func, reflect.Chan:
		fmt.Fprintf(b, "%s(set=%t)", v.Kind(), !v.IsNil())
	case reflect.String:
		fmt.Fprintf(b, "%q", v.String())
	case reflect.Bool:
		fmt.Fprint(b, v.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if v.Type() == reflect.TypeOf(time.Duration(0)) {
			b.WriteString(time.Duration(v.Int()).String())
			return
		}
		fmt.Fprint(b, v.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		fmt.Fprint(b, v.Uint())
	case reflect.Float32, reflect.Float64:
		fmt.Fprint(b, v.Float())
	default:
		b.WriteString("<" + v.Kind().String() + ">")
	}
}

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "server_roles", name)
	if *updateServerRoleSurface {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(got), 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "missing golden: run with -update-server-role-surface")
	require.Equal(t, string(want), got, "%s changed; if on purpose, rerun with -update-server-role-surface and review the diff", path)
}

// File: otelenv.go — the one place pkg/observe reads the process environment.
//
// The OpenTelemetry environment variables are the contract between a forge app
// and whatever collector the platform runs: an app never learns the backend, the
// platform renders OTEL_* and the runtime obeys. The exporters read most of them
// themselves (headers, timeout, compression, TLS material, the per-signal
// endpoint variants); this file only reads what they cannot decide:
//
//   - which signals are exported at all (an endpoint is present, the SDK is not
//     disabled, the signal's exporter is not "none"),
//   - which wire protocol to build (the SDK has one package per protocol),
//   - whether the opt-in OTLP logs exporter is on.
//
// pkg/.golangci.yml exempts exactly this file from the os.Getenv rule.

package observe

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Environment variable names of the contract. Exported so docs, doctor checks
// and renderers can refer to one spelling.
const (
	EnvOTLPEndpoint = "OTEL_EXPORTER_OTLP_ENDPOINT"
	EnvOTLPProtocol = "OTEL_EXPORTER_OTLP_PROTOCOL"
	EnvOTLPHeaders  = "OTEL_EXPORTER_OTLP_HEADERS"

	// EnvLogsExporter is the explicit switch for the OTLP logs exporter. The
	// OpenTelemetry specification defaults it to "otlp"; forge defaults it OFF
	// because logs already have a shipping path (stdout/file collection), and
	// only the value "otlp" turns the exporter on.
	EnvLogsExporter = "OTEL_LOGS_EXPORTER"

	// EnvSDKDisabled turns every export off. Context propagation stays on.
	EnvSDKDisabled = "OTEL_SDK_DISABLED"

	// EnvProfilesExporter is the switch for continuous profiling. Profiles are
	// exported by default whenever an OTLP endpoint is configured; the value
	// "none" turns them off (the same spelling OTEL_TRACES_EXPORTER=none uses).
	EnvProfilesExporter = "OTEL_PROFILES_EXPORTER"

	// EnvOTLPProfilesEndpoint is the per-signal profiles endpoint. Unlike the
	// base OTEL_EXPORTER_OTLP_ENDPOINT it is a FULL URL used as given, so a
	// platform can send profiles to a different collector than traces. It
	// exists because profiles need a collector built with the profiles feature
	// gate and a profiles-capable exporter, which the traces collector may not be.
	EnvOTLPProfilesEndpoint = "OTEL_EXPORTER_OTLP_PROFILES_ENDPOINT"
)

const (
	// ProtocolHTTPProtobuf is OTLP over HTTP with protobuf bodies (port 4318).
	// It is the default.
	ProtocolHTTPProtobuf = "http/protobuf"
	// ProtocolGRPC is OTLP over gRPC (port 4317).
	ProtocolGRPC = "grpc"
)

// signal names an OTLP signal as it appears in OTEL_EXPORTER_OTLP_<SIGNAL>_*.
type signal string

const (
	signalTraces  signal = "TRACES"
	signalMetrics signal = "METRICS"
	signalLogs    signal = "LOGS"
	// signalProfiles is OTLP profiles (v1development). Its HTTP path is
	// /v1development/profiles, not /v1/profiles.
	signalProfiles signal = "PROFILES"
)

func envValue(name string) string { return strings.TrimSpace(os.Getenv(name)) }

func (s signal) env(suffix string) string {
	return envValue("OTEL_EXPORTER_OTLP_" + string(s) + "_" + suffix)
}

// sdkDisabled reports OTEL_SDK_DISABLED=true.
func sdkDisabled() bool { return strings.EqualFold(envValue(EnvSDKDisabled), "true") }

// logsExportRequested reports whether OTEL_LOGS_EXPORTER asks for OTLP.
func logsExportRequested() bool {
	for _, v := range strings.Split(envValue(EnvLogsExporter), ",") {
		if strings.EqualFold(strings.TrimSpace(v), "otlp") {
			return true
		}
	}
	return false
}

// exportEnabled reports whether signal s is exported. With no endpoint from any
// source (Config.OTLPEndpoint, OTEL_EXPORTER_OTLP_ENDPOINT or the per-signal
// variable) there is nothing to send to, and export stays off.
func exportEnabled(s signal, cfg Config) bool {
	if sdkDisabled() {
		return false
	}
	switch s {
	case signalLogs:
		if !cfg.OTLPLogs && !logsExportRequested() {
			return false
		}
	case signalProfiles:
		// On by default; off by the typed field, OTEL_PROFILES_EXPORTER=none,
		// or a profile selection that names no type.
		if cfg.ProfilesDisabled || strings.EqualFold(envValue(EnvProfilesExporter), "none") {
			return false
		}
		if s, _ := resolveProfileSettings(cfg.Profiles); !s.enabled() {
			return false
		}
	default:
		if strings.EqualFold(envValue("OTEL_"+string(s)+"_EXPORTER"), "none") {
			return false
		}
	}
	return cfg.OTLPEndpoint != "" || envValue(EnvOTLPEndpoint) != "" || s.env("ENDPOINT") != ""
}

// protocolFor resolves the wire protocol of signal s: the per-signal variable,
// then OTEL_EXPORTER_OTLP_PROTOCOL, then http/protobuf.
func protocolFor(s signal) (string, error) {
	p := strings.ToLower(s.env("PROTOCOL"))
	if p == "" {
		p = strings.ToLower(envValue(EnvOTLPProtocol))
	}
	switch p {
	case "", ProtocolHTTPProtobuf:
		return ProtocolHTTPProtobuf, nil
	case ProtocolGRPC:
		return ProtocolGRPC, nil
	default:
		return "", fmt.Errorf("unsupported OTLP protocol %q for %s (want %q or %q)", p, strings.ToLower(string(s)), ProtocolHTTPProtobuf, ProtocolGRPC)
	}
}

// typedEndpointURL turns the caller's Config.OTLPEndpoint into the URL handed
// to an exporter's WithEndpointURL. A bare "host:port" gains an http:// scheme.
// For HTTP the value has base-endpoint semantics, exactly like
// OTEL_EXPORTER_OTLP_ENDPOINT, so the signal path (/v1/traces, ...) is appended.
func typedEndpointURL(endpoint, protocol string, s signal) (string, error) {
	if !strings.Contains(endpoint, "://") {
		endpoint = "http://" + endpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("invalid OTLP endpoint %q: %w", endpoint, err)
	}
	if protocol == ProtocolHTTPProtobuf {
		u.Path = strings.TrimRight(u.Path, "/") + signalPath(s)
	}
	return u.String(), nil
}

// signalPath is the OTLP/HTTP path of signal s under a base endpoint.
func signalPath(s signal) string {
	if s == signalProfiles {
		return "/v1development/profiles"
	}
	return "/v1/" + strings.ToLower(string(s))
}

// profilesTarget is where and how the profiler pushes, resolved once from Config
// and the environment.
type profilesTarget struct {
	Protocol string
	// URL is the full OTLP/HTTP URL, or the gRPC dial target (host:port).
	URL string
	// Insecure is gRPC-only: plaintext, because the endpoint said http:// or
	// named no scheme.
	Insecure bool
	Headers  map[string]string
	// FileHeaders, when non-nil, adds the headers of
	// OTEL_EXPORTER_OTLP_HEADERS_FILE to every send; Headers wins on a clash.
	FileHeaders *headerSource
	Timeout     time.Duration
	Gzip        bool
}

// resolveProfilesTarget turns Config and the OTEL_* environment into the
// profiles destination. Precedence for the address, most specific first:
//
//  1. OTEL_EXPORTER_OTLP_PROFILES_ENDPOINT, a FULL URL used as given;
//  2. Config.OTLPEndpoint, then OTEL_EXPORTER_OTLP_ENDPOINT, both BASE
//     endpoints that gain /v1development/profiles for http/protobuf.
//
// Profiles are gzip-compressed unless OTEL_EXPORTER_OTLP_[PROFILES_]COMPRESSION
// says none: a CPU profile is protobuf with heavy string repetition.
func resolveProfilesTarget(cfg Config) (profilesTarget, error) {
	proto, err := protocolFor(signalProfiles)
	if err != nil {
		return profilesTarget{}, err
	}
	t := profilesTarget{Protocol: proto, Timeout: 10 * time.Second, Gzip: true, Headers: map[string]string{}}

	full := signalProfiles.env("ENDPOINT")
	base := cfg.OTLPEndpoint
	if base == "" {
		base = envValue(EnvOTLPEndpoint)
	}
	endpoint := full
	if endpoint == "" {
		endpoint = base
	}
	if endpoint == "" {
		return profilesTarget{}, fmt.Errorf("no OTLP endpoint configured")
	}
	if !strings.Contains(endpoint, "://") {
		endpoint = "http://" + endpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return profilesTarget{}, fmt.Errorf("invalid OTLP profiles endpoint %q: %w", endpoint, err)
	}
	if proto == ProtocolGRPC {
		t.URL = u.Host
		t.Insecure = u.Scheme != "https"
	} else {
		if full == "" {
			u.Path = strings.TrimRight(u.Path, "/") + signalPath(signalProfiles)
		}
		t.URL = u.String()
	}

	for _, raw := range []string{envValue(EnvOTLPHeaders), signalProfiles.env("HEADERS")} {
		for _, kv := range strings.Split(raw, ",") {
			k, v, ok := strings.Cut(kv, "=")
			if !ok || strings.TrimSpace(k) == "" {
				continue
			}
			if dv, err := url.PathUnescape(strings.TrimSpace(v)); err == nil {
				v = dv
			}
			t.Headers[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}

	t.FileHeaders = headerSourceFor(signalProfiles)

	ms := signalProfiles.env("TIMEOUT")
	if ms == "" {
		ms = envValue("OTEL_EXPORTER_OTLP_TIMEOUT")
	}
	if n, err := strconv.Atoi(ms); err == nil && n > 0 {
		t.Timeout = time.Duration(n) * time.Millisecond
	}
	comp := signalProfiles.env("COMPRESSION")
	if comp == "" {
		comp = envValue("OTEL_EXPORTER_OTLP_COMPRESSION")
	}
	if strings.EqualFold(comp, "none") {
		t.Gzip = false
	}
	return t, nil
}

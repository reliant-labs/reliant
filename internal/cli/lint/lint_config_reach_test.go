package lint

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
)

// cfg builds a ConfigMessage with n leaf fields, for brevity.
func cfgMsg(name, binary, frontend string, fields ...codegen.ConfigField) codegen.ConfigMessage {
	return codegen.ConfigMessage{Name: name, Binary: binary, Frontend: frontend, Fields: fields}
}

func leaf(name, env string) codegen.ConfigField {
	return codegen.ConfigField{Name: name, GoName: name, ProtoType: "string", EnvVar: env}
}

func block(goName, msgType string) codegen.ConfigField {
	return codegen.ConfigField{
		Name: goName, GoName: goName, ProtoType: "message", MessageType: msgType,
	}
}

// THE MOTIVATING CASE. Once configs are per-binary, a message can be
// annotated for a binary that was renamed or deleted, or simply never
// composed anywhere. Its fields are then loaded by no process: they
// still exist in config.proto, still generate, and are read by nothing.
func TestCollectConfigReachFindings_FlagsMessageNoBinaryLoads(t *testing.T) {
	msgs := []codegen.ConfigMessage{
		cfgMsg("AppConfig", "", "", leaf("port", "PORT")),
		// Annotated for a binary that does not exist in cmd/.
		cfgMsg("GhostConfig", "ghost", "", leaf("ghost_url", "GHOST_URL")),
	}

	findings := collectConfigReachFindings(msgs, []string{"server"}, nil)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %+v", len(findings), findings)
	}
	f := findings[0]
	if f.Message != "GhostConfig" || f.Field != "ghost_url" {
		t.Errorf("finding = %s.%s, want GhostConfig.ghost_url", f.Message, f.Field)
	}
	if f.Reason != configReachNoSuchBinary {
		t.Errorf("reason = %q, want %q", f.Reason, configReachNoSuchBinary)
	}
	hint := configReachFixHint(f)
	for _, want := range []string{"ghost", "cmd/", "GhostConfig"} {
		if !strings.Contains(hint, want) {
			t.Errorf("fix hint missing %q:\n%s", want, hint)
		}
	}
}

// THE FALSE-POSITIVE TRAP, taken directly from the real project this
// check was designed against. Its AppConfig is DELIBERATELY not annotated
// with (forge.v1.binary_config) — it is the PROJECT-GLOBAL root config
// that pkg/config.Load emits for, and forge picks the first unbound
// non-block message as exactly that (codegen: RootConfigMessage).
//
// A naive "composed by no binary" check flags all 75 of its fields. That
// is the spurious-failure-on-every-project outcome; the whole check is
// worthless if it does this, so it is pinned here.
func TestCollectConfigReachFindings_RootAppConfigIsReachable(t *testing.T) {
	msgs := []codegen.ConfigMessage{
		cfgMsg("AppConfig", "", "", leaf("port", "PORT"), leaf("log_level", "LOG_LEVEL")),
		cfgMsg("OneshotConfig", "oneshot", "", leaf("idp_url", "IDP_URL")),
	}

	findings := collectConfigReachFindings(msgs, []string{"control-plane", "oneshot"}, nil)
	if len(findings) != 0 {
		t.Fatalf("the unannotated root AppConfig is the project-global config, not dead: %+v", findings)
	}
}

// A shared DEFINITION block (BaseConfig) carries no binary annotation of
// its own and is reachable through the messages that COMPOSE it. This is
// the documented shared-config pattern; flagging it would punish the
// exact shape forge tells people to use.
func TestCollectConfigReachFindings_ComposedBlockIsReachable(t *testing.T) {
	msgs := []codegen.ConfigMessage{
		cfgMsg("BaseConfig", "", "", leaf("log_level", "LOG_LEVEL")),
		cfgMsg("AdminConfig", "admin", "", block("Base", "BaseConfig"), leaf("admin_key", "ADMIN_KEY")),
	}

	findings := collectConfigReachFindings(msgs, []string{"admin"}, nil)
	if len(findings) != 0 {
		t.Fatalf("a composed shared block is reachable through its composer: %+v", findings)
	}
}

// A FRONTEND-bound message is loaded by a browser bundle, not a binary.
// It is reachable, and flagging it would fire on every project using
// frontend config.
func TestCollectConfigReachFindings_FrontendBoundIsReachable(t *testing.T) {
	msgs := []codegen.ConfigMessage{
		cfgMsg("AppConfig", "", "", leaf("port", "PORT")),
		cfgMsg("WebConfig", "", "web", leaf("api_base", "API_BASE")),
	}

	findings := collectConfigReachFindings(msgs, []string{"server"}, []string{"web"})
	if len(findings) != 0 {
		t.Fatalf("a frontend-bound config is loaded by its bundle: %+v", findings)
	}
}

// A frontend-bound message whose FRONTEND no longer exists is the
// frontend twin of the ghost binary, and is reported the same way.
func TestCollectConfigReachFindings_FlagsMissingFrontend(t *testing.T) {
	msgs := []codegen.ConfigMessage{
		cfgMsg("AppConfig", "", "", leaf("port", "PORT")),
		cfgMsg("OldWebConfig", "", "retired-web", leaf("api_base", "API_BASE")),
	}

	findings := collectConfigReachFindings(msgs, []string{"server"}, []string{"web"})
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %+v", len(findings), findings)
	}
	if findings[0].Reason != configReachNoSuchFrontend {
		t.Errorf("reason = %q, want %q", findings[0].Reason, configReachNoSuchFrontend)
	}
}

// A SECOND unbound, uncomposed message is genuinely orphaned: the first
// unbound message is the root config, but there is only one root. This
// is the case the check exists to find in an all-annotated project.
func TestCollectConfigReachFindings_FlagsOrphanedUnboundMessage(t *testing.T) {
	msgs := []codegen.ConfigMessage{
		cfgMsg("AppConfig", "", "", leaf("port", "PORT")),
		// Not annotated, not composed by anything, not the root.
		cfgMsg("StrayConfig", "", "", leaf("stray_url", "STRAY_URL")),
	}

	findings := collectConfigReachFindings(msgs, []string{"server"}, nil)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %+v", len(findings), findings)
	}
	f := findings[0]
	if f.Message != "StrayConfig" {
		t.Errorf("message = %q, want StrayConfig", f.Message)
	}
	if f.Reason != configReachOrphaned {
		t.Errorf("reason = %q, want %q", f.Reason, configReachOrphaned)
	}
}

// When a project is FULLY per-binary (every message annotated, no root
// AppConfig at all), nothing is the root and nothing should be flagged
// for it. Guards against the "first unbound message" rule mis-firing
// when there is no unbound message.
func TestCollectConfigReachFindings_AllBinaryBoundIsClean(t *testing.T) {
	msgs := []codegen.ConfigMessage{
		cfgMsg("ServerConfig", "server", "", leaf("port", "PORT")),
		cfgMsg("WorkerConfig", "worker", "", leaf("queue", "QUEUE")),
	}

	findings := collectConfigReachFindings(msgs, []string{"server", "worker"}, nil)
	if len(findings) != 0 {
		t.Fatalf("an all-annotated project is clean: %+v", findings)
	}
}

// A project with no config messages at all, and one with no binaries
// discovered, must both be silent rather than flag everything. The
// "cannot determine the binary set" case is the dangerous one: it must
// NOT be read as "no binary composes anything".
func TestCollectConfigReachFindings_UnknownBinarySetIsSilent(t *testing.T) {
	msgs := []codegen.ConfigMessage{
		cfgMsg("AppConfig", "", "", leaf("port", "PORT")),
		cfgMsg("AdminConfig", "admin", "", leaf("admin_key", "ADMIN_KEY")),
	}

	// nil binaries = "could not enumerate cmd/" — the check must not
	// conclude that `admin` does not exist.
	findings := collectConfigReachFindings(msgs, nil, nil)
	if len(findings) != 0 {
		t.Fatalf("an unknown binary set must not be read as an empty one: %+v", findings)
	}

	if got := collectConfigReachFindings(nil, []string{"server"}, nil); len(got) != 0 {
		t.Fatalf("no config messages, no findings: %+v", got)
	}
}

// Nested composition (a block composing another block) keeps the inner
// block reachable.
func TestCollectConfigReachFindings_TransitiveCompositionIsReachable(t *testing.T) {
	msgs := []codegen.ConfigMessage{
		cfgMsg("InnerConfig", "", "", leaf("deep", "DEEP")),
		cfgMsg("MiddleConfig", "", "", block("Inner", "InnerConfig")),
		cfgMsg("ServerConfig", "server", "", block("Middle", "MiddleConfig")),
	}

	findings := collectConfigReachFindings(msgs, []string{"server"}, nil)
	if len(findings) != 0 {
		t.Fatalf("transitively composed blocks are reachable: %+v", findings)
	}
}

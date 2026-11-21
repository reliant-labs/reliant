package codegen

import (
	"strings"
	"testing"
)

// reliant's shape (forge bug 938f0340): ServerProcessConfig composes a
// ClientVersionPolicy block whose own DaemonClientVersionFloor block carries
// the env-bound floors. The first level projects; the second is named.
func TestUnprojectedNestedConfigBlocks_NamesABlockTwoDeep(t *testing.T) {
	messages := []ConfigMessage{
		{Name: "ServerProcessConfig", Fields: []ConfigField{
			{Name: "port", ProtoType: "int32", EnvVar: "PORT"},
			{Name: "client_versions", ProtoType: "message", MessageType: "ClientVersionPolicy"},
		}},
		{Name: "ClientVersionPolicy", Fields: []ConfigField{
			{Name: "enforce", ProtoType: "bool", EnvVar: "RELIANT_ENFORCE_CLIENT_VERSIONS"},
			{Name: "daemon", ProtoType: "message", MessageType: "DaemonClientVersionFloor"},
		}},
		{Name: "DaemonClientVersionFloor", Fields: []ConfigField{
			{Name: "min_version", ProtoType: "string", EnvVar: "RELIANT_MIN_DAEMON_VERSION"},
			{Name: "min_commit_time_unix", ProtoType: "int64", EnvVar: "RELIANT_MIN_DAEMON_COMMIT_TIME_UNIX"},
		}},
	}
	got := UnprojectedNestedConfigBlocks(messages)
	if len(got) != 1 {
		t.Fatalf("got %d warnings, want 1: %q", len(got), got)
	}
	for _, want := range []string{"ServerProcessConfig.client_versions.daemon", "DaemonClientVersionFloor", "RELIANT_MIN_DAEMON_COMMIT_TIME_UNIX, RELIANT_MIN_DAEMON_VERSION"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("warning %q does not name %q", got[0], want)
		}
	}
	if strings.Contains(got[0], "RELIANT_ENFORCE_CLIENT_VERSIONS") {
		t.Errorf("a first-level leaf is projected and must not be named: %q", got[0])
	}

	// What the warning describes is what the projection drops: the deep
	// leaves are absent from the flattened list, the first-level one is not.
	flat := FlattenBlockLeaves(messages, "ServerProcessConfig")
	var envs []string
	for _, f := range flat {
		envs = append(envs, f.EnvVar)
	}
	if joined := strings.Join(envs, " "); !strings.Contains(joined, "RELIANT_ENFORCE_CLIENT_VERSIONS") || strings.Contains(joined, "RELIANT_MIN_DAEMON_VERSION") {
		t.Errorf("flattened env vars %q: want the first level projected and the second dropped", joined)
	}
}

// One level of blocks, an unknown message type, a frontend config and a
// self-composing block are all silent.
func TestUnprojectedNestedConfigBlocks_SilentWhenNothingIsDropped(t *testing.T) {
	if got := UnprojectedNestedConfigBlocks(nestedBlockMessages()); len(got) != 0 {
		t.Errorf("one level of blocks warned: %q", got)
	}
	messages := []ConfigMessage{
		{Name: "AppConfig", Fields: []ConfigField{
			{Name: "timeout", ProtoType: "message", MessageType: "google.protobuf.Duration"},
		}},
		{Name: "WebConfig", Frontend: "web", Fields: []ConfigField{
			{Name: "theme", ProtoType: "message", MessageType: "ThemeConfig"},
		}},
		{Name: "ThemeConfig", Fields: []ConfigField{
			{Name: "palette", ProtoType: "message", MessageType: "PaletteConfig"},
		}},
		{Name: "PaletteConfig", Fields: []ConfigField{{Name: "accent", ProtoType: "string"}}},
	}
	if got := UnprojectedNestedConfigBlocks(messages); len(got) != 0 {
		t.Errorf("nothing the backend projection drops, but warned: %q", got)
	}
}

func TestUnprojectedNestedConfigBlocks_ListsLeavesAtAnyDepthOnce(t *testing.T) {
	messages := []ConfigMessage{
		{Name: "AppConfig", Fields: []ConfigField{{Name: "a", ProtoType: "message", MessageType: "A"}}},
		{Name: "A", Fields: []ConfigField{{Name: "b", ProtoType: "message", MessageType: "B"}}},
		{Name: "B", Fields: []ConfigField{
			{Name: "x", ProtoType: "string", EnvVar: "X"},
			{Name: "c", ProtoType: "message", MessageType: "C"},
		}},
		{Name: "C", Fields: []ConfigField{
			{Name: "y", ProtoType: "string", EnvVar: "Y"},
			{Name: "loop", ProtoType: "message", MessageType: "C"},
		}},
	}
	got := UnprojectedNestedConfigBlocks(messages)
	if len(got) != 1 || !strings.Contains(got[0], "AppConfig.a.b") || !strings.Contains(got[0], "(X, Y)") {
		t.Errorf("got %q, want one warning for AppConfig.a.b naming X and Y", got)
	}
}

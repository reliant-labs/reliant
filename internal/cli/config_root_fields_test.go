package cli

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
)

// TestConfigFieldsExcludingBinaries_FlattensRootComposedBlocks reproduces a
// codegen bug: a composed block (message-typed field) referenced by the
// project-global AppConfig was dropped entirely instead of having its
// leaves flattened in, one nesting level, the same way ConfigFieldsForBinary
// and BuildConfigTemplateData already do for their own projections.
func TestConfigFieldsExcludingBinaries_FlattensRootComposedBlocks(t *testing.T) {
	msgs := []codegen.ConfigMessage{
		{Name: "AppConfig", Fields: []codegen.ConfigField{
			{Name: "port", GoName: "Port", GoType: "int32", ProtoType: "int32", EnvVar: "PORT", DefaultValue: "8080"},
			{Name: "github", GoName: "Github", ProtoType: "message", MessageType: "GithubConfig"},
		}},
		{Name: "GithubConfig", Fields: []codegen.ConfigField{
			{Name: "github_client_id", GoName: "GithubClientId", GoType: "string", ProtoType: "string", EnvVar: "GITHUB_CLIENT_ID"},
		}},
		{Name: "OneshotConfig", Binary: "idp-provision-oneshot", Fields: []codegen.ConfigField{
			{Name: "zitadel_endpoint", GoName: "ZitadelEndpoint", GoType: "string", ProtoType: "string", EnvVar: "ZITADEL_ENDPOINT"},
		}},
	}
	root := configFieldsExcludingBinaries(msgs)
	var names []string
	for _, f := range root {
		names = append(names, f.EnvVar)
	}
	if !strings.Contains(strings.Join(names, ","), "GITHUB_CLIENT_ID") {
		t.Errorf("block leaf GITHUB_CLIENT_ID dropped from root KCL fields; got %v", names)
	}
	if !strings.Contains(strings.Join(names, ","), "PORT") {
		t.Errorf("plain root scalar PORT missing; got %v", names)
	}
	if strings.Contains(strings.Join(names, ","), "ZITADEL_ENDPOINT") {
		t.Errorf("binary-bound field ZITADEL_ENDPOINT leaked into root fields; got %v", names)
	}
}

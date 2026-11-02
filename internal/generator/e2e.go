package generator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/naming"
	"github.com/reliant-labs/forge/internal/templates"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// E2EMethodInfo holds method metadata for E2E test template rendering:
// the RPC and its messages as the proto spells them. GenerateE2ETests
// re-cases them to the Go identifiers the generators declare.
type E2EMethodInfo struct {
	Name       string
	InputType  string
	OutputType string
}

// E2ETemplateData holds all data needed to render E2E test templates.
type E2ETemplateData struct {
	Module         string
	ServiceName    string // display form, may contain hyphens
	ServicePackage string // Go/proto-package-safe form (snake_case)
	// ConnectServiceGoName is protoc-gen-connect-go's base for the service
	// (naming.ConnectServiceGoName): the client is <base>Client.
	ConnectServiceGoName string
	ProtoPackage         string
	ProjectName          string
	Port                 int
	Methods              []E2EMethodInfo
	FirstRequestType     string // Used to anchor the pb import in helpers
	// ServeArgs is the argv (after the binary) that runs this service. A
	// project composed by the graph (codegen.GraphComposition — every new
	// project) has no per-component subcommands: `server` runs the default
	// root, which serves every service. A legacy Components project gives
	// each service its own top-level subcommand, named by the same
	// derivation that generates the CLI tree (codegen.CmdServiceCommand), so
	// the harness and the CLI cannot disagree; a service whose name collides
	// with a built-in command gets none, and `server` runs it.
	ServeArgs []string
}

// e2eServeArgs returns the CLI argv that serves serviceName in the scaffolded
// binary at projectDir. See E2ETemplateData.ServeArgs.
func e2eServeArgs(projectDir, serviceName string) []string {
	if codegen.GraphComposition(projectDir) {
		return []string{"server"}
	}
	if cmd, _, ok := codegen.CmdServiceCommand(serviceName); ok {
		return []string{cmd}
	}
	return []string{"server"}
}

// GenerateE2ETests renders E2E test templates into e2e/<servicePackage>/ under projectDir.
// It does not overwrite existing files — only creates new ones.
func GenerateE2ETests(projectDir, serviceName, modulePath, projectName string, methods []E2EMethodInfo) error {
	// The proto template declares `service <PascalCase>Service`; the client
	// is spelled the way protoc-gen-connect-go spells it from that name.
	connectService := naming.ConnectServiceGoName(naming.ToPascalCase(serviceName) + "Service")
	servicePackage := strings.ReplaceAll(strings.ToLower(serviceName), "-", "_")

	destDir := filepath.Join(projectDir, "e2e", servicePackage)
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("create e2e directory: %w", err)
	}

	// The templates spell generated Go identifiers: the client's methods
	// and the pb request/response types.
	goMethods := make([]E2EMethodInfo, len(methods))
	for i, m := range methods {
		goMethods[i] = E2EMethodInfo{
			Name:       naming.GoCamelCase(m.Name),
			InputType:  naming.GoCamelCase(m.InputType),
			OutputType: naming.GoCamelCase(m.OutputType),
		}
	}

	data := E2ETemplateData{
		Module:               modulePath,
		ServiceName:          serviceName,
		ServicePackage:       servicePackage,
		ConnectServiceGoName: connectService,
		ProtoPackage:         servicePackage + "v1",
		ProjectName:          projectName,
		Port:                 0, // E2E uses freePort(); this is available as a template var
		Methods:              goMethods,
		FirstRequestType:     "",
		ServeArgs:            e2eServeArgs(projectDir, serviceName),
	}

	templateFiles := []struct {
		tmplName string // relative to test/ dir
		destName string // filename in e2e/<service>/
	}{
		{"e2e/main_test.go.tmpl", "main_test.go"},
		{"e2e/helpers_test.go.tmpl", "helpers_test.go"},
		{"e2e/service_test.go.tmpl", "service_test.go"},
		{"e2e/docker-compose.e2e.yml.tmpl", "docker-compose.e2e.yml"},
	}

	for _, tf := range templateFiles {
		destPath := filepath.Join(destDir, tf.destName)

		// Don't overwrite existing files
		if _, err := os.Stat(destPath); err == nil {
			fmt.Printf("  e2e: skipping %s (already exists)\n", tf.destName)
			continue
		}

		content, err := templates.TestTemplates().Render(tf.tmplName, data)
		if err != nil {
			return fmt.Errorf("render e2e template %s: %w", tf.tmplName, err)
		}

		if err := atomicfile.WriteFile(destPath, content, 0644); err != nil {
			return fmt.Errorf("write e2e file %s: %w", tf.destName, err)
		}
	}

	return nil
}

// MethodsFromProtoStub returns placeholder E2E method info when no proto methods
// are available yet (e.g., when creating a brand-new service).
func MethodsFromProtoStub(serviceName string) []E2EMethodInfo {
	_ = serviceName
	// New services start with no RPCs; return empty slice.
	// Users add RPCs to the proto and re-generate.
	return nil
}

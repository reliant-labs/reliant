package generator

import (
	"fmt"
	"os"
	"path"
	"path/filepath"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/naming"
	"github.com/reliant-labs/forge/internal/templates"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// GenerateWebhookFiles generates all files for a webhook endpoint within a service:
//   - internal/handlers/<service>/webhook_<name>.go       (handler + signature verification + dedup)
//   - internal/handlers/<service>/webhook_<name>_test.go   (tests)
//
// Every symbol a handler needs is either its own (uniquely named per webhook)
// or comes from forge/pkg/middleware, so each webhook is one self-contained
// pair of files and adding a second webhook to a service cannot collide with
// the first.
func GenerateWebhookFiles(root, modulePath, serviceName, webhookName string) error {
	svcPkg := naming.ServicePackage(serviceName)
	svcDir := filepath.Join(root, "internal", "handlers", svcPkg)

	// Ensure the service directory exists.
	if _, err := os.Stat(svcDir); os.IsNotExist(err) {
		return fmt.Errorf("service directory %s does not exist", svcDir)
	}

	data := templates.WebhookTemplateData{
		Name:           webhookName,
		ServiceName:    serviceName,
		ServicePackage: svcPkg,
		Module:         modulePath,
		// The test builds the SERVICE's Deps, so it passes a Config exactly
		// when that Deps takes one, from the package it names.
		ConfigImport: codegen.DetectDepsConfigImport(svcDir),
	}
	if data.ConfigImport != "" && path.Base(data.ConfigImport) != "config" {
		data.ConfigImportAlias = "config"
	}

	// -- webhook handler --
	handlerContent, err := templates.WebhookTemplates().Render("webhooks.go.tmpl", data)
	if err != nil {
		return fmt.Errorf("render webhook handler: %w", err)
	}
	handlerPath := filepath.Join(svcDir, fmt.Sprintf("webhook_%s.go", webhookName))
	if err := atomicfile.WriteFile(handlerPath, handlerContent, 0644); err != nil {
		return fmt.Errorf("write webhook handler: %w", err)
	}

	// -- webhook test --
	testContent, err := templates.WebhookTemplates().Render("webhooks_test.go.tmpl", data)
	if err != nil {
		return fmt.Errorf("render webhook test: %w", err)
	}
	testPath := filepath.Join(svcDir, fmt.Sprintf("webhook_%s_test.go", webhookName))
	if err := atomicfile.WriteFile(testPath, testContent, 0644); err != nil {
		return fmt.Errorf("write webhook test: %w", err)
	}

	return nil
}

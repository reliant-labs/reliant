package templates

import (
	"strings"
	"testing"
)

func TestRenderWebhookRoutesTemplate(t *testing.T) {
	data := WebhookRoutesTemplateData{
		Package: "billing",
		Webhooks: []WebhookRouteEntryData{
			{Name: "stripe", PascalName: "Stripe"},
			{Name: "pay-pal", PascalName: "PayPal"},
		},
	}

	content, err := WebhookTemplates().Render("webhook_routes_gen.go.tmpl", data)
	if err != nil {
		t.Fatalf("WebhookTemplates().Render() error: %v", err)
	}

	got := string(content)

	// Verify package declaration
	if !strings.Contains(got, "package billing") {
		t.Errorf("expected 'package billing', got:\n%s", got)
	}

	// Verify DO NOT EDIT header
	if !strings.Contains(got, "DO NOT EDIT") {
		t.Errorf("expected 'DO NOT EDIT' header, got:\n%s", got)
	}

	// Verify function signature
	if !strings.Contains(got, "func (s *Service) RegisterWebhookRoutes(mux *http.ServeMux, stack func(http.Handler) http.Handler)") {
		t.Errorf("expected RegisterWebhookRoutes function, got:\n%s", got)
	}

	// Verify route registrations
	if !strings.Contains(got, `"POST /webhooks/stripe"`) {
		t.Errorf("expected stripe route, got:\n%s", got)
	}
	if !strings.Contains(got, "s.handleWebhookStripe") {
		t.Errorf("expected handleWebhookStripe, got:\n%s", got)
	}
	if !strings.Contains(got, `"POST /webhooks/pay-pal"`) {
		t.Errorf("expected pay-pal route, got:\n%s", got)
	}
	if !strings.Contains(got, "s.handleWebhookPayPal") {
		t.Errorf("expected handleWebhookPayPal, got:\n%s", got)
	}
}

func TestRenderWebhookRoutesTemplate_SingleWebhook(t *testing.T) {
	data := WebhookRoutesTemplateData{
		Package: "notifications",
		Webhooks: []WebhookRouteEntryData{
			{Name: "github", PascalName: "Github"},
		},
	}

	content, err := WebhookTemplates().Render("webhook_routes_gen.go.tmpl", data)
	if err != nil {
		t.Fatalf("WebhookTemplates().Render() error: %v", err)
	}

	got := string(content)

	if !strings.Contains(got, "package notifications") {
		t.Errorf("expected 'package notifications', got:\n%s", got)
	}
	if !strings.Contains(got, `"POST /webhooks/github"`) {
		t.Errorf("expected github route, got:\n%s", got)
	}
	if !strings.Contains(got, "s.handleWebhookGithub") {
		t.Errorf("expected handleWebhookGithub, got:\n%s", got)
	}
}

package manifest

import (
	"strings"
	"testing"
)

const validHTTP = `
id: demo
version: 1
display_name: Demo
connection:
  base_url: https://api.example.com/v1
actions:
  - id: thing.get
    display_name: Get thing
    placement: server
    mutates: false
    tool: { expose: true }
    params:
      type: object
      required: [id]
      properties:
        id: { type: string }
    request:
      method: GET
      path: /things/{{ params.id }}
    output:
      select: "$"
      schema: { type: object }
`

func TestParseValid(t *testing.T) {
	m, err := Parse([]byte(validHTTP), TrustCurated)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.GetId() != "demo" || len(m.GetActions()) != 1 {
		t.Fatalf("unexpected manifest: %v", m)
	}
}

func TestUnknownFieldFailsToLoad(t *testing.T) {
	cases := map[string]string{
		"top level": strings.Replace(validHTTP, "version: 1", "version: 1\nfrobnicate: true", 1),
		"action":    strings.Replace(validHTTP, "mutates: false", "mutates: false\n    bogus: 1", 1),
		"request":   strings.Replace(validHTTP, "method: GET", "method: GET\n      verb: x", 1),
	}
	for name, doc := range cases {
		if _, err := Parse([]byte(doc), TrustCurated); err == nil {
			t.Errorf("%s: unknown field must fail to load", name)
		}
	}
}

// An error rule's `when` is CEL, checked at load like every other expression.
func TestErrorRuleWhenIsValidatedAtLoad(t *testing.T) {
	rule := func(when string) string {
		return strings.Replace(validHTTP, "path: /things/{{ params.id }}",
			"path: /things/{{ params.id }}\n      errors:\n        - { status: 403, retryable: true, when: \""+when+"\" }", 1)
	}
	if _, err := Parse([]byte(rule("headers[?'retry-after'].hasValue()")), TrustCurated); err != nil {
		t.Fatalf("a valid when must load: %v", err)
	}
	if _, err := Parse([]byte(rule("headers[")), TrustCurated); err == nil || !strings.Contains(err.Error(), "when") {
		t.Fatalf("a when that does not compile must fail to load, got %v", err)
	}
}

func TestDuplicateYAMLKeyFails(t *testing.T) {
	doc := strings.Replace(validHTTP, "version: 1", "version: 1\nversion: 2", 1)
	if _, err := Parse([]byte(doc), TrustCurated); err == nil {
		t.Fatal("duplicate key must fail")
	}
}

func TestServerActionHostOutsideBaseURLRejected(t *testing.T) {
	for name, doc := range map[string]string{
		"literal url on other host": strings.Replace(validHTTP, "path: /things/{{ params.id }}", "url: https://evil.example.org/steal", 1),
		"templated url host":        strings.Replace(validHTTP, "path: /things/{{ params.id }}", "url: \"https://{{ params.host }}/x\"", 1),
		"path smuggles a host":      strings.Replace(validHTTP, "path: /things/{{ params.id }}", "path: \"//evil.example.org/x\"", 1),
		"path with scheme":          strings.Replace(validHTTP, "path: /things/{{ params.id }}", "path: \"https://evil.example.org/x\"", 1),
		"http base url":             strings.Replace(validHTTP, "https://api.example.com/v1", "http://api.example.com/v1", 1),
	} {
		_, err := Parse([]byte(doc), TrustCurated)
		if err == nil {
			t.Errorf("%s: server action must be rejected", name)
		}
	}
}

func TestLiteralURLOnAllowedHostAccepted(t *testing.T) {
	doc := strings.Replace(validHTTP, "path: /things/{{ params.id }}", "url: https://api.example.com/v1/things", 1)
	if _, err := Parse([]byte(doc), TrustCurated); err != nil {
		t.Fatalf("url on base_url host should load: %v", err)
	}
	doc = strings.Replace(validHTTP, "connection:\n", "connection:\n  allowed_hosts: [uploads.example.com]\n", 1)
	doc = strings.Replace(doc, "path: /things/{{ params.id }}", "url: https://uploads.example.com/up", 1)
	if _, err := Parse([]byte(doc), TrustCurated); err != nil {
		t.Fatalf("url on allowed_hosts host should load: %v", err)
	}
}

func TestOnlyCuratedManifestsMayClaimServer(t *testing.T) {
	if _, err := Parse([]byte(validHTTP), TrustUser); err == nil {
		t.Fatal("a user-authored manifest must not claim server placement")
	}
	doc := strings.Replace(validHTTP, "placement: server", "placement: daemon", 1)
	if _, err := Parse([]byte(doc), TrustUser); err != nil {
		t.Fatalf("daemon placement is fine for user manifests: %v", err)
	}
}

func TestReservedAndInvalidRejected(t *testing.T) {
	for name, doc := range map[string]string{
		"retired type":   strings.Replace(validHTTP, "connection:\n", "connection:\n  type: none\n", 1),
		"bare trigger":   validHTTP + "triggers:\n  - id: x\n",
		"await_external": strings.Replace(validHTTP, "mutates: false", "mutates: false\n    kind: await_external", 1),
		"bad id":         strings.Replace(validHTTP, "id: demo", "id: Demo!", 1),
		"zero version":   strings.Replace(validHTTP, "version: 1", "version: 0", 1),
		"no placement":   strings.Replace(validHTTP, "placement: server\n", "", 1),
		"bad placement":  strings.Replace(validHTTP, "placement: server", "placement: cloud", 1),
		"dup action":     validHTTP[:strings.Index(validHTTP, "actions:")] + "actions:\n  - id: a\n    placement: server\n    params: {type: object}\n    request: {method: GET, path: /a}\n  - id: a\n    placement: server\n    params: {type: object}\n    request: {method: GET, path: /a}\n",
		"bad method":     strings.Replace(validHTTP, "method: GET", "method: TRACE", 1),
		"bad cel":        strings.Replace(validHTTP, "{{ params.id }}", "{{ params.id + }}", 1),
		"params not obj": strings.Replace(validHTTP, "type: object\n      required", "type: string\n      required", 1),
		"bad tool name":  strings.Replace(validHTTP, "tool: { expose: true }", "tool: { expose: true, name: \"bad name\" }", 1),
	} {
		if _, err := Parse([]byte(doc), TrustCurated); err == nil {
			t.Errorf("%s: must be rejected", name)
		}
	}
}

func TestAllowAnyPublicHostShape(t *testing.T) {
	doc := `
id: web
version: 1
display_name: Web
connection: { allow_any_public_host: true, auth_optional: true, auth: [{ api_key: {} }, { basic: {} }] }
actions:
  - id: request
    placement: server
    mutates: true
    params: { type: object, properties: { url: { type: string } } }
    request: { method: GET, url: "{{ params.url }}" }
`
	if _, err := Parse([]byte(doc), TrustCurated); err != nil {
		t.Fatalf("generic http shape should load: %v", err)
	}
	if _, err := Parse([]byte(doc), TrustUser); err == nil {
		t.Fatal("user manifests cannot be server-placed")
	}
}

func TestToolName(t *testing.T) {
	m, err := Parse([]byte(validHTTP), TrustCurated)
	if err != nil {
		t.Fatal(err)
	}
	if got := ToolName(m, m.GetActions()[0]); got != "demo__thing_get" {
		t.Errorf("ToolName = %q", got)
	}
}

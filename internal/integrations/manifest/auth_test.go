package manifest

import (
	"strings"
	"testing"
)

// authManifest exercises every auth kind, connection params, a probe, an
// executor action and the search-index fields in one document.
const authManifest = `
id: acme
version: 1
display_name: Acme
description: The Acme API.
category: crm
keywords: [acme, crm, "customer records"]
connection:
  base_url: "https://{{ connection.params.tenant }}.acme.example/api/{{ connection.params.region }}"
  default_headers: { Accept: application/json }
  connection_params:
    - name: tenant
      display_name: Tenant
      pattern: "[a-z0-9-]{1,40}"
    - name: region
      default_value: eu
  auth:
    - delegated: { broker: acme-broker }
    - oauth2:
        authorize_url: "https://{{ connection.params.tenant }}.acme.example/oauth/authorize"
        token_url: "https://{{ connection.params.tenant }}.acme.example/oauth/token"
        scopes: [read, write]
        scope_separator: ","
        pkce: S256
        authorize_params: { prompt: consent }
        revoke:
          method: POST
          url: "https://{{ connection.params.tenant }}.acme.example/oauth/revoke"
          client_auth: basic
          token_in: form
    - api_key: { in: query, name: api_key, label: API key }
    - basic: { username_param: tenant, password_label: Secret }
  probe:
    path: /me
    ok: "response.ok == true"
    external_id: response.user.id
    label: response.user.name
actions:
  - id: record.get
    summary: Get one record.
    keywords: [lookup]
    placement: server
    params: { type: object, required: [id], properties: { id: { type: string } } }
    request: { method: GET, path: "/records/{{ params.id }}" }
    output: { schema: { type: object, properties: { id: { type: string } } } }
  - id: record.sign
    summary: Sign a record (a Go executor).
    placement: server
    executor: go:acme.sign
    params: { type: object, properties: { id: { type: string } } }
    output: { schema: { type: object } }
`

func TestAuthManifestLoads(t *testing.T) {
	m, err := Parse([]byte(authManifest), TrustCurated)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	conn := m.GetConnection()
	if got := len(conn.GetAuth()); got != 4 {
		t.Fatalf("auth methods = %d, want 4", got)
	}
	var kinds []string
	for _, a := range conn.GetAuth() {
		kinds = append(kinds, AuthKind(a))
	}
	if strings.Join(kinds, ",") != "delegated,oauth2,api_key,basic" {
		t.Errorf("kinds in manifest order = %v", kinds)
	}
	if oa, ok := Method(conn, AuthOAuth2); !ok || oa.GetOauth2().GetAuthorizeParams()["prompt"] != "consent" {
		t.Errorf("oauth2 method lookup failed: %v", oa)
	}
	if name, ok := ExecutorName(m.GetActions()[1]); !ok || name != "acme.sign" {
		t.Errorf("ExecutorName = %q, %v", name, ok)
	}
	if _, ok := ExecutorName(m.GetActions()[0]); ok {
		t.Error("a declarative action has no executor")
	}
	if m.GetKeywords()[2] != "customer records" || m.GetActions()[0].GetSummary() != "Get one record." {
		t.Error("search-index fields did not load")
	}
}

// Every action's params and output render as a JSON Schema, which is what the
// builder's forms and the catalog's GetCatalogEntry are generated from.
func TestActionSchemas(t *testing.T) {
	m, err := Parse([]byte(authManifest), TrustCurated)
	if err != nil {
		t.Fatal(err)
	}
	params, output := ActionSchemas(m.GetActions()[0])
	if params["type"] != "object" || params["required"].([]any)[0] != "id" {
		t.Errorf("params schema = %v", params)
	}
	if output["properties"].(map[string]any)["id"] == nil {
		t.Errorf("output schema = %v", output)
	}
	// An action that declares neither still yields renderable object schemas.
	bare, err := Parse([]byte(strings.Replace(validHTTP, "    output:\n      select: \"$\"\n      schema: { type: object }\n", "", 1)), TrustCurated)
	if err != nil {
		t.Fatal(err)
	}
	_, output = ActionSchemas(bare.GetActions()[0])
	if output["type"] != "object" {
		t.Errorf("default output schema = %v", output)
	}
}

// Each invalid manifest fails with an error naming the offending field.
func TestAuthValidationErrors(t *testing.T) {
	sub := func(old, new string) string {
		if !strings.Contains(authManifest, old) {
			t.Fatalf("fixture does not contain %q", old)
		}
		return strings.Replace(authManifest, old, new, 1)
	}
	cases := map[string]struct{ doc, want string }{
		"two methods, one arm each": {sub("- delegated: { broker: acme-broker }", "- { delegated: { broker: acme-broker }, basic: {} }"), "oneof"},
		"duplicate kind":            {sub("- basic: { username_param: tenant, password_label: Secret }", "- api_key: { in: header, name: X-Key }"), "connection.auth[3]: api_key is declared twice"},
		"empty method":              {sub("- delegated: { broker: acme-broker }", "- {}"), "connection.auth[0]: exactly one of oauth2, api_key, basic or delegated"},
		"http token url":            {sub(`token_url: "https://{{ connection.params.tenant }}.acme.example/oauth/token"`, "token_url: http://acme.example/oauth/token"), "connection.auth[1].oauth2.token_url"},
		"undeclared param in url":   {sub(`authorize_url: "https://{{ connection.params.tenant }}`, `authorize_url: "https://{{ connection.params.shop }}`), `unknown connection param "shop"`},
		"param is not whole label":  {sub(`base_url: "https://{{ connection.params.tenant }}.acme`, `base_url: "https://x{{ connection.params.tenant }}.acme`), "connection.base_url: a templated host label must be exactly"},
		"param picks the domain":    {sub(`base_url: "https://{{ connection.params.tenant }}.acme.example`, `base_url: "https://{{ connection.params.tenant }}.example`), "at least two literal labels"},
		"cel in a url":              {sub(`/api/{{ connection.params.region }}`, `/api/{{ params.region }}`), `connection.base_url: only {{ connection.params.<name> }}`},
		"reserved authorize param":  {sub("authorize_params: { prompt: consent }", "authorize_params: { redirect_uri: https://evil.example }"), `authorize_params: "redirect_uri" is set by the flow`},
		"bad pkce":                  {sub("pkce: S256", "pkce: S512"), `pkce "S512" must be S256 or plain`},
		"bad scope separator":       {sub(`scope_separator: ","`, `scope_separator: ";"`), "scope_separator"},
		"scope with space":          {sub("scopes: [read, write]", `scopes: ["read write"]`), "scopes[0]"},
		"api key in body":           {sub("in: query, name: api_key", "in: body, name: api_key"), `connection.auth[2].api_key.in "body" must be header or query`},
		"api key no name":           {sub("in: query, name: api_key", "in: query"), "connection.auth[2].api_key: in and name are set together"},
		"api key host header":       {sub("in: query, name: api_key", "in: header, name: Host"), `header "Host" cannot carry a credential`},
		"api key open placement":    {sub("in: query, name: api_key, label: API key", "label: API key"), "only an allow_any_public_host integration"},
		"api key prefix newline":    {sub("in: query, name: api_key", "in: header, name: X-Key, prefix: \"a\\nb\""), "prefix"},
		"basic unknown param":       {sub("username_param: tenant", "username_param: account"), `connection.auth[3].basic.username_param "account" is not a connection param`},
		"bad broker id":             {sub("broker: acme-broker", "broker: Acme Broker"), "connection.auth[0].delegated.broker"},
		"oauth2 without probe":      {sub("  probe:\n    path: /me\n    ok: \"response.ok == true\"\n    external_id: response.user.id\n    label: response.user.name\n", ""), "connection.probe is required with oauth2"},
		"probe without id":          {sub("    external_id: response.user.id\n", ""), "connection.probe.external_id is required"},
		"probe bad cel":             {sub("label: response.user.name", "label: response.user.name +"), "connection.probe.label"},
		"probe off host":            {sub("path: /me", "url: https://evil.example/me"), "connection.probe.url"},
		"bad param name":            {sub("- name: region", "- name: Region"), `connection.connection_params[1].name "Region"`},
		"dup param":                 {sub("- name: region", "- name: tenant"), `connection.connection_params[1]: "tenant" is declared twice`},
		"secret-looking param":      {sub("- name: region", "- name: api_token"), `connection.connection_params[1]: "api_token" reads like a credential`},
		"bad param pattern":         {sub(`pattern: "[a-z0-9-]{1,40}"`, `pattern: "[a-z"`), "connection.connection_params[0].pattern"},
		"bad revoke token_in":       {sub("token_in: form", "token_in: cookie"), "revoke.token_in"},
		"bad revoke client_auth":    {sub("client_auth: basic", "client_auth: jwt"), "revoke.client_auth"},
		"executor and request":      {sub("executor: go:acme.sign\n", "executor: go:acme.sign\n    request: { method: GET, path: /x }\n"), `action "record.sign": executor and request are mutually exclusive`},
		"executor bad name":         {sub("executor: go:acme.sign", "executor: lambda:acme"), `action "record.sign": executor "lambda:acme" must be go:<name>`},
		"executor without params":   {sub("    params: { type: object, properties: { id: { type: string } } }\n    output: { schema: { type: object } }\n", "    output: { schema: { type: object } }\n"), `action "record.sign": params are required with an executor`},
		"executor on daemon":        {sub("placement: server\n    executor", "placement: daemon\n    executor"), `action "record.sign": an executor runs on the server`},
		"no request no executor":    {sub("    request: { method: GET, path: \"/records/{{ params.id }}\" }\n", ""), `action "record.get": request is required`},
		"multi-line summary":        {sub("summary: Get one record.", "summary: \"Get one\\nrecord.\""), `action "record.get": summary must be one line`},
		"bad keyword":               {sub("keywords: [lookup]", "keywords: [\"Look Up!\"]"), `action "record.get": keywords[0]`},
		"bad manifest keyword":      {sub(`keywords: [acme, crm, "customer records"]`, `keywords: [""]`), "keywords[0]"},
		"bad output schema":         {sub("output: { schema: { type: object, properties: { id: { type: string } } } }", "output: { schema: { type: 7 } }"), `action "record.get": output.schema`},
		"any host with oauth":       {strings.Replace(sub(`base_url: "https://{{ connection.params.tenant }}.acme.example/api/{{ connection.params.region }}"`, "allow_any_public_host: true"), "    request: { method: GET, path: \"/records/{{ params.id }}\" }", "    request: { method: GET, url: \"{{ params.url }}\" }", 1), "allow_any_public_host"},
	}
	for name, tc := range cases {
		_, err := Parse([]byte(tc.doc), TrustCurated)
		if err == nil {
			t.Errorf("%s: must be rejected", name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q does not mention %q", name, err, tc.want)
		}
	}
}

func TestAuthOptionalNeedsAMethod(t *testing.T) {
	doc := strings.Replace(validHTTP, "base_url: https://api.example.com/v1", "base_url: https://api.example.com/v1\n  auth_optional: true", 1)
	if _, err := Parse([]byte(doc), TrustCurated); err == nil || !strings.Contains(err.Error(), "auth_optional needs at least one connection.auth method") {
		t.Errorf("auth_optional without a method: %v", err)
	}
}

func TestExpandURL(t *testing.T) {
	u, err := ExpandURL("https://{{ connection.params.tenant }}.acme.example/api/{{ connection.params.region }}/x",
		map[string]string{"connection.params.tenant": "Acme-1", "connection.params.region": "eu west"})
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "acme-1.acme.example" || u.EscapedPath() != "/api/eu%20west/x" {
		t.Errorf("expanded to %s (path %s)", u.Host, u.EscapedPath())
	}
	for name, value := range map[string]string{
		"empty":            "",
		"dot smuggles":     "evil.com",
		"slash":            "a/b",
		"at sign":          "user@evil",
		"leading hyphen":   "-x",
		"too long":         strings.Repeat("a", 64),
		"percent encoding": "a%2e",
	} {
		if _, err := ExpandURL("https://{{ connection.params.tenant }}.acme.example/", map[string]string{"connection.params.tenant": value}); err == nil {
			t.Errorf("%s: host label %q must be refused", name, value)
		}
	}
	if _, err := ExpandURL("https://{{ connection.params.tenant }}.acme.example/", nil); err == nil {
		t.Error("a missing value must be an error, not an empty label")
	}
}

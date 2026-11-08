package codegen

import (
	"reflect"
	"strings"
	"testing"
)

// A managed deployment's frontend: self-hosted on its defaults, and in
// HOSTING=cloud unable to run without its endpoints.
func cloudFrontendConfig() FrontendConfig {
	cloud := []ConfigCondition{{Field: "hosting", Equals: "cloud"}}
	return FrontendConfig{Frontend: "web", MessageName: "WebConfig", Fields: []ConfigField{
		{Name: "hosting", EnvVar: "HOSTING", ProtoType: "string", AllowedValues: []string{"cloud"}},
		{Name: "api_url", EnvVar: "API_URL", ProtoType: "string", RequiredWhen: cloud},
		{Name: "supabase_url", EnvVar: "SUPABASE_URL", ProtoType: "string", DefaultValue: "http://127.0.0.1:54321", RequiredWhen: cloud},
		{Name: "region", EnvVar: "REGION", ProtoType: "string", Required: true},
		{Name: "theme", EnvVar: "THEME", ProtoType: "string", Required: true, DefaultValue: "light"},
	}}
}

func TestGenerateFrontendConfigSchema_CarriesRequiredness(t *testing.T) {
	body, err := GenerateFrontendConfigSchema(cloudFrontendConfig())
	if err != nil {
		t.Fatal(err)
	}
	doc, ok, err := ParseFrontendConfigSchema([]byte(body))
	if err != nil || !ok {
		t.Fatalf("round trip: ok=%v err=%v", ok, err)
	}
	keys := map[string]FrontendConfigSchemaKey{}
	for _, k := range doc.Keys {
		keys[k.Name] = k
	}
	cloud := []RequiredWhenKey{{Key: "HOSTING", Equals: "cloud"}}
	if k := keys["API_URL"]; !reflect.DeepEqual(k.RequiredWhen, cloud) || k.Required || k.Default != "" {
		t.Errorf("API_URL = %+v", k)
	}
	if k := keys["SUPABASE_URL"]; !reflect.DeepEqual(k.RequiredWhen, cloud) || k.Default != "http://127.0.0.1:54321" {
		t.Errorf("SUPABASE_URL = %+v; a mode-dependent key records its default (which does not satisfy it)", k)
	}
	if k := keys["REGION"]; !k.Required {
		t.Errorf("REGION = %+v; required with no default", k)
	}
	if k := keys["THEME"]; k.Required {
		t.Errorf("THEME = %+v; a required field with a default is always satisfied", k)
	}
	if strings.Contains(body, `"required": false`) || strings.Contains(body, `"default": ""`) {
		t.Errorf("unset required-ness must be omitted, keeping old schemas byte-identical:\n%s", body)
	}
	// A config with no required-ness renders exactly what forge did before.
	plain, _ := GenerateFrontendConfigSchema(FrontendConfig{Frontend: "web", Fields: []ConfigField{{Name: "a", EnvVar: "A", ProtoType: "string"}}})
	if strings.Contains(plain, "required") || strings.Contains(plain, "default") {
		t.Errorf("plain schema grew required-ness fields:\n%s", plain)
	}
}

func TestFrontendConfigSchema_MissingRequired(t *testing.T) {
	body, _ := GenerateFrontendConfigSchema(cloudFrontendConfig())
	schema, _, _ := ParseFrontendConfigSchema([]byte(body))
	judge := func(doc map[string]string) []string {
		var out []string
		for _, m := range schema.MissingRequired(
			func(k string) bool { return strings.TrimSpace(doc[k]) != "" },
			func(k string) (string, bool) { v, ok := doc[k]; return v, ok },
		) {
			out = append(out, m.Reason())
		}
		return out
	}
	for name, tc := range map[string]struct {
		doc  map[string]string
		want []string
	}{
		"self-hosted": {map[string]string{"REGION": "eu"}, nil},
		"cloud, nothing set": {map[string]string{"REGION": "eu", "HOSTING": "cloud"}, []string{
			`API_URL (required when HOSTING is "cloud")`, `SUPABASE_URL (required when HOSTING is "cloud")`,
		}},
		"cloud, supabase at its default": {map[string]string{"REGION": "eu", "HOSTING": "cloud", "API_URL": "https://a", "SUPABASE_URL": "http://127.0.0.1:54321"}, []string{
			`SUPABASE_URL (required when HOSTING is "cloud")`,
		}},
		"cloud, complete":  {map[string]string{"REGION": "eu", "HOSTING": "cloud", "API_URL": "https://a", "SUPABASE_URL": "https://sb"}, nil},
		"region empty":     {map[string]string{"REGION": " "}, []string{"REGION (required)"}},
		"api_url is blank": {map[string]string{"REGION": "eu", "HOSTING": "cloud", "API_URL": "", "SUPABASE_URL": "https://sb"}, []string{`API_URL (required when HOSTING is "cloud")`}},
	} {
		if got := judge(tc.doc); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: missing = %q, want %q", name, got, tc.want)
		}
	}
}

func TestFrontendConfigSchema_UnstatedSelectors(t *testing.T) {
	body, _ := GenerateFrontendConfigSchema(cloudFrontendConfig())
	schema, _, _ := ParseFrontendConfigSchema([]byte(body))
	judge := func(doc map[string]string) []string {
		var out []string
		for _, m := range schema.UnstatedSelectors(func(k string) bool { return strings.TrimSpace(doc[k]) != "" }) {
			out = append(out, m.Reason())
		}
		return out
	}
	unstated := []string{`HOSTING (unstated: it selects the mode that decides whether API_URL, SUPABASE_URL are required — ` +
		`state the mode this environment serves, e.g. "cloud")`}
	for name, tc := range map[string]struct {
		doc  map[string]string
		want []string
	}{
		"no mode stated": {map[string]string{"REGION": "eu"}, unstated},
		"mode blank":     {map[string]string{"REGION": "eu", "HOSTING": ""}, unstated},
		"cloud":          {map[string]string{"HOSTING": "cloud"}, nil},
		// Any stated value names a mode; whether it is an allowed one is the
		// bundle's zod enum's to say.
		"another mode": {map[string]string{"HOSTING": "self-hosted"}, nil},
	} {
		if got := judge(tc.doc); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: unstated = %q, want %q", name, got, tc.want)
		}
	}

	plain, _ := GenerateFrontendConfigSchema(FrontendConfig{Frontend: "web", Fields: []ConfigField{{Name: "a", EnvVar: "A", ProtoType: "string"}}})
	ps, _, _ := ParseFrontendConfigSchema([]byte(plain))
	if got := ps.UnstatedSelectors(func(string) bool { return false }); len(got) != 0 {
		t.Errorf("a schema without required_when has no selectors, got %+v", got)
	}
}

func TestGenerateFrontendConfigTS_EnforcesRequiredWhenOnTheRawDocument(t *testing.T) {
	out, err := GenerateFrontendConfigTS(cloudFrontendConfig(), "demo", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"export function missingRequiredConfig(raw: Record<string, unknown> = rawConfig()): MissingConfigKey[] {",
		`{ key: "API_URL", when: [{ key: "HOSTING", equals: "cloud" }] },`,
		`{ key: "SUPABASE_URL", default: "http://127.0.0.1:54321", when: [{ key: "HOSTING", equals: "cloud" }] },`,
		"const missing = missingRequiredConfig(raw);",
		"if (!parsed.success || missing.length > 0) {",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("config_gen.ts lacks %q:\n%s", want, out)
		}
	}
	if strings.Index(out, "function rawConfig()") > strings.Index(out, "export function missingRequiredConfig") {
		t.Error("missingRequiredConfig's default argument reads rawConfig, which must be declared first")
	}

	plain, err := GenerateFrontendConfigTS(FrontendConfig{Frontend: "web", Fields: []ConfigField{{Name: "a", EnvVar: "A", ProtoType: "string"}}}, "demo", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain, "missingRequiredConfig") {
		t.Error("a config without required_when must keep its module unchanged")
	}
}

func TestRenderFrontendConfigSchema_ChecksRequiredWhen(t *testing.T) {
	out, err := renderFrontendConfigSchema(cloudFrontendConfig(), "demo", "WebConfig")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"\n    check:\n",
		`hosting != "cloud" or api_url, "API_URL is required when HOSTING is \"cloud\""`,
		`hosting != "cloud" or (supabase_url and supabase_url != "http://127.0.0.1:54321"), `,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("KCL schema lacks %q:\n%s", want, out)
		}
	}
}

func TestValidateFrontendConfigs_RefusesMisusedRequiredWhen(t *testing.T) {
	when := func(field, equals string) []ConfigCondition { return []ConfigCondition{{Field: field, Equals: equals}} }
	for name, tc := range map[string]struct {
		messages []ConfigMessage
		want     string
	}{
		"on a binary config": {[]ConfigMessage{{Name: "ServerConfig", Binary: "server", Fields: []ConfigField{
			{Name: "mode", ProtoType: "string"},
			{Name: "api_url", ProtoType: "string", RequiredWhen: when("mode", "cloud")},
		}}}, "ServerConfig.api_url: required_when is for a (forge.v1.frontend_config) message"},
		"with required": {[]ConfigMessage{{Name: "WebConfig", Frontend: "web", Fields: []ConfigField{
			{Name: "mode", ProtoType: "string"},
			{Name: "api_url", ProtoType: "string", Required: true, RequiredWhen: when("mode", "cloud")},
		}}}, "sets both `required: true` and required_when"},
		"unknown field": {[]ConfigMessage{{Name: "WebConfig", Frontend: "web", Fields: []ConfigField{
			{Name: "api_url", ProtoType: "string", RequiredWhen: when("mood", "cloud")},
		}}}, `required_when names "mood", which frontend "web"'s config does not declare`},
		"itself": {[]ConfigMessage{{Name: "WebConfig", Frontend: "web", Fields: []ConfigField{
			{Name: "api_url", ProtoType: "string", RequiredWhen: when("api_url", "x")},
		}}}, "required_when names the field itself"},
		"non-string condition": {[]ConfigMessage{{Name: "WebConfig", Frontend: "web", Fields: []ConfigField{
			{Name: "on", ProtoType: "bool"},
			{Name: "api_url", ProtoType: "string", RequiredWhen: when("on", "true")},
		}}}, `required_when names "on", a bool field`},
		"no equals": {[]ConfigMessage{{Name: "WebConfig", Frontend: "web", Fields: []ConfigField{
			{Name: "mode", ProtoType: "string"},
			{Name: "api_url", ProtoType: "string", RequiredWhen: when("mode", "")},
		}}}, "has no `equals`"},
		"outside allowed_values": {[]ConfigMessage{{Name: "WebConfig", Frontend: "web", Fields: []ConfigField{
			{Name: "mode", ProtoType: "string", AllowedValues: []string{"cloud"}},
			{Name: "api_url", ProtoType: "string", RequiredWhen: when("mode", "clod")},
		}}}, `equals "clod", which is outside its allowed_values [cloud]`},
	} {
		err := ValidateFrontendConfigs(tc.messages)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to contain %q", name, err, tc.want)
		}
	}

	// Accepted: on the frontend config, and on a block composed only onto
	// frontend configs, naming a field of the composing message.
	ok := []ConfigMessage{
		{Name: "WebConfig", Frontend: "web", Fields: []ConfigField{
			{Name: "mode", ProtoType: "string", AllowedValues: []string{"cloud"}},
			{Name: "api_url", ProtoType: "string", RequiredWhen: when("mode", "cloud")},
			{Name: "auth", ProtoType: "message", MessageType: "AuthBlock"},
		}},
		{Name: "AuthBlock", Fields: []ConfigField{
			{Name: "issuer", ProtoType: "string", RequiredWhen: when("mode", "cloud")},
		}},
	}
	if err := ValidateFrontendConfigs(ok); err != nil {
		t.Errorf("valid required_when refused: %v", err)
	}
}

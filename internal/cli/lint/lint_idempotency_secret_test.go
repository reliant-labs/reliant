package lint

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
)

func strField(name string) codegen.SchemaFieldDef {
	return codegen.SchemaFieldDef{Name: name, Kind: "string"}
}

func msgField(name, typ string) codegen.SchemaFieldDef {
	return codegen.SchemaFieldDef{Name: name, Kind: "message", TypeName: typ}
}

func TestSecretFieldSignal(t *testing.T) {
	for _, tc := range []struct {
		field codegen.SchemaFieldDef
		want  string // a substring of the signal, "" for none
	}{
		{field: strField("access_token"), want: "(token)"},
		{field: strField("refresh_token"), want: "(token)"},
		{field: strField("accessToken"), want: "(token)"},
		{field: strField("token"), want: "(token)"},
		{field: strField("client_secret"), want: "(secret)"},
		{field: strField("secret_value"), want: "(secret)"},
		{field: strField("password"), want: "(password)"},
		{field: strField("credentials"), want: "(credentials)"},
		{field: strField("nonce"), want: "(nonce)"},
		{field: strField("challenge"), want: "(challenge)"},
		{field: strField("code_challenge"), want: "(challenge)"},
		{field: strField("otp_code"), want: "(otp)"},
		{field: strField("one_time_code"), want: "one-time"},
		{field: strField("api_key"), want: "(api_key)"},
		{field: strField("private_key"), want: "(private_key)"},
		{field: strField("oauth_state"), want: "OAuth state"},
		{field: strField("state_token"), want: "(token)"},
		{field: strField("presigned_url"), want: "presigned"},
		{field: strField("presigned_put"), want: "presigned"},
		{field: strField("signed_url"), want: "minted"},
		{field: strField("upload_url"), want: "minted"},
		{field: strField("download_url"), want: "minted"},
		{field: strField("management_url"), want: "minted"},
		{field: strField("checkout_url"), want: "minted"},
		{field: strField("invite_url"), want: "minted"},
		{field: strField("invite_link"), want: "minted"},
		{field: strField("claim_url"), want: "minted"},
		{field: strField("billing_portal_url"), want: "minted"},
		{field: strField("export_url"), want: "minted"},
		{field: strField("authorize_url"), want: "minted"},
		{field: strField("reset_url"), want: "minted"},
		{field: strField("success_url")},
		{field: strField("redirect_uri")},
		{field: codegen.SchemaFieldDef{Name: "tokens", Kind: "string", Repeated: true}, want: "(tokens)"},
		{field: codegen.SchemaFieldDef{Name: "secrets", Kind: "map", MapKeyKind: "string", MapValueKind: "string"}, want: "(secrets)"},
		{field: codegen.SchemaFieldDef{Name: "session_key", Kind: "bytes"}},
		{field: codegen.SchemaFieldDef{Name: "blob", Kind: "bytes", Redacted: true}, want: "debug_redact"},
		{field: codegen.SchemaFieldDef{Name: "profile", Kind: "message", TypeName: "x.v1.Profile", Redacted: true}, want: "debug_redact"},
		// Not secrets.
		{field: strField("state")},
		{field: strField("job_state")},
		{field: strField("page_token")},
		{field: strField("next_page_token")},
		{field: strField("prev_page_token")},
		{field: strField("continuation_token")},
		{field: strField("cursor")},
		{field: strField("token_id")},
		{field: strField("token_prefix")},
		{field: strField("token_type")},
		{field: strField("secret_name")},
		{field: strField("password_hash")},
		{field: strField("challenge_id")},
		{field: strField("idempotency_key")},
		{field: strField("object_key")},
		{field: strField("url")},
		{field: strField("avatar_url")},
		{field: strField("error_code")},
		{field: strField("resume_token")},
		{field: strField("sync_token")},
		{field: strField("masked_api_key")},
		{field: strField("hashed_token")},
		{field: strField("redacted_password")},
		{field: codegen.SchemaFieldDef{Name: "input_tokens", Kind: "int64"}},
		{field: codegen.SchemaFieldDef{Name: "max_tokens", Kind: "int32"}},
		{field: codegen.SchemaFieldDef{Name: "password_set", Kind: "bool"}},
		{field: msgField("credentials", "x.v1.Credentials")}, // judged by its fields, not its name
		// forge's read path never packs a forge:secret field.
		{field: codegen.SchemaFieldDef{Name: "api_token", Kind: "string", Secret: true}},
		{field: codegen.SchemaFieldDef{Name: "api_token", Kind: "string", Secret: true, Redacted: true}},
	} {
		t.Run(tc.field.Name, func(t *testing.T) {
			got := secretFieldSignal(tc.field)
			if tc.want == "" {
				if got != "" {
					t.Fatalf("want no signal, got %q", got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("signal = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

func TestSecretResponseField(t *testing.T) {
	schemas := map[string][]codegen.SchemaFieldDef{
		// A token two messages down.
		"x.v1.GetSessionResponse": {strField("id"), msgField("session", "x.v1.Session")},
		"x.v1.Session":            {strField("user_id"), msgField("credentials", "x.v1.Credentials")},
		"x.v1.Credentials":        {strField("token_type"), strField("access_token")},
		// The shallowest hit wins over a deeper one listed first.
		"x.v1.Shallow": {msgField("nested", "x.v1.Credentials"), strField("upload_url")},
		// A map's message value is walked.
		"x.v1.ByName": {{Name: "by_name", Kind: "map", MapKeyKind: "string", MapValueKind: "message", MapValueTypeName: "x.v1.Credentials"}},
		// Cycles terminate: a self-referential tree and a mutual pair.
		"x.v1.Tree": {strField("name"), {Name: "children", Kind: "message", TypeName: "x.v1.Tree", Repeated: true}},
		"x.v1.A":    {msgField("b", "x.v1.B")},
		"x.v1.B":    {msgField("a", "x.v1.A"), strField("state")},
		// Pagination and a bare state are not secrets.
		"x.v1.ListOrdersResponse": {{Name: "orders", Kind: "message", TypeName: "x.v1.Order", Repeated: true}, strField("next_page_token")},
		"x.v1.Order":              {strField("id"), strField("state")},
		// A forge:secret field is never packed, and the walk does not enter one.
		"x.v1.GetUserResponse": {{Name: "user", Kind: "message", TypeName: "x.v1.User"}},
		"x.v1.User":            {strField("id"), {Name: "password", Kind: "string", Secret: true}, {Name: "keys", Kind: "message", TypeName: "x.v1.Credentials", Secret: true}},
	}
	// A chain deeper than the walk's cap: D0 → D1 → … → D9, a token in D9.
	for i := 0; i < 9; i++ {
		schemas["x.v1.D"+itoa(i)] = []codegen.SchemaFieldDef{msgField("next", "x.v1.D"+itoa(i+1))}
	}
	schemas["x.v1.D9"] = []codegen.SchemaFieldDef{strField("token")}
	// Within the cap: E0 → … → E5, a token in E5 (six levels).
	for i := 0; i < 5; i++ {
		schemas["x.v1.E"+itoa(i)] = []codegen.SchemaFieldDef{msgField("next", "x.v1.E"+itoa(i+1))}
	}
	schemas["x.v1.E5"] = []codegen.SchemaFieldDef{strField("token")}

	for _, tc := range []struct {
		fq, want string
	}{
		{fq: "x.v1.GetSessionResponse", want: "session.credentials.access_token"},
		{fq: "x.v1.Shallow", want: "upload_url"},
		{fq: "x.v1.ByName", want: "by_name.access_token"},
		{fq: "x.v1.Tree"},
		{fq: "x.v1.A"},
		{fq: "x.v1.ListOrdersResponse"},
		{fq: "x.v1.GetUserResponse"},
		{fq: "x.v1.D0"},
		{fq: "x.v1.E0", want: "next.next.next.next.next.token"},
		{fq: "google.protobuf.Empty"}, // not in the schema
		{fq: ""},                      // an old descriptor without OutputTypeFQ
	} {
		t.Run(tc.fq, func(t *testing.T) {
			got, why := secretResponseField(schemas, tc.fq)
			if got != tc.want {
				t.Fatalf("path = %q (%s), want %q", got, why, tc.want)
			}
			if got != "" && why == "" {
				t.Error("a hit carries no reason")
			}
		})
	}
}

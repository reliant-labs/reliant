package naming

import (
	"testing"
)

func TestToSnakeCaseWords(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		// The rules.
		{"GitHubAccessLoop", "github_access_loop"},
		{"GitHubAccess", "github_access"},
		{"OAuthClient", "oauth_client"},
		{"OAuth2Token", "oauth2_token"},
		{"MyOAuthFlow", "my_oauth_flow"},
		{"APIOAuth", "api_oauth"},
		{"OpenAIProvider", "openai_provider"},
		{"APIURL", "api_url"},
		{"LLMMCPBridge", "llm_mcp_bridge"},
		{"NATSConn", "nats_conn"},
		{"HTTPAPIServer", "http_api_server"},
		{"UserIDs", "user_ids"},
		{"APIIDs", "api_ids"},
		{"URLsByHost", "urls_by_host"},
		// Everything else is ToSnakeCase's answer, unchanged.
		{"APIListener", "api_listener"},
		{"HTTPStatus", "http_status"},
		{"DB", "db"},
		{"DBRepo", "db_repo"},
		{"UserAuth", "user_auth"},
		{"TLSFiles", "tls_files"},
		{"MCP", "mcp"},
		{"ThisIsIt", "this_is_it"},
		{"Github", "github"},
		{"UTF8Reader", "utf8reader"}, // digits do not split, as in ToSnakeCase
		{"V2Client", "v2client"},
		{"already_snake", "already_snake"},
		{"", ""},
	} {
		if got := ToSnakeCaseWords(tc.in); got != tc.want {
			t.Errorf("ToSnakeCaseWords(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Outside the three rules the answer is ToSnakeCase's, so a project's labels
// change only where the rules name.
func TestToSnakeCaseWords_AgreesWithToSnakeCaseOutsideItsRules(t *testing.T) {
	for _, in := range []string{
		"firstName", "LastName", "HTTPStatus", "userID", "UPPER_CASE", "createdAt",
		"XMLParser", "CaféName", "APIListener", "PayloadGC", "ReliantKeySweep",
		"StreamingHub", "TemporalClient", "WorkflowReconciler", "ProcessRuntime",
		"APIDebugServer", "Daemon", "HTTPRoutes", "UserRoutes",
	} {
		if got, want := ToSnakeCaseWords(in), ToSnakeCase(in); got != want {
			t.Errorf("ToSnakeCaseWords(%q) = %q, ToSnakeCase = %q", in, got, want)
		}
	}
}

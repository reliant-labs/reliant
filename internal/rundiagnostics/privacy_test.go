package rundiagnostics

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/require"
)

func TestCredentialFormatsFailClosedBeforeTailAndFailureMessage(t *testing.T) {
	for _, text := range []string{
		"token: opaque-secret-value", "api_key: opaque-secret-value", "client_secret: opaque-secret-value",
		"Authorization: Bearer opaque-secret-value", "Authorization: custom opaque-secret-value",
		"AWS_SECRET_ACCESS_KEY=opaque-secret-value", "AWS_SESSION_TOKEN=opaque-secret-value",
		"Cookie: session=opaque-secret-value", "Set-Cookie: session=opaque-secret-value; HttpOnly",
		"client_secret: |\n  opaque-secret-value\n  more-private-data",
		"client_secret: >\n  opaque-secret-value\n  more-private-data",
		`{"client_secret":"opaque-secret-value"}`, `\"client_secret\":\"opaque-secret-value\"`,
		`password="prefix\"opaque-secret-value"`,
		`password='prefix'\''opaque-secret-value'`,
		`password="prefix\\opaque-secret-value"`,
		`password="prefix\\nopaque-secret-value"`,
		`password="prefix;opaque-secret-value"`,
		`password=[REDACTED]opaque-secret-value`,
		`password="prefix""opaque-secret-value"`,
		`Authorization: custom "prefix\"opaque-secret-value"`,
		`password=opaque-secret-value`,
		"-----BEGIN PRIVATE KEY-----\nopaque-secret-value\n-----END PRIVATE KEY-----",
	} {
		t.Run(text, func(t *testing.T) {
			for _, text := range []string{text, strings.Repeat("earlier\n", 45) + text + strings.Repeat("\nafter", 49)} {
				raw, _ := json.Marshal(map[string]any{"stdout": text, "stderr": text})
				rows := []*core.StepExecution{{StepID: "gate", ActivityName: "ExecuteRunStep", ExitCode: sql.NullInt64{Int64: 2, Valid: true}, OutputJSON: sql.NullString{String: string(raw), Valid: true}}}
				nodes := Nodes(rows)
				encoded, _ := json.Marshal(nodes)
				require.NotContains(t, string(encoded), "opaque-secret-value")
				require.NotContains(t, string(encoded), "more-private-data")
				require.NotContains(t, Failure(rows), "opaque-secret-value")
				require.NotContains(t, ErrorText(text), "opaque-secret-value")
			}
		})
	}
}

func TestOversizedRawProjectionIsOmittedBeforeParsing(t *testing.T) {
	raw := `{"stdout":"` + strings.Repeat("s", 2<<20) + `"}`
	rows := []*core.StepExecution{{ActivityName: "ExecuteRunStep", OutputJSON: sql.NullString{String: raw, Valid: true}}}
	n := Nodes(rows)
	require.Equal(t, "[output omitted: safe diagnostic limit]", n[0].StdoutTail)
	large := testing.AllocsPerRun(10, func() { Nodes(rows) })
	rows[0].OutputJSON.String = strings.Repeat("x", ScanLimit+1)
	small := testing.AllocsPerRun(10, func() { Nodes(rows) })
	require.Equal(t, small, large, "oversized content must not be parsed or scanned; allocation count is independent of payload size")
}

package audit

import (
	"bytes"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cli/audittype"
)

// The remediation only helps if the person who saw the refusal can read
// it. `forge project audit` renders details through formatDetailValue,
// which JSON-marshals a slice and truncates it at 200 characters — a
// scoping wrapper is ten times that, so the snippet that exists in the
// report is invisible on the terminal where the failure was seen.
//
// Sending the reader to --json for the fix would be the dead end again in
// a smaller form: the gate refuses on stdout and the remedy lives
// somewhere else. So the gating category prints its wrappers in full,
// below the truncated detail lines.

func TestPrintAuditCategory_PrintsTheScopingWrapperInFull(t *testing.T) {
	cat := audittype.Category{
		Status:  audittype.StatusError,
		Summary: "1 authenticated RPC(s) over forge:owner-declared data never resolve the caller",
		Details: map[string]any{
			"owner_scoping_hint": ownerScopingHint(),
			"owner_scoped_unscoped_rpcs": []unscopedRPC{{
				Service: "DocumentsService", Method: "GetDocument",
				File:   "internal/handlers/documents/handlers_crud.go",
				Entity: "Document", Table: "documents",
				Remediation: "func (s *Service) GetDocument(\n\t// ...\n\topts, orm.WhereEq(\"org_id\", owner)\n}",
			}},
		},
	}

	var buf bytes.Buffer
	printAuditCategory(&buf, "unscoped_auth", cat)
	out := buf.String()

	for _, want := range []string{
		"internal/handlers/documents/handlers_crud.go",
		"GetDocument",
		`orm.WhereEq("org_id", owner)`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("printed report is missing %q — the wrapper must be readable where the refusal was seen, not only in --json\n---\n%s", want, out)
		}
	}
}

// A category with no remediation must print exactly as it did before.
// The wrapper block is an addition to the armed path, not a new section
// every category grows.
func TestPrintAuditCategory_NoRemediationNoBlock(t *testing.T) {
	cat := audittype.Category{
		Status:  audittype.StatusWarn,
		Summary: "3 of 9 authenticated RPC(s) never resolve the caller",
		Details: map[string]any{
			"unscoped_rpcs": []unscopedRPC{{Service: "ShopService", Method: "GetProduct"}},
		},
	}

	var buf bytes.Buffer
	printAuditCategory(&buf, "unscoped_auth", cat)

	if strings.Contains(buf.String(), "paste") {
		t.Errorf("an advisory finding must not grow a remediation block\n---\n%s", buf.String())
	}
}

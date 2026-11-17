package forgeconv

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestHandlerErrorMapping_FlagsBadFixture is the headline assertion:
// the canonical 4× duplication shape from cpnext fires both name and
// body signals, producing two findings (toConnectError + mapServiceError).
func TestHandlerErrorMapping_FlagsBadFixture(t *testing.T) {
	res, err := LintHandlerErrorMapping(filepath.Join("testdata", "handlers_bad"))
	if err != nil {
		t.Fatalf("LintHandlerErrorMapping: %v", err)
	}
	got := findingsForRule(res.Findings, "forgeconv-no-handler-error-mapping")
	if len(got) != 2 {
		t.Fatalf("expected 2 findings (toConnectError + mapServiceError), got %d:\n%s",
			len(got), res.FormatText())
	}

	// All findings must be warnings — false-positive risk is too high
	// for a hard error.
	for _, f := range got {
		if f.Severity != SeverityWarning {
			t.Errorf("severity = %s, want warning", f.Severity)
		}
		// Each finding must point at svcerr.Wrap as the canonical
		// replacement so the LLM can act on the message alone.
		if !strings.Contains(f.Remediation, "svcerr.Wrap") {
			t.Errorf("remediation should reference svcerr.Wrap; got: %s", f.Remediation)
		}
		if !strings.Contains(f.Remediation, "forge/pkg/svcerr") {
			t.Errorf("remediation should reference forge/pkg/svcerr import path; got: %s", f.Remediation)
		}
	}

	// The body-shape finding (toConnectError) must mention the
	// switch+sentinel evidence so the user understands WHY it fired.
	combined := res.FormatText()
	if !strings.Contains(combined, "toConnectError") {
		t.Errorf("expected finding text to reference toConnectError; got:\n%s", combined)
	}
	if !strings.Contains(combined, "mapServiceError") {
		t.Errorf("expected finding text to reference mapServiceError; got:\n%s", combined)
	}
}

// TestHandlerErrorMapping_CleanFixture checks the false-positive
// surface: a handler file that constructs ONE connect.NewError ad-hoc,
// without a switch and without a suspect name, must not fire.
func TestHandlerErrorMapping_CleanFixture(t *testing.T) {
	res, err := LintHandlerErrorMapping(filepath.Join("testdata", "handlers_clean"))
	if err != nil {
		t.Fatalf("LintHandlerErrorMapping: %v", err)
	}
	got := findingsForRule(res.Findings, "forgeconv-no-handler-error-mapping")
	if len(got) != 0 {
		t.Fatalf("expected 0 findings on clean fixture, got %d:\n%s", len(got), res.FormatText())
	}
}

// TestHandlerErrorMapping_SvcerrImportDoesNotSuppress: importing
// forge/pkg/svcerr does not silence the rule. It used to, so one
// svcerr.Wrap in a file — or a codemod adding the import — hid every
// mapper beside it without fixing one (F-P7a-2a: reliant's 11 findings
// would have gone quiet the moment `forge fix connect-errors` ran).
func TestHandlerErrorMapping_SvcerrImportDoesNotSuppress(t *testing.T) {
	res, err := LintHandlerErrorMapping(filepath.Join("testdata", "handlers_uses_svcerr"))
	if err != nil {
		t.Fatalf("LintHandlerErrorMapping: %v", err)
	}
	got := findingsForRule(res.Findings, "forgeconv-no-handler-error-mapping")
	if len(got) != 1 || !strings.Contains(got[0].Message, "toConnectError") {
		t.Fatalf("expected the toConnectError mapper beside svcerr.Wrap to be flagged once, got %d:\n%s", len(got), res.FormatText())
	}
}

// TestHandlerErrorMapping_NoHandlersDir confirms the analyzer is a no-op
// in projects without a handlers/ tree.
func TestHandlerErrorMapping_NoHandlersDir(t *testing.T) {
	tmp := t.TempDir()
	res, err := LintHandlerErrorMapping(tmp)
	if err != nil {
		t.Fatalf("LintHandlerErrorMapping: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Errorf("empty project should produce 0 findings, got %d", len(res.Findings))
	}
}

// TestHandlerErrorMapping_SkipsTestFiles verifies the walk excludes
// _test.go files. Table tests sometimes legitimately construct
// connect.NewError for assertion fixtures and should not trip the rule.
func TestHandlerErrorMapping_SkipsTestFiles(t *testing.T) {
	tmp := t.TempDir()
	pkgDir := filepath.Join(tmp, "handlers", "thing")
	must(t, mkdirAll(pkgDir))
	must(t, writeFile(filepath.Join(pkgDir, "handlers_test.go"), `package thing
import (
	"errors"
	"connectrpc.com/connect"
)
var ErrA = errors.New("a")
var ErrB = errors.New("b")
func toConnectError(err error) error {
	switch {
	case errors.Is(err, ErrA):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, ErrB):
		return connect.NewError(connect.CodeAborted, err)
	}
	return nil
}
`))
	res, err := LintHandlerErrorMapping(tmp)
	if err != nil {
		t.Fatalf("LintHandlerErrorMapping: %v", err)
	}
	got := findingsForRule(res.Findings, "forgeconv-no-handler-error-mapping")
	if len(got) != 0 {
		t.Fatalf("expected 0 findings (test files skipped), got %d:\n%s", len(got), res.FormatText())
	}
}

// TestHandlerErrorMapping_IgnoresNonHandlerDirs verifies that mapping-
// shaped helpers in non-handler trees (e.g. internal/<pkg>/, cmd/,
// pkg/middleware/) are not flagged. The rule is scoped to handlers/.
func TestHandlerErrorMapping_IgnoresNonHandlerDirs(t *testing.T) {
	tmp := t.TempDir()
	pkgDir := filepath.Join(tmp, "internal", "billing")
	must(t, mkdirAll(pkgDir))
	must(t, writeFile(filepath.Join(pkgDir, "errors.go"), `package billing
import (
	"errors"
	"connectrpc.com/connect"
)
var ErrA = errors.New("a")
var ErrB = errors.New("b")
func toConnectError(err error) error {
	switch {
	case errors.Is(err, ErrA):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, ErrB):
		return connect.NewError(connect.CodeAborted, err)
	}
	return nil
}
`))
	res, err := LintHandlerErrorMapping(tmp)
	if err != nil {
		t.Fatalf("LintHandlerErrorMapping: %v", err)
	}
	got := findingsForRule(res.Findings, "forgeconv-no-handler-error-mapping")
	if len(got) != 0 {
		t.Fatalf("expected 0 findings (non-handler dirs ignored), got %d:\n%s", len(got), res.FormatText())
	}
}

package cli

// THE TWO-REPOSITORIES WARNING: the control plane's answer to the code source
// forge sends (code_source.go). An app is one forge.yaml `name` in an org, so
// two repositories that share a name deploy to the same environments; once an
// app has received sources from more than one repository the control plane
// says so (control-plane docs/design/unified-shell-and-projects.md, D5), and
// forge prints it.
//
// THE WIRE SHAPE is controlplane.v1.DeployAppCodeSources, returned as
// `code_sources` on CutReleaseResponse and OpenDeployAttemptResponse, and as
// `app_code_sources` (one per app) on ListDeployEnvironmentsResponse. forge
// reads only its warning:
//
//	message DeployAppCodeSources {
//	  string project = 1;
//	  repeated DeployCodeSourceSighting sources = 2;
//	  string warning = 3; // "" unless the app has more than one repository
//	}
//
// A WARNING, NEVER A FAILURE. It is printed once per invocation and changes no
// exit code: a fork or a renamed repository is a second remote too. A control
// plane that predates the field sends nothing, and forge prints nothing.
//
// WHERE IT IS PRINTED. A release cut outside a deploy prints it right after
// the cut (`forge env build --release`, `forge release record`). Inside a
// deploy, the cut's warning and the attempt's (the same text: one app) are
// held by the attempt and printed once, after the deploy's own output
// (deployAttempt.finish). `forge env status` carries it in its report.

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// wireAppCodeSources is DeployAppCodeSources as forge reads it.
type wireAppCodeSources struct {
	Project string `json:"project,omitempty"`
	Warning string `json:"warning,omitempty"`
}

// warning is the app's two-repositories warning, "" when there is none or the
// server sent no sources.
func (s *wireAppCodeSources) warning() string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(s.Warning)
}

// writeCodeSourceWarning prints the warning as forge prints every warning.
func writeCodeSourceWarning(w io.Writer, msg string) {
	if msg == "" || w == nil {
		return
	}
	fmt.Fprintf(w, "warning: %s\n", msg)
}

// noteCodeSourceWarning reports a cut's warning: to the deploy attempt in ctx,
// which prints it after the deploy, else now. A store prints one text once,
// however many releases this process cuts through it.
func (s *hostedStore) noteCodeSourceWarning(a *deployAttempt, msg string) {
	if msg == "" {
		return
	}
	if a != nil {
		a.noteCodeSourceWarning(msg)
		return
	}
	s.mu.Lock()
	seen := s.codeSourceWarned == msg
	s.codeSourceWarned = msg
	s.mu.Unlock()
	if seen {
		return
	}
	w := s.warn
	if w == nil {
		w = os.Stderr
	}
	writeCodeSourceWarning(w, msg)
}

// noteCodeSourceWarning holds a warning for finish. One deploy ships one app,
// so the cut and the attempt carry the same text; the latest is kept.
func (a *deployAttempt) noteCodeSourceWarning(msg string) {
	if a == nil || msg == "" {
		return
	}
	a.mu.Lock()
	a.codeSourceWarning = msg
	a.mu.Unlock()
}

// printCodeSourceWarning prints the held warning, once.
func (a *deployAttempt) printCodeSourceWarning() {
	a.mu.Lock()
	msg := a.codeSourceWarning
	a.codeSourceWarning = ""
	a.mu.Unlock()
	writeCodeSourceWarning(a.warn, msg)
}

// envCodeSourcesReport is the app's code sources as `forge env status` reports
// them: today only the warning.
type envCodeSourcesReport struct {
	Warning string `json:"warning"`
}

// codeSourcesReport is nil without a warning, so the document omits it.
func codeSourcesReport(warning string) *envCodeSourcesReport {
	if warning == "" {
		return nil
	}
	return &envCodeSourcesReport{Warning: warning}
}

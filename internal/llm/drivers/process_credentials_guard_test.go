// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"bufio"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"net"
	"net/textproto"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/anthropic"
	"github.com/reliant-labs/reliant/internal/llm/drivers/antigravity"
	"github.com/reliant-labs/reliant/internal/llm/drivers/codex"
	"github.com/reliant-labs/reliant/internal/llm/drivers/copilot"
	"github.com/reliant-labs/reliant/internal/llm/drivers/gemini"
	"github.com/reliant-labs/reliant/internal/llm/drivers/local"
	"github.com/reliant-labs/reliant/internal/llm/drivers/openai"
	"github.com/reliant-labs/reliant/internal/llm/drivers/openrouter"
	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/drivers/reliant"
	"github.com/reliant-labs/reliant/internal/llm/drivers/vertexai"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// The process-credential guard.
//
// Drivers are built on the multi-tenant server roles (api, worker, gateway).
// A driver that, given no credential of the account's, reaches for one of the
// PROCESS — an env var, a credential file under HOME, a cloud default chain, an
// SDK's own env default — serves one tenant's request with the operator's
// credential. Every such fallback found has been removed (reliant#3031, #3034,
// #3036, #3039). This guard keeps the class from coming back, two ways:
//
//   - TestNoDriverSendsAProcessCredential builds EVERY registered driver family
//     with empty credentials under a process environment holding a sentinel in
//     every provider credential variable and credential file we know of, and
//     asserts that no request carries it and nothing follows an env base URL.
//   - TestDriversReadNoAmbientCredentialSource fails a driver that calls an
//     env, home-dir or default-credential API at all, so a fallback is caught
//     in review even for a driver the dynamic test cannot reach.

const processCredentialSentinel = "PROCESS_CREDENTIAL_SENTINEL_driver_guard"

// processCredentialEnv is every provider credential or endpoint variable a
// driver or its SDK could read. Base-URL variables are set to the trap.
var processCredentialEnv = []string{
	"OPENAI_API_KEY", "OPENAI_ADMIN_KEY", "OPENAI_ORG_ID", "OPENAI_PROJECT_ID", "OPENAI_WEBHOOK_SECRET",
	"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN",
	"GOOGLE_API_KEY", "GEMINI_API_KEY", "GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_LOCATION", "GOOGLE_CLOUD_REGION",
	"GITHUB_TOKEN", "GH_TOKEN", "COPILOT_GITHUB_TOKEN",
	"OPENROUTER_API_KEY", "CODEX_API_KEY", "XAI_API_KEY", "GROQ_API_KEY", "MISTRAL_API_KEY",
	"AZURE_OPENAI_API_KEY", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_BEARER_TOKEN_BEDROCK",
}

var processBaseURLEnv = []string{"OPENAI_BASE_URL", "ANTHROPIC_BASE_URL", "GOOGLE_GEMINI_BASE_URL", "GOOGLE_VERTEX_BASE_URL"}

// processCredentialFiles are credential files under HOME / XDG_CONFIG_HOME
// that CLIs of these providers write.
var processCredentialFiles = map[string]string{
	".config/github-copilot/hosts.json":      `{"github.com":{"oauth_token":"` + processCredentialSentinel + `"}}`,
	".config/github-copilot/apps.json":       `{"github.com":{"oauth_token":"` + processCredentialSentinel + `"}}`,
	".config/gh/hosts.yml":                   "github.com:\n  oauth_token: " + processCredentialSentinel + "\n",
	".codex/auth.json":                       `{"OPENAI_API_KEY":"` + processCredentialSentinel + `","tokens":{"access_token":"` + processCredentialSentinel + `"}}`,
	".claude/.credentials.json":              `{"claudeAiOauth":{"accessToken":"` + processCredentialSentinel + `"}}`,
	".aws/credentials":                       "[default]\naws_access_key_id = " + processCredentialSentinel + "\naws_secret_access_key = " + processCredentialSentinel + "\n",
	".config/anthropic/configs/default.json": `{"api_key":"` + processCredentialSentinel + `"}`,
}

// driverGuardCase is how the dynamic guard exercises one driver family.
type driverGuardCase int

const (
	// sendsToBaseURL: the driver builds with no credential and honours
	// DriverOptions.BaseURL, so its ValidateKey request is inspected.
	sendsToBaseURL driverGuardCase = iota
	// refusesNoCredential: building the driver with no credential must fail
	// (its error is the "connect X" message).
	refusesNoCredential
	// staticOnly: the driver builds with no credential but its host is fixed,
	// so the dynamic guard cannot reach it without leaving the machine.
	// TestDriversReadNoAmbientCredentialSource covers it.
	staticOnly
)

// driverGuardCases names a case for every registered family. A family missing
// here fails TestNoDriverSendsAProcessCredential: choose one when adding a
// driver.
var driverGuardCases = map[models.Family]driverGuardCase{
	openai.Family:          sendsToBaseURL,
	openrouter.Family:      sendsToBaseURL,
	reliant.Family:         sendsToBaseURL,
	local.Family:           sendsToBaseURL,
	anthropic.Family:       sendsToBaseURL,
	anthropic.ClaudeFamily: sendsToBaseURL,
	copilot.Family:         refusesNoCredential,
	codex.Family:           refusesNoCredential,
	gemini.Family:          refusesNoCredential,
	// Vertex AI takes only the connected account's service-account key,
	// never the server's Application Default Credentials.
	vertexai.Family:    refusesNoCredential,
	antigravity.Family: staticOnly,
}

// rawRecorder is a raw TCP listener that records each request's bytes and
// answers 400. Raw, because the Claude Code driver writes an exact-cased host
// header beside Go's own, which net/http's server rejects before a handler.
type rawRecorder struct {
	ln   net.Listener
	mu   sync.Mutex
	reqs []string
}

func newRawRecorder(t *testing.T) *rawRecorder {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := &rawRecorder{ln: ln}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go r.handle(conn)
		}
	}()
	return r
}

func (r *rawRecorder) url() string { return "http://" + r.ln.Addr().String() }

func (r *rawRecorder) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	tp := textproto.NewReader(bufio.NewReader(conn))
	line, err := tp.ReadLine()
	if err != nil {
		return
	}
	header, _ := tp.ReadMIMEHeader()
	var rec strings.Builder
	rec.WriteString(line)
	for name, values := range header {
		rec.WriteString("\n" + name + ": " + strings.Join(values, ","))
	}
	if n, _ := strconv.Atoi(header.Get("Content-Length")); n > 0 {
		body := make([]byte, n)
		if _, err := io.ReadFull(tp.R, body); err == nil {
			rec.WriteString("\n\n" + string(body))
		}
	}
	r.mu.Lock()
	r.reqs = append(r.reqs, rec.String())
	r.mu.Unlock()
	const resp = `{"error":{"type":"invalid_request_error","message":"guard"}}`
	_, _ = fmt.Fprintf(conn, "HTTP/1.1 400 Bad Request\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(resp), resp)
}

func (r *rawRecorder) requests() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.reqs...)
}

// setProcessCredentials fills the process environment and a temp HOME with
// sentinel credentials, and points every env base URL at trap.
func setProcessCredentials(t *testing.T, trap string) {
	t.Helper()
	for _, name := range processCredentialEnv {
		t.Setenv(name, processCredentialSentinel)
	}
	for _, name := range processBaseURLEnv {
		t.Setenv(name, trap)
	}
	t.Setenv("OPENAI_CUSTOM_HEADERS", "X-Process-Credential: "+processCredentialSentinel)
	t.Setenv("ANTHROPIC_CUSTOM_HEADERS", "X-Process-Credential: "+processCredentialSentinel)
	t.Setenv("GOOGLE_GENAI_USE_VERTEXAI", "true")
	home := t.TempDir()
	for rel, content := range processCredentialFiles {
		path := filepath.Join(home, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
}

// guardModel is a catalog model the family serves, so its factory builds the
// real client for it.
func guardModel(family models.Family) models.Model {
	if defs := models.MustGetRegistry().ListModelsByProvider(string(family)); len(defs) > 0 {
		return defs[0].ToModel()
	}
	return models.Model{ID: "guard-model", APIModel: "guard-model"}
}

func TestNoDriverSendsAProcessCredential(t *testing.T) {
	trap := newRawRecorder(t)
	setProcessCredentials(t, trap.url())

	families := registry.Families()
	if len(families) == 0 {
		t.Fatal("no driver family is registered")
	}
	for _, family := range families {
		guard, ok := driverGuardCases[family]
		if !ok {
			t.Errorf("driver family %q has no entry in driverGuardCases: choose how the process-credential guard exercises it", family)
			continue
		}
		t.Run(string(family), func(t *testing.T) {
			factory, _ := registry.GetDriverFactory(family)
			recorder := newRawRecorder(t)
			opts := &llm.DriverOptions{Model: guardModel(family), MaxTokens: 16, BaseURL: recorder.url()}
			client, err := factory(opts)
			switch guard {
			case refusesNoCredential:
				if err == nil {
					t.Fatalf("%s built with no credential; it must refuse rather than find one of the process", family)
				}
				if strings.Contains(err.Error(), processCredentialSentinel) {
					t.Fatalf("%s's error carries a process credential: %v", family, err)
				}
				return
			case staticOnly:
				return
			}
			if err != nil {
				t.Fatalf("%s did not build with no credential: %v", family, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = client.ValidateKey(ctx) // the recorder answers 400; only the request matters
			reqs := recorder.requests()
			if len(reqs) == 0 {
				t.Fatalf("%s sent no request to DriverOptions.BaseURL", family)
			}
			for _, r := range reqs {
				if strings.Contains(r, processCredentialSentinel) {
					t.Errorf("%s sent a credential of the server process:\n%s", family, r)
				}
			}
		})
	}
	if reqs := trap.requests(); len(reqs) > 0 {
		t.Errorf("a driver followed a base URL named only by the process environment:\n%s", strings.Join(reqs, "\n---\n"))
	}
}

// ambientCredentialAPIs are calls that read a credential, or where one lives,
// from the process: its environment, its home directory, or a cloud default
// credential chain. Keyed by import path, then function name.
var ambientCredentialAPIs = map[string]map[string]bool{
	"os":                                   {"Getenv": true, "LookupEnv": true, "Environ": true, "ExpandEnv": true, "UserHomeDir": true, "UserConfigDir": true},
	"syscall":                              {"Getenv": true, "Environ": true},
	"golang.org/x/oauth2/google":           {"FindDefaultCredentials": true, "DefaultTokenSource": true, "DefaultClient": true},
	"cloud.google.com/go/auth/credentials": {"DetectDefault": true},
	"github.com/aws/aws-sdk-go-v2/config":  {"LoadDefaultConfig": true},
	"github.com/anthropics/anthropic-sdk-go/bedrock": {"WithLoadDefaultConfig": true},
	"github.com/openai/openai-go/v3":                 {"DefaultClientOptions": true},
	"github.com/anthropics/anthropic-sdk-go":         {"DefaultClientOptions": true},
}

// ambientCredentialAllowed are the reviewed exceptions, by "file: pkg.Func",
// each with why it is not a tenant fallback. There are none: the server's own
// Google credentials (ADC) included, a driver takes every credential from the
// account (owner decision 2026-11-20).
var ambientCredentialAllowed = map[string]string{}

func TestDriversReadNoAmbientCredentialSource(t *testing.T) {
	var offenders []string
	used := map[string]bool{}
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		imports := map[string]string{} // local name -> import path
		for _, imp := range file.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			name := p[strings.LastIndex(p, "/")+1:]
			if p == "github.com/openai/openai-go/v3" {
				name = "openai"
			}
			if p == "github.com/anthropics/anthropic-sdk-go" {
				name = "anthropic"
			}
			if imp.Name != nil {
				name = imp.Name.Name
			}
			imports[name] = p
		}
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			pkg, ok := imports[ident.Name]
			if !ok || !ambientCredentialAPIs[pkg][sel.Sel.Name] {
				return true
			}
			key := fmt.Sprintf("%s: %s.%s", filepath.ToSlash(path), ident.Name, sel.Sel.Name)
			if _, ok := ambientCredentialAllowed[key]; ok {
				used[key] = true
				return true
			}
			offenders = append(offenders, fmt.Sprintf("%s (%s)", key, fset.Position(sel.Pos())))
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(offenders)
	for _, o := range offenders {
		t.Errorf("a driver reads a credential source of the server process: %s.\n"+
			"Drivers run on the multi-tenant server roles: take the credential from DriverOptions (the account's), "+
			"and platform-managed settings from typed config. A reviewed exception goes in ambientCredentialAllowed with why.", o)
	}
	for key := range ambientCredentialAllowed {
		if !used[key] {
			t.Errorf("ambientCredentialAllowed entry %q matches nothing: remove it", key)
		}
	}
}

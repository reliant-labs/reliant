package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/deploy"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// progressLog collects the progress notifications a client receives.
type progressLog struct {
	mu       sync.Mutex
	messages []string
	first    chan struct{}
	once     sync.Once
}

func newProgressLog() *progressLog { return &progressLog{first: make(chan struct{})} }

func (p *progressLog) handler(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
	p.mu.Lock()
	p.messages = append(p.messages, req.Params.Message)
	p.mu.Unlock()
	p.once.Do(func() { close(p.first) })
}

func (p *progressLog) joined() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.messages, "\n")
}

func callMCPStructured(ctx context.Context, t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, token any) (string, map[string]any, bool, error) {
	t.Helper()
	params := &mcp.CallToolParams{Name: name, Arguments: args}
	if token != nil {
		params.SetProgressToken(token)
	}
	res, err := cs.CallTool(ctx, params)
	if err != nil {
		return "", nil, false, err
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	var structured map[string]any
	if res.StructuredContent != nil {
		data, _ := json.Marshal(res.StructuredContent)
		_ = json.Unmarshal(data, &structured)
	}
	return b.String(), structured, res.IsError, nil
}

// TestMCP_DeployClaimable_StreamsProgressAndReturnsTheClaim is the tool's
// acceptance path against the fake control plane: progress arrives while the
// deploy runs, the result carries the URL, the claim link, the expiry and the
// relay text, and deploy_status shows the same claim again.
func TestMCP_DeployClaimable_StreamsProgressAndReturnsTheClaim(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL and runs the whole deploy; skipped in -short")
	}
	fake, _ := claimableFixture(t)
	dir, _ := os.Getwd()
	progress := newProgressLog()
	cs, _ := connectMCPWith(t, dir, &mcp.ClientOptions{ProgressNotificationHandler: progress.handler})

	text, doc, isErr, err := callMCPStructured(context.Background(), t, cs, "deploy_claimable",
		map[string]any{"env": "hosted", "no_wait": true}, "deploy-1")
	if err != nil {
		t.Fatal(err)
	}
	if isErr {
		t.Fatalf("deploy_claimable failed:\n%s", text)
	}
	for k, want := range map[string]any{
		"ok": true, "env": "hosted", "state": "live", "rollout_verified": false, "claim_url": "https://app.reliant.dev/claim#rcl_9f3KQe",
		"expires_at": "2026-10-29T14:02:00Z", "purge_after": "2026-11-05T14:02:00Z", "exit_code": float64(0),
	} {
		if doc[k] != want {
			t.Errorf("%s = %v, want %v", k, doc[k], want)
		}
	}
	if u, _ := doc["url"].(string); !strings.HasPrefix(u, "https://") {
		t.Errorf("url = %q, want the live address", u)
	}
	next, _ := doc["next_steps"].(string)
	for _, want := range []string{"Give them the claim link https://app.reliant.dev/claim#rcl_9f3KQe", "expires at 2026-10-29T14:02:00Z", "deploy_status"} {
		if !strings.Contains(next, want) {
			t.Errorf("next_steps lacks %q:\n%s", want, next)
		}
	}
	if lim, _ := doc["limits"].(string); !strings.Contains(lim, "72 hours") {
		t.Errorf("limits = %q", lim)
	}
	if !strings.Contains(text, "Claim link: https://app.reliant.dev/claim#rcl_9f3KQe") {
		t.Errorf("text lacks the claim link:\n%s", text)
	}
	if got := progress.joined(); !strings.Contains(got, "Requesting an anonymous deploy for acme/hosted") {
		t.Errorf("progress never reported the deploy's own lines:\n%s", got)
	}
	if fake.starts != 1 {
		t.Errorf("starts = %d, want 1", fake.starts)
	}

	text, doc, isErr, err = callMCPStructured(context.Background(), t, cs, "deploy_status", map[string]any{"env": "hosted"}, nil)
	if err != nil || isErr {
		t.Fatalf("deploy_status: %v\n%s", err, text)
	}
	if doc["claim_url"] != "https://app.reliant.dev/claim#rcl_9f3KQe" || doc["state"] != "live" {
		t.Errorf("deploy_status = %v", doc)
	}
}

// A client's cancel stops the deploy: the call returns, the runner is free,
// and nothing was started.
func TestMCP_DeployClaimable_CancelStopsTheDeploy(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL and runs the whole deploy; skipped in -short")
	}
	fake, _ := claimableFixture(t)
	fake.holdChallenge = make(chan struct{})
	// Release a held challenge before the fake server closes (cleanups run
	// last-registered first), so a failure here fails rather than hangs.
	t.Cleanup(func() { close(fake.holdChallenge) })
	dir, _ := os.Getwd()
	progress := newProgressLog()
	cs, r := connectMCPWith(t, dir, &mcp.ClientOptions{ProgressNotificationHandler: progress.handler})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, _, err := callMCPStructured(ctx, t, cs, "deploy_claimable", map[string]any{"env": "hosted", "no_wait": true}, "deploy-2")
		done <- err
	}()
	// Wait until the deploy is in flight (its first progress line), then
	// until it reached the held challenge. The render before it runs KCL,
	// which a loaded machine makes slow: the budgets are generous on purpose.
	select {
	case <-progress.first:
	case <-time.After(5 * time.Minute):
		t.Fatal("no progress before the challenge")
	}
	deadline := time.Now().Add(10 * time.Minute)
	for {
		fake.mu.Lock()
		n := fake.challenges
		fake.mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the deploy never asked for a challenge")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a cancelled call returned a result")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the call did not return after cancel")
	}
	// The server side stops too: the runner's slot frees.
	select {
	case r.sem <- struct{}{}:
		<-r.sem
	case <-time.After(30 * time.Second):
		t.Fatal("the deploy kept running after the cancel")
	}
	if fake.starts != 0 {
		t.Errorf("a cancelled deploy started %d anonymous deploys", fake.starts)
	}
}

// A directory forge cannot adopt is refused with the command to run instead,
// before anything reaches a control plane.
func TestMCP_DeployClaimable_RefusesWhatItCannotAdopt(t *testing.T) {
	empty := t.TempDir()
	goMod := t.TempDir()
	if err := os.WriteFile(filepath.Join(goMod, "go.mod"), []byte("module example.com/api\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cs := connectMCP(t, t.TempDir())
	for dir, want := range map[string]string{
		empty: "project new " + adoptionName(empty, "") + " --kind site",
		goMod: "project new " + adoptionName(goMod, "") + " --service api --claimable",
	} {
		text, doc, isErr, err := callMCPStructured(context.Background(), t, cs, "deploy_claimable", map[string]any{"dir": dir}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !isErr || doc["ok"] != false {
			t.Fatalf("%s: want a refusal, got:\n%s", dir, text)
		}
		actions, _ := doc["next_actions"].([]any)
		if len(actions) == 0 || !strings.Contains(actions[0].(string), want) {
			t.Errorf("%s: next_actions = %v, want first %q", dir, actions, want)
		}
		if _, err := os.Stat(filepath.Join(dir, "forge.yaml")); err == nil {
			t.Errorf("%s: a refusal wrote forge.yaml", dir)
		}
	}
}

// An existing Vite app with no forge.yaml is adopted in place: the forge files
// are added, the app's are untouched.
func TestMCPRunner_AdoptForClaimable_AdoptsAViteAppInPlace(t *testing.T) {
	if testing.Short() {
		t.Skip("scaffolds a site project; skipped in -short")
	}
	dir := filepath.Join(t.TempDir(), "My Landing")
	pkg := `{"name":"landing","dependencies":{"vite":"^5.0.0"}}`
	for rel, body := range map[string]string{"package.json": pkg, "index.html": "<h1>hi</h1>\n"} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	resetCmdRoute(t)
	t.Chdir(dir)
	r, err := newMCPRunner(NewRootCmd)
	if err != nil {
		t.Fatal(err)
	}
	adopted, refusal, err := r.adoptForClaimable(context.Background(), dir, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if refusal != nil {
		t.Fatalf("refused: %+v", refusal(&claimableDeployResult{Env: "prod"}))
	}
	if !adopted {
		t.Fatal("adopted = false")
	}
	yaml, err := os.ReadFile(filepath.Join(dir, "forge.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(yaml), "my-landing") {
		t.Errorf("forge.yaml does not name the project my-landing:\n%s", yaml)
	}
	prod, err := os.ReadFile(filepath.Join(dir, "deploy", "kcl", "prod", "main.k"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(prod), "OnHosted") {
		t.Errorf("prod is not hosted:\n%s", prod)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "package.json")); string(got) != pkg {
		t.Errorf("adoption changed package.json:\n%s", got)
	}
	// A second call sees the project and adopts nothing.
	again, _, err := r.adoptForClaimable(context.Background(), dir, "", nil)
	if err != nil || again {
		t.Errorf("second adoption = %v, %v; want a no-op", again, err)
	}
}

// An existing app with its own Dockerfile and no forge.yaml is adopted in
// place as `--kind app --claimable`: prod declares the anonymous plan, and the
// app's files are untouched.
func TestMCPRunner_AdoptForClaimable_AdoptsADockerfileAppInPlace(t *testing.T) {
	testkit.Slow(t, "adopts an app through the command tree; the .gitignore merge execs git")
	dir := nodeExpressApp(t)
	dockerfile := readFile(t, filepath.Join(dir, "Dockerfile"))
	resetCmdRoute(t)
	t.Chdir(dir)
	r, err := newMCPRunner(NewRootCmd)
	if err != nil {
		t.Fatal(err)
	}
	adopted, refusal, err := r.adoptForClaimable(context.Background(), dir, "todo", nil)
	if err != nil {
		t.Fatal(err)
	}
	if refusal != nil {
		t.Fatalf("refused: %+v", refusal(&claimableDeployResult{Env: "prod"}))
	}
	if !adopted {
		t.Fatal("adopted = false")
	}
	prod := readFile(t, filepath.Join(dir, "deploy", "kcl", "prod", "main.k"))
	if !strings.Contains(prod, "claimable = True") || !strings.Contains(prod, "OnHosted") {
		t.Errorf("prod is not a hosted claimable env:\n%s", prod)
	}
	if got := readFile(t, filepath.Join(dir, "Dockerfile")); got != dockerfile {
		t.Errorf("adoption changed the Dockerfile:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
		t.Error("adoption wrote a go.mod")
	}
}

func TestAdoptionName(t *testing.T) {
	for dir, want := range map[string]string{
		"/x/landing":     "landing",
		"/x/My Landing!": "my-landing",
		"/x/2026-site":   "site-2026-site",
		"/x/___":         "site",
	} {
		if got := adoptionName(dir, ""); got != want {
			t.Errorf("adoptionName(%q) = %q, want %q", dir, got, want)
		}
	}
	if got := adoptionName("/x/whatever", "chosen"); got != "chosen" {
		t.Errorf("an explicit name was replaced: %q", got)
	}
}

func TestFindDirectLLMCalls(t *testing.T) {
	dir := t.TempDir()
	for rel, body := range map[string]string{
		"src/chat.ts":                  `fetch("https://api.openai.com/v1/chat/completions")`,
		"package.json":                 `{"dependencies":{"@anthropic-ai/sdk":"^0.30.0","react":"18"}}`,
		"node_modules/openai/index.js": `const base = "https://api.openai.com"`,
		"dist/bundle.js":               `"https://api.anthropic.com"`,
		"src/ok.ts":                    `fetch("/api/chat")`,
		"README.md":                    "we call api.openai.com",
	} {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := findDirectLLMCalls(dir)
	want := []string{"package.json: @anthropic-ai/sdk (calls api.anthropic.com)", "src/chat.ts: api.openai.com"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("findDirectLLMCalls = %q, want %q", got, want)
	}
	note := directLLMNote(got)
	for _, want := range []string{reliantLLMProviderDocURL, "no outbound internet", "never in a static site's browser bundle", "claim the deploy", "backend billing", "forge login", "managed secret", "without --claimable", "actual model request server-side", "do not assume an unclaimed gateway route is enabled", "disable the feature, not crash startup"} {
		if !strings.Contains(note, want) {
			t.Errorf("recovery note lacks %q:\n%s", want, note)
		}
	}
	if strings.Contains(note, "/forge/llm-provider") || strings.Contains(note, "Route those calls through Reliant's LLM provider instead") {
		t.Errorf("recovery must not point to the missing page or promise unclaimed gateway access:\n%s", note)
	}
	if calls := findDirectLLMCalls(t.TempDir()); len(calls) != 0 {
		t.Errorf("an empty tree reported %v", calls)
	}
}

func TestExplainForgeError(t *testing.T) {
	for _, tc := range []struct {
		code       int
		reason     string
		text       string
		wantNext   string
		wantRetry  string
		wantInText string
	}{
		{exitRefused, cloud.ReasonClaimableThrottled, "", "env deploy prod --claimable", "later", "retry_at"},
		{exitRefused, cloud.ReasonClaimableUnavailable, "", "env deploy prod --claimable", "later", "not the project"},
		{exitWrong, reasonClaimableSignedIn, "", "env deploy prod", "no", "signed in"},
		{exitRefused, reasonClaimableIneligible, "", "--claimable --plan-only", "no", "does not fit"},
		{exitRefused, cloud.ReasonClaimableClaimed, "", "login && ", "no", "claimed"},
		{exitUndetermined, ReasonNoControlPlaneCredential, "", "env deploy prod --claimable", "no", "no Reliant credential"},
		{exitWrong, cloud.ReasonClientUpgradeRequired, "", "npx -y @reliantlabs/forge@latest", "no", "older"},
		{exitWrong, "", "Error: forge.yaml not found in current directory", "project new --in-place --kind site", "no", "not a forge project"},
		{exitTimedOut, "", "", "obs check prod --wait", "later", "still progressing"},
		{exitQueued, "", "", "", "no", "action URL"},
		{exitConflict, "", "", "", "no", "ask your user"},
	} {
		ex := explainForgeError(tc.code, tc.reason, tc.text, "prod")
		if tc.wantNext == "" && len(ex.Next) != 0 {
			t.Errorf("%d/%s: next = %v, want none (a person acts)", tc.code, tc.reason, ex.Next)
		}
		if tc.wantNext != "" && (len(ex.Next) == 0 || !strings.Contains(ex.Next[0], tc.wantNext)) {
			t.Errorf("%d/%s: next = %v, want first containing %q", tc.code, tc.reason, ex.Next, tc.wantNext)
		}
		if ex.Retry != tc.wantRetry {
			t.Errorf("%d/%s: retry = %q, want %q", tc.code, tc.reason, ex.Retry, tc.wantRetry)
		}
		if !strings.Contains(ex.Meaning, tc.wantInText) {
			t.Errorf("%d/%s: meaning %q lacks %q", tc.code, tc.reason, ex.Meaning, tc.wantInText)
		}
	}
	// Every exit code in the table has an answer of its own.
	seen := map[string]int{}
	for code := exitOK; code <= exitNeedsHuman; code++ {
		m := explainForgeError(code, "", "", "prod").Meaning
		if prev, dup := seen[m]; dup {
			t.Errorf("exit %d and %d explain the same: %q", prev, code, m)
		}
		seen[m] = code
	}
}

func TestMCP_ExplainError(t *testing.T) {
	cs := connectMCP(t, t.TempDir())
	text, doc, isErr, err := callMCPStructured(context.Background(), t, cs, "explain_error",
		map[string]any{"exit_code": 4, "error_reason": "claimable_throttled", "env": "staging"}, nil)
	if err != nil || isErr {
		t.Fatalf("explain_error: %v\n%s", err, text)
	}
	if !strings.Contains(text, "run: "+Name()+" env deploy staging --claimable") {
		t.Errorf("text lacks the next command:\n%s", text)
	}
	if doc["retry"] != "later" {
		t.Errorf("retry = %v", doc["retry"])
	}
}

// The tools call a deploy live only when the claim's window is open AND the
// call verified the rollout: an accepted (no_wait) deploy, a status read, and
// a suspended, claimed or expired claim each say what is true instead.
func TestClaimableResultSaysLiveOnlyWhenVerified(t *testing.T) {
	base := claimableDeployResult{
		OK: true, Env: "prod", URL: "https://acme.reliant.app", ClaimURL: "https://app.reliant.dev/claim#rcl_x",
		ExpiresAt: "2026-10-29T14:02:00Z", PurgeAfter: "2026-11-05T14:02:00Z", Limits: claimableLimitsSummary,
	}
	for _, tc := range []struct {
		name     string
		state    string
		verified bool
		wantText []string
		wantNext []string
		noText   []string
		noNext   []string
	}{
		{"live and verified", "live", true,
			[]string{"Live:       https://acme.reliant.app"}, []string{"it is live at https://acme.reliant.app", "Give them the claim link"},
			nil, []string{"not verified"}},
		{"accepted, not ready", "live", false,
			[]string{"URL:        https://acme.reliant.app (rollout not verified yet)"},
			[]string{"rollout is not verified", "Do not tell your user it is live", "Give them the claim link"},
			[]string{"Live:"}, []string{"it is live at"}},
		{"suspended", "suspended", false,
			[]string{"Offline:    https://acme.reliant.app", "Claim link: https://app.reliant.dev/claim#rcl_x"},
			[]string{"OFFLINE", "Do not tell your user it is live", "deleted at 2026-11-05T14:02:00Z"},
			[]string{"Live:"}, []string{"it is live at"}},
		{"suspended though verified", "suspended", true,
			[]string{"Offline:"}, []string{"OFFLINE"}, []string{"Live:"}, []string{"it is live at"}},
		{"claimed", "claimed", false,
			[]string{"State:      claimed"}, []string{"was claimed", "forge login"},
			[]string{"Live:", "Claim link:"}, []string{"it is live at", "Give them the claim link"}},
		{"expired", "expired", false,
			[]string{"State:      expired"}, []string{"was deleted", "--claimable"},
			[]string{"Live:", "Claim link:"}, []string{"it is live at", "Give them the claim link"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := base
			res.State, res.RolloutVerified = tc.state, tc.verified
			res.NextSteps = claimNextSteps(&res)
			out := claimableToolResult(&res)
			text := out.Content[0].(*mcp.TextContent).Text
			for _, want := range tc.wantText {
				if !strings.Contains(text, want) {
					t.Errorf("text lacks %q:\n%s", want, text)
				}
			}
			for _, bad := range tc.noText {
				if strings.Contains(text, bad) {
					t.Errorf("text has %q:\n%s", bad, text)
				}
			}
			for _, want := range tc.wantNext {
				if !strings.Contains(res.NextSteps, want) {
					t.Errorf("next_steps lacks %q:\n%s", want, res.NextSteps)
				}
			}
			for _, bad := range tc.noNext {
				if strings.Contains(res.NextSteps, bad) {
					t.Errorf("next_steps has %q:\n%s", bad, res.NextSteps)
				}
			}
		})
	}
}

func TestLastDeployDocument(t *testing.T) {
	out := "cut v1.2.3\n{\n  \"ok\": false,\n  \"exit_code\": 4,\n  \"error_reason\": \"claimable_throttled\",\n" +
		"  \"next_actions\": [\"forge env deploy prod --claimable\"],\n  \"claim\": null\n}\n"
	doc, ok := lastDeployDocument(out)
	if !ok || doc.OK == nil || *doc.OK || doc.ExitCode != 4 || doc.ErrorReason != "claimable_throttled" ||
		len(doc.NextActions) != 1 {
		t.Fatalf("lastDeployDocument = %+v, %v", doc, ok)
	}
	if _, ok := lastDeployDocument("no document here\n"); ok {
		t.Error("prose parsed as a document")
	}
}

// Every refusal the claimable path raises itself ends with a command and
// carries it as next_actions (experiment 7: errors that teach agents).
func TestClaimableRefusalsEndWithTheNextCommand(t *testing.T) {
	prev := claimableNow
	claimableNow = func() time.Time { return time.Date(2026, 10, 26, 14, 2, 0, 0, time.UTC) }
	defer func() { claimableNow = prev }()
	for name, err := range map[string]error{
		"throttle":    newClaimableThrottle("prod", cloud.ClaimableThrottle{Scope: "network", RetryAfter: time.Minute}),
		"unavailable": claimableUnavailableError("prod", cloud.Endpoint{URL: "https://cp"}, []cloud.ClaimableCause{{Code: cloud.ClaimableCauseDisabled, Message: "off"}}),
		"claimed":     claimableClaimedError("prod"),
		"ineligible":  claimableIneligibleError("prod", deploy.ClaimableLimitsFallback(), []string{"2 services (1 is allowed)"}),
		"flags":       refuseClaimableFlags("prod", deployCmdFlags{dryRun: true}),
	} {
		lines := strings.Split(strings.TrimSpace(err.Error()), "\n")
		last := lines[len(lines)-1]
		if !strings.Contains(last, Name()+" env deploy prod") {
			t.Errorf("%s: last line %q names no command:\n%s", name, last, err)
		}
		facts := errorFactsOf(err)
		if len(facts.NextActions) == 0 || !strings.Contains(facts.NextActions[0], "env deploy prod") {
			t.Errorf("%s: next_actions = %v", name, facts.NextActions)
		}
	}
}

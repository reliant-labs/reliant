package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/cli/cmdutil"
)

// mcpInstructions is what an MCP client shows the model when it connects.
//
// Two jobs. The disambiguation: "forge" collides with Atlassian Forge and
// Foundry's forge, and a model that guesses wrong calls the wrong tools. And
// the front door: this server is usually where an agent working in some
// existing repo first meets forge, so it leads with what forge does for THAT
// repo — the pieces that work with no forge.yaml — and routes to the
// `benefits` tool and the `adopt` skill. The tools that need a forge project
// come last, as what codegen adds. Every claim here ships: the benefits
// registry (scope: any) is where each is checked.
const mcpInstructions = "This is Reliant Forge (github.com/reliant-labs/forge) — not Atlassian Forge and not " +
	"Foundry's forge. Forge helps the repo you are in today, in any language, without a rewrite, and it need not " +
	"build or deploy the app. Monitoring: after forge login, the CLI's `forge obs init <env>…` mints an ingest token per env " +
	"and prints the OTEL_* variables that send any app's traces, logs and metrics to dashboards, issues and alerts " +
	"at https://forge.reliantlabs.io (sign-in required). Deploy history: one `forge release record` step after the " +
	"deploy job the repo's CI already runs gives each release a deploy marker and a `forge obs check` verdict. " +
	"From there, `forge env verify` shows forge would change nothing about what runs before it drives the repo's " +
	"own deploy tooling, and `forge project new --in-place --kind app` declares its Dockerfile so forge can deploy " +
	"it. With no account at all: lint catches unsafe SQL migrations (db/migrations/*.up.sql, or the directory of " +
	"the goose, atlas, flyway, dbmate or golang-migrate setup forge.yaml names) and, in a Go module, runs " +
	"golangci-lint and flags cut traces and timezone-dependent date buckets; skills are playbooks for debugging, " +
	"testing, migrations, observability and deploys; forge/pkg (Go) and @reliantlabs/forge-web-runtime (its /node " +
	"entry ships a Node process's logs, errors and spans over OTLP) are runtime libraries you can import as they " +
	"are; deploy_claimable puts an existing Vite or Next.js app live and returns a URL plus a claim link for your " +
	"user. Start with benefits: what applies to this repo, each with its first step and the agent time to first " +
	"value to quote to your user. Then skill_load name=adopt: the survey, the estimate rubric and every entry " +
	"point. Once the repo adopts forge's codegen (a forge.yaml with services), skill_load name=forge is the entry " +
	"point, and capabilities, annotations, libraries, audit, shapes and generate work on the project. " +
	"deploy_status shows a claimable deploy again and explain_error turns any forge failure into the next " +
	"command. Every tool takes an optional dir: the repo to act on (default: the directory the server was " +
	"started in). The account's monitoring and releases (obs_status, obs_issues, obs_check, releases_list, " +
	"release_record …) are also a hosted connector with no install, https://api.reliantapi.com/forge/mcp " +
	"(sign in): skill_load name=connect sets it up for each client."

// maxMCPOutput bounds one tool result. Head and tail are both kept because
// the verdict of lint and generate is the last line.
const (
	maxMCPOutputHead = 160 << 10
	maxMCPOutputTail = 64 << 10
)

func newMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Run forge as an MCP server on stdio, so any agent harness can drive it",
		Long: `Run forge as a Model Context Protocol server on stdio.

Any MCP-capable harness (Claude Code, Codex, Cursor, ...) can then call forge's
benefits, skill, lint and introspection verbs as tools, in ANY repo, with no
memory file telling it how. The server says what forge is and what it does for
the repo the agent is in, forge project or not, and sends the model to the
benefits tool and the 'adopt' skill (the 'forge' skill in a forge project).

Tools (each but explain_error takes an optional "dir": the repo to act on;
default is the directory the server was started in, or -C):

  read-only   benefits       what forge does for this repo, with the first step
                             and the agent time to each; any repo
              capabilities   every verb, lint analyzer and marker
              annotations    the entity-authoring vocabulary
              libraries      forge/pkg and web-runtime APIs
              audit          what THIS project currently is
              shapes         where every rpc/message/table/handler is, with file:line
              skill_list     the skill catalog
              skill_search   find a skill by keyword
              skill_load     print one skill
              deploy_status  the claimable deploy of an env: URL, claim link, state
              explain_error  a forge failure's meaning and the exact next command
  mutating    lint           forge lint --quiet [--scope PATH...]
              generate       forge generate [--dry-run | --check]
              deploy_claimable
                             forge env deploy <env> --claimable: a live URL plus a
                             claim link, no account; adopts a Vite/Next.js app with
                             no forge.yaml in place first. Streams progress
                             (notifications/progress) and stops on cancel

Each tool runs the same command in-process; it does not shell out to a forge
binary, and it does not hand a project pinned to another forge over to that
forge (it reports the skew instead).

Register it. With no install, the command is "npx -y @reliantlabs/forge mcp"
(the npm shim downloads the matching forge binary on first run):

  Claude Code   claude mcp add forge -- npx -y @reliantlabs/forge mcp
                claude mcp add forge -- forge mcp
  Codex         codex mcp add forge -- forge mcp
  Cursor        {"mcpServers": {"forge": {"command": "forge", "args": ["mcp"]}}}
                in .cursor/mcp.json

docs/mcp.md has the config files in full.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return runMCP(ctx, cmd.InOrStdin(), os.Stdout)
		},
	}
}

// runMCP serves MCP over in/out until in closes or ctx ends.
//
// stdout belongs to the protocol: one stray byte on it corrupts the framing.
// Commands print to os.Stdout directly (generate does), so the protocol gets
// the real stdout and os.Stdout is pointed at stderr for the rest of the
// process; tool calls capture both streams themselves (mcpRunner.exec).
func runMCP(ctx context.Context, in io.Reader, protocolOut *os.File) error {
	os.Stdout = os.Stderr

	// Every tool call must run the forge this binary is, never exec the
	// project's pinned one: that replaces this process mid-request. And
	// each is checked against its own project's pin.
	serveRequestsUnderTheirOwnPins()

	r, err := newMCPRunner(NewRootCmd)
	if err != nil {
		return err
	}
	srv := newMCPServer(r)
	fmt.Fprintf(os.Stderr, "forge mcp: serving on stdio (project %s)\n", r.defaultDir)
	return srv.Run(ctx, newDrainingTransport(&mcp.IOTransport{
		Reader: io.NopCloser(in),
		Writer: nopWriteCloser{protocolOut},
	}, maxMCPEOFDrain))
}

// maxMCPEOFDrain bounds how long the server keeps answering requests it has
// already read once stdin has closed. Tools have no deadline of their own (a
// deploy waits on its rollout), so this is the cap that keeps a stuck call
// from holding the process open forever.
const maxMCPEOFDrain = 10 * time.Minute

// drainingTransport makes stdin EOF a graceful end of input instead of an
// abort. The SDK's read loop answers EOF by cancelling every request still in
// flight and refusing to write their responses, so a client that pipes
// initialize, initialized and tools/list and closes stdin got no output at
// all. Here EOF is held back until every call already read has been answered.
type drainingTransport struct {
	mcp.Transport
	maxDrain time.Duration
}

func newDrainingTransport(inner mcp.Transport, maxDrain time.Duration) mcp.Transport {
	return &drainingTransport{Transport: inner, maxDrain: maxDrain}
}

func (t *drainingTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	conn, err := t.Transport.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &drainingConn{Connection: conn, maxDrain: t.maxDrain, idle: make(chan struct{}), closed: make(chan struct{})}, nil
}

type drainingConn struct {
	mcp.Connection
	maxDrain time.Duration

	mu      sync.Mutex
	pending int           // calls read whose response has not been written
	idle    chan struct{} // closed-and-replaced each time pending reaches 0

	closeOnce sync.Once
	closed    chan struct{}
}

func (c *drainingConn) Read(ctx context.Context) (jsonrpc.Message, error) {
	msg, err := c.Connection.Read(ctx)
	if err != nil {
		if errors.Is(err, io.EOF) {
			c.drain(ctx)
		}
		return nil, err
	}
	if req, ok := msg.(*jsonrpc.Request); ok && req.IsCall() {
		c.mu.Lock()
		c.pending++
		c.mu.Unlock()
	}
	return msg, nil
}

func (c *drainingConn) Write(ctx context.Context, msg jsonrpc.Message) error {
	err := c.Connection.Write(ctx, msg)
	if _, ok := msg.(*jsonrpc.Response); ok {
		c.mu.Lock()
		if c.pending > 0 {
			c.pending--
		}
		if c.pending == 0 {
			close(c.idle)
			c.idle = make(chan struct{})
		}
		c.mu.Unlock()
	}
	return err
}

func (c *drainingConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return c.Connection.Close()
}

// drain blocks until every call read so far has been answered, the bound
// passes, the context ends or the connection is closed.
func (c *drainingConn) drain(ctx context.Context) {
	timer := time.NewTimer(c.maxDrain)
	defer timer.Stop()
	for {
		c.mu.Lock()
		pending, idle := c.pending, c.idle
		c.mu.Unlock()
		if pending == 0 {
			return
		}
		select {
		case <-idle:
		case <-timer.C:
			return
		case <-ctx.Done():
			return
		case <-c.closed:
			return
		}
	}
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

func newMCPServer(r *mcpRunner) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "forge",
		Title:   "Reliant Forge",
		Version: buildinfo.Version(),
	}, &mcp.ServerOptions{Instructions: mcpInstructions})
	addForgeTools(srv, r)
	return srv
}

// mcpRunner runs forge commands in-process on behalf of tool calls.
type mcpRunner struct {
	newRoot    func() *cobra.Command
	defaultDir string
	// route is how forge is mounted in the executing binary ("forge" when
	// standalone, ["forge"] under `reliant`): the per-call root is re-parented
	// under stand-ins for it so self-invocation and next-step hints keep
	// working when forge is embedded.
	route []string
	// sem serializes calls: a command's working directory, project-dir
	// override, stdio and mount route are all process-global.
	sem chan struct{}
}

func newMCPRunner(newRoot func() *cobra.Command) (*mcpRunner, error) {
	dir, err := cmdutil.ResolutionRoot()
	if err != nil {
		return nil, fmt.Errorf("resolve project directory: %w", err)
	}
	route, _ := cmdutil.CmdRouteTokens()
	return &mcpRunner{
		newRoot:    newRoot,
		defaultDir: dir,
		route:      append([]string(nil), route...),
		sem:        make(chan struct{}, 1),
	}, nil
}

func (r *mcpRunner) resolveDir(dir string) (string, error) {
	if dir == "" {
		return r.defaultDir, nil
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(r.defaultDir, dir)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("dir %q: %w", dir, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("dir %q is not a directory", dir)
	}
	return dir, nil
}

// exec runs `forge <args>` against dir and returns everything it printed to
// stdout and stderr, interleaved in order. A non-nil error is the command's own
// failure (lint findings and audit errors are failures), not a transport one.
func (r *mcpRunner) exec(ctx context.Context, dir string, args ...string) (string, error) {
	var out string
	err := r.run(ctx, dir, args, func() (*os.File, *os.File, func(), error) {
		c, err := newOutputCapture(nil)
		if err != nil {
			return nil, nil, nil, err
		}
		return c.w, c.w, func() { out = c.close() }, nil
	})
	return truncateMCPOutput(out), err
}

// execStreams is exec for a long verb whose stdout is a document (--json): it
// returns stdout apart from stderr, and hands each stderr line to onLine as it
// is printed, which is how a deploy's progress reaches the client while it
// runs.
func (r *mcpRunner) execStreams(ctx context.Context, dir string, onLine func(string), args ...string) (stdout, stderr string, err error) {
	err = r.run(ctx, dir, args, func() (*os.File, *os.File, func(), error) {
		so, err := newOutputCapture(nil)
		if err != nil {
			return nil, nil, nil, err
		}
		se, err := newOutputCapture(onLine)
		if err != nil {
			so.close()
			return nil, nil, nil, err
		}
		return so.w, se.w, func() { stdout, stderr = so.close(), se.close() }, nil
	})
	return stdout, truncateMCPOutput(stderr), err
}

// run runs `forge <args>` in-process against dir, one call at a time, with
// the process's stdio pointed at what open returns until the command ends;
// done is called after stdio is restored.
func (r *mcpRunner) run(ctx context.Context, dir string, args []string, open func() (stdout, stderr *os.File, done func(), err error)) error {
	select {
	case r.sem <- struct{}{}:
		defer func() { <-r.sem }()
	case <-ctx.Done():
		return ctx.Err()
	}

	abs, err := r.resolveDir(dir)
	if err != nil {
		return err
	}
	prevWd, err := os.Getwd()
	if err != nil {
		return err
	}
	// Code that asks os.Getwd for the project (shapes, lint) does not read -C.
	if err := os.Chdir(abs); err != nil {
		return err
	}
	defer func() { _ = os.Chdir(prevWd) }()
	// The root command installs -C as a process-wide override and leaves it.
	prevOverride, _ := cmdutil.ProjectDirOverride()
	defer func() { _ = cmdutil.SetProjectDir(prevOverride) }()

	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	defer func() { _ = devNull.Close() }()
	stdout, stderr, done, err := open()
	if err != nil {
		return err
	}

	prevIn, prevOut, prevErr := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = devNull, stdout, stderr
	runErr := r.runRoot(ctx, devNull, stdout, stderr, append([]string{"-C", abs}, args...))
	os.Stdin, os.Stdout, os.Stderr = prevIn, prevOut, prevErr
	done()
	return runErr
}

// outputCapture collects what is written to w, a pipe, and optionally hands
// each complete line to onLine as it arrives.
type outputCapture struct {
	w      *os.File
	r      *os.File
	buf    bytes.Buffer
	copied chan struct{}
}

func newOutputCapture(onLine func(string)) (*outputCapture, error) {
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	c := &outputCapture{w: pw, r: pr, copied: make(chan struct{})}
	go func() {
		defer close(c.copied)
		if onLine == nil {
			_, _ = io.Copy(&c.buf, pr)
			return
		}
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			c.buf.Write(sc.Bytes())
			c.buf.WriteByte('\n')
			onLine(sc.Text())
		}
		// A line past the scanner's limit: keep the rest without splitting.
		_, _ = io.Copy(&c.buf, pr)
	}()
	return c, nil
}

// close ends the capture and returns everything written.
func (c *outputCapture) close() string {
	_ = c.w.Close()
	<-c.copied
	_ = c.r.Close()
	return c.buf.String()
}

func (r *mcpRunner) runRoot(ctx context.Context, in io.Reader, out, errOut io.Writer, args []string) error {
	forgeRoot := r.newRoot()
	forgeRoot.SetIn(in)
	forgeRoot.SetOut(out)
	forgeRoot.SetErr(errOut)

	top := forgeRoot
	if n := len(r.route); n > 0 {
		top = &cobra.Command{Use: filepath.Base(os.Args[0]), SilenceErrors: true, SilenceUsage: true}
		parent := top
		for _, tok := range r.route[:n-1] {
			c := &cobra.Command{Use: tok}
			parent.AddCommand(c)
			parent = c
		}
		parent.AddCommand(forgeRoot)
		top.SetIn(in)
		top.SetOut(out)
		top.SetErr(errOut)
		args = append(append([]string(nil), r.route...), args...)
	}
	top.SetArgs(args)
	return top.ExecuteContext(ctx)
}

func truncateMCPOutput(s string) string {
	if len(s) <= maxMCPOutputHead+maxMCPOutputTail {
		return s
	}
	cut := len(s) - maxMCPOutputHead - maxMCPOutputTail
	return s[:maxMCPOutputHead] +
		fmt.Sprintf("\n\n[... %d bytes omitted; narrow the request (a scope, a filter, a package) to see them ...]\n\n", cut) +
		s[len(s)-maxMCPOutputTail:]
}

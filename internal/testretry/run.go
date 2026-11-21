package testretry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Schema identifies the artifact passed to trusted cleanup after the gate.
const Schema = "merge-test-retry/v1"

// Config is supplied by the CI runner, never deserialized from a test log.
type Config struct {
	Root, Repository, Base, Head, RunID, JSONPath, ReportPath string
	PR, Parallel                                              int
	NoBuildVCS, Echo                                          bool
	Timeout                                                   time.Duration
	Packages                                                  []string
	Stdout, Stderr                                            io.Writer
}

// Test is a single verified retry and its tracked source identity.
type Test struct {
	Package          string `json:"package"`
	Test             string `json:"test"`
	File             string `json:"file"`
	SourceBlob       string `json:"source_blob"`
	FailureSignature string `json:"failure_signature"`
	RetrySHA256      string `json:"retry_sha256"`
}

// Report is a content-bound record, not permission to delete code. A consumer
// must tie it to trusted job metadata and re-derive its evidence and source.
type Report struct {
	Schema        string `json:"schema"`
	Repository    string `json:"repo"`
	Base          string `json:"base"`
	Head          string `json:"head"`
	Tree          string `json:"tree"`
	RunID         string `json:"run"`
	PR            int    `json:"pr"`
	InitialSHA256 string `json:"initial_sha256"`
	Tests         []Test `json:"tests"`
}

var (
	repoPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	runPattern  = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,100}$`)
	goFlag      = regexp.MustCompile(`^-(trimpath(=(true|false))?|buildvcs=(true|false)|mod=(readonly|vendor)|skip=\S+)$`)
)

// Run owns both initial and retry processes, with the same executable,
// directory, resolved toolchain and inherited environment. A retry never
// replaces the original red unless all targets ran once, passed, and left an
// atomic report. This command is for a credential-free CI runner only.
func Run(ctx context.Context, cfg Config) error {
	if !repoPattern.MatchString(cfg.Repository) || !shaPattern.MatchString(cfg.Base) || !shaPattern.MatchString(cfg.Head) ||
		!runPattern.MatchString(cfg.RunID) || cfg.PR < 0 || cfg.Parallel < 0 || cfg.Parallel > 256 || cfg.Timeout <= 0 || cfg.Timeout > 10*time.Minute {
		return errors.New("invalid merge test gate arguments")
	}
	for _, arg := range cfg.Packages {
		if arg == "" || strings.HasPrefix(arg, "-") {
			return errors.New("package operands cannot contain Go test options")
		}
	}
	if cfg.Stdout == nil {
		cfg.Stdout = io.Discard
	}
	if cfg.Stderr == nil {
		cfg.Stderr = io.Discard
	}
	goExe, err := exec.LookPath("go")
	if err != nil {
		return err
	}
	root, err := filepath.Abs(cfg.Root)
	if err != nil {
		return err
	}
	cfg.Root = root
	env := os.Environ()
	settings := []string{"test", "-short", "-json", "-timeout", cfg.Timeout.String()}
	if cfg.Parallel > 0 {
		settings = append(settings, "-p", fmt.Sprint(cfg.Parallel))
	}
	if cfg.NoBuildVCS {
		settings = append(settings, "-buildvcs=false")
	}
	if err := freshPath(cfg.ReportPath); err != nil {
		return err
	}
	// Read the effective flags even when Go takes them from its config file.
	// An unreadable or unsupported configuration still gets its ordinary
	// run; it just cannot turn a failure green.
	flags, flagsErr := goSetting(ctx, cfg.Root, goExe, env, "env", "GOFLAGS")
	eligibleFlags := flagsErr == nil && ordinaryFlags(flags) && os.Getenv("GOEXPERIMENT") == "" && os.Getenv("GOCOVERDIR") == ""
	if eligibleFlags {
		// Both processes use the same flags even if the Go env file changes.
		// A nonempty whitespace value explicitly overrides an empty flag list.
		if flags == "" {
			flags = " "
		}
		env = append(slices.DeleteFunc(env, func(value string) bool { return strings.HasPrefix(value, "GOFLAGS=") }), "GOFLAGS="+flags)
	}
	initialArgs := append(append([]string(nil), settings...), cfg.Packages...)
	noisy, red := invoke(ctx, cfg, goExe, env, initialArgs, cfg.JSONPath, cfg.Echo)
	if red == nil {
		return nil
	}
	var status *exec.ExitError
	if noisy || !errors.As(red, &status) || status.ExitCode() != 1 {
		return red
	}
	if !eligibleFlags {
		return fmt.Errorf("original failure stands (not an ordinary non-race invocation): %w", red)
	}
	proof, err := recoverGate(ctx, cfg, goExe, env, settings)
	if err != nil {
		return fmt.Errorf("original failure stands (%v): %w", err, red)
	}
	for _, field := range proof.Tests {
		_, _ = fmt.Fprintf(cfg.Stderr, "retried:%s (%s)\n", field.Test, field.Package)
	}
	return nil
}

func recoverGate(ctx context.Context, cfg Config, goExe string, env, settings []string) (Report, error) {
	module, err := goSetting(ctx, cfg.Root, goExe, env, "list", "-m", "-f", "{{.Path}}")
	if err != nil || !packagePattern.MatchString(module) {
		return Report{}, errors.New("could not resolve a single Go module")
	}
	selected, err := packageSet(module, cfg.Packages)
	if err != nil {
		return Report{}, err
	}
	gitExe, err := exec.LookPath("git")
	if err != nil {
		return Report{}, err
	}
	tree, err := treeState(ctx, cfg, gitExe)
	if err != nil {
		return Report{}, err
	}
	firstSHA, err := fileDigest(cfg.JSONPath)
	if err != nil {
		return Report{}, err
	}
	failed, err := judge(cfg.JSONPath, selected, nil)
	if err != nil {
		return Report{}, err
	}
	report := Report{Schema: Schema, Repository: cfg.Repository, Base: cfg.Base, Head: cfg.Head, Tree: tree,
		RunID: cfg.RunID, PR: cfg.PR, InitialSHA256: firstSHA}
	// Prove eligibility for every failure before running any retry.
	for _, item := range failed {
		if err := trackedTestInputs(ctx, cfg, gitExe, module, item.pkg); err != nil {
			return Report{}, err
		}
		source, err := locate(ctx, cfg, gitExe, module, cfg.Head, item.identity)
		if err != nil {
			return Report{}, err
		}
		previous, err := locate(ctx, cfg, gitExe, module, cfg.Base, item.identity)
		if err != nil || source != previous {
			return Report{}, errors.New("test source is not unchanged on gate base")
		}
		report.Tests = append(report.Tests, Test{Package: item.pkg, Test: item.name, File: source.file,
			SourceBlob: source.blob, FailureSignature: item.signature})
	}
	for i, item := range failed {
		if now, err := treeState(ctx, cfg, gitExe); err != nil || now != tree {
			return Report{}, errors.New("gate tree changed before retry")
		}
		retry := fmt.Sprintf("%s.retry-%d.ndjson", cfg.ReportPath, i)
		retryArgs := append(append([]string(nil), settings...), "-count=1", "-run", "^"+regexp.QuoteMeta(item.name)+"$", item.pkg)
		_, _ = fmt.Fprintf(cfg.Stderr, "merge-test-retry: checking %s %s once\n", item.pkg, item.name)
		if noisy, err := invoke(ctx, cfg, goExe, env, retryArgs, retry, false); err != nil || noisy {
			return Report{}, errors.New("target test failed again or emitted stderr")
		}
		if _, err := judge(retry, map[string]bool{item.pkg: true}, &item.identity); err != nil {
			return Report{}, err
		}
		report.Tests[i].RetrySHA256, err = fileDigest(retry)
		if err != nil {
			return Report{}, err
		}
	}
	if now, err := treeState(ctx, cfg, gitExe); err != nil || now != tree {
		return Report{}, errors.New("gate tree changed on retry")
	}
	if sha, err := fileDigest(cfg.JSONPath); err != nil || sha != firstSHA {
		return Report{}, errors.New("original test evidence changed on retry")
	}
	return report, writeReport(cfg.ReportPath, report)
}

func ordinaryFlags(flags string) bool {
	for _, flag := range strings.Fields(flags) {
		if !goFlag.MatchString(flag) {
			return false
		}
	}
	return true
}

func goSetting(ctx context.Context, root, goExe string, env []string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, goExe, args...)
	command.Dir, command.Env = root, env
	raw, err := command.Output()
	return strings.TrimSpace(string(raw)), err
}

func invoke(ctx context.Context, cfg Config, goExe string, env, args []string, path string, echo bool) (bool, error) {
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return false, err
	}
	defer func() { _ = out.Close() }()
	command := exec.CommandContext(ctx, goExe, args...)
	command.Dir, command.Env, command.Stdout = cfg.Root, env, out
	if echo {
		command.Stdout = io.MultiWriter(out, cfg.Stdout)
	}
	stderr := &observedWriter{target: cfg.Stderr}
	command.Stderr = stderr
	err = command.Run()
	if e := out.Close(); err == nil {
		err = e
	}
	return stderr.written, err
}

type observedWriter struct {
	target  io.Writer
	written bool
}

func (w *observedWriter) Write(p []byte) (int, error) {
	w.written = w.written || len(p) != 0
	return w.target.Write(p)
}

func fileDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, maxEvidence+1))
	if err != nil {
		return "", err
	}
	if n > maxEvidence {
		return "", errors.New("test evidence exceeds digest budget")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func freshPath(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("report path must be absolute")
	}
	_, err := os.Lstat(path)
	if !errors.Is(err, os.ErrNotExist) {
		return errors.New("report path must not exist")
	}
	return nil
}

func writeReport(path string, report Report) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".retry-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := json.NewEncoder(tmp).Encode(report); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Link(tmp.Name(), path)
}

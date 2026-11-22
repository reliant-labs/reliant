// Copyright (c) 2026 Reliant Labs
package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/selfupdate"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/reliant-labs/reliant/internal/cliupdate"
	"github.com/reliant-labs/reliant/internal/version"
)

// swappedFlag is the hidden flag `reliant update` re-executes the new binary
// with: the new binary confirms itself (selfupdate's revert watchdog) and says
// what it updated from. It is how the swap ends, never something a person types.
const swappedFlag = "swapped"

func newUpdateCmd() *cobra.Command {
	var (
		check, yes, jsonOut, swapped bool
		channel, from                string
	)
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update this reliant to the newest signed release",
		Long: `Replaces this reliant binary with the newest release on its channel, after
checking the release's Sigstore signature: release.yml at a release tag of
reliant-labs/reliant must have signed it, for that tag. An unsigned or
mis-signed release is refused, and nothing is replaced.

The channel is the one this build came from: alpha for a release candidate,
latest otherwise. --channel picks one. A client on alpha takes the newer of
alpha and latest, so a stable release that follows an RC train reaches it.

Only a reliant that nothing else owns updates itself: one you unpacked from a
release archive into a directory you can write. A reliant that the desktop
app, Homebrew, a system package or a Reliant cloud machine installed is left
alone, and the command says what updates it instead. Nothing is replaced on
Windows yet; --check works there.

The new binary is checked before anything changes (it must run 'version'),
and the old one is kept under ~/.reliant/upgrade/previous. After the swap the
new binary starts and confirms itself; one that does not is put back. A
running daemon keeps its version until it restarts, or updates itself when it
was started with --auto-update.

--check reports what an update would do, and changes nothing.

  reliant update --check
  reliant update --yes
  reliant update --channel alpha --json --yes`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts := cliupdate.Options{Channel: channel, Managed: cliupdate.ManagedHost(isManagedMachine())}
			if swapped {
				return finishSwap(cmd.OutOrStdout(), opts, from, jsonOut)
			}
			if jsonOut && !check && !yes {
				return errors.New("--json needs --check or --yes: there is no prompt to answer in JSON mode")
			}
			if channel != "" && !cliupdate.ValidChannel(channel) {
				return fmt.Errorf("--channel %q: use %q or %q", channel, cliupdate.ChannelLatest, cliupdate.ChannelAlpha)
			}
			if !verbose {
				opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
			}
			u := updateRun{
				out:         cmd.OutOrStdout(),
				in:          cmd.InOrStdin(),
				interactive: term.IsTerminal(int(os.Stdin.Fd())),
				check:       check,
				yes:         yes,
				json:        jsonOut,
				opts:        opts,
				goos:        runtime.GOOS,
				detect:      func() (selfupdate.Install, error) { return cliupdate.Detect(opts.Managed) },
				apply:       execSwap,
			}
			return u.run(cmd.Context())
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "Report whether an update is available and whether this install can take it; change nothing")
	cmd.Flags().BoolVar(&yes, "yes", false, "Update without asking")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print the report as JSON (with --check, or with --yes)")
	cmd.Flags().StringVar(&channel, "channel", "", "Release channel: latest or alpha (default: the channel this build came from)")
	cmd.Flags().BoolVar(&swapped, swappedFlag, false, "Internal: run by the new binary at the end of an update")
	cmd.Flags().StringVar(&from, "from", "", "Internal: the version an update replaced")
	_ = cmd.Flags().MarkHidden(swappedFlag)
	_ = cmd.Flags().MarkHidden("from")
	return cmd
}

// updateRun is one `reliant update`, its I/O and its two irreversible steps
// (finding the install's owner, and the swap itself) injected for tests.
type updateRun struct {
	out         io.Writer
	in          io.Reader
	interactive bool
	check       bool
	yes         bool
	json        bool
	opts        cliupdate.Options
	goos        string

	detect func() (selfupdate.Install, error)
	// apply arms the swap of staged and replaces this process with the new
	// binary run as argv; it returns only on failure.
	apply func(ctx context.Context, u *selfupdate.Updater, staged selfupdate.Staged, argv []string) error
}

// updateReport is what --check (and --json) reports.
type updateReport struct {
	Current         updateBuild   `json:"current"`
	Channel         string        `json:"channel"`
	Release         *updateBuild  `json:"release,omitempty"`
	UpdateAvailable bool          `json:"update_available"`
	Reason          string        `json:"reason,omitempty"`
	Install         updateInstall `json:"install"`
	// Updated is set by the new binary once the swap is confirmed.
	Updated bool   `json:"updated,omitempty"`
	From    string `json:"from,omitempty"`
}

type updateBuild struct {
	Version        string `json:"version"`
	Tag            string `json:"tag,omitempty"`
	CommitTimeUnix int64  `json:"commit_time_unix,omitempty"`
}

type updateInstall struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
	// SelfUpdatable is whether `reliant update` may replace it.
	SelfUpdatable bool   `json:"self_updatable"`
	Owner         string `json:"owner,omitempty"`
	Reason        string `json:"reason,omitempty"`
	// Advice is how to update it: `reliant update`, or what owns it.
	Advice string `json:"advice"`
}

func (r updateRun) run(ctx context.Context) error {
	cfg, err := cliupdate.Config(r.opts)
	if err != nil {
		return err
	}
	install, err := r.detect()
	if err != nil {
		return fmt.Errorf("could not tell who installed this reliant: %w", err)
	}
	cfg.Shutdown = func() {} // a CLI run has nothing to tear down before the swap
	updater, err := selfupdate.New(cfg)
	if err != nil {
		return err
	}

	rel, decision, err := updater.Check(ctx)
	if err != nil {
		return fmt.Errorf("could not read the %s release channel (%s): %w", cfg.Channel, cliupdate.ManifestURL, err)
	}
	report := newUpdateReport(cfg, rel, decision, install)

	if r.check {
		return r.print(report)
	}
	if !report.UpdateAvailable {
		return r.print(report)
	}
	if !install.SelfOwned() {
		return fmt.Errorf("reliant %s is available, but this reliant (%s) is not one `reliant update` may replace: %s. Update it this way instead: %s",
			report.Release.Version, install.Path, install.Reason, cliupdate.Advice(install))
	}
	if r.goos == "windows" {
		return fmt.Errorf("reliant %s is available, but `reliant update` cannot replace a running reliant.exe on Windows yet: download https://github.com/reliant-labs/reliant/releases/latest/download/reliant_windows_%s.tar.gz and replace %s with the reliant.exe inside", report.Release.Version, runtime.GOARCH, install.Path)
	}
	if !r.yes {
		if err := r.confirm(report); err != nil {
			return err
		}
	}

	if !r.json {
		fmt.Fprintf(r.out, "Downloading reliant %s and checking its signature...\n", report.Release.Version)
	}
	staged, err := updater.Prepare(ctx, rel)
	if err != nil {
		return fmt.Errorf("reliant %s was not installed, and nothing was replaced: %w", report.Release.Version, err)
	}
	if !r.json {
		fmt.Fprintf(r.out, "Signature verified. Replacing %s...\n", install.Path)
	}
	argv := []string{os.Args[0], "update", "--" + swappedFlag, "--from", cfg.Current.Version}
	if r.json {
		argv = append(argv, "--json")
	}
	if err := r.apply(ctx, updater, staged, argv); err != nil {
		return fmt.Errorf("reliant %s was not installed; %s is unchanged: %w", report.Release.Version, install.Path, err)
	}
	return nil
}

func newUpdateReport(cfg selfupdate.Config, rel selfupdate.Release, d selfupdate.Decision, in selfupdate.Install) updateReport {
	r := updateReport{
		Current: updateBuild{Version: cfg.Current.Version, CommitTimeUnix: unixOrZero(cfg.Current.CommitTime)},
		Channel: cfg.Channel,
		// The default policy stages every newer release it is asked about:
		// anything but Skip is an update.
		UpdateAvailable: d.Action != selfupdate.Skip,
		Reason:          d.Reason,
		Install: updateInstall{
			Path: in.Path, Kind: in.Kind.String(), SelfUpdatable: in.SelfOwned(),
			Owner: in.Owner, Reason: in.Reason, Advice: cliupdate.Advice(in),
		},
	}
	if rel.Tag != "" {
		r.Release = &updateBuild{Version: rel.Version, Tag: rel.Tag, CommitTimeUnix: unixOrZero(rel.CommitTime)}
	}
	return r
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func (r updateRun) print(report updateReport) error {
	if r.json {
		enc := json.NewEncoder(r.out)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	_, err := io.WriteString(r.out, updateReportText(report))
	return err
}

// updateReportText is the human form of the report.
func updateReportText(r updateReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "reliant %s, channel %s\n", r.Current.Version, r.Channel)
	if r.Release != nil {
		fmt.Fprintf(&b, "Newest release: %s", r.Release.Version)
		if r.Release.CommitTimeUnix > 0 {
			fmt.Fprintf(&b, " (built %s)", time.Unix(r.Release.CommitTimeUnix, 0).UTC().Format("2006-01-02"))
		}
		b.WriteString("\n")
	}
	switch {
	case !r.UpdateAvailable:
		fmt.Fprintf(&b, "Up to date (%s).\n", r.Reason)
	case r.Install.SelfUpdatable:
		fmt.Fprintf(&b, "An update is available: run `reliant update`.\n")
	default:
		fmt.Fprintf(&b, "An update is available. This reliant (%s) is %s: %s.\nUpdate it this way: %s\n", r.Install.Path, r.Install.Kind, r.Install.Reason, r.Install.Advice)
	}
	return b.String()
}

// confirm asks before the swap; it refuses to guess with no terminal.
func (r updateRun) confirm(report updateReport) error {
	if !r.interactive {
		return errors.New("not updating without --yes: there is no terminal to confirm on")
	}
	fmt.Fprintf(r.out, "Update reliant %s to %s (channel %s)? [y/N] ", report.Current.Version, report.Release.Version, report.Channel)
	line, err := bufio.NewReader(r.in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return nil
	}
	return errors.New("update cancelled; nothing was changed")
}

// execSwap arms the swap and execs the new binary with argv, the hidden
// --swapped run that confirms it. ExecPending execs os.Args, so argv is put
// there first; it returns only on failure, with the old binary in place.
func execSwap(ctx context.Context, u *selfupdate.Updater, staged selfupdate.Staged, argv []string) error {
	if err := u.Request(ctx, staged, selfupdate.Decision{Action: selfupdate.Apply, Reason: "reliant update"}); err != nil {
		return err
	}
	os.Args = argv
	return u.ExecPending()
}

// finishSwap is the new binary's first act after `reliant update` swapped it
// in: it confirms itself, so selfupdate's watchdog does not put the previous
// binary back, and reports the update.
func finishSwap(w io.Writer, opts cliupdate.Options, from string, jsonOut bool) error {
	stateDir := opts.StateDir
	if stateDir == "" {
		dir, err := selfupdate.DefaultStateDir(cliupdate.Binary)
		if err != nil {
			return err
		}
		stateDir = dir
	}
	pending := selfupdate.Boot(selfupdate.BootOptions{Binary: cliupdate.Binary, StateDir: stateDir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err := pending.Confirm(); err != nil {
		return fmt.Errorf("reliant %s is installed but could not record that it started: %w", version.Version, err)
	}
	if jsonOut {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(updateReport{
			Current: updateBuild{Version: version.Version, CommitTimeUnix: version.CommitTimeUnix()},
			Updated: true,
			From:    from,
		})
	}
	fmt.Fprintf(w, "Updated reliant %s to %s.\n", from, version.Version)
	fmt.Fprintln(w, "A daemon that is already running keeps its version until it restarts (reliant daemon stop, then reliant daemon start).")
	return nil
}

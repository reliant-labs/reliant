package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/pkg/hostslot"
)

// newSlotCmd builds `forge slot`: the host-wide heavy-job semaphore
// (pkg/hostslot) as a command, so a Taskfile line or a shell queues its heavy
// work behind the same slots `forge generate`, `forge lint` and `forge ci
// gate` take.
func newSlotCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "slot",
		Short: "Queue heavy work behind host-wide slots: at most N heavy jobs at once on this machine",
		Long: "A host-wide counting semaphore for heavy work (builds, test lanes, codegen, full lints).\n\n" +
			"On a shared machine, past a handful of concurrent heavy jobs every job slows down — measured,\n" +
			"`go build ./...` went from ~45s to ~130s beside 8 others — so queueing them finishes sooner\n" +
			"than running them all at once. A slot is an OS file lock: a crashed holder releases it.\n\n" +
			"Slots per class default to min(CPUs / 2, memory GiB / 8), read from the cgroup limit when\n" +
			"there is one, at least 1. FORGE_HEAVY_SLOTS=<n> overrides it; FORGE_HEAVY_SLOTS=off turns\n" +
			"the limit off. FORGE_SLOTS_DIR relocates the slot files (default: the user cache dir).\n\n" +
			"`forge generate`, `forge lint` and the heavy checks of `forge ci gate` take a slot on\n" +
			"their own. Anything running inside a slot passes it to its children (FORGE_SLOT_HELD),\n" +
			"so a nested heavy command never waits on its own parent.",
	}
	cmd.AddCommand(newSlotRunCmd(), newSlotStatusCmd())
	return cmdutil.StrictGroup(cmd)
}

func newSlotRunCmd() *cobra.Command {
	var (
		class string
		slots int
	)
	cmd := &cobra.Command{
		Use:   "run [--class heavy] -- <command> [args...]",
		Short: "Run a command holding a host slot, waiting while every slot is held",
		Long: "Runs <command> once a slot of --class is free, holding the slot until the command\n" +
			"exits, and exits with the command's status. While it waits it prints who holds the\n" +
			"slots, and repeats that every 30s. Ctrl-C while waiting gives up (exit 130).\n\n" +
			"Already inside a slot of the class — a parent ran under `forge slot run`, or a gate\n" +
			"check — it runs the command at once: a child never queues behind its own parent.",
		Example: "  forge slot run -- go test -race ./...\n" +
			"  forge slot run --class heavy -- npm run build\n" +
			"  FORGE_HEAVY_SLOTS=2 forge slot run -- task test",
		Args:         cobra.MinimumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInSlot(cmd.Context(), class, slots, args, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	// Everything after the command name belongs to the command.
	cmd.Flags().SetInterspersed(false)
	cmd.Flags().StringVar(&class, "class", hostslot.Heavy, "Slot class")
	cmd.Flags().IntVar(&slots, "slots", 0, "Slots in the class (default: FORGE_<CLASS>_SLOTS, else derived from the machine)")
	return cmd
}

// runInSlot is `forge slot run`. The command runs as a child, not via exec
// into it: the slot's lock is close-on-exec, so it is held by THIS process,
// and a background process the command leaves behind cannot keep the slot
// after the command itself has finished.
func runInSlot(ctx context.Context, class string, slots int, argv []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	waitCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	slot, err := hostslot.Acquire(waitCtx, class, hostslot.Options{
		Slots:   slots,
		Out:     stderr,
		Label:   strings.Join(argv, " "),
		Subtree: true,
		Env:     os.Environ(),
	})
	interrupted := waitCtx.Err() != nil && ctx.Err() == nil
	stop()
	if err != nil {
		if interrupted {
			return exitCodeError{code: 130, msg: "interrupted while waiting for a " + class + " slot"}
		}
		return err
	}
	defer func() { _ = slot.Release() }()
	if slot.Waited() >= time.Second {
		fmt.Fprintf(stderr, "▶ forge: %s after %s\n", slot, slot.Waited().Round(time.Second))
	}

	c := exec.Command(argv[0], argv[1:]...)
	c.Env = append(os.Environ(), slot.Env()...)
	c.Stdin, c.Stdout, c.Stderr = stdin, stdout, stderr
	if err := c.Start(); err != nil {
		code := 126
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			code = 127
		}
		return exitCodeError{code: code, msg: err.Error()}
	}
	// The child shares this process group, so a terminal's Ctrl-C reaches it
	// directly; forwarding covers a signal sent to this pid alone (a task
	// runner stopping its command).
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case s := <-sigs:
				_ = c.Process.Signal(s)
			case <-done:
				return
			}
		}
	}()
	err = c.Wait()
	signal.Stop(sigs)
	close(done)
	if err == nil {
		return nil
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return err
	}
	code := ee.ExitCode()
	if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		code = 128 + int(ws.Signal())
	}
	if code <= 0 {
		code = 1
	}
	return exitCodeError{code: code, msg: fmt.Sprintf("%s exited with status %d", argv[0], code)}
}

func newSlotStatusCmd() *cobra.Command {
	var (
		class   string
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:          "status",
		Short:        "Show a slot class's capacity, why, who holds its slots and who waits",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rep, err := hostslot.Status(class, hostslot.Options{Env: os.Environ()})
			if err != nil {
				return err
			}
			if jsonOut {
				return writeSlotStatusJSON(cmd.OutOrStdout(), rep)
			}
			writeSlotStatus(cmd.OutOrStdout(), rep)
			return nil
		},
	}
	cmd.Flags().StringVar(&class, "class", hostslot.Heavy, "Slot class")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print the status as JSON")
	return cmd
}

func writeSlotStatus(w io.Writer, rep hostslot.Report) {
	c := rep.Capacity
	fmt.Fprintf(w, "%s slots: %s — %s overrides\n", c.Class, c, hostslot.SlotsEnv(c.Class))
	fmt.Fprintf(w, "machine:  %s\n", c.Machine)
	fmt.Fprintf(w, "dir:      %s\n", rep.Dir)
	if c.Unlimited {
		fmt.Fprintln(w, "the limit is off: nothing waits")
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "\nheld (%d", len(rep.Holders))
	if !c.Unlimited {
		fmt.Fprintf(tw, " of %d", c.Slots)
	}
	fmt.Fprintln(tw, "):")
	for _, h := range rep.Holders {
		fmt.Fprintf(tw, "  slot %d\tpid %d\t%s\t%s\n", h.Slot+1, h.PID, age(h.Since), h.Command)
	}
	fmt.Fprintf(tw, "waiting (%d):\n", len(rep.Waiting))
	for _, h := range rep.Waiting {
		fmt.Fprintf(tw, "  \tpid %d\t%s\t%s\n", h.PID, age(h.Since), h.Command)
	}
	_ = tw.Flush()
}

func age(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return time.Since(t).Round(time.Second).String()
}

func writeSlotStatusJSON(w io.Writer, rep hostslot.Report) error {
	type holder struct {
		Slot    int       `json:"slot,omitempty"`
		PID     int       `json:"pid"`
		Command string    `json:"command"`
		Since   time.Time `json:"since"`
	}
	out := struct {
		Class     string   `json:"class"`
		Slots     int      `json:"slots"`
		Unlimited bool     `json:"unlimited"`
		Source    string   `json:"source"`
		Machine   string   `json:"machine"`
		Dir       string   `json:"dir"`
		Holders   []holder `json:"holders"`
		Waiting   []holder `json:"waiting"`
	}{
		Class: rep.Capacity.Class, Slots: rep.Capacity.Slots, Unlimited: rep.Capacity.Unlimited,
		Source: rep.Capacity.Source, Machine: rep.Capacity.Machine.String(), Dir: rep.Dir,
		Holders: []holder{}, Waiting: []holder{},
	}
	for _, h := range rep.Holders {
		out.Holders = append(out.Holders, holder{Slot: h.Slot + 1, PID: h.PID, Command: h.Command, Since: h.Since})
	}
	for _, h := range rep.Waiting {
		out.Waiting = append(out.Waiting, holder{PID: h.PID, Command: h.Command, Since: h.Since})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

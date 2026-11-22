// Command reexec is the real binary reexec_test.go builds several times
// (-X main.version, -X main.mode) to prove an upgrade end to end: stage from
// a fake release server, swap, exec in place, confirm or revert.
//
// Every start prints one line the test reads:
//
//	boot pid=<pid> version=<v> swapped=<bool> reverted=<v>
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/reliant-labs/forge/pkg/selfupdate"
)

var (
	version = "dev"
	// mode is what a swapped-in binary does: "confirm", "hang" (never
	// confirms, so the watchdog reverts it) or "crash" (exits before
	// confirming, so the boot-loop guard reverts it).
	mode = "confirm"
)

type acceptAll struct{}

// Verify stands in for the Sigstore verifier (F2): it only checks that the
// files it was handed exist.
func (acceptAll) Verify(_ context.Context, _ selfupdate.Release, files map[string]string) error {
	for name, path := range files {
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

type applyAll struct{}

func (applyAll) Decide(context.Context, selfupdate.Build, selfupdate.Release) selfupdate.Decision {
	return selfupdate.Decision{Action: selfupdate.Apply, Urgency: selfupdate.Normal}
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println(version)
		return
	}
	api := flag.String("api", "", "fake GitHub API base URL")
	caFile := flag.String("ca", "", "PEM of the fake server's certificate")
	state := flag.String("state", "", "state directory")
	within := flag.Duration("confirm-within", time.Minute, "")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	p := selfupdate.Boot(selfupdate.BootOptions{Binary: "reexec", StateDir: *state, Logger: log})
	fmt.Printf("boot pid=%d version=%s swapped=%v reverted=%s\n", os.Getpid(), version, p.Swapped, p.Reverted)

	if p.Swapped {
		switch mode {
		case "confirm":
			if err := p.Confirm(); err != nil {
				fail("confirm", err)
			}
			fmt.Println("confirmed")
			return
		case "crash":
			os.Exit(3)
		case "hang":
			// Never confirm. The watchdog's exec of the previous binary
			// replaces this process, which is the event the test waits
			// for; nothing here is timed. A signal (the test's context
			// expiring) is the only other way out.
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			<-ctx.Done()
			fail("hang", fmt.Errorf("the watchdog never reverted"))
		}
	}
	if p.Reverted != "" {
		fmt.Println("reverted", p.Reverted)
		return
	}

	pem, err := os.ReadFile(*caFile)
	if err != nil {
		fail("ca", err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	u, err := selfupdate.New(selfupdate.Config{
		Binary:        "reexec",
		Current:       selfupdate.Build{Version: version},
		Source:        selfupdate.GitHubSource{Repo: "o/r", APIBaseURL: *api, Client: client},
		Verifier:      acceptAll{},
		Policy:        applyAll{},
		Shutdown:      cancel,
		StateDir:      *state,
		ConfirmWithin: *within,
		HTTPClient:    client,
		Logger:        log,
	})
	if err != nil {
		fail("new", err)
	}
	rel, d, err := u.Check(ctx)
	if err != nil {
		fail("check", err)
	}
	if d.Action != selfupdate.Apply {
		fmt.Println("skip:", d.Reason)
		return
	}
	staged, err := u.Prepare(ctx, rel)
	if err != nil {
		fail("prepare", err)
	}
	if err := u.Request(ctx, staged, d); err != nil {
		fail("request", err)
	}
	<-ctx.Done() // the app's teardown would run here
	fail("exec", u.ExecPending())
}

func fail(step string, err error) {
	fmt.Printf("error %s: %v\n", step, err)
	os.Exit(2)
}

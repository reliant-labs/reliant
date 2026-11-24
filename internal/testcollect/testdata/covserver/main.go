// Command covserver is a stand-in for an instrumented forge server, for
// TestCollect_RealCoverBuild, which builds it with `go build -cover`: it
// serves serverkit's /debug/coverage on a loopback port, prints the address,
// counts /hit and /other, and returns from main on SIGTERM, so Go flushes its counters
// to GOCOVERDIR the way a draining server does.
package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/reliant-labs/forge/pkg/serverkit"
)

func main() {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	mux := http.NewServeMux()
	mux.Handle(serverkit.CoveragePath, serverkit.CoverageHandler())
	mux.HandleFunc("/hit", func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprintln(w, "hit") })
	mux.HandleFunc("/other", func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprintln(w, "other") })
	srv := &http.Server{Handler: mux}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()
	fmt.Println("ADDR", ln.Addr().String())
	_ = srv.Serve(ln)
}

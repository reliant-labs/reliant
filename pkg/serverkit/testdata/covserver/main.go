// Command covserver serves serverkit.CoverageHandler on a loopback port and
// prints it, for TestCoverageHandler_RealCoverBuild, which builds it with
// `go build -cover`. A test binary cannot stand in: Go writes a test
// binary's coverage meta-data only at exit, so runtime/coverage has nothing
// to serve while it runs.
package main

import (
	"fmt"
	"net"
	"net/http"
	"os"

	"github.com/reliant-labs/forge/pkg/serverkit"
)

func main() {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("ADDR", ln.Addr().String())
	mux := http.NewServeMux()
	mux.Handle(serverkit.CoveragePath, serverkit.CoverageHandler())
	_ = http.Serve(ln, mux) // until the test kills it
}

// Command schemagen writes the published manifest JSON Schemas into the
// schema package directory. Run it with `go generate` there.
package main

import (
	"fmt"
	"os"

	"github.com/reliant-labs/reliant/internal/integrations/manifest/schema"
)

func main() {
	if err := schema.Write(".", schema.ProtoGoFile); err != nil {
		fmt.Fprintln(os.Stderr, "schemagen:", err)
		os.Exit(1)
	}
}

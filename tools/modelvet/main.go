// Command modelvet runs the model serialization rule with go vet's per-package
// complete type information. Use scripts/vet.sh, or go vet -vettool=<binary>.
package main

import (
	"github.com/reliant-labs/reliant/internal/analysis/modelserialization"
	"golang.org/x/tools/go/analysis/unitchecker"
)

func main() { unitchecker.Main(modelserialization.Analyzer) }

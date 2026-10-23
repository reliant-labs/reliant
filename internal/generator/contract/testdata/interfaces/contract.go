package interfaces

import (
	"context"
	"io"
)

// Result is a same-package struct return type. The mock fallback for
// methods returning Result must use the composite literal "Result{}".
type Result struct {
	Value string
}

// Debugger is a same-package interface return type. The mock fallback
// for methods returning Debugger must use "nil" — composite literals
// are not valid for interface types.
type Debugger interface {
	Step() error
	Close() error
}

// Service exercises a mix of struct returns, local-interface returns,
// and well-known cross-package interface returns to verify zeroValue
// emits compilable code for each shape.
type Service interface {
	// Struct return: must produce "Result{}".
	GetResult(ctx context.Context) (Result, error)

	// Local-interface return: must produce "nil" (not "Debugger{}").
	NewDebugger(ctx context.Context) (Debugger, error)

	// Single-return local interface (no error): must produce "nil".
	BareDebugger() Debugger

	// Cross-package interface from the allow-list: must produce "nil".
	OpenReader(ctx context.Context) (io.Reader, error)
}

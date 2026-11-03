package zerovalues

import (
	"context"
	"time"
)

// LocalStruct is a same-package struct value return.
type LocalStruct struct {
	Name string
	N    int
}

// Service exercises the various return shapes zeroValue must handle
// so a generated mock_gen.go compiles cleanly.
type Service interface {
	Struct(ctx context.Context) (LocalStruct, error)
	PtrStruct(ctx context.Context) (*LocalStruct, error)
	Slice(ctx context.Context) ([]LocalStruct, error)
	Map(ctx context.Context) (map[string]LocalStruct, error)
	Basic(ctx context.Context) (string, int, bool, float64, error)
	NoError(ctx context.Context) LocalStruct
	Channel(ctx context.Context) (<-chan int, error)
	SendChannel(ctx context.Context) (chan<- int, error)
	Func(ctx context.Context) (func(int) int, error)
	Any(ctx context.Context) (any, error)
	IfaceLiteral(ctx context.Context) (interface{}, error)
	// A cross-package NAMED SCALAR: `time.Duration{}` is not a valid
	// composite literal, so the zero value must not be guessed as `T{}`.
	Interval(ctx context.Context) (time.Duration, error)
}

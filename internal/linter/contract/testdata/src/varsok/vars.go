package varsok

import (
	"errors"
	"fmt"
)

// Sentinel errors are allowed.
var ErrNotFound = errors.New("not found")
var ErrInvalid = fmt.Errorf("invalid input")

// Compile-time interface checks are allowed.
type Doer interface {
	Do()
}

type myDoer struct{}

func (m *myDoer) Do() {}

var _ Doer = (*myDoer)(nil)

// Unexported vars are fine.
var internalState = "ok"

// Exported funcs are fine (not vars).
func NewService() {}

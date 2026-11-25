package requireprivateescape // want "package requireprivateescape has exported methods but no contract.go"

import "io"

// impl is unexported but escapes through New's result, so a caller outside
// the package can call every exported method on it.
type impl struct{}

func (i *impl) Close() error { return nil }

var _ io.Closer = (*impl)(nil)

// New returns the concrete type.
func New() *impl { return &impl{} }

package dbbrowse

import (
	"errors"
	"fmt"
)

// ErrInvalidRequest is wrapped by every error [Compile] returns: the request
// is wrong, and retrying it unchanged cannot help. A transport maps it to its
// invalid-argument code with one errors.Is. The finer sentinels below wrap it,
// so errors.Is(err, ErrInvalidRequest) holds for each of them too.
var ErrInvalidRequest = errors.New("invalid browse request")

var (
	// ErrUnknownTable: the request named a table the role cannot read, or
	// that does not exist. The two are deliberately indistinguishable.
	ErrUnknownTable = fmt.Errorf("%w: unknown table", ErrInvalidRequest)
	// ErrUnknownColumn: a column, filter or sort named a column the table
	// does not have or the role cannot read.
	ErrUnknownColumn = fmt.Errorf("%w: unknown column", ErrInvalidRequest)
	// ErrUnknownOp: a filter's operator is not one of the [Op] constants.
	ErrUnknownOp = fmt.Errorf("%w: unknown filter operator", ErrInvalidRequest)
)

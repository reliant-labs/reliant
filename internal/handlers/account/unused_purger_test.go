// Copyright (c) 2025 Reliant Labs
package account_test

import (
	"context"
	"errors"

	"github.com/reliant-labs/reliant/internal/accountpurge"
)

// unusedPurger is the Purger of a test whose rows are refused before the purge is reached; a call
// to it is a failure.
type unusedPurger struct{}

var errPurgerReached = errors.New("the request reached the purger")

func (*unusedPurger) Preview(context.Context, string) (accountpurge.Counts, error) {
	return accountpurge.Counts{}, errPurgerReached
}

func (*unusedPurger) Purge(context.Context, string) (int64, error) { return 0, errPurgerReached }

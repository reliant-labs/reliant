// Package lister reads a handler's errors in process without importing the
// handler: Getter is declared here, where it is consumed, and satisfied by
// *viaiface.Service. unwrap reads a *connect.Error, so a bare svcerr kind
// from viaiface would read differently here (reliant's
// triggertools.unwrapConnect, reaching internal/handlers/trigger).
package lister

import (
	"context"
	"errors"

	"connectrpc.com/connect"
)

// Getter is what Lister needs.
type Getter interface {
	Get(ctx context.Context, id string) (string, error)
}

// Lister names things.
type Lister struct{ g Getter }

// New builds a Lister.
func New(g Getter) *Lister { return &Lister{g: g} }

// Name gets id's name.
func (l *Lister) Name(ctx context.Context, id string) (string, error) {
	n, err := l.g.Get(ctx, id)
	if err != nil {
		return "", unwrap(err)
	}
	return n, nil
}

func unwrap(err error) error {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return errors.New(ce.Message())
	}
	return err
}

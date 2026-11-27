// Copyright (c) 2026 Reliant Labs

package email

import (
	"context"
	"errors"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/gtm"
)

// ErrNoGmailAccount is a send whose Gmail account cannot be named: no usable
// Gmail connection, or one that does not say its address
// (FailedPrecondition). No card is shown for an email whose sender the
// person cannot see.
var ErrNoGmailAccount = svcerr.FailedPrecondition("could not tell which Gmail account would send this email; connect or reconnect Gmail, then ask again. Nothing was sent")

// Account is the Gmail account a send goes out from.
type Account struct {
	// ConnectionID is the connection the send is made with.
	ConnectionID string
	// Address is the mailbox's own address, as the connection recorded it.
	Address string
}

// Accounts names the Gmail account a send as an actor goes out from, for
// the approval card's From row and to pin the send to it.
type Accounts interface {
	// GmailAccount is connectionID's account when it is set (the actor's
	// own connection, or one of the actor's org), else the actor's default
	// Gmail connection's: the same choice connauth makes for the actor's
	// run. A connection that is missing, not the actor's, not Gmail, not
	// active, or that recorded no address is ErrNoGmailAccount.
	GmailAccount(ctx context.Context, actor gtm.Actor, connectionID string) (Account, error)
}

// ConnectionStore reads saved connections' metadata, never their secrets:
// the reads connections.Resolver resolves a call's connection with.
// connectionvault's Service (and *db.Repo) satisfy it.
type ConnectionStore interface {
	// GetConnection is userID's own live connection id.
	GetConnection(ctx context.Context, userID, id string) (*core.Connection, error)
	// GetOwnedConnection is owner's live connection id.
	GetOwnedConnection(ctx context.Context, owner core.ConnectionOwner, id string) (*core.Connection, error)
	// DefaultConnection is userID's default live connection to integrationID.
	DefaultConnection(ctx context.Context, userID, integrationID string) (*core.Connection, error)
}

// NewConnectionAccounts is Accounts over the saved connections.
//
// It follows connections.Resolver's lookup: a named connection is the
// actor's own, else their org's; none named is the actor's default Gmail
// connection. Org membership is not checked again here: every caller has
// passed the GTM gate, which admits only a live member of actor.OrgID. The
// address is the connection's sender id (Gmail's probe records the
// mailbox's emailAddress there), else its account label, and must be a bare
// ASCII address.
func NewConnectionAccounts(store ConnectionStore) Accounts { return connectionAccounts{store: store} }

type connectionAccounts struct{ store ConnectionStore }

func (a connectionAccounts) GmailAccount(ctx context.Context, actor gtm.Actor, connectionID string) (Account, error) {
	if a.store == nil || actor.UserID == "" {
		return Account{}, ErrNoGmailAccount
	}
	var conn *core.Connection
	var err error
	if connectionID != "" {
		conn, err = a.store.GetConnection(ctx, actor.UserID, connectionID)
		if connectionNotFound(err) && actor.OrgID != "" {
			conn, err = a.store.GetOwnedConnection(ctx, core.OrgConnectionOwner(actor.OrgID), connectionID)
		}
	} else {
		conn, err = a.store.DefaultConnection(ctx, actor.UserID, gtm.GmailIntegration)
	}
	switch {
	case connectionNotFound(err):
		return Account{}, ErrNoGmailAccount
	case err != nil:
		return Account{}, svcerr.WithCause(svcerr.Unavailable("could not read the Gmail connection; try again"), err)
	case conn == nil || conn.IntegrationID != gtm.GmailIntegration || conn.Status != core.ConnectionStatusActive:
		return Account{}, ErrNoGmailAccount
	}
	addr := ""
	if conn.SenderID != nil {
		addr = *conn.SenderID
	}
	if addr == "" && conn.AccountLabel != nil {
		addr = *conn.AccountLabel
	}
	if checkAddress(addr) != nil {
		return Account{}, ErrNoGmailAccount
	}
	return Account{ConnectionID: conn.ID, Address: addr}, nil
}

func connectionNotFound(err error) bool {
	return err != nil && (errors.Is(err, core.ErrConnectionNotFound) || svcerr.IsNotFound(err))
}

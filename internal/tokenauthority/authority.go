package tokenauthority

import (
	"errors"
	"fmt"

	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/controlplane"
)

// ErrInactive reports a token that is not live (unknown, revoked, expired,
// malformed, or not an `rlat_` at all).
var ErrInactive = accesstokenclient.ErrInactive

// DepsFor is the Deps for a deployment hosted by cp (the zero Endpoint is
// self-hosted), with db for the self-hosted store.
func DepsFor(cp controlplane.Endpoint, db orm.Context) Deps {
	return Deps{
		ControlPlaneURL:       cp.URL,
		InternalServiceSecret: cp.InternalServiceSecret,
		DB:                    db,
	}
}

// New returns the deployment's ONE Authority and which store it is. It
// returns the Authority contract rather than a concrete store: choosing the
// store is this constructor's whole job, so no caller may depend on which one
// it got.
//
// Not observed here: New only SELECTS a store. The I/O belongs to the store it
// returns (accesstokenclient, itself marked forge:constructor, or LocalStore),
// so wrapping this selector would double-count every call.
//
// forge:no-observe
func New(deps Deps) (Authority, Mode, error) {
	if deps.ControlPlaneURL != "" {
		if deps.InternalServiceSecret == "" {
			return nil, "", errors.New("tokenauthority: RELIANT_CONTROL_PLANE_URL is set but INTERNAL_SERVICE_SECRET is not; " +
				"a hosted reliant validates every machine credential through control-plane and cannot do so unauthenticated")
		}
		secret := deps.InternalServiceSecret
		return accesstokenclient.NewInstrumented(accesstokenclient.Deps{
			BaseURL: deps.ControlPlaneURL,
			Sign:    func() (string, error) { return auth.SignInternalServiceToken(secret) },
		}), ModeControlPlane, nil
	}
	if deps.DB == nil {
		return nil, "", errors.New("tokenauthority: no control-plane configured and no database for the self-hosted token store")
	}
	return NewLocalStore(deps.DB), ModeLocal, nil
}

// IsNotFound reports whether err means "no such token for this user" in
// either store.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound) || accesstokenclient.IsNotFound(err)
}

// IsInvalidGrant reports whether err is a caller-correctable grant error.
func IsInvalidGrant(err error) bool {
	if errors.Is(err, fat.ErrInvalidGrant) || errors.Is(err, fat.ErrEscalation) {
		return true
	}
	var rpc *accesstokenclient.RPCError
	return errors.As(err, &rpc) && (rpc.Code == "invalid_argument" || rpc.Code == "permission_denied")
}

// Describe renders an Authority error for a log line without leaking a token.
func Describe(err error) string { return fmt.Sprint(err) }

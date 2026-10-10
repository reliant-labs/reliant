package bootstrap

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// ForeignDaemonIDReason is the stable machine-readable reason the gateway
// attaches, as svcerr.ReasonHeader metadata, when it refuses a registration
// because the asserted daemon id belongs to a different user.
//
// It exists so a daemon can tell "this saved identity belongs to another
// account" from "this credential is bad". They have opposite remedies — forget
// the saved id, versus get a new token — and describing the first as the
// second sent a user with two accounts on one laptop to re-mint a token that
// was fine.
//
// The reason names the condition, never the owner: who holds the id is the
// other account's business.
const ForeignDaemonIDReason = "daemon_id_owned_by_another_user"

// foreignDaemonIDMessage is the refusal's text. IsForeignDaemonIDError also
// matches it alone: the gateway and the daemon ship separately, and a daemon
// talking to a gateway that predates ForeignDaemonIDReason must still recover
// rather than wedge until that gateway is redeployed.
const foreignDaemonIDMessage = "daemon id is owned by another user"

// NewForeignDaemonIDError is the gateway's refusal of a daemon id owned by
// another user.
func NewForeignDaemonIDError() *connect.Error {
	err := connect.NewError(connect.CodePermissionDenied, errors.New(foreignDaemonIDMessage))
	err.Meta().Set(svcerr.ReasonHeader, ForeignDaemonIDReason)
	return err
}

// IsForeignDaemonIDError reports whether err, or anything it wraps, is the
// gateway refusing a daemon id because another user owns it.
func IsForeignDaemonIDError(err error) bool {
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodePermissionDenied {
		return false
	}
	if connectErr.Meta().Get(svcerr.ReasonHeader) == ForeignDaemonIDReason {
		return true
	}
	return connectErr.Message() == foreignDaemonIDMessage
}

// SetAsideDaemonID moves this instance's daemon-id file out of the way and
// returns where it went, so the next registration asserts no id and the
// gateway mints a fresh one.
//
// Renamed, never deleted. The file holds another account's machine identity,
// and it is the only local evidence of what happened; a human comparing the
// set-aside file with the new one can see exactly which id was refused.
// The suffix sorts by time and is made unique, so repeated recoveries never
// overwrite an earlier one.
func SetAsideDaemonID(dataDir string) (string, error) {
	if strings.TrimSpace(dataDir) == "" {
		return "", fmt.Errorf("cannot set aside daemon id: no data directory")
	}
	src := DaemonIDPath(dataDir)
	base := src + ".foreign-" + time.Now().UTC().Format("20060102T150405Z")
	dst := base
	for i := 2; ; i++ {
		if _, err := os.Lstat(dst); errors.Is(err, os.ErrNotExist) {
			break
		}
		dst = fmt.Sprintf("%s-%d", base, i)
	}
	if err := os.Rename(src, dst); err != nil {
		return "", fmt.Errorf("setting aside daemon id %s: %w", src, err)
	}
	return dst, nil
}

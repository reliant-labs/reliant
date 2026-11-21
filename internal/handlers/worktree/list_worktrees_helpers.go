package worktree

import (
	"fmt"
	"strconv"

	"github.com/reliant-labs/forge/pkg/orm"
)

const defaultListWorktreesPageSize = 100

// The token is an offset into a stable (main first, last_active, id) ordering.
// It is opaque to clients so the ordering can move to a keyset later.
func encodeWorktreePageToken(offset int) string {
	return orm.EncodeCursor(strconv.Itoa(offset))
}

func decodeWorktreePageToken(token string) (int, error) {
	if token == "" {
		return 0, nil
	}
	raw, err := orm.DecodeCursor(token)
	if err != nil {
		return 0, err
	}
	offset, err := strconv.Atoi(raw)
	if err != nil || offset < 0 {
		return 0, fmt.Errorf("invalid offset")
	}
	return offset, nil
}

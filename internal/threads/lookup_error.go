// Copyright (c) 2025 Reliant Labs
package threads

import (
	"fmt"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// lookupFailed wraps the error of a by-key read that the caller needs to
// succeed. It says the row is missing ("not found", "does not exist") only
// when it genuinely is — svcerr.NotFound, which every store miss carries — and
// otherwise says the read failed.
//
// The wording is load-bearing, not cosmetic. The activity layer treats "not
// found" and "does not exist" as terminal: retrying cannot create a missing
// row. Before this, every failure was labelled a miss, so on 2026-11-02 a
// 28-second database restart — PgBouncer answering "server login has been
// failing (server_login_retry)" — reached the workflow as "thread … does not
// exist - must be created before saving messages", was classified terminal, and
// failed three agent runs for good (reliant#2100). A database that did not
// answer said nothing about the row.
func lookupFailed(err error, missing, failed string) error {
	if svcerr.IsNotFound(err) {
		return fmt.Errorf("%s: %w", missing, err)
	}
	return fmt.Errorf("%s: %w", failed, err)
}

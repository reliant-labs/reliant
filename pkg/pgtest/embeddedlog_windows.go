//go:build windows

package pgtest

import "errors"

// ownOpenPaths: Windows refuses to unlink a file any process holds open, and
// the library never closes its log, so a log cannot be removed while its
// instance's process lives. Reporting an error makes StartEmbedded claim
// nothing; the stale sweep reclaims the file once no process holds it.
func ownOpenPaths() ([]string, error) {
	return nil, errors.New("pgtest: own open files are not enumerated on windows")
}

// A package with no exported methods of its own. Its test file declares a
// fake with exported methods, which must not make the package look like it
// has a behavioral surface that needs a contract.go. No finding expected.
package requiretestonly

import "time"

func now() time.Time { return time.Now() }

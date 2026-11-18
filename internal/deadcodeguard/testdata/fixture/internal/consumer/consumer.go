// Package consumer exists to prove the scan is WHOLE-PROGRAM. CrossWritten is
// declared in one package and written only from this one; a per-package
// analyzer would report it, and would be wrong.
package consumer

import "deadcodeguardfixture/internal/phantom"

// Configure writes a field of another package's struct.
func Configure(c *phantom.Cross) {
	c.CrossWritten = "set from another package"
}

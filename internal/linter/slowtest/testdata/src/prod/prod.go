// Package prod is production code: it does not import "testing", so the
// analyzer does not read through it. What its functions exec at runtime is
// the timing guard's to measure.
package prod

import "os/exec"

func Deploy() error {
	return exec.Command("kubectl", "apply", "-f", "-").Run()
}

// Large handler file fixture — over the test threshold.
// The body declares more than 10 non-blank, non-comment source lines so
// the test (which uses threshold=10) fires the rule.

package billing

import (
	"errors"
	"fmt"
)

func A() error {
	if x := 1; x == 1 {
		return errors.New("a")
	}
	return nil
}

func B() error {
	if x := 2; x == 2 {
		return errors.New("b")
	}
	return nil
}

func C() error {
	if x := 3; x == 3 {
		return errors.New("c")
	}
	return nil
}

func D() error {
	if x := 4; x == 4 {
		return errors.New("d")
	}
	return fmt.Errorf("ok")
}

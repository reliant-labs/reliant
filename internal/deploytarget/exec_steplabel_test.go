package deploytarget

import (
	"strings"
	"testing"
)

func TestStepLabel(t *testing.T) {
	if got := stepLabel("/work/shop/frontends/web", "npm", []string{"run", "build"}); got != "web: npm run build" {
		t.Errorf("got %q", got)
	}
	if got := stepLabel("", "kubectl", []string{"apply", "-f", "-"}); got != "kubectl apply -f -" {
		t.Errorf("got %q", got)
	}
	long := stepLabel("", "docker", []string{"build", strings.Repeat("x", 100)})
	if r := []rune(long); len(r) != 60 || !strings.HasSuffix(long, "…") {
		t.Errorf("a long command is not bounded: %q", long)
	}
}

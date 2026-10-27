package daemon

import (
	"context"
	"fmt"
	"github.com/stretchr/testify/require"
	"runtime"
	"testing"
)

func TestRunCommandPreservesPayloadExit124125AndNormalStatus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell fixture; native platform contract is in osutil")
	}
	for _, code := range []int{0, 7, 124, 125} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			ctx := context.Background()
			result, err := NewLocalClient().RunCommand(ctx, &RunCommandRequest{Command: fmt.Sprintf("printf ordinary; exit %d", code), WorkingDir: t.TempDir(), TimeoutMs: 3000})
			require.NoError(t, err)
			require.Equal(t, code, result.ExitCode)
			require.Equal(t, "ordinary", result.Stdout)
			require.False(t, result.OutputIncomplete)
		})
	}
}

package daemonruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/osutil"
	"github.com/stretchr/testify/require"
	"runtime"
	"testing"
)

func TestExecRunPayload124125NeverBecomesSupervisorControl(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixture; native execution contract in osutil")
	}
	for _, code := range []int{0, 7, 124, 125} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			d := cancelFixture(t)
			finish, err := d.trackRunExecution("request", "chat", "root", func() {})
			require.NoError(t, err)
			ctx := osutil.WithCancellationUncertainty(context.Background(), func(e error) { d.runExecutions["request"].uncertainty = e })
			payload, _ := json.Marshal(daemon.RunCommandRequest{Command: fmt.Sprintf("printf ordinary; exit %d", code), WorkingDir: t.TempDir(), TimeoutMs: 3000})
			raw, err := handleExecRun(ctx, payload)
			require.NoError(t, err)
			var result daemon.CommandResult
			require.NoError(t, json.Unmarshal(raw, &result))
			require.Equal(t, code, result.ExitCode)
			require.False(t, result.OutputIncomplete)
			require.Equal(t, "ordinary", result.Stdout)
			finish()
			ack, err := d.cancelOwnedRun(context.Background(), []byte(`{"chat_id":"chat","root_first_run_id":"root"}`))
			require.NoError(t, err, "payload 125 is not uncertain tree shutdown")
			require.JSONEq(t, `{"stopped":true}`, string(ack))
		})
	}
}

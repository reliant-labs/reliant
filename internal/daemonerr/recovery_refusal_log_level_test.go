// Copyright (c) 2026 Reliant Labs
package daemonerr

import (
	"context"
	"log/slog"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/observe"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/reliant-labs/reliant/internal/errclass"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// levelRecorder keeps the level of every record logged through it.
type levelRecorder struct{ levels []slog.Level }

func (r *levelRecorder) Enabled(context.Context, slog.Level) bool { return true }
func (r *levelRecorder) Handle(_ context.Context, rec slog.Record) error {
	r.levels = append(r.levels, rec.Level)
	return nil
}
func (r *levelRecorder) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *levelRecorder) WithGroup(string) slog.Handler      { return r }

// The other prod lines of reliant#2429: forge.topology answered through
// daemoncmd.Dispatch (DispatchConnectError) logged ERROR twice per refusal,
// once in daemoncmd's component chain and once at the RPC edge. Handlers on
// Error's path did the same. A refusal while the daemon finalizes a
// worktree's recovery logs at INFO in both layers, its code unchanged; a
// genuine daemon failure stays an ERROR in both.
func TestRecoveryRefusalLogsBelowErrorInEveryLayer(t *testing.T) {
	for name, tc := range map[string]struct {
		text      string
		wantLevel slog.Level
	}{
		"recovery finalizing": {text: toolexec.ErrRecoveryFinalizing.Error(), wantLevel: slog.LevelInfo},
		"genuine failure":     {text: "forge topology: exit status 2: panic: nil map", wantLevel: slog.LevelError},
	} {
		reported := toolexec.DaemonReportedFailure("forge.topology", tc.text)
		for mapping, mapped := range map[string]error{
			"DispatchConnectError": DispatchConnectError("forge.topology", reported),
			"Error":                Error("daemon command forge.topology", reported),
		} {
			t.Run(name+"/"+mapping, func(t *testing.T) {
				assert.Equal(t, connect.CodeUnavailable, svcerr.Code(mapped), "the wire code is unchanged")
				assert.Contains(t, mapped.Error(), tc.text)

				// The component chain forge generates for daemoncmd: no
				// project classifier.
				component := &levelRecorder{}
				_ = observe.LogMiddleware(slog.New(component), slog.LevelDebug).WrapComponent(context.Background(),
					"daemoncmd.Dispatch", func(context.Context) error { return mapped })

				// The RPC edge, as internal/grpc/user_chain.go wires it.
				edge := &levelRecorder{}
				handler := observe.LoggingInterceptor(slog.New(edge), observe.WithErrorClassifier(errclass.Library)).WrapUnary(
					func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) { return nil, mapped })
				_, err := handler(context.Background(), connect.NewRequest(&emptypb.Empty{}))
				require.Error(t, err)

				assert.Equal(t, []slog.Level{tc.wantLevel}, edge.levels, "rpc failed")
				if mapping == "DispatchConnectError" {
					assert.Equal(t, []slog.Level{tc.wantLevel}, component.levels, "daemoncmd.Dispatch")
				}
			})
		}
	}
}

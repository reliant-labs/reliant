package observe

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	connectv2 "connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/emptypb"
)

// A handler that panics ends its watchdog: the panic unwinds past the
// Deadline slot to a recovery interceptor outside it, which answers
// INTERNAL. Before, nothing ended the watchdog, so every recovered panic was
// followed by "rpc still in flight" reports for a call already answered,
// repeating for the life of the process.
func TestDeadlineWatchdogEndsWhenTheHandlerPanics(t *testing.T) {
	for name, wrap := range map[string]func(Deadline, func(context.Context)) func(){
		"connect v1": func(d Deadline, handler func(context.Context)) func() {
			call := d.WrapUnary(func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
				handler(ctx)
				return nil, nil
			})
			return func() { _, _ = call(context.Background(), connect.NewRequest(&emptypb.Empty{})) }
		},
		"connect v2": func(d Deadline, handler func(context.Context)) func() {
			call := d.InterceptorV2()(func(ctx context.Context, _ connectv2.Spec, _ connectv2.ServerStream) error {
				handler(ctx)
				return nil
			})
			return func() {
				_ = call(context.Background(), connectv2.Spec{Procedure: "/p.v1.S/M", StreamType: connectv2.StreamTypeUnary}, nil)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			reports := make(chan SlowCall, 8)
			d := Deadline{SlowAfter: time.Nanosecond, SlowRepeat: time.Hour, OnSlow: func(_ context.Context, c SlowCall) { reports <- c }}
			// The handler panics once the watchdog has reported it in
			// flight, so that report cannot race the call's end.
			call := wrap(d, func(context.Context) {
				<-reports
				panic("slice bounds out of range")
			})
			func() {
				defer func() {
					if recover() == nil {
						t.Fatal("the panic did not unwind past the Deadline slot")
					}
				}()
				call()
			}()
			select {
			case done := <-reports:
				if !done.Done || done.Err == nil {
					t.Errorf("report after the panic = %+v, want the call's end, failed", done)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("the watchdog never saw the panicked call end")
			}
		})
	}
}

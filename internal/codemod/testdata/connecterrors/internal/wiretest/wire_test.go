// Package wiretest prints what a real Connect client reads for every site in
// the corpus. The codemod test runs it before and after the rewrite and
// requires identical lines: that is the "never changes a code or a message"
// proof, over the wire and through svcerr.Interceptor exactly as forge's
// chain builders install it.
package wiretest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"example.com/cescratch/internal/app"
	"example.com/cescratch/internal/handlers/asserted"
	"example.com/cescratch/internal/handlers/blocked"
	"example.com/cescratch/internal/handlers/pingsvc"
	"example.com/cescratch/internal/handlers/reader"
	"example.com/cescratch/internal/handlers/suiteas"
	"example.com/cescratch/internal/handlers/testifyas"
	"example.com/cescratch/internal/handlers/testonly"
	"example.com/cescratch/internal/handlers/things"
	"example.com/cescratch/internal/handlers/viaiface"
	"example.com/cescratch/internal/handlers/wired"
	"example.com/cescratch/internal/interceptors"
	"example.com/cescratch/internal/lister"
)

// call serves one unary handler returning handlerErr — behind
// svcerr.Interceptor when intercepted — and returns what the client read.
func call(t *testing.T, intercepted bool, handlerErr error) string {
	t.Helper()
	const procedure = "/wire.v1.Svc/Do"
	var opts []connect.HandlerOption
	if intercepted {
		opts = append(opts, connect.WithInterceptors(svcerr.Interceptor()))
	}
	mux := http.NewServeMux()
	mux.Handle(procedure, connect.NewUnaryHandler(procedure,
		func(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
			return nil, handlerErr
		}, opts...))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := connect.NewClient[emptypb.Empty, emptypb.Empty](srv.Client(), srv.URL+procedure)
	_, err := client.CallUnary(context.Background(), connect.NewRequest(&emptypb.Empty{}))
	// An importer's test reading a CLIENT's error: it stays a
	// *connect.Error whatever the handler returned, so this errors.As does
	// not keep the codemod off bare kinds in the packages it imports.
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return fmt.Sprintf("not a connect error: %v", err)
	}
	return fmt.Sprintf("code=%s message=%q reason=%q", ce.Code(), ce.Message(), ce.Meta().Get(svcerr.ReasonHeader))
}

func TestWire(t *testing.T) {
	var lines []string
	for name, site := range things.Sites {
		lines = append(lines, "handler things."+name+" "+call(t, true, site()))
	}
	lines = append(lines,
		"handler reader.Lookup "+call(t, true, reader.Lookup()),
		"handler blocked.Offline "+call(t, true, blocked.Offline()),
		"handler asserted.Refuse "+call(t, true, asserted.Refuse()),
		"handler testifyas.Approve "+call(t, true, testifyas.Approve()),
		"handler suiteas.Deny "+call(t, true, suiteas.Deny()),
		"handler testonly.Missing "+call(t, true, testonly.Missing()),
		"handler wired.Busy "+call(t, true, wired.Busy()),
		"handler viaiface.Get "+call(t, true, viaifaceErr()),
		"handler pingsvc.Ping "+call(t, true, pingsvcErr()),
		// An interceptor runs outside svcerr.Interceptor.
		"outside interceptors.Deny "+call(t, false, interceptors.Deny()),
		fmt.Sprintf("in-process reader.IsMissing=%v", reader.IsMissing(reader.Lookup())),
		fmt.Sprintf("in-process app.Probe=%v", app.Probe()),
		// lister reads viaiface's error through Getter, in process: what it
		// returns must not change.
		fmt.Sprintf("in-process lister.Name=%s", listerName()),
	)
	sort.Strings(lines)
	for _, l := range lines {
		fmt.Println("WIRE " + l)
	}
}

func viaifaceErr() error {
	_, err := (&viaiface.Service{}).Get(context.Background(), "w")
	return err
}

func pingsvcErr() error {
	_, err := (&pingsvc.Service{}).Ping(context.Background(), connect.NewRequest(&emptypb.Empty{}))
	return err
}

func listerName() string {
	_, err := lister.New(&viaiface.Service{}).Name(context.Background(), "w")
	return fmt.Sprintf("%T %v", err, err)
}

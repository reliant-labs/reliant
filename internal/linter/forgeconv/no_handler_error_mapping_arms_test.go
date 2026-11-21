package forgeconv

import (
	"path/filepath"
	"strings"
	"testing"
)

// The body-shape signal used to fire on ANY function that called errors.Is
// twice and connect.NewError once, anywhere. reliant's PollCopilotDeviceAuth
// switches on four Copilot device-flow sentinels to choose a RESPONSE status
// and builds connect errors only for unrelated failures; it was reported as
// "switches on 4 sentinels". The rule now counts a sentinel only when the arm
// it selects produces a connect error.

const armsHeader = `package settings

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
)

var (
	ErrPending = errors.New("pending")
	ErrSlowDown = errors.New("slow down")
	ErrExpired = errors.New("expired")
	ErrDenied  = errors.New("denied")
)

type Status int

const (
	StatusPending Status = iota
	StatusExpired
	StatusDenied
	StatusError
)

type Resp struct{ Status Status }

func poll(context.Context, string) error { return nil }
func save(context.Context) error        { return nil }
`

func lintArmsFile(t *testing.T, body string) []Finding {
	t.Helper()
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "internal", "handlers", "settings")
	must(t, mkdirAll(dir))
	must(t, writeFile(filepath.Join(dir, "settings.go"), armsHeader+body))
	res, err := LintHandlerErrorMapping(tmp)
	if err != nil {
		t.Fatalf("LintHandlerErrorMapping: %v", err)
	}
	return findingsForRule(res.Findings, "forgeconv-no-handler-error-mapping")
}

// Sentinels mapped to response statuses, with connect.NewError elsewhere in
// the same handler: not a mapper.
func TestHandlerErrorMapping_SentinelsToResponseStatusDoNotFire(t *testing.T) {
	for name, body := range map[string]string{
		"switch": `
func PollDeviceAuth(ctx context.Context, code string) (*Resp, error) {
	if code == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("device_code is required"))
	}
	if err := poll(ctx, code); err != nil {
		switch {
		case errors.Is(err, ErrPending), errors.Is(err, ErrSlowDown):
			return &Resp{Status: StatusPending}, nil
		case errors.Is(err, ErrExpired):
			return &Resp{Status: StatusExpired}, nil
		case errors.Is(err, ErrDenied):
			return &Resp{Status: StatusDenied}, nil
		default:
			return &Resp{Status: StatusError}, nil
		}
	}
	if err := save(ctx); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save"))
	}
	return &Resp{}, nil
}
`,
		"if chain": `
func PollDeviceAuth(ctx context.Context, code string) (*Resp, error) {
	err := poll(ctx, code)
	if errors.Is(err, ErrPending) {
		return &Resp{Status: StatusPending}, nil
	}
	if errors.Is(err, ErrExpired) {
		return &Resp{Status: StatusExpired}, nil
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return &Resp{}, nil
}
`,
	} {
		t.Run(name, func(t *testing.T) {
			if got := lintArmsFile(t, body); len(got) != 0 {
				t.Fatalf("sentinels mapped to response statuses were reported as an error mapper: %+v", got)
			}
		})
	}
}

// Sentinels mapped to connect errors, in each spelling a mapper takes: still
// reported, with the count of sentinels that map.
func TestHandlerErrorMapping_SentinelsToConnectErrorsFire(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		arms string
	}{
		"switch returning NewError, inside an rpc": {arms: "3", body: `
func PollDeviceAuth(ctx context.Context, code string) (*Resp, error) {
	if err := poll(ctx, code); err != nil {
		switch {
		case errors.Is(err, ErrPending), errors.Is(err, ErrSlowDown):
			return nil, connect.NewError(connect.CodeUnavailable, err)
		case errors.Is(err, ErrDenied):
			return nil, connect.NewError(connect.CodePermissionDenied, err)
		case errors.Is(err, ErrExpired):
			return &Resp{Status: StatusExpired}, nil
		}
	}
	return &Resp{}, nil
}
`},
		"if chain returning NewError": {arms: "2", body: `
func classify(err error) error {
	if errors.Is(err, ErrDenied) {
		return connect.NewError(connect.CodePermissionDenied, err)
	}
	if errors.Is(err, ErrExpired) {
		return connect.NewError(connect.CodeDeadlineExceeded, err)
	}
	return err
}
`},
		"code picked by the switch, one NewError after it": {arms: "2", body: `
func classify(err error) error {
	code := connect.CodeInternal
	switch {
	case errors.Is(err, ErrDenied):
		code = connect.CodePermissionDenied
	case errors.Is(err, ErrExpired):
		code = connect.CodeDeadlineExceeded
	}
	return connect.NewError(code, err)
}
`},
		"code mapper": {arms: "2", body: `
func codeOf(err error) connect.Code {
	switch {
	case errors.Is(err, ErrDenied):
		return connect.CodePermissionDenied
	case errors.Is(err, ErrExpired):
		return connect.CodeDeadlineExceeded
	}
	return connect.CodeUnknown
}
`},
	} {
		t.Run(name, func(t *testing.T) {
			got := lintArmsFile(t, tc.body)
			if len(got) != 1 {
				t.Fatalf("want one finding for a sentinel → connect mapper, got %+v", got)
			}
			if !strings.Contains(got[0].Message, "maps "+tc.arms+" sentinel(s) to connect errors") {
				t.Errorf("message should count the %s mapped sentinels: %s", tc.arms, got[0].Message)
			}
		})
	}
}

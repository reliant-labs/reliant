// Copyright (c) 2025 Reliant Labs
package daemoncmd

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeSender struct {
	reply []byte
	err   error
}

func (f fakeSender) SendDaemonCommand(context.Context, string, string, []byte, int32) ([]byte, error) {
	return f.reply, f.err
}

// A request or reply this process cannot encode is an Internal fault whose
// client text names the command and withholds the encoder's own words (they can
// quote the payload); the encoder's error stays the logged cause.
func TestDispatch_LocalCodecFailuresAreInternalAndRedacted(t *testing.T) {
	t.Run("request does not marshal", func(t *testing.T) {
		err := New(Deps{Sender: fakeSender{}}).Dispatch(context.Background(), "u", "fs.list", make(chan int), nil, 1000)
		require.Error(t, err)
		assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
		assert.Equal(t, "marshal fs.list request failed", svcerr.ToConnect(err).Message())
		assert.NotNil(t, svcerr.Cause(err), "the marshal error is the logged cause")
	})
	t.Run("reply does not unmarshal", func(t *testing.T) {
		var resp struct{ N int }
		err := New(Deps{Sender: fakeSender{reply: []byte(`{"N":"secret-payload"}`)}}).Dispatch(context.Background(), "u", "fs.list", struct{}{}, &resp, 1000)
		require.Error(t, err)
		assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
		assert.Equal(t, "unmarshal fs.list response failed", svcerr.ToConnect(err).Message())
		assert.NotNil(t, svcerr.Cause(err), "the unmarshal error is the logged cause")
	})
}

// A transport failure keeps the code daemonerr classifies it as.
func TestDispatch_SenderFailureKeepsItsDispatchCode(t *testing.T) {
	err := New(Deps{Sender: fakeSender{err: errors.New("no daemon connected")}}).Dispatch(context.Background(), "u", "fs.list", struct{}{}, nil, 1000)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
}

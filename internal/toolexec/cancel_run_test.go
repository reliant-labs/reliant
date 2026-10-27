package toolexec

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCancelFanoutOfflineFirstDoesNotBlockExecutingMachine(t *testing.T) {
	reached := make(chan struct{})
	err := cancelOnMachines(context.Background(), []string{"offline", "executing"}, func(ctx context.Context, id string) ([]byte, error) {
		if id == "offline" {
			select {
			case <-reached:
			case <-time.After(time.Second):
				t.Error("offline machine serialized cancellation")
			}
			return nil, errors.New("offline")
		}
		close(reached)
		return []byte(`{"stopped":true}`), nil
	})
	require.ErrorContains(t, err, "offline")
	select {
	case <-reached:
	default:
		t.Fatal("executing daemon not cancelled")
	}
}

func TestCancelFanoutAggregatesMissingProof(t *testing.T) {
	err := cancelOnMachines(context.Background(), []string{"one", "two"}, func(context.Context, string) ([]byte, error) { return []byte(`{}`), nil })
	require.ErrorContains(t, err, "machine one")
	require.ErrorContains(t, err, "machine two")
}

// Copyright (c) 2025 Reliant Labs
package logging

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/telemetry"
)

func TestUserHandlerStampsUserIDOfTheRequest(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(newUserHandler(slog.NewJSONHandler(&buf, nil)))

	scoped := telemetry.WithRequestScope(context.Background())
	telemetry.BindUser(scoped, "user-9")
	logger.ErrorContext(scoped, "rpc failed")
	assert.Contains(t, buf.String(), `"user.id":"user-9"`)

	buf.Reset()
	logger.ErrorContext(context.Background(), "background")
	assert.NotContains(t, buf.String(), "user.id")

	buf.Reset()
	logger.ErrorContext(scoped, "explicit", "user.id", "other")
	assert.Equal(t, 1, bytes.Count(buf.Bytes(), []byte("user.id")))
}

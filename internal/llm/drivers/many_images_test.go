// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/imagefit"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/require"
)

// capturingClient records the messages each call hands the provider client.
type capturingClient struct {
	sent [][]message.Message
}

func (c *capturingClient) Name() string                      { return "capturing" }
func (c *capturingClient) ValidateKey(context.Context) error { return nil }
func (c *capturingClient) SendMessages(_ context.Context, _ []string, msgs []message.Message, _ []tools.Tool) (*llm.DriverResponse, error) {
	c.sent = append(c.sent, msgs)
	return &llm.DriverResponse{}, nil
}

func (c *capturingClient) StreamResponse(_ context.Context, _ []string, msgs []message.Message, _ []tools.Tool) <-chan llm.DriverEvent {
	c.sent = append(c.sent, msgs)
	ch := make(chan llm.DriverEvent)
	close(ch)
	return ch
}

// TestBaseDriver_FitsImagesOfAManyImageRequest: every production driver
// streams through baseDriver, so a history of more than 20 images with one
// over 3000px reaches no provider as it is stored (prod obs d25b380e, trace
// a4285b4a: Anthropic refused every later turn of the chat).
func TestBaseDriver_FitsImagesOfAManyImageRequest(t *testing.T) {
	encode := func(w, h int) []byte {
		var buf bytes.Buffer
		require.NoError(t, png.Encode(&buf, image.NewGray(image.Rect(0, 0, w, h))))
		return buf.Bytes()
	}
	small := encode(32, 32)
	msgs := []message.Message{{Role: message.User, Parts: []message.ContentPart{
		message.TextContent{Text: "compare"},
		message.BinaryContent{MIMEType: "image/png", Data: encode(3500, 200)},
	}}}
	for i := 0; i < imagefit.ManyImageThreshold; i++ {
		msgs[0].Parts = append(msgs[0].Parts, message.BinaryContent{MIMEType: "image/png", Data: small})
	}

	client := &capturingClient{}
	d := &baseDriver{client: client, model: models.Model{ID: "m"}}
	for range d.StreamResponse(context.Background(), nil, msgs, nil) { //nolint:revive // drain
	}
	_, err := d.SendMessages(context.Background(), nil, msgs, nil)
	require.NoError(t, err)

	require.Len(t, client.sent, 2)
	for _, sent := range client.sent {
		cfg, _, err := image.DecodeConfig(bytes.NewReader(sent[0].BinaryContent()[0].Data))
		require.NoError(t, err)
		require.LessOrEqual(t, cfg.Width, imagefit.ManyImageMaxDimension)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(msgs[0].BinaryContent()[0].Data))
	require.NoError(t, err)
	require.Equal(t, 3500, cfg.Width, "the stored message must not change")
}

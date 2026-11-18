// Copyright (c) 2025 Reliant Labs
package imagefit

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/models/message"
)

func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	// A gradient, so the encoded size is realistic rather than a flat run.
	for y := 0; y < h; y += 7 {
		for x := 0; x < w; x += 7 {
			img.Set(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func jpegOf(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func dims(t *testing.T, data []byte) (int, int, string) {
	t.Helper()
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Width, cfg.Height, format
}

// history is a chat with n small tool-result screenshots and one large user
// attachment: the shape that wedged prod (obs d25b380e).
func history(t *testing.T, n int, large message.BinaryContent) []message.Message {
	t.Helper()
	small := pngOf(t, 64, 64)
	var msgs []message.Message
	msgs = append(msgs, message.Message{Role: message.User, Parts: []message.ContentPart{
		message.TextContent{Text: "look at this"}, large,
	}})
	for i := 0; i < n; i++ {
		msgs = append(msgs, message.Message{Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "call", Content: "shot", BinaryParts: []message.BinaryContent{{MIMEType: "image/png", Data: small}}},
		}})
	}
	return msgs
}

func TestForRequest_FewImagesAreSentAsTheyAre(t *testing.T) {
	large := message.BinaryContent{MIMEType: "image/png", Data: pngOf(t, 4000, 300)}
	msgs := history(t, ManyImageThreshold-1, large) // 20 images in all
	got := ForRequest(context.Background(), msgs)
	if &got[0] != &msgs[0] {
		t.Fatal("a request at the threshold was copied; it must be sent unchanged")
	}
}

func TestForRequest_ManyImagesDownscaleTheLargeOnesOnly(t *testing.T) {
	largeData := pngOf(t, 4000, 300)
	large := message.BinaryContent{MIMEType: "image/png", Data: largeData}
	msgs := history(t, ManyImageThreshold, large) // 21 images in all
	got := ForRequest(context.Background(), msgs)

	fitted := got[0].BinaryContent()
	if len(fitted) != 1 {
		t.Fatalf("user message has %d binary parts, want 1", len(fitted))
	}
	w, h, format := dims(t, fitted[0].Data)
	if w != ManyImageMaxDimension || h != 225 || format != "png" || fitted[0].MIMEType != "image/png" {
		t.Fatalf("downscaled to %dx%d %s (%s), want %dx225 png", w, h, format, fitted[0].MIMEType, ManyImageMaxDimension)
	}
	if got[0].TextContents()[0].Text != "look at this" {
		t.Fatal("the message's text changed")
	}
	// The stored history is untouched.
	if !bytes.Equal(msgs[0].BinaryContent()[0].Data, largeData) {
		t.Fatal("the input message was modified")
	}
	// Small images are not re-encoded: the same backing bytes are sent.
	if &got[1].ToolResults()[0].BinaryParts[0].Data[0] != &msgs[1].ToolResults()[0].BinaryParts[0].Data[0] {
		t.Fatal("a small image was re-encoded")
	}
}

func TestForRequest_ToolResultImagesAndJPEGStayJPEG(t *testing.T) {
	big := message.BinaryContent{MIMEType: "image/jpeg", Data: jpegOf(t, 1000, 3500)}
	msgs := history(t, ManyImageThreshold, message.BinaryContent{MIMEType: "image/png", Data: pngOf(t, 10, 10)})
	msgs = append(msgs, message.Message{Role: message.Tool, Parts: []message.ContentPart{
		message.ToolResult{ToolCallID: "c2", Content: "page", BinaryParts: []message.BinaryContent{big}},
	}})
	got := ForRequest(context.Background(), msgs)
	tr := got[len(got)-1].ToolResults()[0]
	if tr.Content != "page" || len(tr.BinaryParts) != 1 {
		t.Fatalf("tool result = %+v", tr)
	}
	w, h, format := dims(t, tr.BinaryParts[0].Data)
	if w != 857 || h != ManyImageMaxDimension || format != "jpeg" || tr.BinaryParts[0].MIMEType != "image/jpeg" {
		t.Fatalf("downscaled to %dx%d %s, want 857x%d jpeg", w, h, format, ManyImageMaxDimension)
	}
	if orig := msgs[len(msgs)-1].ToolResults()[0].BinaryParts[0]; !bytes.Equal(orig.Data, big.Data) {
		t.Fatal("the input tool result was modified")
	}
}

func TestForRequest_IsDeterministic(t *testing.T) {
	// Every turn must send identical bytes, or the prompt cache stops matching.
	data := pngOf(t, 3200, 100)
	a, err := downscale(data, "png")
	if err != nil {
		t.Fatal(err)
	}
	b, err := downscale(data, "png")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.data, b.data) {
		t.Fatal("two downscales of one image differ")
	}
}

func TestForRequest_UnreadableImagesAreLeftToTheProvider(t *testing.T) {
	bad := message.BinaryContent{MIMEType: "image/png", Data: []byte("not a png")}
	msgs := history(t, ManyImageThreshold, bad)
	got := ForRequest(context.Background(), msgs)
	if bc := got[0].BinaryContent(); len(bc) != 1 || string(bc[0].Data) != "not a png" {
		t.Fatalf("unreadable image changed: %+v", bc)
	}
}

func TestFitParts_AnImageThatCannotBeFitBecomesANote(t *testing.T) {
	// A PNG whose header claims a size the decoder then fails on: DecodeConfig
	// reads it, Decode cannot.
	data := pngOf(t, 3100, 10)
	truncated := data[:len(data)/2]
	parts := []message.ContentPart{
		message.TextContent{Text: "hi"},
		message.BinaryContent{MIMEType: "image/png", Data: truncated},
		message.ToolResult{ToolCallID: "c", Content: "shot", BinaryParts: []message.BinaryContent{
			{MIMEType: "image/png", Data: truncated},
			{MIMEType: "image/png", Data: pngOf(t, 8, 8)},
		}},
	}
	got, changed := fitParts(context.Background(), parts)
	if !changed {
		t.Fatal("nothing changed")
	}
	note, ok := got[1].(message.TextContent)
	if !ok || !strings.Contains(note.Text, "is not shown") || !strings.Contains(note.Text, "3100x10") {
		t.Fatalf("user image became %#v, want a note", got[1])
	}
	tr := got[2].(message.ToolResult)
	if len(tr.BinaryParts) != 1 || !strings.Contains(tr.Content, "is not shown") || !strings.HasPrefix(tr.Content, "shot\n") {
		t.Fatalf("tool result = %+v", tr)
	}
	if _, ok := parts[1].(message.BinaryContent); !ok {
		t.Fatal("the input parts were modified")
	}
}

func TestTargetSize(t *testing.T) {
	for _, tc := range []struct{ w, h, ww, wh int }{
		{4000, 300, 3000, 225},
		{1000, 3500, 857, 3000},
		{8000, 8000, 3000, 3000},
		{8000, 1, 3000, 1},
		{3000, 3000, 3000, 3000},
	} {
		if w, h := targetSize(tc.w, tc.h, ManyImageMaxDimension); w != tc.ww || h != tc.wh {
			t.Errorf("targetSize(%d, %d) = %dx%d, want %dx%d", tc.w, tc.h, w, h, tc.ww, tc.wh)
		}
	}
}

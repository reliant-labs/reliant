// Copyright (c) 2025 Reliant Labs
package imagefit

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"runtime"
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
	a, err := downscale(data, "png", 3200, 100)
	if err != nil {
		t.Fatal(err)
	}
	b, err := downscale(data, "png", 3200, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("two downscales of one image differ")
	}
}

// pngHeader is a PNG whose header claims w x h and that carries no pixels:
// what a decompression bomb, or a forged header, looks like to DecodeConfig.
func pngHeader(w, h uint32) []byte {
	chunk := func(typ string, data []byte) []byte {
		var c bytes.Buffer
		_ = binary.Write(&c, binary.BigEndian, uint32(len(data)))
		c.WriteString(typ)
		c.Write(data)
		_ = binary.Write(&c, binary.BigEndian, crc32.ChecksumIEEE(append([]byte(typ), data...)))
		return c.Bytes()
	}
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8], ihdr[9] = 8, 6 // 8-bit RGBA
	out := []byte("\x89PNG\r\n\x1a\n")
	out = append(out, chunk("IHDR", ihdr)...)
	return append(out, chunk("IEND", nil)...)
}

// withAnimationControl inserts an APNG acTL chunk after a PNG's IHDR.
func withAnimationControl(data []byte) []byte {
	const afterIHDR = 8 + 8 + 13 + 4
	actl := append([]byte{0, 0, 0, 8}, "acTL"...)
	actl = append(actl, 0, 0, 0, 2, 0, 0, 0, 0, 0, 0, 0, 0)
	return append(append(append([]byte(nil), data[:afterIHDR]...), actl...), data[afterIHDR:]...)
}

func gifOf(t *testing.T, w, h, frames int) []byte {
	t.Helper()
	pal := color.Palette{color.Transparent, color.Black, color.White}
	anim := &gif.GIF{}
	for i := 0; i < frames; i++ {
		img := image.NewPaletted(image.Rect(0, 0, w, h), pal)
		for x := 0; x < w; x += 3 {
			img.SetColorIndex(x, 0, uint8(1+(x+i)%2))
		}
		anim.Image = append(anim.Image, img)
		anim.Delay = append(anim.Delay, 10)
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, anim); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// countDecodes replaces the full decode for the test and reports how many
// images it was asked to decode.
func countDecodes(t *testing.T) *int {
	t.Helper()
	n := 0
	prev := decode
	decode = func(r io.Reader) (image.Image, string, error) {
		n++
		return prev(r)
	}
	t.Cleanup(func() { decode = prev })
	return &n
}

// TestForRequest_UnverifiableImagesAreSentUnchanged: anything fitting cannot
// verify and write back in its declared format reaches the provider exactly
// as stored, as a user attachment or a tool result; nothing is dropped or
// replaced by a note.
func TestForRequest_UnverifiableImagesAreSentUnchanged(t *testing.T) {
	large := pngOf(t, 3100, 10)
	for _, tc := range []struct {
		name    string
		part    message.BinaryContent
		decodes bool // the header is sound, so only the full decode can fail
	}{
		{"corrupt png", message.BinaryContent{MIMEType: "image/png", Data: []byte("not a png")}, false},
		{"truncated png", message.BinaryContent{MIMEType: "image/png", Data: large[:len(large)/2]}, true},
		{"png declared as jpeg", message.BinaryContent{MIMEType: "image/jpeg", Data: large}, false},
		{"pdf document", message.BinaryContent{MIMEType: "application/pdf", Data: []byte("%PDF-1.7\n" + strings.Repeat("x", 64))}, false},
		{"png with no supported mime", message.BinaryContent{MIMEType: "image/x-png", Data: large}, false},
		{"animated gif", message.BinaryContent{MIMEType: "image/gif", Data: gifOf(t, 3100, 10, 2)}, false},
		{"animated png", message.BinaryContent{MIMEType: "image/png", Data: withAnimationControl(large)}, false},
		{"decompression bomb", message.BinaryContent{MIMEType: "image/png", Data: pngHeader(100000, 100000)}, false},
		{"elongated header", message.BinaryContent{MIMEType: "image/png", Data: pngHeader(20000, 10)}, false},
		{"over the pixel budget", message.BinaryContent{MIMEType: "image/png", Data: pngHeader(9000, 9000)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decodes := countDecodes(t)
			msgs := history(t, ManyImageThreshold, tc.part)
			msgs = append(msgs, message.Message{Role: message.Tool, Parts: []message.ContentPart{
				message.ToolResult{ToolCallID: "c2", Content: "page", BinaryParts: []message.BinaryContent{tc.part}},
			}})

			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			got := ForRequest(context.Background(), msgs)
			runtime.ReadMemStats(&after)

			if !tc.decodes {
				if *decodes != 0 {
					t.Fatalf("decoded %d images, want none", *decodes)
				}
				// A forged header must not cost what it claims (40GB for the bomb).
				if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 16<<20 {
					t.Fatalf("fitting allocated %d bytes", alloc)
				}
			}
			if bc := got[0].BinaryContent(); len(bc) != 1 || bc[0].MIMEType != tc.part.MIMEType || !bytes.Equal(bc[0].Data, tc.part.Data) {
				t.Fatalf("user attachment changed: %d parts", len(bc))
			}
			if text := got[0].TextContents(); len(text) != 1 || text[0].Text != "look at this" {
				t.Fatalf("user text changed: %+v", text)
			}
			tr := got[len(got)-1].ToolResults()[0]
			if tr.Content != "page" || len(tr.BinaryParts) != 1 || tr.BinaryParts[0].MIMEType != tc.part.MIMEType || !bytes.Equal(tr.BinaryParts[0].Data, tc.part.Data) {
				t.Fatalf("tool result changed: content %q, %d parts", tr.Content, len(tr.BinaryParts))
			}
		})
	}
}

func TestForRequest_LargeJPEGAttachmentStaysJPEG(t *testing.T) {
	msgs := history(t, ManyImageThreshold, message.BinaryContent{MIMEType: "image/jpeg", Data: jpegOf(t, 4000, 100)})
	got := ForRequest(context.Background(), msgs)
	fitted := got[0].BinaryContent()
	if len(fitted) != 1 {
		t.Fatalf("user message has %d binary parts, want 1", len(fitted))
	}
	w, h, format := dims(t, fitted[0].Data)
	if w != ManyImageMaxDimension || h != 75 || format != "jpeg" || fitted[0].MIMEType != "image/jpeg" {
		t.Fatalf("downscaled to %dx%d %s (%s), want %dx75 jpeg", w, h, format, fitted[0].MIMEType, ManyImageMaxDimension)
	}
}

func TestForRequest_LargeStaticGIFStaysGIF(t *testing.T) {
	msgs := history(t, ManyImageThreshold, message.BinaryContent{MIMEType: "image/gif", Data: gifOf(t, 3100, 20, 1)})
	got := ForRequest(context.Background(), msgs)
	fitted := got[0].BinaryContent()
	w, h, format := dims(t, fitted[0].Data)
	if w != ManyImageMaxDimension || h != 19 || format != "gif" || fitted[0].MIMEType != "image/gif" {
		t.Fatalf("downscaled to %dx%d %s (%s), want %dx19 gif", w, h, format, fitted[0].MIMEType, ManyImageMaxDimension)
	}
	// The source's transparent color is kept: its untouched rows stay clear.
	img, err := gif.Decode(bytes.NewReader(fitted[0].Data))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, a := img.At(0, h-1).RGBA(); a != 0 {
		t.Fatalf("a transparent pixel has alpha %d", a)
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

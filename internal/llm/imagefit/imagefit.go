// Copyright (c) 2025 Reliant Labs

// Package imagefit fits the images of one model request within the per-image
// limits providers apply to requests that carry many images.
//
// Anthropic takes images up to 8000px on a side, but once a request holds
// more than 20 image (and, on Vertex and Bedrock, document) blocks, every
// image in it must be at most 3000px on a side. Every request re-sends the
// whole history, so a chat that accumulated 21 images with one large
// screenshot among them was refused on every later turn (prod obs d25b380e,
// trace a4285b4a: "At least one of the image dimensions exceed max allowed
// size for many-image requests: 3000 pixels") — the chat was wedged.
//
// The stored history is never changed: the large image stays what the user or
// tool produced, and only the copy a request sends is downscaled. Downscaling
// is deterministic, so every turn sends the same bytes and the provider's
// prompt cache keeps matching.
//
// Only an image fitting can verify is touched: one declared as a format
// providers take (JPEG, PNG, GIF, WebP) whose bytes decode as that format and
// that can be written back in it. Anything else — a document, an unreadable
// or mislabelled image, an animated one, one too large to decode safely — is
// sent exactly as stored, and the provider judges it as it did before.
package imagefit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"sync"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // register the WebP decoder, so an oversized WebP is recognized and logged

	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"
)

const (
	// ManyImageThreshold is how many image and document blocks one request
	// may carry before the stricter per-image limit applies to all of them.
	// Anthropic counts every image block in the request, earlier turns and
	// tool results included; on Vertex and Bedrock documents count too, so
	// they are counted here as well.
	ManyImageThreshold = 20

	// ManyImageMaxDimension is the largest width or height an image may have
	// in a request over ManyImageThreshold. Anthropic downsamples far below
	// this before the model sees an image, so nothing the model could read is
	// lost; the other providers we route downsample at least as far.
	ManyImageMaxDimension = 3000

	// MaxEncodedBytes is the largest image, raw, a provider accepts: Anthropic
	// refuses an image whose base64 exceeds 5MiB, and base64 is 4/3 of raw.
	// A downscaled image is always encoded within it.
	MaxEncodedBytes = 5 * 1024 * 1024 * 3 / 4

	// MaxSourceBytes is the largest stored image fitting reads. It bounds the
	// hashing and decoding one image costs; a larger one is sent unchanged.
	MaxSourceBytes = 32 * 1024 * 1024

	// maxDecodePixels and maxDecodeSide bound the images fitting decodes.
	// image.Decode allocates the whole raster its header declares, and the
	// header is whatever the bytes say: a few hundred bytes claiming
	// 100000x100000 would ask for 40GB and take the worker down. Fitting
	// reads the header alone (image.DecodeConfig) and decodes nothing over
	// either bound. 50MP (200MB as 8-bit RGBA) is above any real photo or
	// screenshot (an 8000x6000 photo is 48MP), and 16384 a side bounds an
	// elongated image whose area alone passes; no provider takes a side over
	// 8000px anyway, so nothing sendable is refused here.
	maxDecodePixels = 50_000_000
	maxDecodeSide   = 16384
)

// formatOf maps the MIME types drivers send as image blocks, and that
// providers accept, to the format image.DecodeConfig names for them.
var formatOf = map[string]string{
	"image/jpeg": "jpeg",
	"image/png":  "png",
	"image/gif":  "gif",
	"image/webp": "webp",
}

// ForRequest returns msgs with every image fit for one request: when the
// request carries more than ManyImageThreshold image and document blocks,
// each image larger than ManyImageMaxDimension on a side is downscaled to
// fit, keeping its format. An image that cannot be verified or downscaled is
// sent unchanged.
//
// The input is not modified; a message whose parts change is copied, and msgs
// itself is returned when nothing changes.
func ForRequest(ctx context.Context, msgs []message.Message) []message.Message {
	if countMedia(msgs) <= ManyImageThreshold {
		return msgs
	}
	var out []message.Message
	for i := range msgs {
		parts, changed := fitParts(ctx, msgs[i].Parts)
		if !changed {
			continue
		}
		if out == nil {
			out = append([]message.Message(nil), msgs...)
		}
		out[i].Parts = parts
	}
	if out == nil {
		return msgs
	}
	return out
}

// countMedia counts the blocks a driver encodes as an image or a document:
// a message's own binary parts and its tool results' binary parts.
func countMedia(msgs []message.Message) int {
	n := 0
	for i := range msgs {
		for _, part := range msgs[i].Parts {
			switch p := part.(type) {
			case message.BinaryContent:
				n++
			case message.ToolResult:
				n += len(p.BinaryParts)
			}
		}
	}
	return n
}

// fitParts fits the images among parts. It returns a new slice, and true,
// only when one of them changed.
func fitParts(ctx context.Context, parts []message.ContentPart) ([]message.ContentPart, bool) {
	var out []message.ContentPart
	set := func(i int, p message.ContentPart) {
		if out == nil {
			out = append([]message.ContentPart(nil), parts...)
		}
		out[i] = p
	}
	for i, part := range parts {
		switch p := part.(type) {
		case message.BinaryContent:
			if fitted, changed := fitBinary(ctx, p); changed {
				set(i, fitted)
			}
		case message.ToolResult:
			var binary []message.BinaryContent
			for k, bp := range p.BinaryParts {
				fitted, changed := fitBinary(ctx, bp)
				if !changed {
					continue
				}
				if binary == nil {
					binary = append([]message.BinaryContent(nil), p.BinaryParts...)
				}
				binary[k] = fitted
			}
			if binary != nil {
				p.BinaryParts = binary
				set(i, p)
			}
		}
	}
	return out, out != nil
}

// fitBinary returns part fit for a many-image request, and true, when it is an
// image larger than ManyImageMaxDimension that could be downscaled in its own
// format. Otherwise it returns part unchanged.
func fitBinary(ctx context.Context, part message.BinaryContent) (message.BinaryContent, bool) {
	want, ok := formatOf[part.MIMEType]
	if !ok || len(part.Data) == 0 {
		return part, false // not sent as an image block
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(part.Data))
	if err != nil || (cfg.Width <= ManyImageMaxDimension && cfg.Height <= ManyImageMaxDimension) {
		return part, false
	}

	attrs := []any{"mime_type", part.MIMEType, "format", format, "width", cfg.Width, "height", cfg.Height, "bytes", len(part.Data)}
	unchanged := func(why string) (message.BinaryContent, bool) {
		logging.WarnContext(ctx, "[imagefit] Sending an oversized image unchanged in a many-image request: "+why, attrs...)
		return part, false
	}
	switch {
	case format != want:
		return unchanged("its bytes are not its declared format")
	case len(part.Data) > MaxSourceBytes:
		return unchanged(fmt.Sprintf("it is over %d bytes", MaxSourceBytes))
	case cfg.Width > maxDecodeSide || cfg.Height > maxDecodeSide || int64(cfg.Width)*int64(cfg.Height) > maxDecodePixels:
		return unchanged("it is too large to decode safely")
	case !reencodable(format, part.Data):
		return unchanged("it cannot be written back in its format (animated, or no encoder)")
	}

	data, err := cachedDownscale(ctx, part.Data, format, cfg.Width, cfg.Height)
	if err != nil {
		return unchanged(err.Error())
	}
	logging.DebugContext(ctx, "[imagefit] Downscaled an image for a many-image request", append(attrs, "out_bytes", len(data))...)
	part.Data = data
	return part, true
}

// reencodable reports whether an image of format can be downscaled and
// written back in that format without losing what it is. WebP has no Go
// encoder, and re-encoding an animated GIF or PNG would keep one frame.
func reencodable(format string, data []byte) bool {
	switch format {
	case "jpeg":
		return true
	case "png":
		return !isAnimatedPNG(data)
	case "gif":
		return isStaticGIF(data)
	}
	return false
}

// isAnimatedPNG reports whether data carries an APNG animation control chunk,
// which precedes the image data.
func isAnimatedPNG(data []byte) bool {
	const sigLen = 8
	for i := sigLen; i+8 <= len(data); {
		n := int(binary.BigEndian.Uint32(data[i:]))
		switch string(data[i+4 : i+8]) {
		case "acTL":
			return true
		case "IDAT":
			return false
		}
		if n < 0 || n > len(data) {
			return true // malformed: do not rewrite it
		}
		i += 12 + n
	}
	return true
}

// isStaticGIF reports whether data is a well-formed GIF of exactly one frame,
// walking its blocks without decoding any pixels.
func isStaticGIF(d []byte) bool {
	const headerLen = 13 // signature, version and logical screen descriptor
	if len(d) < headerLen {
		return false
	}
	i := headerLen
	if d[10]&0x80 != 0 {
		i += 3 << (d[10]&7 + 1) // global color table
	}
	frames := 0
	for i < len(d) {
		switch d[i] {
		case 0x3B: // trailer
			return frames == 1
		case 0x21: // extension: introducer, label, then sub-blocks
			i += 2
		case 0x2C: // image descriptor
			if frames++; frames > 1 || i+10 > len(d) {
				return false
			}
			flags := d[i+9]
			i += 10
			if flags&0x80 != 0 {
				i += 3 << (flags&7 + 1) // local color table
			}
			i++ // LZW minimum code size
		default:
			return false
		}
		for { // sub-blocks, ended by a zero-length one
			if i >= len(d) {
				return false
			}
			n := int(d[i])
			i += 1 + n
			if n == 0 {
				break
			}
		}
	}
	return frames == 1
}

var (
	// cache keeps recent results by the source's hash: every turn of a chat
	// re-sends the same history, and decoding and resampling a large image
	// is far costlier than hashing it. Bounded by cacheMaxBytes of output.
	cacheMu    sync.Mutex
	cache      = map[[sha256.Size]byte][]byte{}
	cacheOrder [][sha256.Size]byte
	cacheBytes int

	// work bounds how many images are decoded at once: a decoded image of
	// maxDecodePixels is 200MB, and many chats call models at once on one
	// worker.
	work = make(chan struct{}, 2)

	// decode is image.Decode; a test replaces it to prove what is never
	// decoded.
	decode = image.Decode
)

const cacheMaxBytes = 64 * 1024 * 1024

func cachedDownscale(ctx context.Context, data []byte, format string, w, h int) ([]byte, error) {
	key := sha256.Sum256(data)
	cacheMu.Lock()
	res, ok := cache[key]
	cacheMu.Unlock()
	if ok {
		return res, nil
	}

	select {
	case work <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	res, err := downscale(data, format, w, h)
	<-work
	if err != nil {
		return nil, err
	}

	cacheMu.Lock()
	defer cacheMu.Unlock()
	if _, ok := cache[key]; !ok && len(res) <= cacheMaxBytes {
		for cacheBytes+len(res) > cacheMaxBytes && len(cacheOrder) > 0 {
			oldest := cacheOrder[0]
			cacheOrder = cacheOrder[1:]
			cacheBytes -= len(cache[oldest])
			delete(cache, oldest)
		}
		cache[key] = res
		cacheOrder = append(cacheOrder, key)
		cacheBytes += len(res)
	}
	return res, nil
}

// fitLimits are the sides tried in order: ManyImageMaxDimension, then smaller
// ones for an image whose encoding at that size is over MaxEncodedBytes.
var fitLimits = []int{ManyImageMaxDimension, 2000, 1500}

// jpegQualities are tried in order at each size of a JPEG.
var jpegQualities = []int{90, 80, 65}

// downscale resamples data, a w x h image of format, to fit within
// ManyImageMaxDimension and encodes it in the same format no larger than
// MaxEncodedBytes. The caller has checked w and h against the decode budget.
func downscale(data []byte, format string, w, h int) ([]byte, error) {
	src, _, err := decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	var pal color.Palette
	if p, ok := src.(*image.Paletted); ok {
		pal = p.Palette
	}
	if b := src.Bounds(); b != image.Rect(0, 0, w, h) {
		// A GIF's frame may cover part of its canvas: scale the canvas.
		canvas := image.NewNRGBA(image.Rect(0, 0, w, h))
		draw.Draw(canvas, b, src, b.Min, draw.Src)
		src = canvas
	}
	for _, limit := range fitLimits {
		tw, th := targetSize(w, h, limit)
		dst := image.NewNRGBA(image.Rect(0, 0, tw, th))
		draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)
		out, err := encode(dst, format, pal)
		if err != nil || out != nil {
			return out, err
		}
	}
	return nil, fmt.Errorf("still over %d bytes at %dpx", MaxEncodedBytes, fitLimits[len(fitLimits)-1])
}

// encode writes img in format, returning nil when every encoding is over
// MaxEncodedBytes. pal is the source's palette, if it had one.
func encode(img image.Image, format string, pal color.Palette) ([]byte, error) {
	var buf bytes.Buffer
	fits := func() []byte {
		if buf.Len() > MaxEncodedBytes {
			return nil
		}
		return buf.Bytes()
	}
	switch format {
	case "jpeg":
		for _, q := range jpegQualities {
			buf.Reset()
			if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: q}); err != nil {
				return nil, fmt.Errorf("encode jpeg: %w", err)
			}
			if out := fits(); out != nil {
				return out, nil
			}
		}
		return nil, nil
	case "png":
		if err := png.Encode(&buf, img); err != nil {
			return nil, fmt.Errorf("encode png: %w", err)
		}
		return fits(), nil
	case "gif":
		if len(pal) > 0 {
			// Keep the source's colors, its transparent one included: a
			// paletted image is written with its own palette.
			pm := image.NewPaletted(img.Bounds(), pal)
			draw.FloydSteinberg.Draw(pm, pm.Bounds(), img, img.Bounds().Min)
			img = pm
		}
		if err := gif.Encode(&buf, img, &gif.Options{NumColors: 256, Drawer: draw.FloydSteinberg}); err != nil {
			return nil, fmt.Errorf("encode gif: %w", err)
		}
		return fits(), nil
	}
	return nil, errors.New("no encoder for " + format)
}

// targetSize scales w x h to fit within limit on both sides, keeping the
// aspect ratio. Neither side drops below 1px.
func targetSize(w, h, limit int) (int, int) {
	if w <= limit && h <= limit {
		return w, h
	}
	if w >= h {
		return limit, max(1, (h*limit+w/2)/w)
	}
	return max(1, (w*limit+h/2)/h), limit
}

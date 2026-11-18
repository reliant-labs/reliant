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
package imagefit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif" // register the GIF decoder
	"image/jpeg"
	"image/png"
	"strings"
	"sync"

	_ "golang.org/x/image/bmp" // register the BMP decoder
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // register the WebP decoder

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
	MaxEncodedBytes = 5 * 1024 * 1024 * 3 / 4

	// omittedImageNote replaces an image that could not be fit, so the
	// request goes through and the model knows something was there.
	omittedImageNote = "<system>An image (%s, %dx%d px) is not shown: it is larger than the %dpx a model accepts in a conversation with this many images, and it could not be downscaled (%s).</system>"
)

// ForRequest returns msgs with every image fit for one request: when the
// request carries more than ManyImageThreshold image and document blocks,
// each image larger than ManyImageMaxDimension on a side is downscaled to
// fit. An image that cannot be downscaled is replaced by a note rather than
// sent to a provider that would refuse the whole request.
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
			fitted, note, changed := fitBinary(ctx, p)
			switch {
			case note != "":
				set(i, message.TextContent{Text: note})
			case changed:
				set(i, fitted)
			}
		case message.ToolResult:
			var binary []message.BinaryContent
			var notes []string
			for k, bp := range p.BinaryParts {
				fitted, note, changed := fitBinary(ctx, bp)
				if !changed && note == "" {
					if binary != nil {
						binary = append(binary, bp)
					}
					continue
				}
				if binary == nil {
					binary = append(make([]message.BinaryContent, 0, len(p.BinaryParts)), p.BinaryParts[:k]...)
				}
				if note != "" {
					notes = append(notes, note)
					continue
				}
				binary = append(binary, fitted)
			}
			if binary == nil {
				continue
			}
			p.BinaryParts = binary
			if len(notes) > 0 {
				p.Content += "\n" + strings.Join(notes, "\n")
			}
			set(i, p)
		}
	}
	return out, out != nil
}

// fitBinary returns part fit for a many-image request. changed reports that
// fitted replaces part; a non-empty note means part cannot be sent and the
// note stands in for it. A part that is not an image, cannot be read, or
// already fits is returned unchanged: the provider judges it as before.
func fitBinary(ctx context.Context, part message.BinaryContent) (fitted message.BinaryContent, note string, changed bool) {
	if !strings.HasPrefix(strings.ToLower(part.MIMEType), "image/") || len(part.Data) == 0 {
		return part, "", false
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(part.Data))
	if err != nil || (cfg.Width <= ManyImageMaxDimension && cfg.Height <= ManyImageMaxDimension) {
		return part, "", false
	}

	res, err := cachedDownscale(ctx, part.Data, format)
	if err != nil {
		logging.WarnContext(ctx, "[imagefit] Could not downscale an image for a many-image request; sending a note in its place",
			"mime_type", part.MIMEType, "width", cfg.Width, "height", cfg.Height, "bytes", len(part.Data), "error", err)
		return part, fmt.Sprintf(omittedImageNote, part.MIMEType, cfg.Width, cfg.Height, ManyImageMaxDimension, err), false
	}
	logging.DebugContext(ctx, "[imagefit] Downscaled an image for a many-image request",
		"mime_type", part.MIMEType, "width", cfg.Width, "height", cfg.Height, "bytes", len(part.Data),
		"out_mime_type", res.mimeType, "out_bytes", len(res.data))
	part.Data = res.data
	part.MIMEType = res.mimeType
	return part, "", true
}

// downscaled is one image fit within ManyImageMaxDimension.
type downscaled struct {
	data     []byte
	mimeType string
}

var (
	// cache keeps recent results by the source's hash: every turn of a chat
	// re-sends the same history, and decoding and resampling a large image
	// is far costlier than hashing it. Bounded by cacheMaxBytes of output.
	cacheMu    sync.Mutex
	cache      = map[[sha256.Size]byte]downscaled{}
	cacheOrder [][sha256.Size]byte
	cacheBytes int

	// work bounds how many images are decoded at once: a decoded 8000px
	// image is 256MB, and many chats call models at once on one worker.
	work = make(chan struct{}, 2)
)

const cacheMaxBytes = 64 * 1024 * 1024

func cachedDownscale(ctx context.Context, data []byte, format string) (downscaled, error) {
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
		return downscaled{}, ctx.Err()
	}
	res, err := downscale(data, format)
	<-work
	if err != nil {
		return downscaled{}, err
	}

	cacheMu.Lock()
	defer cacheMu.Unlock()
	if _, ok := cache[key]; !ok && len(res.data) <= cacheMaxBytes {
		for cacheBytes+len(res.data) > cacheMaxBytes && len(cacheOrder) > 0 {
			oldest := cacheOrder[0]
			cacheOrder = cacheOrder[1:]
			cacheBytes -= len(cache[oldest].data)
			delete(cache, oldest)
		}
		cache[key] = res
		cacheOrder = append(cacheOrder, key)
		cacheBytes += len(res.data)
	}
	return res, nil
}

// jpegQualities are tried in order when an image must be (or fit as) a JPEG.
var jpegQualities = []int{90, 80, 65}

// downscale resamples data to fit within ManyImageMaxDimension and encodes
// it no larger than MaxEncodedBytes: a JPEG stays a JPEG, anything else
// becomes a PNG, falling back to a JPEG when the PNG is too large.
func downscale(data []byte, format string) (downscaled, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return downscaled{}, fmt.Errorf("decode: %w", err)
	}
	b := src.Bounds()
	w, h := targetSize(b.Dx(), b.Dy(), ManyImageMaxDimension)
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)

	if format != "jpeg" {
		var buf bytes.Buffer
		if err := png.Encode(&buf, dst); err != nil {
			return downscaled{}, fmt.Errorf("encode png: %w", err)
		}
		if buf.Len() <= MaxEncodedBytes {
			return downscaled{data: buf.Bytes(), mimeType: "image/png"}, nil
		}
		// JPEG has no alpha: flatten onto white, as a viewer shows it.
		flat := image.NewRGBA(dst.Bounds())
		draw.Draw(flat, flat.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
		draw.Draw(flat, flat.Bounds(), dst, image.Point{}, draw.Over)
		return encodeJPEG(flat)
	}
	return encodeJPEG(dst)
}

func encodeJPEG(img image.Image) (downscaled, error) {
	for _, q := range jpegQualities {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: q}); err != nil {
			return downscaled{}, fmt.Errorf("encode jpeg: %w", err)
		}
		if buf.Len() <= MaxEncodedBytes {
			return downscaled{data: buf.Bytes(), mimeType: "image/jpeg"}, nil
		}
	}
	return downscaled{}, errors.New("still too large after downscaling")
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

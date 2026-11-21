package requireprivateiface

import (
	"image"
	"image/color"
	"image/gif"
)

// fixedPalette is unexported and never escapes the package's API. Its one
// exported method exists only to satisfy image/draw.Quantizer for
// gif.Options, so it cannot be unexported and describes no package surface:
// no contract.go is required.
type fixedPalette struct{ p color.Palette }

func (f fixedPalette) Quantize(dst color.Palette, _ image.Image) color.Palette {
	return append(dst, f.p...)
}

// Options builds GIF encoder options.
func Options(p color.Palette) *gif.Options {
	return &gif.Options{NumColors: len(p), Quantizer: fixedPalette{p: p}}
}

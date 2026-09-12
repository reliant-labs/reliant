// Copyright (c) 2025 Reliant Labs
package models

import "slices"

// Modality identifies a kind of content a model can produce.
//
// This is the OUTPUT side of a model's capability. The input side is
// ModelCapabilities.SupportedFileTypes, which lists what a model can accept as
// an attachment. The two are deliberately separate: a model that can READ an
// image (vision) is not necessarily able to GENERATE one, and conflating them
// would route an image-generation request to a vision model that has no
// generation endpoint at all.
type Modality string

const (
	// ModalityText is a model that emits text (and tool calls). Every chat
	// model is ModalityText.
	ModalityText Modality = "text"

	// ModalityImage is a model that emits images, e.g. via an image
	// generation endpoint rather than chat completions.
	ModalityImage Modality = "image"
)

// DefaultOutputModalities is what a model is assumed to produce when its
// definition declares no output_modalities.
//
// Text is the default because every model in the registry predates this field
// and all of them are chat models. Defaulting to text (rather than to "unknown"
// or to nothing) keeps every existing definition and every existing selector
// resolving exactly as it did before this field existed.
var DefaultOutputModalities = []Modality{ModalityText}

// EffectiveOutputModalities returns the modalities this model can produce,
// applying the text default for definitions that declare none.
//
// Always use this rather than reading Capabilities.OutputModalities directly:
// the zero value of the field means "not declared", not "produces nothing", and
// a direct read would treat every pre-existing model as incapable of text.
func (c ModelCapabilities) EffectiveOutputModalities() []Modality {
	if len(c.OutputModalities) == 0 {
		return DefaultOutputModalities
	}
	return c.OutputModalities
}

// CanOutput reports whether this model can produce the given modality.
func (c ModelCapabilities) CanOutput(m Modality) bool {
	return slices.Contains(c.EffectiveOutputModalities(), m)
}

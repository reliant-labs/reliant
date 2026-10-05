// Copyright (c) 2025 Reliant Labs
package models

import (
	"strings"
	"testing"
)

func TestValidateVideoRequest(t *testing.T) {
	veo := &VideoCapabilities{
		Durations: []int{4, 6, 8}, Resolutions: []string{"720p", "1080p", "4k"}, Aspects: []string{"16:9", "9:16"},
		MaxReferenceImages: 3, SupportsNegativePrompt: true, SupportsExtend: true,
		FullResolutionDuration: 8, HighResolutions: []string{"1080p", "4k"},
	}
	cases := []struct {
		name    string
		p       VideoRequestParams
		wantErr string
	}{
		{"defaults ok", VideoRequestParams{}, ""},
		{"4s 720p ok", VideoRequestParams{DurationSeconds: 4, Resolution: "720p"}, ""},
		{"bad duration", VideoRequestParams{DurationSeconds: 5}, "allowed: 4, 6, 8"},
		{"1080p needs 8s", VideoRequestParams{DurationSeconds: 4, Resolution: "1080p"}, "requires duration_seconds=8 for 1080p"},
		{"1080p 8s ok", VideoRequestParams{DurationSeconds: 8, Resolution: "1080p"}, ""},
		{"bad resolution", VideoRequestParams{Resolution: "360p"}, "resolution"},
		{"bad aspect", VideoRequestParams{AspectRatio: "1:1"}, "aspect_ratio"},
		{"too many refs", VideoRequestParams{ReferenceImages: 4}, "at most 3"},
		{"no edit", VideoRequestParams{Edit: true}, "cannot edit"},
		{"extend ok", VideoRequestParams{Extend: true}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := veo.ValidateVideoRequest("veo-3.1-generate", c.p)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, c.wantErr)
			}
			if !strings.Contains(err.Error(), "veo-3.1-generate") {
				t.Errorf("error should name the model: %v", err)
			}
		})
	}
	lite := &VideoCapabilities{Durations: []int{4, 6, 8}}
	if err := lite.ValidateVideoRequest("lite", VideoRequestParams{ReferenceImages: 1}); err == nil || !strings.Contains(err.Error(), "does not support reference") {
		t.Errorf("lite refs error = %v", err)
	}
	var none *VideoCapabilities
	if err := none.ValidateVideoRequest("x", VideoRequestParams{}); err == nil {
		t.Error("nil capabilities must error")
	}
}

// Omni accepts 1080p and 4k but renders at 720p and upscales; Veo renders them
// natively. Treating the two as equal is what would send a "sharp 4K" request
// to the model that cannot actually produce one.
func TestVideoCapabilities_IsUpscaled(t *testing.T) {
	reg := MustGetRegistry()
	omni, ok := reg.GetDefinition("gemini-omni-1.1-flash")
	if !ok || omni.Capabilities.Video == nil {
		t.Fatal("gemini-omni-1.1-flash must declare video capabilities")
	}
	for res, want := range map[string]bool{"720p": false, "360p": false, "1080p": true, "4k": true, "": false} {
		if got := omni.Capabilities.Video.IsUpscaled(res); got != want {
			t.Errorf("omni IsUpscaled(%q) = %v, want %v", res, got, want)
		}
	}
	veo, ok := reg.GetDefinition("veo-3.1-generate")
	if !ok || veo.Capabilities.Video == nil {
		t.Fatal("veo-3.1-generate must declare video capabilities")
	}
	for _, res := range []string{"720p", "1080p", "4k"} {
		if veo.Capabilities.Video.IsUpscaled(res) {
			t.Errorf("veo renders %s natively; IsUpscaled must be false", res)
		}
	}
}

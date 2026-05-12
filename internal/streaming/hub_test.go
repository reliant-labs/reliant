package streaming

import (
	"testing"
)

func TestNewStreamingHub_UnknownDriver(t *testing.T) {
	_, err := NewStreamingHub(StreamingConfig{Driver: "redis"})
	if err == nil {
		t.Fatal("expected error for unknown driver")
	}
}

func TestParseStreamingDriver(t *testing.T) {
	tests := []struct {
		input    string
		expected StreamingDriver
		wantErr  bool
	}{
		{"", DriverNATS, false},
		{"nats", DriverNATS, false},
		{"NATS", DriverNATS, false},
		{" nats ", DriverNATS, false},
		{"memory", "", true},
		{"redis", "", true},
		{"kafka", "", true},
	}

	for _, tt := range tests {
		t.Run("input="+tt.input, func(t *testing.T) {
			got, err := ParseStreamingDriver(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseStreamingDriver(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if got != tt.expected {
				t.Errorf("ParseStreamingDriver(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

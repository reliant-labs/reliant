package config

import (
	"encoding/json"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestBuildContextUnionRoundTrip(t *testing.T) {
	for _, input := range []string{`"../lib"`, `"docker-image://alpine:3.22"`, `{"repo":"github.com/acme/lib","ref":"v1","subdir":"src"}`} {
		t.Run(input, func(t *testing.T) {
			var c BuildContext
			if err := json.Unmarshal([]byte(input), &c); err != nil {
				t.Fatal(err)
			}
			c.SourceDir = "/machine/cache/not-declared"
			data, err := yaml.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), "machine") {
				t.Fatalf("resolved path leaked: %s", data)
			}
			var out BuildContext
			if err := yaml.Unmarshal(data, &out); err != nil {
				t.Fatal(err)
			}
			got, err := json.Marshal(out)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != input {
				t.Fatalf("round-trip = %s, want %s", got, input)
			}
		})
	}
}

func TestBuildContextRefusesMalformedPins(t *testing.T) {
	for _, input := range []string{`null`, `42`, `""`, `{}`, `{"repo":"github.com/acme/lib"}`, `{"repo":"github.com/acme/lib","ref":"v1","typo":true}`, `{"repo":"github.com/acme/lib","ref":"v1","subdir":"../escape"}`, `{"repo":"github.com/acme/lib","ref":"v1","subdir":"C:\\escape"}`} {
		t.Run(input, func(t *testing.T) {
			var c BuildContext
			if err := json.Unmarshal([]byte(input), &c); err == nil {
				t.Fatalf("accepted malformed context %s", input)
			}
			if err := yaml.Unmarshal([]byte(input), &c); err == nil && input != "null" {
				t.Fatalf("accepted malformed YAML context %s", input)
			}
		})
	}
	if err := (BuildContext{Path: "./local", Source: &GitSource{Repo: "github.com/acme/lib", Ref: "v1"}}).Validate(); err == nil {
		t.Fatal("accepted both path and source")
	}
}

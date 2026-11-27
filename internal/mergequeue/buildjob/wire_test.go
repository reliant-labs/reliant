// Copyright (c) 2025 Reliant Labs
package buildjob

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestStartRequestValidate(t *testing.T) {
	sha := strings.Repeat("a", 40)
	build := &BuildRequest{Repository: "acme/widgets", BatchID: "b1", Base: sha, Entries: []Entry{{Number: 1, Head: sha}}}
	paths := &PathsRequest{Repository: "acme/widgets", Base: sha, Head: sha}
	cfg := &ConfigRequest{Repository: "acme/widgets", SHA: sha, Path: ".reliant/pipeline.yaml"}
	for name, tc := range map[string]struct {
		req StartRequest
		ok  bool
	}{
		"a build":               {StartRequest{JobID: "build-1", Kind: KindBuild, Build: build}, true},
		"a paths job":           {StartRequest{JobID: "paths-1", Kind: KindPaths, Paths: paths}, true},
		"a config job":          {StartRequest{JobID: "config-1", Kind: KindConfig, Config: cfg}, true},
		"no id":                 {StartRequest{Kind: KindPaths, Paths: paths}, false},
		"an unknown kind":       {StartRequest{JobID: "j", Kind: "gate", Paths: paths}, false},
		"a body of another":     {StartRequest{JobID: "j", Kind: KindBuild, Paths: paths}, false},
		"two bodies":            {StartRequest{JobID: "j", Kind: KindBuild, Build: build, Paths: paths}, false},
		"a batch id with slash": {StartRequest{JobID: "j", Kind: KindBuild, Build: &BuildRequest{Repository: "acme/widgets", BatchID: "a/b", Base: sha, Entries: build.Entries}}, false},
		"a build of nothing":    {StartRequest{JobID: "j", Kind: KindBuild, Build: &BuildRequest{Repository: "acme/widgets", BatchID: "b1", Base: sha}}, false},
		"a branch for a base":   {StartRequest{JobID: "j", Kind: KindPaths, Paths: &PathsRequest{Repository: "acme/widgets", Base: "main", Head: sha}}, false},
		"a bad repository":      {StartRequest{JobID: "j", Kind: KindPaths, Paths: &PathsRequest{Repository: "widgets", Base: sha, Head: sha}}, false},
	} {
		err := tc.req.Validate()
		if tc.ok != (err == nil) || (err != nil && !errors.Is(err, ErrInvalidRequest)) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestValidConfigPath(t *testing.T) {
	for p, ok := range map[string]bool{
		".reliant/pipeline.yaml": true,
		".reliant/a/b.yaml":      true,
		".reliant/":              false,
		".reliant/../x":          false,
		"/.reliant/x":            false,
		"x.txt":                  false,
		".reliant//x":            false,
		".reliant/x\n":           false,
	} {
		if ValidConfigPath(p) != ok {
			t.Errorf("%q: want %v", p, ok)
		}
	}
}

// The wire is additive JSON: a reply's unset results are omitted, and the
// bytes of a config read survive a round trip.
func TestStatusReplyRoundTrips(t *testing.T) {
	in := StatusReply{State: JobDone, Config: &ConfigResult{Found: true, Bytes: []byte("version: 1\n")}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"build"`) || strings.Contains(string(b), `"error"`) {
		t.Fatalf("unset fields on the wire: %s", b)
	}
	var out StatusReply
	if err := json.Unmarshal(b, &out); err != nil || out.Config == nil || string(out.Config.Bytes) != "version: 1\n" {
		t.Fatalf("round trip: %+v, %v", out, err)
	}
}

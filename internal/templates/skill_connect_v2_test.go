package templates_test

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/templates"
)

// TestConnectV2SkillTeachesBothSides pins the connect-go v2 facts a Ranger
// build had to learn from connect-go's source (forge friction #30): v2 is the
// default for a new service project, yet no skill said how a v2 client is
// built, how it sends an API key, that a server-stream client is a struct
// value, or where one service's payload cap goes. Each want is a fact an
// agent would otherwise rediscover by reading connect.go and connecthttp.
func TestConnectV2SkillTeachesBothSides(t *testing.T) {
	t.Parallel()
	skills := shippedSkills(t)

	body, ok := skills["forge/api/connect-v2/SKILL.md"]
	if !ok {
		t.Fatal("skills/forge/api/connect-v2/SKILL.md is not shipped")
	}
	for _, want := range []string{
		// handlers and metadata
		"connect.CallInfoForServerContext(ctx)",
		"stream.SendHeaders()",
		"svcerr.InterceptorV2()",
		// building a client, and an API-key interceptor
		"connect.NewClient(connecthttp.NewTransport(",
		"connect.CallInfoForClientContext(ctx)",
		"info.RequestHeader().Set(",
		"connect.NewClientContext(ctx)",
		"observe.NewClientStackV2",
		"testkit.NewConnectClientV2",
		// the server-stream client is a value: `return nil, err` does not compile
		"struct VALUE",
		"JobsServiceWatchJobClientStream{}, err",
		"errors.Is(err, io.EOF)",
		// defaults that differ from v1, and the per-service override
		"4 MiB",
		"gzip",
		"connecthttp.WithCompressors()",
		"mountkit.WithHTTPOptionsV2(",
		"connecthttp.WithSendMaxBytes(",
		"MountOptions",
		// the upstream guide that ships in the module cache
		"docs/v2-guide.md",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("api/connect-v2 skill must teach %q", want)
		}
	}

	if api := skills["forge/api/SKILL.md"]; !strings.Contains(api, "`api/connect-v2`") {
		t.Error("the api skill's handler example is connect-go v1 shaped; it must point v2 projects at `api/connect-v2`")
	}
}

// TestRoutesGenMountOptionsNamesPerServiceCaps pins the v2 MountOptions
// comment in routes_gen.go: it is the file an agent reads when looking for
// where one service's send cap goes, and it named WithHTTPOptionsV2 without
// saying it overrides the chain's global cap (forge friction #30).
func TestRoutesGenMountOptionsNamesPerServiceCaps(t *testing.T) {
	t.Parallel()
	tmpl, err := templates.ProjectTemplates().Get("routes_gen.go.tmpl")
	if err != nil {
		t.Fatalf("read routes_gen.go.tmpl: %v", err)
	}
	for _, want := range []string{
		"mountkit.WithHTTPOptionsV2(connecthttp.WithSendMaxBytes(",
		"the last one wins",
		"gzip",
		"api/connect-v2",
	} {
		if !strings.Contains(string(tmpl), want) {
			t.Errorf("routes_gen.go.tmpl's MountOptions comment must say %q", want)
		}
	}
}

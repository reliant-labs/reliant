package activity

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/reliant-labs/forge/pkg/forgepb"
)

func TestResolve_Explicit(t *testing.T) {
	md := fixtureRPC(t, fixtureMethod{name: "Promote", activity: promoteDecl()})
	d, err := Resolve(md)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if d == nil || d.Implied || d.None != "" {
		t.Fatalf("Resolve = %+v, want an explicit declaration", d)
	}
	if d.Verb != "promote" || d.TargetKind != "release" || d.Writer != WriterInterceptor {
		t.Errorf("verb/kind/writer = %q/%q/%v", d.Verb, d.TargetKind, d.Writer)
	}
	if d.TargetID.Response || !d.TargetLabel.Response {
		t.Errorf("target id should resolve against the request and label against the response: %+v / %+v", d.TargetID, d.TargetLabel)
	}
	if got := []string{d.Detail[0].Key(), d.Detail[1].Key()}; got[0] != "from_env" || got[1] != "replicas" {
		t.Errorf("detail keys = %v", got)
	}
}

func TestResolve_None(t *testing.T) {
	md := fixtureRPC(t, fixtureMethod{name: "Preview", activity: &forgepb.ActivityOptions{None: "dry run"}})
	d, err := Resolve(md)
	if err != nil || d == nil || d.None != "dry run" {
		t.Fatalf("Resolve = %+v, %v; want None=dry run", d, err)
	}
}

func TestResolve_Malformed(t *testing.T) {
	for _, tc := range []struct {
		name      string
		opt       *forgepb.ActivityOptions
		streaming bool
		want      []string
	}{
		{
			name: "none with other fields",
			opt:  &forgepb.ActivityOptions{None: "x", Verb: "promote"},
			want: []string{"`none` excludes every other field"},
		},
		{
			name: "empty declaration",
			opt:  &forgepb.ActivityOptions{},
			want: []string{`verb ""`, `target.kind ""`, "target.id is required"},
		},
		{
			name: "verb not a token",
			opt:  &forgepb.ActivityOptions{Verb: "Promoted", Target: &forgepb.ActivityTarget{Kind: "release", Id: "release_id"}},
			want: []string{`verb "Promoted"`},
		},
		{
			name: "unknown field",
			opt:  &forgepb.ActivityOptions{Verb: "promote", Target: &forgepb.ActivityTarget{Kind: "release", Id: "release_idd"}},
			want: []string{`has no field "release_idd"`},
		},
		{
			name: "ends on a message",
			opt:  &forgepb.ActivityOptions{Verb: "promote", Target: &forgepb.ActivityTarget{Kind: "release", Id: "release"}},
			want: []string{`ends on message field "release"`},
		},
		{
			name: "walks through a scalar",
			opt:  &forgepb.ActivityOptions{Verb: "promote", Target: &forgepb.ActivityTarget{Kind: "release", Id: "to_env.id"}},
			want: []string{`field "to_env" is not a message`},
		},
		{
			name: "repeated",
			opt:  &forgepb.ActivityOptions{Verb: "promote", Target: &forgepb.ActivityTarget{Kind: "release", Id: "tags"}},
			want: []string{`field "tags" is repeated`},
		},
		{
			name: "response path to unknown field",
			opt:  &forgepb.ActivityOptions{Verb: "promote", Target: &forgepb.ActivityTarget{Kind: "release", Id: "response.release.sha"}},
			want: []string{`fixture.v1.Release has no field "sha"`},
		},
		{
			name:      "streaming with the interceptor writer",
			opt:       &forgepb.ActivityOptions{Verb: "tail", Target: &forgepb.ActivityTarget{Kind: "release", Id: "release_id"}},
			streaming: true,
			want:      []string{"streaming rpc"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			md := fixtureRPC(t, fixtureMethod{name: "Do", activity: tc.opt, streaming: tc.streaming})
			d, err := Resolve(md)
			if err == nil {
				t.Fatalf("Resolve = %+v, want an error", d)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not mention %q", err, w)
				}
			}
		})
	}
}

func TestResolve_StreamingHandlerWriterIsValid(t *testing.T) {
	opt := &forgepb.ActivityOptions{
		Verb: "tail", Target: &forgepb.ActivityTarget{Kind: "release", Id: "release_id"},
		Writer: forgepb.ActivityWriter_ACTIVITY_WRITER_HANDLER,
	}
	d, err := Resolve(fixtureRPC(t, fixtureMethod{name: "Tail", activity: opt, streaming: true}))
	if err != nil || d.Writer != WriterHandler {
		t.Fatalf("Resolve = %+v, %v; want a HANDLER declaration", d, err)
	}
}

func TestResolve_ImpliedCRUD(t *testing.T) {
	svc := fixtureService(t,
		fixtureMethod{name: "CreateWidget", in: "CreateWidgetRequest", out: "CreateWidgetResponse"},
		fixtureMethod{name: "UpdateWidget", in: "UpdateWidgetRequest", out: "UpdateWidgetResponse"},
		fixtureMethod{name: "DeleteWidget", in: "DeleteWidgetRequest", out: "DeleteWidgetResponse"},
		// CRUD-named, wrong shape: the response carries no Release.
		fixtureMethod{name: "CreateRelease", in: "CreateWidgetRequest", out: "CreateWidgetResponse"},
		// Not CRUD-named.
		fixtureMethod{name: "Promote", in: "PromoteRequest", out: "PromoteResponse"},
	)
	for _, tc := range []struct {
		rpc, verb, id, label string
		response             bool
	}{
		{"CreateWidget", "create", "response.widget.id", "response.widget.name", true},
		{"UpdateWidget", "update", "widget.id", "widget.name", false},
		{"DeleteWidget", "delete", "id", "", false},
	} {
		d, err := Resolve(svc.Methods().ByName(protoName(tc.rpc)))
		if err != nil || d == nil {
			t.Fatalf("%s: Resolve = %+v, %v", tc.rpc, d, err)
		}
		if !d.Implied || d.Verb != tc.verb || d.TargetKind != "widget" || d.TargetID.Spec != tc.id ||
			d.TargetLabel.Spec != tc.label || d.TargetID.Response != tc.response {
			t.Errorf("%s: got %+v", tc.rpc, d)
		}
	}
	for _, rpc := range []string{"CreateRelease", "Promote"} {
		if d, err := Resolve(svc.Methods().ByName(protoName(rpc))); d != nil || err != nil {
			t.Errorf("%s: Resolve = %+v, %v; want nothing declared", rpc, d, err)
		}
	}
}

func TestResolve_ExplicitBeatsImplied(t *testing.T) {
	md := fixtureRPC(t, fixtureMethod{
		name: "DeleteWidget", in: "DeleteWidgetRequest", out: "DeleteWidgetResponse",
		activity: &forgepb.ActivityOptions{None: "deletes a draft nobody else saw"},
	})
	d, err := Resolve(md)
	if err != nil || d.None == "" || d.Implied {
		t.Fatalf("Resolve = %+v, %v; want the explicit none", d, err)
	}
}

func TestIsMutating(t *testing.T) {
	for _, tc := range []struct {
		name          string
		noSideEffects bool
		want          bool
	}{
		{"GetRelease", false, false},
		{"ListReleases", false, false},
		{"WatchEvents", false, false},
		{"SearchLogs", false, false},
		{"BatchGetUsers", false, false},
		{"Describe", false, false},
		{"Getaway", false, true},
		{"Listen", false, true},
		{"PromoteRelease", false, true},
		{"CreateWidget", false, true},
		{"PreviewPromotion", true, false},
	} {
		md := fixtureRPC(t, fixtureMethod{name: tc.name, noSideEffects: tc.noSideEffects})
		if got := IsMutating(md); got != tc.want {
			t.Errorf("IsMutating(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestSnake(t *testing.T) {
	for in, want := range map[string]string{
		"Widget": "widget", "DeployTree": "deploy_tree", "APIKey": "api_key", "OAuth2Client": "o_auth2_client", "V2Thing": "v2_thing",
	} {
		if got := snake(in); got != want {
			t.Errorf("snake(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPathValue(t *testing.T) {
	md := fixtureRPC(t, fixtureMethod{name: "Promote", activity: promoteDecl()})
	d, err := Resolve(md)
	if err != nil {
		t.Fatal(err)
	}
	req := dynamicpb.NewMessage(md.Input())
	req.Set(md.Input().Fields().ByName("release_id"), protoValue("rel_1"))
	req.Set(md.Input().Fields().ByName("replicas"), protoInt32(3))
	if got := d.TargetID.value(req); got != "rel_1" {
		t.Errorf("target id = %q", got)
	}
	if got := d.Detail[1].value(req); got != "3" {
		t.Errorf("replicas = %q", got)
	}
	resp := dynamicpb.NewMessage(md.Output())
	if got := d.TargetLabel.value(resp); got != "" {
		t.Errorf("label through an unset message = %q, want empty", got)
	}
	if got := d.TargetLabel.value(nil); got != "" {
		t.Errorf("label from a nil message = %q, want empty", got)
	}
}

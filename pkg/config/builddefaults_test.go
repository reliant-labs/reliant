package config

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// The build-time defaults layer sits above the proto defaults and below the
// config file, environment and flags. These tests swap the process-wide stamp,
// so none of them is parallel.

func withStamp(t *testing.T, stamp string, msgs ...proto.Message) {
	t.Helper()
	SetBuildDefaults(stamp, msgs...)
	t.Cleanup(func() { SetBuildDefaults("") })
}

func portOf(m protoreflect.Message) int64 {
	return m.Get(m.Descriptor().Fields().ByName("port")).Int()
}

func TestBuildDefaults_OverrideProtoDefaultsAndLoseToEnvAndFlags(t *testing.T) {
	m := buildBlockMessage(t)
	withStamp(t, "PORT=7000&TRADER_MAX_PER_TICK=20", m.Interface())

	if err := LoadInto(nil, m.Interface()); err != nil {
		t.Fatal(err)
	}
	if got := portOf(m); got != 7000 {
		t.Errorf("port = %d, want the build default 7000 over the proto default 8080", got)
	}
	if got := blockIntOf(t, m, "trader", "max_per_tick"); got != 20 {
		t.Errorf("trader.max_per_tick = %d, want the build default 20 (blocks are stamped too)", got)
	}

	t.Setenv("PORT", "7100")
	m = buildBlockMessage(t)
	if err := LoadInto(nil, m.Interface()); err != nil {
		t.Fatal(err)
	}
	if got := portOf(m); got != 7100 {
		t.Errorf("port = %d, want env 7100 over the build default", got)
	}

	m = buildBlockMessage(t)
	cmd := &cobra.Command{Use: "t"}
	if err := RegisterFlagsFor(cmd.Flags(), m.Interface()); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("port", "7200"); err != nil {
		t.Fatal(err)
	}
	if err := LoadInto(cmd, m.Interface()); err != nil {
		t.Fatal(err)
	}
	if got := portOf(m); got != 7200 {
		t.Errorf("port = %d, want the flag 7200 over everything", got)
	}
}

func TestBuildDefaults_EscapedValues(t *testing.T) {
	m := buildValidatableMessage(t)
	withStamp(t, "CORS_ORIGINS=https%3A%2F%2Fa.example.com%2Chttps%3A%2F%2Fb.example.com", m.Interface())
	if err := LoadInto(nil, m.Interface()); err != nil {
		t.Fatal(err)
	}
	if got := m.Get(m.Descriptor().Fields().ByName("cors_origins")).String(); got != "https://a.example.com,https://b.example.com" {
		t.Errorf("cors_origins = %q", got)
	}
}

func TestBuildDefaults_AStampNobodyCheckedFailsEveryLoad(t *testing.T) {
	for _, c := range []struct {
		name, stamp, want string
	}{
		{"unknown key", "PROT=1", "PROT, which is the env_var of no config field"},
		{"near miss", "port=1", "did you mean PORT?"},
		{"sensitive", "DB_PASSWORD=hunter2", "DB_PASSWORD, a `sensitive` field"},
		{"twice", "PORT=1&PORT=2", "set PORT 2 times"},
		{"malformed", "PORT=%zz", "not in URL query form"},
		{"bad value", "PORT=eighty", `invalid build default "eighty" for config field port (PORT`},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := buildBlockMessage(t)
			withStamp(t, c.stamp, m.Interface(), buildFileLayerMessage(t).Interface())
			err := LoadInto(nil, m.Interface())
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want a load error containing %q, got: %v", c.want, err)
			}
		})
	}
}

func TestBuildDefaults_EmptyStampIsNoLayer(t *testing.T) {
	m := buildBlockMessage(t)
	withStamp(t, "", m.Interface())
	if err := LoadInto(nil, m.Interface()); err != nil {
		t.Fatal(err)
	}
	if got := portOf(m); got != 8080 {
		t.Errorf("port = %d, want the proto default", got)
	}
}

// Copyright (c) 2026 Reliant Labs

package clientversion

import (
	"errors"
	"testing"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
)

func TestParse(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		value   string
		want    Client
		missing bool
		wantErr bool
	}{
		{value: "web/1.9.2", want: Client{Kind: KindWeb, Version: "1.9.2"}},
		{value: "desktop/1.9.2", want: Client{Kind: KindDesktop, Version: "1.9.2"}},
		{value: "daemon/1.8.2-dev+abc123", want: Client{Kind: KindDaemon, Version: "1.8.2-dev+abc123"}},
		{value: "cli/1.9.2+abc123", want: Client{Kind: KindCLI, Version: "1.9.2+abc123"}},
		{value: "sdk-go/0.0.0-20261120010203-abcdef123456", want: Client{Kind: KindSDKGo, Version: "0.0.0-20261120010203-abcdef123456"}},
		{value: " sdk-ts/0.1.0 ", want: Client{Kind: KindSDKTS, Version: "0.1.0"}},
		{value: "", missing: true, wantErr: true},
		{value: "   ", missing: true, wantErr: true},
		{value: "web", wantErr: true},
		{value: "web/", wantErr: true},
		{value: "web/v1.9.2", wantErr: true},
		{value: "web/1.9", wantErr: true},
		{value: "web/latest", wantErr: true},
		{value: "toaster/1.0.0", wantErr: true},
		{value: "/1.0.0", wantErr: true},
	} {
		t.Run(tc.value, func(t *testing.T) {
			t.Parallel()
			got, err := Parse(tc.value)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Parse(%q) = %+v, want an error", tc.value, got)
				}
				if errors.Is(err, ErrMissing) != tc.missing {
					t.Fatalf("Parse(%q) error %v: ErrMissing = %v, want %v", tc.value, err, errors.Is(err, ErrMissing), tc.missing)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("Parse(%q) = %+v, %v; want %+v", tc.value, got, err, tc.want)
			}
		})
	}
}

func TestJudge(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		version, min string
		want         Result
	}{
		{"1.9.0", "1.9.0", Met},
		{"1.10.0", "1.9.0", Met},
		{"2.0.0", "1.9.0", Met},
		{"1.8.9", "1.9.0", Below},
		{"1.9.0-dev", "1.9.0", Below},
		{"1.9.0-rc.1", "1.9.0-rc.2", Below},
		{"1.9.0+abc", "1.9.0", Met},
		{"1.9.0-dev+abc123", "1.9.0-dev", Met},
		{"0.0.0-20261120010203-abcdef123456", "1.9.0", Below},
		{"1.9.0", "", Unknown},
		{"", "1.9.0", Unknown},
		{"garbage", "1.9.0", Unknown},
		{"1.9.0", "v1.9.0", Unknown},
	} {
		if got := Judge(tc.version, tc.min); got != tc.want {
			t.Errorf("Judge(%q, %q) = %v, want %v", tc.version, tc.min, got, tc.want)
		}
	}
}

func TestFloorFor(t *testing.T) {
	t.Parallel()

	policy := &configv1.ClientVersionPolicy{
		Web:     &configv1.WebClientVersionFloor{MinVersion: "1.0.0"},
		Desktop: &configv1.DesktopClientVersionFloor{MinVersion: "1.1.0"},
		Daemon:  &configv1.DaemonClientVersionFloor{MinVersion: "1.2.0"},
		Cli:     &configv1.CliClientVersionFloor{MinVersion: "1.3.0"},
		SdkGo:   &configv1.SdkGoClientVersionFloor{MinVersion: "1.4.0"},
		SdkTs:   &configv1.SdkTsClientVersionFloor{MinVersion: "1.5.0", UpgradeCommand: "npm i @reliantlabs/sdk"},
	}
	want := map[Kind]string{
		KindWeb: "1.0.0", KindDesktop: "1.1.0", KindDaemon: "1.2.0",
		KindCLI: "1.3.0", KindSDKGo: "1.4.0", KindSDKTS: "1.5.0",
	}
	for _, k := range Kinds {
		if got := FloorFor(policy, k).GetMinVersion(); got != want[k] {
			t.Errorf("FloorFor(%s) = %q, want %q", k, got, want[k])
		}
	}
	if got := FloorFor(policy, KindSDKTS).GetUpgradeCommand(); got != "npm i @reliantlabs/sdk" {
		t.Errorf("FloorFor(sdk-ts).UpgradeCommand = %q", got)
	}
	if got := FloorFor(nil, KindWeb).GetMinVersion(); got != "" {
		t.Errorf("FloorFor(nil policy) = %q, want no floor", got)
	}
	if got := FloorFor(policy, Kind("toaster")).GetMinVersion(); got != "" {
		t.Errorf("FloorFor(unknown kind) = %q, want no floor", got)
	}
}

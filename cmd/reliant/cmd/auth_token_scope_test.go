package cmd

import (
	"reflect"
	"strings"
	"testing"
)

func TestTokenScopesFromFlags(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      []string
		want    []string
		wantErr string
	}{
		{"default is reliant:api", nil, []string{"reliant:api"}, ""},
		{"repeated", []string{"runs:start", "triggers:fire"}, []string{"runs:start", "triggers:fire"}, ""},
		{"comma list and duplicates", []string{"runs:start,runs:read", "runs:start"}, []string{"runs:start", "runs:read"}, ""},
		{"explicit reliant:api", []string{"reliant:api"}, []string{"reliant:api"}, ""},
		{"manage", []string{"triggers:manage"}, []string{"triggers:manage"}, ""},
		{"another product's scope", []string{"deploy:write"}, nil, "want reliant:api, runs:read"},
		{"unknown", []string{"runs:everything"}, nil, "--scope"},
		{"empty", []string{""}, nil, "needs a value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tokenScopesFromFlags(tc.in)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}

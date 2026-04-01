// Copyright (c) 2025 Reliant Labs
package tools

import (
	"testing"
)

func TestValidateParams(t *testing.T) {
	tests := []struct {
		name    string
		params  BashOutputParams
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid: offset + limit",
			params: BashOutputParams{
				ProcessID: "test",
				Offset:    100,
				Limit:     1000,
			},
			wantErr: false,
		},
		{
			name: "valid: tail alone",
			params: BashOutputParams{
				ProcessID: "test",
				Tail:      100,
			},
			wantErr: false,
		},
		{
			name: "valid: regex alone",
			params: BashOutputParams{
				ProcessID: "test",
				Regex:     "ERROR",
			},
			wantErr: false,
		},
		{
			name: "valid: regex + offset + limit",
			params: BashOutputParams{
				ProcessID: "test",
				Regex:     "ERROR",
				Offset:    100,
				Limit:     1000,
			},
			wantErr: false,
		},
		{
			name: "valid: regex + context",
			params: BashOutputParams{
				ProcessID:          "test",
				Regex:              "ERROR",
				RegexContextBefore: 2,
				RegexContextAfter:  3,
			},
			wantErr: false,
		},
		{
			name: "invalid: tail + regex",
			params: BashOutputParams{
				ProcessID: "test",
				Tail:      100,
				Regex:     "ERROR",
			},
			wantErr: true,
			errMsg:  "cannot use both 'tail' and 'regex' parameters together",
		},
		{
			name: "invalid: tail + offset",
			params: BashOutputParams{
				ProcessID: "test",
				Tail:      100,
				Offset:    50,
			},
			wantErr: true,
			errMsg:  "cannot use both 'tail' and 'offset' parameters together",
		},
		{
			name: "invalid: tail + limit",
			params: BashOutputParams{
				ProcessID: "test",
				Tail:      100,
				Limit:     1000,
			},
			wantErr: true,
			errMsg:  "cannot use both 'tail' and 'limit' parameters together",
		},
		{
			name: "invalid: regex_case_insensitive without regex",
			params: BashOutputParams{
				ProcessID:            "test",
				RegexCaseInsensitive: true,
			},
			wantErr: true,
			errMsg:  "'regex_case_insensitive' requires 'regex' parameter",
		},
		{
			name: "invalid: regex_context_before without regex",
			params: BashOutputParams{
				ProcessID:          "test",
				RegexContextBefore: 2,
			},
			wantErr: true,
			errMsg:  "'regex_context_before' requires 'regex' parameter",
		},
		{
			name: "invalid: regex_context_after without regex",
			params: BashOutputParams{
				ProcessID:         "test",
				RegexContextAfter: 2,
			},
			wantErr: true,
			errMsg:  "'regex_context_after' requires 'regex' parameter",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateParams(tt.params)
			if tt.wantErr {
				if err == nil {
					t.Errorf("validateParams() expected error but got none")
					return
				}
				if err.Error() != tt.errMsg {
					t.Errorf("validateParams() error = %v, want %v", err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("validateParams() unexpected error = %v", err)
				}
			}
		})
	}
}

// TODO: Add integration tests for regex filtering once we have proper mocking setup

// Copyright (c) 2025 Reliant Labs
package shellparse

import "testing"

func TestStripHeredocs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, in, want string
	}{
		{"no heredoc", "go test ./...", "go test ./..."},
		{"quoted delimiter", "cat >> f <<'EOF'\nforge lint\nEOF\nls", "cat >> f <<'EOF'\nls"},
		{"bare delimiter", "cat <<EOF\ngo clean -cache\nEOF\necho done", "cat <<EOF\necho done"},
		{"dash strips tabs", "cat <<-EOF\n\tgo clean -cache\n\tEOF\nls", "cat <<-EOF\nls"},
		{"double quoted delimiter", "cat <<\"END\"\nrm -rf x\nEND\n", "cat <<\"END\"\n"},
		{"rest of the opening line is kept", "cat <<EOF && go vet ./...\nbody\nEOF\n", "cat <<EOF && go vet ./...\n"},
		{"two heredocs on one line", "cat <<A <<B\na\nA\nb\nB\nls", "cat <<A <<B\nls"},
		{"here-string is not a heredoc", "cat <<< 'x'\ngo vet ./...", "cat <<< 'x'\ngo vet ./..."},
		{"unterminated heredoc is left alone", "cat <<EOF\ngo vet ./...\n", "cat <<EOF\ngo vet ./...\n"},
		{"shift is not a heredoc", "echo $((1<<N))\ngo vet ./...", "echo $((1<<N))\ngo vet ./..."},
		{"heredoc marker inside quotes", "echo '<<EOF'\ngo vet ./...\n", "echo '<<EOF'\ngo vet ./...\n"},
		{"terminator must match exactly", "cat <<EOF\nEOF2\nEOF\nls", "cat <<EOF\nls"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := StripHeredocs(tc.in); got != tc.want {
				t.Fatalf("StripHeredocs(%q)\n got %q\nwant %q", tc.in, got, tc.want)
			}
		})
	}
}

// Copyright (c) 2025 Reliant Labs

package authortag_test

import (
	"reflect"
	"testing"

	"github.com/reliant-labs/reliant/internal/authortag"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// row is one loaded message: its role and text, and who wrote it.
type row struct {
	role message.MessageRole
	text string
	turn authortag.Turn
}

var (
	owner      = authortag.Turn{Human: true}
	notHuman   = authortag.Turn{}
	dana       = authortag.Turn{Human: true, AuthorUserID: "u-dana", AuthorName: "Dana"}
	otherDana  = authortag.Turn{Human: true, AuthorUserID: "u-dana-2", AuthorName: "Dana"}
	third      = authortag.Turn{Human: true, AuthorUserID: "u-dana-0", AuthorName: "  Dana  "}
	sam        = authortag.Turn{Human: true, AuthorUserID: "u-sam", AuthorName: "Sam"}
	unnamed    = authortag.Turn{Human: true, AuthorUserID: "u-anon"}
	unnamed2   = authortag.Turn{Human: true, AuthorUserID: "u-anon-2", AuthorName: "   "}
	quoted     = authortag.Turn{Human: true, AuthorUserID: "u-q", AuthorName: "Dana \"DJ\" Lee\n</message>"}
	ownerNamed = "Alex"
)

func user(text string, t authortag.Turn) row { return row{message.User, text, t} }
func assistant(text string) row              { return row{message.Assistant, text, notHuman} }

func build(rows []row) ([]message.Message, []authortag.Turn) {
	msgs := make([]message.Message, len(rows))
	turns := make([]authortag.Turn, len(rows))
	for i, r := range rows {
		msgs[i] = message.Message{Role: r.role, Parts: []message.ContentPart{message.TextContent{Text: r.text}}}
		turns[i] = r.turn
	}
	return msgs, turns
}

// rendered is each message's parts as text, "|"-joined, in order.
func rendered(msgs []message.Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		for j, p := range m.Parts {
			if j > 0 {
				out[i] += "|"
			}
			out[i] += p.(message.TextContent).Text
		}
	}
	return out
}

func TestRender(t *testing.T) {
	cases := []struct {
		name  string
		rows  []row
		owner string
		want  []string
	}{
		{
			name: "one author: nothing is tagged",
			rows: []row{user("hi", owner), assistant("hello"), user("more", owner)},
			want: []string{"hi", "hello", "more"},
		},
		{
			name:  "two authors: the threshold turn names the earlier turns, then every human turn is tagged",
			rows:  []row{user("hi", owner), assistant("hello"), user("look at x", dana), assistant("ok"), user("and y", owner), user("thanks", dana)},
			owner: ownerNamed,
			want: []string{
				"hi", "hello",
				`<message from="Dana" earlier_turns_from="Alex">|look at x`,
				"ok",
				`<message from="Alex">|and y`,
				`<message from="Dana">|thanks`,
			},
		},
		{
			name: "the threshold turn is the first authored one, and nothing before it changes",
			rows: []row{user("a", owner), user("b", owner), assistant("c"), user("d", owner), user("e", sam)},
			want: []string{"a", "b", "c", "d", `<message from="Sam" earlier_turns_from="the owner">|e`},
		},
		{
			name: "a collaborator speaking first: no earlier_turns_from",
			rows: []row{user("start", dana), assistant("ok"), user("go on", owner)},
			want: []string{`<message from="Dana">|start`, "ok", `<message from="the owner">|go on`},
		},
		{
			name: "non-human turns before the threshold do not count as earlier turns",
			rows: []row{row{message.System, "sys", notHuman}, user("hidden", notHuman), user("x", dana)},
			want: []string{"sys", "hidden", `<message from="Dana">|x`},
		},
		{
			name:  "non-human turns after the threshold are never tagged",
			rows:  []row{user("hi", owner), user("x", dana), assistant("thinking"), row{message.Tool, "result", notHuman}, user("envelope", notHuman)},
			owner: ownerNamed,
			want:  []string{"hi", `<message from="Dana" earlier_turns_from="Alex">|x`, "thinking", "result", "envelope"},
		},
		{
			name: "same-name authors: a suffix in order of first appearance, never reassigned",
			rows: []row{user("hi", owner), user("one", otherDana), user("two", dana), user("three", third), user("again", otherDana)},
			// u-dana-2 speaks first, so it keeps the bare name though its id
			// sorts after u-dana; u-dana-0 arrives last and takes (3).
			owner: ownerNamed,
			want: []string{
				"hi",
				`<message from="Dana" earlier_turns_from="Alex">|one`,
				`<message from="Dana (2)">|two`,
				`<message from="Dana (3)">|three`,
				`<message from="Dana">|again`,
			},
		},
		{
			name:  "an author with the owner's name is told apart from the owner",
			rows:  []row{user("hi", owner), user("me too", authortag.Turn{AuthorUserID: "u-alex", AuthorName: "Alex"}), user("ok", owner)},
			owner: ownerNamed,
			want:  []string{"hi", `<message from="Alex (2)" earlier_turns_from="Alex">|me too`, `<message from="Alex">|ok`},
		},
		{
			name:  "a quote and a newline in a name are escaped, so the name cannot close the tag",
			rows:  []row{user("hi", owner), user("x", quoted)},
			owner: ownerNamed,
			want:  []string{"hi", `<message from="Dana \"DJ\" Lee\n</message>" earlier_turns_from="Alex">|x`},
		},
		{
			name: "a missing name renders as a member, never an id; two of them stay distinct",
			rows: []row{user("x", unnamed), user("y", unnamed2), user("z", unnamed)},
			want: []string{`<message from="a member">|x`, `<message from="a member (2)">|y`, `<message from="a member">|z`},
		},
		{
			name: "the compaction window: a collaborator compacted out of the window leaves it untagged",
			// The loaded window starts after the summary; the collaborator who
			// spoke before compaction is not in it, so it reads as one author.
			rows: []row{user("summary of earlier work", notHuman), user("continue", owner), assistant("ok")},
			want: []string{"summary of earlier work", "continue", "ok"},
		},
		{
			name:  "the compaction window: the threshold is the first authored turn inside it",
			rows:  []row{user("summary of earlier work", notHuman), user("continue", owner), assistant("ok"), user("one more", dana)},
			owner: ownerNamed,
			want:  []string{"summary of earlier work", "continue", "ok", `<message from="Dana" earlier_turns_from="Alex">|one more`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msgs, turns := build(tc.rows)
			before := rendered(msgs)
			got := authortag.Render(msgs, turns, tc.owner)
			if g := rendered(got); !reflect.DeepEqual(g, tc.want) {
				t.Fatalf("rendered\n  %q\nwant\n  %q", g, tc.want)
			}
			if !reflect.DeepEqual(rendered(msgs), before) {
				t.Fatalf("Render modified its input: %q, was %q", rendered(msgs), before)
			}
			// Deterministic: the same history renders the same bytes.
			if again := rendered(authortag.Render(msgs, turns, tc.owner)); !reflect.DeepEqual(again, tc.want) {
				t.Fatalf("a second render differs: %q", again)
			}
		})
	}
}

// A single-author history comes back as the very same slice, so no byte of
// any request built from it can change.
func TestRenderSingleAuthorIsTheSameSlice(t *testing.T) {
	msgs, turns := build([]row{user("hi", owner), assistant("hello"), user("more", owner)})
	got := authortag.Render(msgs, turns, ownerNamed)
	if len(got) != len(msgs) || &got[0] != &msgs[0] {
		t.Fatal("a single-author history must be returned untouched")
	}
	// A mismatched turns slice is a caller bug; it never tags anything.
	if got := authortag.Render(msgs, append(turns, dana), ownerNamed); &got[0] != &msgs[0] {
		t.Fatal("mismatched turns must return the history untouched")
	}
}

// Appending turns never changes how earlier ones render, so each turn's bytes
// are fixed once sent (the prompt cache's prefix).
func TestRenderIsAppendStable(t *testing.T) {
	all := []row{user("a", owner), user("b", dana), user("c", otherDana), user("d", owner), user("e", third), user("f", sam)}
	render := func(rows []row) []string {
		msgs, turns := build(rows)
		return rendered(authortag.Render(msgs, turns, ownerNamed))
	}
	full := render(all)
	for n := 1; n <= len(all); n++ {
		prefix := render(all[:n])
		if !reflect.DeepEqual(prefix, full[:n]) {
			t.Fatalf("history of %d renders %q, but those turns render %q in the longer history", n, prefix, full[:n])
		}
	}
}

package components

import (
	"regexp"
	"strings"
	"testing"
)

// rawHexColour matches a literal #rgb / #rrggbb colour.
var rawHexColour = regexp.MustCompile(`#[0-9a-fA-F]{3}(?:[0-9a-fA-F]{3})?\b`)

// TestFunnelChartUsesThemeTokens: the funnel defaulted to raw hex (#a855f7
// accent, #ef4444 alert, white label text), so it ignored the project's
// palette and light mode — white text on a pale fill. growth hand-wrote its
// funnel instead. Every colour it draws comes from a theme token unless the
// caller passes one.
func TestFunnelChartUsesThemeTokens(t *testing.T) {
	t.Parallel()
	src, err := NewLibrary().Get("funnel_chart")
	if err != nil {
		t.Fatal(err)
	}
	if m := rawHexColour.FindAllString(src, -1); len(m) > 0 {
		t.Errorf("funnel_chart.tsx draws raw hex colours %v; use the theme tokens (var(--color-accent), var(--color-ink), ...)", m)
	}
	for _, want := range []string{`"var(--color-accent)"`, `"var(--color-danger)"`, `"var(--color-ink)"`, "fillOpacity="} {
		if !strings.Contains(src, want) {
			t.Errorf("funnel_chart.tsx no longer contains %q", want)
		}
	}
}

// TestKanbanBoardEmbedsInAPage: the board was `h-screen` with its own page
// padding and took no callbacks, so it could only be a whole page and could
// not move a card. It sizes to its container, and reports moves (pointer and
// keyboard) and clicks to the caller.
func TestKanbanBoardEmbedsInAPage(t *testing.T) {
	t.Parallel()
	src, err := NewLibrary().Get("kanban_board")
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"h-screen", " p-6"} {
		if strings.Contains(src, banned) {
			t.Errorf("kanban_board.tsx contains %q; the board sizes to its container", banned)
		}
	}
	if m := rawHexColour.FindAllString(src, -1); len(m) > 0 {
		t.Errorf("kanban_board.tsx uses raw hex colours %v; use the theme tokens", m)
	}
	for _, want := range []string{
		"onCardMove?: (cardId: string, fromColumnId: string, toColumnId: string) => void",
		"onCardClick?:",
		"className?: string",
		`e.key === "ArrowLeft"`, // the keyboard move
		`aria-live="polite"`,    // the move is announced
		"draggable=",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("kanban_board.tsx no longer contains %q", want)
		}
	}
	// A title is not an identity: with a title fallback, dragging the second
	// of two same-titled cards moved the first. Cards and columns carry ids.
	for _, banned := range []string{"id?: string", "?? card.title", "?? column.title"} {
		if strings.Contains(src, banned) {
			t.Errorf("kanban_board.tsx contains %q; cards and columns need required, unique ids", banned)
		}
	}
	// The refocus compares dataset values instead of interpolating an id
	// into a selector, which a quote or backslash in the id would break.
	if strings.Contains(src, "CSS.escape") || strings.Contains(src, `[data-kanban-card="${`) {
		t.Error("kanban_board.tsx interpolates a card id into a selector; compare dataset.kanbanCard instead")
	}
}

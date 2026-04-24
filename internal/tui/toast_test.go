package tui

import (
	"strings"
	"testing"
)

// TestToastShowBumpsGeneration ensures every Show invalidates prior
// TTL ticks. Without this, a fast second push would have its toast
// cleared by the first push's stale timer.
func TestToastShowBumpsGeneration(t *testing.T) {
	t.Parallel()
	var tm toastModel
	tm.Show("first", toastInfo)
	gen1 := tm.gen
	tm.Show("second", toastSuccess)
	if tm.gen == gen1 {
		t.Fatalf("expected gen to bump on Show, stayed at %d", tm.gen)
	}
	if tm.text != "second" {
		t.Fatalf("expected text=second, got %q", tm.text)
	}
}

// TestToastHandleClearStaleDropped guards against a TTL message from
// an earlier Show clearing a fresher toast.
func TestToastHandleClearStaleDropped(t *testing.T) {
	t.Parallel()
	var tm toastModel
	tm.Show("first", toastInfo)
	staleGen := tm.gen
	tm.Show("second", toastInfo)
	if cleared := tm.HandleClear(toastClearMsg{gen: staleGen}); cleared {
		t.Fatal("stale toastClearMsg should not clear a fresher toast")
	}
	if !tm.visible || tm.text != "second" {
		t.Fatalf("toast should remain visible with text=second, got visible=%v text=%q", tm.visible, tm.text)
	}
}

// TestToastHandleClearFresh checks the happy path: the matching gen
// clears the toast.
func TestToastHandleClearFresh(t *testing.T) {
	t.Parallel()
	var tm toastModel
	tm.Show("hi", toastInfo)
	if cleared := tm.HandleClear(toastClearMsg{gen: tm.gen}); !cleared {
		t.Fatal("matching toastClearMsg should clear the toast")
	}
	if tm.visible {
		t.Fatal("toast should be invisible after clear")
	}
}

// TestToastRenderInvisible ensures Render returns empty when no toast
// is active — overlayBottomRight relies on the empty-string short
// circuit to avoid composing for a no-op case.
func TestToastRenderInvisible(t *testing.T) {
	t.Parallel()
	var tm toastModel
	if got := tm.Render(80); got != "" {
		t.Fatalf("invisible toast must render empty, got %q", got)
	}
}

// TestToastRenderVisibleContainsText is a smoke test that the toast
// content actually makes it into the rendered output (styled or not).
func TestToastRenderVisibleContainsText(t *testing.T) {
	t.Parallel()
	var tm toastModel
	tm.Show("Pushed feat/login", toastSuccess)
	out := tm.Render(80)
	if !strings.Contains(out, "Pushed feat/login") {
		t.Fatalf("rendered toast should contain text, got %q", out)
	}
}

// TestOverlayBottomRightEmptyToastReturnsBase guarantees the no-op
// path leaves the base view byte-for-byte identical, so a redraw with
// no toast does not perturb the underlying compositor stack.
func TestOverlayBottomRightEmptyToastReturnsBase(t *testing.T) {
	t.Parallel()
	base := "hello\nworld"
	if got := overlayBottomRight(base, "", 20, 5); got != base {
		t.Fatalf("empty toast must return base unchanged, got %q", got)
	}
}

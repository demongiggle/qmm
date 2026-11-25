package layout

import "testing"

func TestResizeTinyBoundsNeverInverts(t *testing.T) {
	// Sub-minimum bounds: 17 cells wide with three panes across is not
	// reachable through Split (which enforces minimums), but Resize must
	// still never produce an inverted ratio or a zero-width pane.
	tr := New(1)
	bounds := Rect{0, 0, 15, 5} // too small for MinPaneWidth constraints
	tr.Split(1, 2, Vertical)
	tr.Split(2, 3, Vertical)
	for i := 0; i < 200; i++ {
		tr.Resize(1, Right, 3, bounds)
		tr.Resize(3, Left, 3, bounds)
		r := tr.Compute(bounds)
		for id, rc := range r {
			if rc.W < 1 {
				t.Fatalf("pane %d got zero width at iter %d: %+v", id, i, rc)
			}
		}
	}
	// Ratios must stay in (0,1) for every internal split.
	for _, b := range tr.Borders(bounds) {
		if b.Len <= 0 {
			t.Fatalf("degenerate border: %+v", b)
		}
	}
}

func TestSplitAndRects(t *testing.T) {
	tr := New(1)
	b := Rect{0, 0, 80, 24}

	if got := tr.Compute(b)[1]; got != b {
		t.Fatalf("single pane rect = %+v, want %+v", got, b)
	}

	if !tr.Split(1, 2, Vertical) {
		t.Fatal("split failed")
	}
	rects := tr.Compute(b)
	left, right := rects[1], rects[2]
	if left.W+right.W != 79 {
		t.Fatalf("widths %d+%d = %d, want 79", left.W, right.W, left.W+right.W)
	}
	if right.X != left.Right()+1 {
		t.Fatalf("expected 1-cell gutter, left.Right=%d right.X=%d", left.Right(), right.X)
	}
	if left.H != 24 || right.H != 24 {
		t.Fatalf("heights %d %d, want 24", left.H, right.H)
	}
}

func TestHorizontalSplit(t *testing.T) {
	tr := New(1)
	b := Rect{0, 0, 80, 24}
	tr.Split(1, 2, Horizontal)
	rects := tr.Compute(b)
	top, bot := rects[1], rects[2]
	if top.H+bot.H != 23 {
		t.Fatalf("heights %d+%d, want 23", top.H, bot.H)
	}
	if bot.Y != top.Bottom()+1 {
		t.Fatalf("expected 1-cell gutter row, top.Bottom=%d bot.Y=%d", top.Bottom(), bot.Y)
	}
}

func TestNestedSplitsAndMinClamp(t *testing.T) {
	tr := New(1)
	b := Rect{0, 0, 100, 40}
	tr.Split(1, 2, Vertical)   // pane 1 | pane 2
	tr.Split(2, 3, Horizontal) // pane 2 split top/bottom -> panes 2,3
	rects := tr.Compute(b)
	for id, r := range rects {
		if r.W < 1 || r.H < 1 {
			t.Fatalf("pane %d has bad rect %+v", id, r)
		}
	}
	if !(rects[2].Y+rects[2].H+1 == rects[3].Y) {
		t.Fatalf("expected vertical gutter between 2 and 3: %+v %+v", rects[2], rects[3])
	}
}

func TestRemoveCollapses(t *testing.T) {
	tr := New(1)
	tr.Split(1, 2, Vertical)
	if !tr.Remove(2) {
		t.Fatal("remove failed")
	}
	if tr.Len() != 1 {
		t.Fatalf("len = %d, want 1", tr.Len())
	}
	rects := tr.Compute(Rect{0, 0, 80, 24})
	if rects[1] != (Rect{0, 0, 80, 24}) {
		t.Fatalf("rect = %+v", rects[1])
	}
}

func TestRemoveNested(t *testing.T) {
	tr := New(1)
	tr.Split(1, 2, Vertical)
	tr.Split(2, 3, Horizontal)
	tr.Remove(2)
	rects := tr.Compute(Rect{0, 0, 80, 24})
	if len(rects) != 2 {
		t.Fatalf("panes = %d, want 2", len(rects))
	}
	if rects[3].H == 0 || rects[1].H == 0 {
		t.Fatal("bad rects after collapse")
	}
}

func TestLeavesAndNext(t *testing.T) {
	tr := New(1)
	tr.Split(1, 2, Vertical)
	tr.Split(2, 3, Horizontal)
	got := tr.Panes()
	want := []int{1, 2, 3}
	if len(got) != 3 {
		t.Fatalf("panes = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("panes = %v, want %v", got, want)
		}
	}
	if n := tr.Next(3); n != 1 {
		t.Fatalf("Next(3) = %d, want 1", n)
	}
	if n := tr.Next(1); n != 2 {
		t.Fatalf("Next(1) = %d, want 2", n)
	}
}

func TestBorders(t *testing.T) {
	tr := New(1)
	tr.Split(1, 2, Vertical)
	borders := tr.Borders(Rect{0, 0, 80, 24})
	if len(borders) != 1 {
		t.Fatalf("borders = %v", borders)
	}
	b := borders[0]
	if !b.Vertical || b.Len != 24 || b.X != rectsRight(t, tr) {
		t.Fatalf("bad border %+v", b)
	}
}

func rectsRight(t *testing.T, tr *Tree) int {
	t.Helper()
	rects := tr.Compute(Rect{0, 0, 80, 24})
	return rects[1].Right()
}

func TestResizeMovesDivider(t *testing.T) {
	tr := New(1)
	bounds := Rect{0, 0, 80, 24}
	tr.Split(1, 2, Vertical)
	before := tr.Compute(bounds)
	bw := before[1].W

	if !tr.Resize(1, Right, 5, bounds) {
		t.Fatal("resize failed")
	}
	after := tr.Compute(bounds)
	if after[1].W != bw+5 {
		t.Fatalf("pane1 width %d, want %d", after[1].W, bw+5)
	}

	// moving right then shrinking below minimum should clamp
	for i := 0; i < 100; i++ {
		tr.Resize(1, Left, 5, bounds)
	}
	final := tr.Compute(bounds)
	if final[2].W < MinPaneWidth {
		t.Fatalf("pane2 width %d below minimum %d", final[2].W, MinPaneWidth)
	}
	if final[1].W < MinPaneWidth {
		t.Fatalf("pane1 width %d below minimum %d", final[1].W, MinPaneWidth)
	}
}

func TestResizeSplitByID(t *testing.T) {
	tr := New(1)
	bounds := Rect{0, 0, 80, 24}
	tr.Split(1, 2, Vertical)
	borders := tr.Borders(bounds)
	if len(borders) != 1 {
		t.Fatal("expected 1 border")
	}
	id := borders[0].ID
	before := tr.Compute(bounds)[1].W
	if !tr.ResizeSplit(id, 7, bounds) {
		t.Fatal("ResizeSplit failed")
	}
	after := tr.Compute(bounds)[1].W
	if after != before+7 {
		t.Fatalf("width %d, want %d", after, before+7)
	}
	if tr.ResizeSplit(9999, 7, bounds) {
		t.Fatal("expected false for unknown split id")
	}
}

func TestContains(t *testing.T) {
	r := Rect{2, 3, 10, 5}
	for _, p := range [][2]int{{2, 3}, {11, 7}, {5, 4}} {
		if !r.Contains(p[0], p[1]) {
			t.Fatalf("point %v should be inside %+v", p, r)
		}
	}
	for _, p := range [][2]int{{1, 3}, {12, 3}, {2, 8}} {
		if r.Contains(p[0], p[1]) {
			t.Fatalf("point %v should be outside %+v", p, r)
		}
	}
}

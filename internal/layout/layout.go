// Package layout implements the binary split tree used to arrange panes
// inside a window. Leaf nodes carry a pane ID; internal nodes describe a
// split with a direction and a ratio for the leading child.
package layout

import "errors"

// Split direction of an internal node.
const (
	// Vertical split: the divider is a vertical line; children sit side by side.
	Vertical Direction = iota
	// Horizontal split: the divider is a horizontal line; children are stacked.
	Horizontal
)

// Direction is the orientation of a split.
type Direction int

// ResizeDirection selects which way Resize grows/shrinks the pane.
const (
	Up    = "up"
	Down  = "down"
	Left  = "left"
	Right = "right"
)

// Minimum pane content size in cells.
const (
	MinPaneWidth  = 10
	MinPaneHeight = 3
)

// Rect is an axis-aligned rectangle in cell units.
type Rect struct {
	X, Y, W, H int
}

// Right returns the exclusive right edge.
func (r Rect) Right() int { return r.X + r.W }

// Bottom returns the exclusive bottom edge.
func (r Rect) Bottom() int { return r.Y + r.H }

// Contains reports whether (x, y) lies inside r.
func (r Rect) Contains(x, y int) bool {
	return x >= r.X && x < r.Right() && y >= r.Y && y < r.Bottom()
}

// Border describes one segment of a divider between two panes.
type Border struct {
	// ID identifies the split that produced this border (for drag-resize).
	ID       int
	X, Y     int
	Len      int
	Vertical bool // true: column of cells; false: row of cells
}

// Contains reports whether (x, y) lies on the border segment.
func (b Border) Contains(x, y int) bool {
	if b.Vertical {
		return x == b.X && y >= b.Y && y < b.Y+b.Len
	}
	return y == b.Y && x >= b.X && x < b.X+b.Len
}

type node struct {
	id    int
	leaf  bool
	pane  int
	dir   Direction
	ratio float64
	left  *node
	right *node
}

// Tree is a split tree for one window.
type Tree struct {
	root      *node
	nextID    int
	paneCount int
}

// New returns a tree whose single root leaf is paneID.
func New(paneID int) *Tree {
	return &Tree{root: &node{id: 1, leaf: true, pane: paneID}, nextID: 2, paneCount: 1}
}

// Empty reports whether the tree has no panes.
func (t *Tree) Empty() bool { return t.root == nil }

// Len returns the number of panes in the tree.
func (t *Tree) Len() int { return t.paneCount }

// Panes returns pane IDs in depth-first (left-to-right, top-to-bottom) order.
func (t *Tree) Panes() []int {
	var out []int
	var walk func(n *node)
	walk = func(n *node) {
		if n == nil {
			return
		}
		if n.leaf {
			out = append(out, n.pane)
			return
		}
		walk(n.left)
		walk(n.right)
	}
	walk(t.root)
	return out
}

// Next returns the pane after current in DFS order, wrapping around.
func (t *Tree) Next(current int) int {
	ids := t.Panes()
	if len(ids) == 0 {
		return 0
	}
	for i, id := range ids {
		if id == current {
			return ids[(i+1)%len(ids)]
		}
	}
	return ids[0]
}

// Has reports whether paneID exists in the tree.
func (t *Tree) Has(paneID int) bool {
	return t.find(t.root, paneID) != nil
}

func (t *Tree) find(n *node, paneID int) *node {
	if n == nil {
		return nil
	}
	if n.leaf {
		if n.pane == paneID {
			return n
		}
		return nil
	}
	if m := t.find(n.left, paneID); m != nil {
		return m
	}
	return t.find(n.right, paneID)
}

// Split divides paneID into two panes. The original pane keeps its ID and is
// placed in the leading position (left or top); the new pane gets newPaneID.
func (t *Tree) Split(paneID, newPaneID int, dir Direction) bool {
	target := t.find(t.root, paneID)
	if target == nil {
		return false
	}
	fresh := &node{id: t.nextID, leaf: true, pane: newPaneID}
	t.nextID++
	old := *target
	*target = node{
		id:    t.nextID,
		dir:   dir,
		ratio: 0.5,
		left:  &node{id: old.id, leaf: true, pane: old.pane},
		right: fresh,
	}
	t.nextID++
	t.paneCount++
	return true
}

// Remove removes paneID from the tree, collapsing single-child splits.
// It reports whether the pane was present.
func (t *Tree) Remove(paneID int) bool {
	if t.find(t.root, paneID) == nil {
		return false
	}
	t.root = t.remove(t.root, paneID)
	t.paneCount--
	return true
}

func (t *Tree) remove(n *node, paneID int) *node {
	if n == nil {
		return nil
	}
	if n.leaf {
		if n.pane == paneID {
			return nil
		}
		return n
	}
	n.left = t.remove(n.left, paneID)
	n.right = t.remove(n.right, paneID)
	switch {
	case n.left == nil && n.right == nil:
		return nil
	case n.left == nil:
		return n.right
	case n.right == nil:
		return n.left
	default:
		return n
	}
}

// Compute assigns rects to every pane and returns them keyed by pane ID.
func (t *Tree) Compute(bounds Rect) map[int]Rect {
	out := make(map[int]Rect, t.paneCount)
	t.compute(t.root, bounds, out, nil)
	return out
}

// Borders returns the divider segments for the given bounds.
func (t *Tree) Borders(bounds Rect) []Border {
	var out []Border
	t.compute(t.root, bounds, map[int]Rect{}, &out)
	return out
}

func (t *Tree) compute(n *node, r Rect, panes map[int]Rect, borders *[]Border) Rect {
	if n == nil {
		return Rect{}
	}
	if n.leaf {
		panes[n.pane] = r
		return r
	}
	if n.dir == Vertical {
		avail := r.W - 1
		if avail < 0 {
			avail = 0
		}
		lw := splitCells(avail, n.ratio, MinPaneWidth)
		left := Rect{X: r.X, Y: r.Y, W: lw, H: r.H}
		right := Rect{X: r.X + lw + 1, Y: r.Y, W: avail - lw, H: r.H}
		if borders != nil {
			*borders = append(*borders, Border{ID: n.id, X: r.X + lw, Y: r.Y, Len: r.H, Vertical: true})
		}
		t.compute(n.left, left, panes, borders)
		t.compute(n.right, right, panes, borders)
		return r
	}
	avail := r.H - 1
	if avail < 0 {
		avail = 0
	}
	lh := splitCells(avail, n.ratio, MinPaneHeight)
	top := Rect{X: r.X, Y: r.Y, W: r.W, H: lh}
	bottom := Rect{X: r.X, Y: r.Y + lh + 1, W: r.W, H: avail - lh}
	if borders != nil {
		*borders = append(*borders, Border{ID: n.id, X: r.X, Y: r.Y + lh, Len: r.W, Vertical: false})
	}
	t.compute(n.left, top, panes, borders)
	t.compute(n.right, bottom, panes, borders)
	return r
}

// splitCells converts a ratio and available cells into the leading child's
// cell count, honouring minimum sizes when the area allows. Both children
// are guaranteed at least one cell whenever avail >= 2.
func splitCells(avail int, ratio float64, min int) int {
	if avail <= 0 {
		return 0
	}
	if avail == 1 {
		return 1
	}
	n := int(float64(avail)*ratio + 0.5)
	if avail >= 2*min {
		if n < min {
			n = min
		}
		if avail-n < min {
			n = avail - min
		}
		return n
	}
	// Not enough room for both minimums; keep both children non-empty.
	if n < 1 {
		n = 1
	}
	if n > avail-1 {
		n = avail - 1
	}
	return n
}

// ratioBounds returns the [lo, hi] range a split ratio may take given that
// both children must keep at least one cell; when the area can fit both
// sides at `min` cells, those minima are enforced instead. The result always
// satisfies lo <= hi for avail >= 2.
func ratioBounds(avail, min float64) (float64, float64) {
	if avail >= 2*min {
		lo := min / avail
		return lo, 1 - lo
	}
	return 1 / avail, 1 - 1/avail
}

type pathEntry struct {
	node   *node
	bounds Rect
}

// findPath walks from the root to paneID, collecting ancestors with bounds.
func (t *Tree) findPath(paneID int, bounds Rect) []pathEntry {
	var path []pathEntry
	cur := bounds
	n := t.root
	for n != nil {
		if n.leaf {
			if n.pane != paneID {
				return nil
			}
			return path
		}
		var child *node
		var childBounds Rect
		if n.dir == Vertical {
			avail := cur.W - 1
			lw := splitCells(avail, n.ratio, MinPaneWidth)
			left := Rect{X: cur.X, Y: cur.Y, W: lw, H: cur.H}
			right := Rect{X: cur.X + lw + 1, Y: cur.Y, W: avail - lw, H: cur.H}
			if t.find(n.left, paneID) != nil {
				child, childBounds = n.left, left
			} else {
				child, childBounds = n.right, right
			}
		} else {
			avail := cur.H - 1
			lh := splitCells(avail, n.ratio, MinPaneHeight)
			top := Rect{X: cur.X, Y: cur.Y, W: cur.W, H: lh}
			bottom := Rect{X: cur.X, Y: cur.Y + lh + 1, W: cur.W, H: avail - lh}
			if t.find(n.left, paneID) != nil {
				child, childBounds = n.left, top
			} else {
				child, childBounds = n.right, bottom
			}
		}
		path = append(path, pathEntry{node: n, bounds: cur})
		cur = childBounds
		n = child
	}
	return nil
}

// Resize moves the nearest split divider on the pane's path in the requested
// direction by delta cells. Left/Right apply to vertical dividers (side-by-side
// panes), Up/Down to horizontal dividers (stacked panes). Moving a divider
// right/down always increases the leading child's share, regardless of which
// side the pane is on. It reports whether any divider moved.
func (t *Tree) Resize(paneID int, dir string, delta int, bounds Rect) bool {
	if delta == 0 {
		return false
	}
	if dir != Left && dir != Right && dir != Up && dir != Down {
		return false
	}
	path := t.findPath(paneID, bounds)
	for i := len(path) - 1; i >= 0; i-- {
		entry := path[i]
		n := entry.node
		horizontal := dir == Left || dir == Right
		if (n.dir == Vertical) != horizontal {
			continue
		}
		var avail float64
		var minC float64
		if n.dir == Vertical {
			avail = float64(entry.bounds.W - 1)
			minC = float64(MinPaneWidth)
		} else {
			avail = float64(entry.bounds.H - 1)
			minC = float64(MinPaneHeight)
		}
		if avail < 2 {
			continue
		}
		sign := 1.0
		if dir == Left || dir == Up {
			sign = -1
		}
		dr := sign * float64(delta) / avail
		lo, hi := ratioBounds(avail, minC)
		newRatio := n.ratio + dr
		if newRatio < lo {
			newRatio = lo
		}
		if newRatio > hi {
			newRatio = hi
		}
		if newRatio == n.ratio {
			continue // divider already at the limit; try an outer split
		}
		n.ratio = newRatio
		return true
	}
	return false
}

// ResizeSplit adjusts an internal split directly by delta cells. Positive
// values move the divider right/down, negative values left/up. It returns
// false when the split does not exist.
func (t *Tree) ResizeSplit(splitID int, delta int, bounds Rect) bool {
	n, r, ok := t.findSplit(t.root, splitID, bounds)
	if !ok {
		return false
	}
	var avail float64
	var minC float64
	if n.dir == Vertical {
		avail = float64(r.W - 1)
		minC = float64(MinPaneWidth)
	} else {
		avail = float64(r.H - 1)
		minC = float64(MinPaneHeight)
	}
	if avail < 2 {
		return false
	}
	dr := float64(delta) / avail
	lo, hi := ratioBounds(avail, minC)
	nr := n.ratio + dr
	if nr < lo {
		nr = lo
	}
	if nr > hi {
		nr = hi
	}
	n.ratio = nr
	return true
}

func (t *Tree) findSplit(n *node, id int, bounds Rect) (*node, Rect, bool) {
	if n == nil {
		return nil, Rect{}, false
	}
	if n.leaf {
		return nil, Rect{}, false
	}
	if n.id == id {
		return n, bounds, true
	}
	var leftB, rightB Rect
	if n.dir == Vertical {
		avail := bounds.W - 1
		lw := splitCells(avail, n.ratio, MinPaneWidth)
		leftB = Rect{bounds.X, bounds.Y, lw, bounds.H}
		rightB = Rect{bounds.X + lw + 1, bounds.Y, bounds.W - lw - 1, bounds.H}
	} else {
		avail := bounds.H - 1
		lh := splitCells(avail, n.ratio, MinPaneHeight)
		leftB = Rect{bounds.X, bounds.Y, bounds.W, lh}
		rightB = Rect{bounds.X, bounds.Y + lh + 1, bounds.W, bounds.H - lh - 1}
	}
	if m, r, ok := t.findSplit(n.left, id, leftB); ok {
		return m, r, true
	}
	return t.findSplit(n.right, id, rightB)
}

var errTreeEmpty = errors.New("layout: tree is empty")

// ActiveRect is a helper for callers that already have bounds and just want
// the leaf rect for paneID. It returns errTreeEmpty-style error when missing.
func (t *Tree) RectOf(paneID int, bounds Rect) (Rect, error) {
	r, ok := t.Compute(bounds)[paneID]
	if !ok {
		return Rect{}, errTreeEmpty
	}
	return r, nil
}

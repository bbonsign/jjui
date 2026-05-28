package parser

import (
	"testing"

	"github.com/idursun/jjui/internal/jj"
	"github.com/idursun/jjui/internal/screen"
	"github.com/stretchr/testify/assert"
)

func makeGutterSegments(chars string) []*screen.Segment {
	runes := []rune(chars)
	segments := make([]*screen.Segment, len(runes))
	for i, r := range runes {
		segments[i] = &screen.Segment{Text: string(r)}
	}
	return segments
}

func makeRow(gutterLines []string) Row {
	row := Row{
		Commit: &jj.Commit{ChangeId: "test"},
		Lines:  make([]*GraphRowLine, len(gutterLines)),
	}
	for i, g := range gutterLines {
		flags := Highlightable
		if i == 0 {
			flags = Revision | Highlightable
		}
		row.Lines[i] = &GraphRowLine{
			Gutter: GraphGutter{Segments: makeGutterSegments(g)},
			Flags:  flags,
		}
	}
	return row
}

func TestNoopTracerAlwaysInLane(t *testing.T) {
	noop := NoopTracer{}
	assert.True(t, noop.IsInSameLane(0))
	assert.True(t, noop.IsInSameLane(100))
	assert.True(t, noop.IsGutterInLane(0, 0, 0))
	assert.Equal(t, "├", noop.UpdateGutterText(0, 0, 0, "├"))
}

func TestTracerEmptyRows(t *testing.T) {
	tracer := NewTracer(nil, 0, 0, 0)
	// NoopTracer is returned for an invalid cursor.
	assert.True(t, tracer.IsInSameLane(0))
}

func TestTracerCursorAtTopVisitsAllAncestors(t *testing.T) {
	// Linear chain. Cursor at top sees every ancestor below.
	// ○   (row 0, cursor)
	// │
	// ○   (row 1)
	// │
	// ○   (row 2)
	rows := []Row{
		makeRow([]string{"○", "│"}),
		makeRow([]string{"○", "│"}),
		makeRow([]string{"○"}),
	}

	tracer := NewTracer(rows, 0, 0, len(rows))
	assert.True(t, tracer.IsInSameLane(0))
	assert.True(t, tracer.IsInSameLane(1))
	assert.True(t, tracer.IsInSameLane(2))
}

func TestTracerCursorAtBottomExcludesDescendants(t *testing.T) {
	// Same linear chain, but cursor at the bottom: nothing above is an
	// ancestor, so only the cursor row is in lane.
	rows := []Row{
		makeRow([]string{"○", "│"}),
		makeRow([]string{"○", "│"}),
		makeRow([]string{"○"}),
	}

	tracer := NewTracer(rows, 2, 0, len(rows))
	assert.False(t, tracer.IsInSameLane(0), "descendants should be off-lane")
	assert.False(t, tracer.IsInSameLane(1), "descendants should be off-lane")
	assert.True(t, tracer.IsInSameLane(2), "cursor row itself is in lane")
}

func TestTracerCursorInMiddleExcludesDescendants(t *testing.T) {
	// Linear chain. Cursor in the middle: only the cursor and ancestors
	// (rows below) are in lane.
	rows := []Row{
		makeRow([]string{"○", "│"}),
		makeRow([]string{"○", "│"}),
		makeRow([]string{"○", "│"}),
		makeRow([]string{"○"}),
	}

	tracer := NewTracer(rows, 2, 0, len(rows))
	assert.False(t, tracer.IsInSameLane(0), "descendant should be off-lane")
	assert.False(t, tracer.IsInSameLane(1), "descendant should be off-lane")
	assert.True(t, tracer.IsInSameLane(2), "cursor row is in lane")
	assert.True(t, tracer.IsInSameLane(3), "ancestor should be in lane")
}

func TestTracerParallelLanes(t *testing.T) {
	// Two parallel lanes that never touch.
	// ○   (row 0, col 0)
	// │ ○ (row 1, col 0 pipe, col 2 node)
	// │ │
	// ○ │ (row 2, col 0 node, col 2 pipe)
	// │ │
	rows := []Row{
		makeRow([]string{"○  ", "│  "}),
		makeRow([]string{"│ ○", "│ │"}),
		makeRow([]string{"○ │", "│ │"}),
	}

	// Cursor on row 1 (right lane): nothing else is on that lane downward.
	tracer := NewTracer(rows, 1, 0, len(rows))
	assert.False(t, tracer.IsInSameLane(0))
	assert.True(t, tracer.IsInSameLane(1))
	assert.False(t, tracer.IsInSameLane(2))

	// Cursor on row 0 (left lane, top): row 2 is an ancestor on the same lane.
	tracer2 := NewTracer(rows, 0, 0, len(rows))
	assert.True(t, tracer2.IsInSameLane(0))
	assert.False(t, tracer2.IsInSameLane(1), "right lane is unrelated")
	assert.True(t, tracer2.IsInSameLane(2), "left lane ancestor is in lane")
}

func TestTracerGutterInLane(t *testing.T) {
	// Two parallel lanes.
	rows := []Row{
		makeRow([]string{"○  ", "│  "}),
		makeRow([]string{"│ ○", "│ │"}),
		makeRow([]string{"○ │"}),
	}

	// Cursor on row 0 — left lane (col 0) is in lane, right lane (col 2) is not.
	tracer := NewTracer(rows, 0, 0, len(rows))
	assert.True(t, tracer.IsGutterInLane(1, 0, 0))  // │ at col 0 is in lane
	assert.False(t, tracer.IsGutterInLane(1, 0, 2)) // ○ at col 2 is NOT in lane
}

func TestTracerForkDoesNotLeakIntoSideBranch(t *testing.T) {
	// Graph with a fork going off to the right (├─╮):
	// ○     (row 0, col 0: top, cursor)
	// │
	// ○     (row 1, col 0)
	// ├─╮
	// │ ○   (row 2, col 0: pipe, col 2: side branch node)
	// │ │
	// ○ │   (row 3, col 0)
	// │
	// ○     (row 4, col 0: bottom)
	rows := []Row{
		makeRow([]string{"○  ", "│  "}),
		makeRow([]string{"○  ", "├─╮"}),
		makeRow([]string{"│ ○", "│ │"}),
		makeRow([]string{"○ │", "│  "}),
		makeRow([]string{"○  "}),
	}

	// Cursor at the top: traces down through the main column and ALSO
	// follows the side branch through ├─╮ to the right node.
	tracer := NewTracer(rows, 0, 0, len(rows))
	assert.True(t, tracer.IsInSameLane(0), "cursor row")
	assert.True(t, tracer.IsInSameLane(1), "main column ancestor")
	assert.True(t, tracer.IsInSameLane(2), "side branch reachable through fork")
	assert.True(t, tracer.IsInSameLane(3), "main column ancestor")
	assert.True(t, tracer.IsInSameLane(4), "main column ancestor")
}

func TestTracerCursorOnSideBranchExcludesMainColumn(t *testing.T) {
	// Same graph as above. Cursor on the side branch should NOT highlight
	// the main column rows because going down from the side node doesn't
	// reach them.
	rows := []Row{
		makeRow([]string{"○  ", "│  "}),
		makeRow([]string{"○  ", "├─╮"}),
		makeRow([]string{"│ ○", "│ │"}),
		makeRow([]string{"○ │", "│  "}),
		makeRow([]string{"○  "}),
	}

	tracer := NewTracer(rows, 2, 0, len(rows))
	assert.False(t, tracer.IsInSameLane(0), "row above cursor is descendant")
	assert.False(t, tracer.IsInSameLane(1), "row above cursor is descendant")
	assert.True(t, tracer.IsInSameLane(2), "cursor row")
	assert.False(t, tracer.IsInSameLane(3), "main column not reachable from side")
	assert.False(t, tracer.IsInSameLane(4), "main column not reachable from side")
}

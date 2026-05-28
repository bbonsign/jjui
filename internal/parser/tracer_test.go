package parser

import (
	"strconv"
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

// makeRow builds a synthetic row with the given gutter lines and an
// auto-assigned change ID. Parents must be wired separately via
// linkLinear / linkParents.
func makeRow(id string, gutterLines []string) Row {
	row := Row{
		Commit: &jj.Commit{ChangeId: id},
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

// linkLinear sets each row's parent to the next row's change ID (the
// jj convention of "newer commits on top, parents below").
func linkLinear(rows []Row) {
	for i := range rows {
		if i+1 < len(rows) && rows[i+1].Commit != nil {
			rows[i].Commit.Parents = []string{rows[i+1].Commit.ChangeId}
		}
	}
}

func id(n int) string { return "c" + strconv.Itoa(n) }

func TestNoopTracerAlwaysInLane(t *testing.T) {
	noop := NoopTracer{}
	assert.True(t, noop.IsInSameLane(0))
	assert.True(t, noop.IsInSameLane(100))
	assert.True(t, noop.IsGutterInLane(0, 0, 0))
	assert.Equal(t, "├", noop.UpdateGutterText(0, 0, 0, "├"))
}

func TestTracerEmptyRows(t *testing.T) {
	tracer := NewTracer(nil, 0, 0, 0)
	assert.True(t, tracer.IsInSameLane(0))
}

func TestTracerCursorAtTopVisitsAllAncestors(t *testing.T) {
	// Linear chain. Cursor at top, every row is an ancestor.
	rows := []Row{
		makeRow(id(0), []string{"○", "│"}),
		makeRow(id(1), []string{"○", "│"}),
		makeRow(id(2), []string{"○"}),
	}
	linkLinear(rows)

	tracer := NewTracer(rows, 0, 0, len(rows))
	assert.True(t, tracer.IsInSameLane(0))
	assert.True(t, tracer.IsInSameLane(1))
	assert.True(t, tracer.IsInSameLane(2))
}

func TestTracerCursorAtBottomExcludesDescendants(t *testing.T) {
	// Cursor at bottom of linear chain; nothing above is an ancestor.
	rows := []Row{
		makeRow(id(0), []string{"○", "│"}),
		makeRow(id(1), []string{"○", "│"}),
		makeRow(id(2), []string{"○"}),
	}
	linkLinear(rows)

	tracer := NewTracer(rows, 2, 0, len(rows))
	assert.False(t, tracer.IsInSameLane(0), "descendant off-lane")
	assert.False(t, tracer.IsInSameLane(1), "descendant off-lane")
	assert.True(t, tracer.IsInSameLane(2), "cursor in lane")
}

func TestTracerSiblingsOnSameColumnAreNotInLane(t *testing.T) {
	// Two unrelated commits A and B both rendered at col 0 (jj reuses the
	// column). They are NOT ancestors of each other. Selecting A must not
	// mark B as in-lane just because the visual gutter has a │ between them.
	rows := []Row{
		makeRow("a", []string{"○"}),
		makeRow("b", []string{"○"}),
	}
	// No parents set — A and B are unrelated.

	tracer := NewTracer(rows, 0, 0, len(rows))
	assert.True(t, tracer.IsInSameLane(0), "cursor")
	assert.False(t, tracer.IsInSameLane(1), "unrelated commit must not be in lane")
}

func TestTracerSiblingsAlongVisualPipeAreNotInLane(t *testing.T) {
	// More realistic case: cursor A at col 0, unrelated commit B further
	// down at col 0 with a │ between them. The gutter BFS could naively
	// flood through the │ and mark B's row, but the commit graph tells us
	// they're unrelated.
	rows := []Row{
		makeRow("a", []string{"○", "│"}),
		makeRow("b", []string{"○", "│"}),
		makeRow("c", []string{"○"}),
	}
	// A has no parents; B has parent C.
	rows[1].Commit.Parents = []string{"c"}

	tracer := NewTracer(rows, 0, 0, len(rows))
	assert.True(t, tracer.IsInSameLane(0), "cursor A")
	assert.False(t, tracer.IsInSameLane(1), "B is unrelated to A")
	assert.False(t, tracer.IsInSameLane(2), "C is B's parent, not A's")
}

func TestTracerSideBranchExcluded(t *testing.T) {
	// Cursor on the main column. A side commit hangs off via a fork but is
	// not a parent — only the main chain is in-lane.
	//
	// ○ A         (cursor)
	// │
	// ○ B
	// ├─╮
	// │ ○ S       (side, not B's parent — B's parent is C below)
	// ○ C
	rows := []Row{
		makeRow("a", []string{"○  ", "│  "}),
		makeRow("b", []string{"○  ", "├─╮"}),
		makeRow("s", []string{"│ ○"}),
		makeRow("c", []string{"○  "}),
	}
	rows[0].Commit.Parents = []string{"b"}
	rows[1].Commit.Parents = []string{"c"} // B's only parent is C; S is unrelated.
	// S and C have no parents listed.

	tracer := NewTracer(rows, 0, 0, len(rows))
	assert.True(t, tracer.IsInSameLane(0), "cursor A")
	assert.True(t, tracer.IsInSameLane(1), "B is parent of A")
	assert.False(t, tracer.IsInSameLane(2), "S is NOT an ancestor of A")
	assert.True(t, tracer.IsInSameLane(3), "C is parent of B")
}

func TestTracerMergeWithSideParentIncluded(t *testing.T) {
	// Same shape, but B is a merge: parents are S and C. Both branches
	// should be in lane.
	rows := []Row{
		makeRow("a", []string{"○  ", "│  "}),
		makeRow("b", []string{"○  ", "├─╮"}),
		makeRow("s", []string{"│ ○"}),
		makeRow("c", []string{"○  "}),
	}
	rows[0].Commit.Parents = []string{"b"}
	rows[1].Commit.Parents = []string{"c", "s"} // merge

	tracer := NewTracer(rows, 0, 0, len(rows))
	assert.True(t, tracer.IsInSameLane(0))
	assert.True(t, tracer.IsInSameLane(1))
	assert.True(t, tracer.IsInSameLane(2), "S is a parent of B")
	assert.True(t, tracer.IsInSameLane(3))
}

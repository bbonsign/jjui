package parser

// LaneTracer determines which graph rows and gutter cells are part of the
// selected revision's lane. A commit is "in lane" iff it is the cursor or
// one of its ancestors (per jj's parent metadata, not the visual graph).
// Gutter cells are "in lane" iff they lie on the visual path between two
// adjacent in-lane commits.
type LaneTracer interface {
	IsInSameLane(rowIndex int) bool
	IsGutterInLane(rowIndex int, lineIndex int, col int) bool
	UpdateGutterText(rowIndex int, lineIndex int, col int, text string) string
}

// NoopTracer always reports everything as in-lane (no dimming).
type NoopTracer struct{}

func (n NoopTracer) IsInSameLane(int) bool                            { return true }
func (n NoopTracer) IsGutterInLane(int, int, int) bool                { return true }
func (n NoopTracer) UpdateGutterText(_, _, _ int, text string) string { return text }

// A single bit suffices: cells are either in the lane (1) or not (0).
const inLaneBit uint64 = 1

type direction int

const (
	down direction = iota
	left
	right
)

type step struct {
	row  int
	line int
	col  int
	dir  direction
}

// Tracer determines lane membership using two passes:
//
//  1. Walk parent IDs from the cursor's commit to build the set of in-lane
//     commits. This uses jj's actual parent metadata, so commits that merely
//     share a visual column (e.g. jj reusing column 0 for unrelated branches)
//     are correctly excluded.
//  2. For each adjacent (child, parent) pair where both are in-lane, trace
//     the visual gutter from the child's node downward until reaching the
//     parent's node, marking visited cells as in-lane.
type Tracer struct {
	rows    []Row
	end     int
	inLane  map[string]bool // change IDs of in-lane commits
	nodeRow map[string]int  // change ID -> row index
}

// NewTracer creates a tracer rooted at the cursor's commit. start/end
// restrict the trace to a row range (typically the whole graph).
func NewTracer(rows []Row, cursor int, start int, end int) LaneTracer {
	if cursor < 0 || cursor >= len(rows) {
		return NoopTracer{}
	}
	if start < 0 {
		start = 0
	}
	if end > len(rows) {
		end = len(rows)
	}
	cursorCommit := rows[cursor].Commit
	if cursorCommit == nil {
		return NoopTracer{}
	}

	// Clear lane bits from previous tracer runs (SetLane ORs into existing).
	for i := start; i < end; i++ {
		for _, line := range rows[i].Lines {
			for _, seg := range line.Gutter.Segments {
				seg.Lane = 0
			}
		}
	}

	t := &Tracer{
		rows:    rows,
		end:     end,
		inLane:  make(map[string]bool),
		nodeRow: make(map[string]int, end-start),
	}
	for i := start; i < end; i++ {
		if c := rows[i].Commit; c != nil && c.ChangeId != "" {
			t.nodeRow[c.ChangeId] = i
		}
	}
	t.buildInLaneSet(cursorCommit.ChangeId)
	t.markGutterCells(cursor)
	return t
}

// buildInLaneSet walks parent IDs from rootID and records every reachable
// change ID. Only commits actually loaded in t.rows can be tested for
// membership; that's fine because IsInSameLane is only asked about loaded
// rows.
func (t *Tracer) buildInLaneSet(rootID string) {
	if rootID == "" {
		return
	}
	queue := []string{rootID}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if t.inLane[id] {
			continue
		}
		t.inLane[id] = true
		rowIdx, ok := t.nodeRow[id]
		if !ok {
			continue
		}
		commit := t.rows[rowIdx].Commit
		if commit == nil {
			continue
		}
		queue = append(queue, commit.Parents...)
	}
}

// markGutterCells traces visual gutter paths between adjacent in-lane
// commits. The cursor's own node is always marked. For each in-lane parent
// also present in the loaded rows, BFS downward from the child's node
// through box-drawing cells until reaching the parent's node, marking
// every visited cell. Cells reachable only via paths to off-lane commits
// are NOT marked.
func (t *Tracer) markGutterCells(cursor int) {
	// Always mark the cursor's node.
	cursorRow := &t.rows[cursor]
	cursorCol := cursorRow.GetNodeIndex()
	cursorRow.SetLane(0, cursorCol, inLaneBit)

	// For each in-lane commit, trace down to each of its in-lane parents.
	for id := range t.inLane {
		rowIdx, ok := t.nodeRow[id]
		if !ok {
			continue
		}
		commit := t.rows[rowIdx].Commit
		if commit == nil {
			continue
		}
		col := t.rows[rowIdx].GetNodeIndex()
		t.rows[rowIdx].SetLane(0, col, inLaneBit)
		for _, parentID := range commit.Parents {
			if !t.inLane[parentID] {
				continue
			}
			parentRowIdx, ok := t.nodeRow[parentID]
			if !ok {
				continue
			}
			t.tracePath(rowIdx, col, parentRowIdx)
		}
	}
}

// tracePath does BFS from (childRow, line 0, childCol) downward, stopping
// at parentRow's node. Visited cells are marked with inLaneBit. The BFS
// only ever moves downward or sideways (not upward), so it cannot wander
// into descendants.
func (t *Tracer) tracePath(childRow, childCol, parentRow int) {
	if parentRow <= childRow {
		return
	}
	parentCol := t.rows[parentRow].GetNodeIndex()

	queue := []step{{row: childRow, line: 0, col: childCol, dir: down}}
	for len(queue) > 0 {
		s := queue[0]
		queue = queue[1:]

		// Move one cell in s.dir.
		nr, nl, nc := s.row, s.line, s.col
		switch s.dir {
		case down:
			nl++
		case left:
			nc--
		case right:
			nc++
		}

		// Cross row boundary on a vertical move.
		if nl >= len(t.rows[nr].Lines) {
			nr++
			nl = 0
			if nr >= t.end {
				continue
			}
		}

		r, ok := t.rows[nr].Get(nl, nc)
		if !ok || r == ' ' {
			continue
		}

		// Stop if we've reached the target parent's node.
		if nr == parentRow && nl == 0 && nc == parentCol {
			t.rows[nr].SetLane(nl, nc, inLaneBit)
			continue
		}

		// Don't cross through other in-lane commit nodes (those are handled
		// by their own tracePath call).
		if nl == 0 && isNodeRune(r) {
			rowCommit := t.rows[nr].Commit
			if rowCommit != nil && t.inLane[rowCommit.ChangeId] && !(nr == childRow && nc == childCol) {
				continue
			}
		}

		// Don't cross through off-lane commit nodes.
		if nl == 0 && isNodeRune(r) {
			rowCommit := t.rows[nr].Commit
			if rowCommit != nil && !t.inLane[rowCommit.ChangeId] {
				continue
			}
		}

		existing := t.rows[nr].GetLane(nl, nc)
		if existing&inLaneBit != 0 {
			continue
		}
		t.rows[nr].SetLane(nl, nc, inLaneBit)

		switch r {
		case '│', '|', '~':
			queue = append(queue, step{row: nr, line: nl, col: nc, dir: down})
		case '─', '-':
			queue = append(queue, step{row: nr, line: nl, col: nc, dir: s.dir})
		case '╭', '┌':
			// down + right
			queue = append(queue, step{row: nr, line: nl, col: nc, dir: down})
			queue = append(queue, step{row: nr, line: nl, col: nc, dir: right})
		case '╮', '┐':
			// down + left
			queue = append(queue, step{row: nr, line: nl, col: nc, dir: down})
			queue = append(queue, step{row: nr, line: nl, col: nc, dir: left})
		case '╰', '└':
			// up + right (we don't follow up)
			queue = append(queue, step{row: nr, line: nl, col: nc, dir: right})
		case '╯', '┘':
			// up + left (we don't follow up)
			queue = append(queue, step{row: nr, line: nl, col: nc, dir: left})
		case '├':
			queue = append(queue, step{row: nr, line: nl, col: nc, dir: down})
			queue = append(queue, step{row: nr, line: nl, col: nc, dir: right})
		case '┤':
			queue = append(queue, step{row: nr, line: nl, col: nc, dir: down})
			queue = append(queue, step{row: nr, line: nl, col: nc, dir: left})
		case '┬':
			queue = append(queue, step{row: nr, line: nl, col: nc, dir: down})
			queue = append(queue, step{row: nr, line: nl, col: nc, dir: left})
			queue = append(queue, step{row: nr, line: nl, col: nc, dir: right})
		case '┴':
			queue = append(queue, step{row: nr, line: nl, col: nc, dir: left})
			queue = append(queue, step{row: nr, line: nl, col: nc, dir: right})
		case '┼', '+':
			queue = append(queue, step{row: nr, line: nl, col: nc, dir: down})
			queue = append(queue, step{row: nr, line: nl, col: nc, dir: left})
			queue = append(queue, step{row: nr, line: nl, col: nc, dir: right})
		}
	}
}

func isNodeRune(r rune) bool {
	return r == '@' || r == '○' || r == '◆' || r == '×'
}

func (t *Tracer) IsInSameLane(rowIndex int) bool {
	if rowIndex < 0 || rowIndex >= len(t.rows) {
		return false
	}
	commit := t.rows[rowIndex].Commit
	if commit == nil {
		return false
	}
	return t.inLane[commit.ChangeId]
}

func (t *Tracer) IsGutterInLane(rowIndex int, lineIndex int, col int) bool {
	if rowIndex < 0 || rowIndex >= len(t.rows) {
		return false
	}
	return t.rows[rowIndex].GetLane(lineIndex, col)&inLaneBit != 0
}

func (t *Tracer) getLane(rowIndex int, lineIndex int, col int) uint64 {
	if rowIndex < 0 || rowIndex >= len(t.rows) {
		return 0
	}
	if lineIndex < 0 {
		rowIndex--
		if rowIndex < 0 || len(t.rows[rowIndex].Lines) == 0 {
			return 0
		}
		lineIndex = len(t.rows[rowIndex].Lines) - 1
	} else if lineIndex >= len(t.rows[rowIndex].Lines) {
		rowIndex++
		lineIndex = 0
		if rowIndex >= len(t.rows) {
			return 0
		}
	}
	return t.rows[rowIndex].GetLane(lineIndex, col)
}

func (t *Tracer) UpdateGutterText(rowIndex int, lineIndex int, col int, text string) string {
	if !t.IsGutterInLane(rowIndex, lineIndex, col) {
		return text
	}

	right := t.getLane(rowIndex, lineIndex, col+1)&inLaneBit != 0
	upper := t.getLane(rowIndex, lineIndex-1, col)&inLaneBit != 0
	lower := t.getLane(rowIndex, lineIndex+1, col)&inLaneBit != 0

	switch text {
	case "├":
		if right && !upper {
			return "╭"
		}
		if !right && upper {
			return "│"
		}
		if !right && !upper {
			return "│"
		}
	case "┼":
		if !right {
			return "│"
		}
	case "┬":
		if !lower {
			return "─"
		}
	case "╮":
		if !lower {
			return "─"
		}
	case "┤":
		if !lower && !upper {
			return "─"
		}
		if !upper {
			return "╭"
		}
	}
	return text
}

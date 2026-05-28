package parser

// LaneTracer determines which graph cells are part of the selected
// revision's lane. A lane is the set of gutter cells reachable by box-drawing
// connectivity going *downward* from the cursor's node — i.e. its ancestors
// (in jj's default ordering where children appear above parents).
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

// A single non-zero bit is enough: cells are either in the lane (1) or not (0).
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

// Tracer marks cells reachable by box-drawing connectivity downward (and
// sideways through junctions) from the cursor's node.
type Tracer struct {
	rows []Row
	end  int
}

// NewTracer creates a tracer rooted at the cursor's node. start/end restrict
// the trace to a row range (typically the whole graph).
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
	// Clear lane bits from previous tracer runs (SetLane ORs into existing).
	for i := start; i < end; i++ {
		for _, line := range rows[i].Lines {
			for _, seg := range line.Gutter.Segments {
				seg.Lane = 0
			}
		}
	}
	t := &Tracer{rows: rows, end: end}
	nodeCol := rows[cursor].GetNodeIndex()
	t.trace(cursor, 0, nodeCol)
	return t
}

func (t *Tracer) IsInSameLane(rowIndex int) bool {
	if rowIndex < 0 || rowIndex >= len(t.rows) {
		return false
	}
	col := t.rows[rowIndex].GetNodeIndex()
	return t.rows[rowIndex].GetLane(0, col)&inLaneBit != 0
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

// trace runs BFS rooted at the cursor's node. It only ever moves downward
// or sideways (through junctions) — never up — so only ancestors of the
// cursor end up marked.
func (t *Tracer) trace(rowIndex, lineIndex, col int) {
	row := &t.rows[rowIndex]
	row.SetLane(lineIndex, col, inLaneBit)

	queue := []step{{row: rowIndex, line: lineIndex, col: col, dir: down}}
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

		existing := t.rows[nr].GetLane(nl, nc)
		if existing&inLaneBit != 0 {
			continue
		}
		t.rows[nr].SetLane(nl, nc, inLaneBit)

		switch r {
		case '│', '|', '~':
			// Continue down; never spread up.
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
			// up + right — but we don't follow up, only the right branch.
			queue = append(queue, step{row: nr, line: nl, col: nc, dir: right})
		case '╯', '┘':
			// up + left — we don't follow up.
			queue = append(queue, step{row: nr, line: nl, col: nc, dir: left})
		case '├':
			// down + up + right
			queue = append(queue, step{row: nr, line: nl, col: nc, dir: down})
			queue = append(queue, step{row: nr, line: nl, col: nc, dir: right})
		case '┤':
			// down + up + left
			queue = append(queue, step{row: nr, line: nl, col: nc, dir: down})
			queue = append(queue, step{row: nr, line: nl, col: nc, dir: left})
		case '┬':
			// left + right + down
			queue = append(queue, step{row: nr, line: nl, col: nc, dir: down})
			queue = append(queue, step{row: nr, line: nl, col: nc, dir: left})
			queue = append(queue, step{row: nr, line: nl, col: nc, dir: right})
		case '┴':
			// left + right + up; no up so only spread sideways.
			queue = append(queue, step{row: nr, line: nl, col: nc, dir: left})
			queue = append(queue, step{row: nr, line: nl, col: nc, dir: right})
		case '┼', '+':
			// All four directions, but never up.
			queue = append(queue, step{row: nr, line: nl, col: nc, dir: down})
			queue = append(queue, step{row: nr, line: nl, col: nc, dir: left})
			queue = append(queue, step{row: nr, line: nl, col: nc, dir: right})
		case '@', '○', '◆', '×':
			// Node characters: continue down through the column.
			queue = append(queue, step{row: nr, line: nl, col: nc, dir: down})
		}
	}
}

# Plan: Reintroduce Lane Tracing

## Goal

When tracing is enabled (default), the selected revision's "lane" — the
set of gutter cells reachable by box-drawing connectivity going **downward**
from the cursor's node (i.e. its ancestors, since jj displays children above
parents) — is treated as the focus:

- Revisions on the same lane render normally.
- Revisions off-lane render dimmed/faint.
- Gutter cells (the box-drawing characters in the graph column) belonging to
  the focused lane render normally; off-lane gutter cells render dimmed.
- Optionally, gutter glyphs that branch *off* the focused lane can be
  visually simplified (e.g. `├` becomes `│` when the right-side branch is
  off-lane).

A "lane" is the set of graph cells reachable through box-drawing
connectivity from the selected revision's node. In practice this is
"ancestors and descendants reachable along graph lines that pass through
the selected node", computed by BFS flood-fill over the rendered gutter.

The feature was previously implemented and removed during a refactor. The
previous reintroduction attempt in this worktree partially landed and is
currently in a conflicted state. We're starting fresh.

## What we keep from the previous attempt

These files already exist on disk and are sound enough to keep as a starting
point. They are not in conflict:

- [internal/parser/tracer.go](internal/parser/tracer.go) — `LaneTracer`
  interface, `NoopTracer`, `Tracer` with BFS flood-fill, glyph rewriting via
  `UpdateGutterText`.
- [internal/parser/tracer_test.go](internal/parser/tracer_test.go) — basic
  unit tests for the tracer.
- The `Lane uint64` field on `screen.Segment` and the `GetLane` / `SetLane`
  helpers on `parser.Row` (already in base).
- The `TracerConfig` addition in
  [internal/config/config.go](internal/config/config.go) and
  [internal/config/default/config.toml](internal/config/default/config.toml).

We may revisit the tracer's glyph-rewriting logic later, but the public
interface (`IsInSameLane`, `IsGutterInLane`, `UpdateGutterText`) is the
right shape and is what the renderer integrates against.

## What we redo from scratch

The integration into the renderer is the part that conflicts and that we
plan to rewrite. The relevant files are:

- [internal/ui/revisions/revisions.go](internal/ui/revisions/revisions.go)
- [internal/ui/revisions/displaycontext_renderer.go](internal/ui/revisions/displaycontext_renderer.go)

## Architecture

```diagram
╭─────────────────────╮       ╭─────────────────╮
│ revisions.Model     │──────▶│ parser.NewTracer│
│  ViewRect(...)      │       │  (BFS flood)    │
╰──────────┬──────────╯       ╰────────┬────────╯
           │ tracer                    │ Lane bits set on
           ▼                           ▼ segments
╭─────────────────────────╮   ╭──────────────────╮
│ DisplayContextRenderer  │──▶│ itemRenderer     │
│  .SetTracer(tracer)     │   │ .renderLine(...) │
│  .Render(...)           │   │  • dim off-lane  │
╰─────────────────────────╯   │  • UpdateGutter  │
                              ╰──────────────────╯
```

Data flow per frame:

1. `Model.ViewRect` builds a `LaneTracer` from `m.rows` and `m.cursor`.
2. The tracer is handed to the `DisplayContextRenderer` for this frame.
3. While rendering each visible row:
   - If the row is off-lane and not the cursor, dim the whole row rect.
   - When emitting gutter segments, ask the tracer per-cell whether to dim
     and whether to rewrite the glyph.

## Concrete steps

### 1. Reset conflicted files to base

`internal/ui/revisions/revisions.go` and
`internal/ui/revisions/displaycontext_renderer.go` are currently conflicted.
Restore them to the parent's clean state before reintegrating:

```sh
jj restore --from @- \
  internal/ui/revisions/revisions.go \
  internal/ui/revisions/displaycontext_renderer.go
```

### 2. Plumb a tracer into `DisplayContextRenderer`

In [internal/ui/revisions/displaycontext_renderer.go](internal/ui/revisions/displaycontext_renderer.go):

- Add a `tracer parser.LaneTracer` field on `DisplayContextRenderer`.
- Add `SetTracer(t parser.LaneTracer)` setter. Default behavior when nil is
  "no dimming" (treat it the same as `NoopTracer`).
- Add a `rowIndex int` field on `itemRenderer` and set it from the render
  callback (we have `index` available in
  [list.go](internal/ui/render/list.go) via the `RenderItemFunc`).
- Change `renderItemToDisplayContext` and `renderLine` to thread
  `rowIndex` and a `lineIndex` (the index into `item.Lines`) down to the
  point where individual gutter segments are emitted.

### 3. Apply dimming and glyph rewriting

In the render loop (`Render` -> per-item callback):

- After rendering an item, if the row is *not* the cursor and the tracer
  reports `!IsInSameLane(rowIndex)`, call `dl.AddDim(rect, 0)` to fade the
  whole row.

In `itemRenderer.renderLine`, when emitting gutter segments:

- For each gutter segment at `(rowIndex, lineIndex, col)`:
  - Let `text = tracer.UpdateGutterText(rowIndex, lineIndex, col, segment.Text)`.
  - If `!tracer.IsGutterInLane(rowIndex, lineIndex, col)`, dim the style
    (e.g. `style.Faint(true).Inherit(dimmedStyle)`).
  - Write the (possibly rewritten) `text` with the (possibly dimmed)
    `style`.

### 4. Build the tracer once per frame

In [internal/ui/revisions/revisions.go](internal/ui/revisions/revisions.go),
inside `ViewRect`:

- If `config.Current.Revisions.Tracer.Disabled`, use `parser.NoopTracer{}`
  (or `nil` if the renderer handles that as no-op).
- Otherwise, call `parser.NewTracer(m.rows, m.cursor, 0, len(m.rows))`.
  - Note: the previous attempt tried to limit the trace to the visible
    range using `displayContextRenderer.GetFirstRowIndex()` /
    `GetLastRowIndex()`. Those methods live on
    [`ListRenderer`](internal/ui/render/list.go#L168-L174) and are populated
    *during* `Render`, so they aren't usable until *after* the first frame.
    They also can't be used to bound the BFS without losing connectivity
    across the boundary. For simplicity and correctness, trace across all
    rows for now. With the default `log_batch_size = 50` this is cheap.
- Pass the tracer to the renderer via `displayContextRenderer.SetTracer(t)`
  before calling `Render`.

### 5. Configuration

The default toml already adds `[revisions.tracer]` with `disabled = false`.
Verify it loads cleanly. No new binding is required for v1: tracing is
always on (unless disabled via config). A toggle action can be added later
under a separate intent if desired.

### 6. Tests and verification

- Keep and extend `internal/parser/tracer_test.go`.
- Add an integration-style test if convenient, but the primary verification
  is:
  1. `go test ./internal/parser/...`
  2. `go test ./internal/ui/revisions/...`
  3. `go test ./...`
  4. Manual smoke test by running `go run ./cmd/jjui` against a repository
     with a branching graph and confirming off-lane revisions dim as the
     cursor moves.

## Non-goals (v1)

- No runtime toggle key/intent. Config flag only.
- No multi-lane highlighting (only the lowest bit of the cursor's lane id
  is used, matching the current tracer implementation).
- No persisted lane decoration beyond a single frame; the tracer is
  recomputed every frame.
- No tweaks to the BFS glyph table beyond what `tracer.go` already does.

## Risks / open questions

- **Glyph rewriting correctness:** `UpdateGutterText` only handles a small
  set of cases (`├ ┼ ┬ ╮ ┤`). It may produce odd glyphs on graphs with
  exotic shapes. Acceptable for v1; revisit with real-world screenshots.
- **Per-frame BFS cost:** O(rows × lines × cols) flood-fill per frame.
  Negligible for the default 50-row batch; worth profiling later if batches
  grow.
- **Mutating shared segment state:** the tracer clears and rewrites
  `segment.Lane` on each run. That's safe as long as the tracer is the only
  writer and runs before rendering each frame. The existing implementation
  already does this.

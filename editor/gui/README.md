# construct-gui (native shell)

An optional native editor for Constfiles, built with [Gio](https://gioui.org)
(pure Go, no cgo). This is a separate Go module so Gio and its dependencies
stay out of the main `construct` binary — the root `go.mod` is unchanged.

The full-featured editor remains `construct ui` (drag-and-drop web editor
with structure view, graph editing, and syntax highlighting). This shell is
for quick native edits without a browser.

## Build

```bash
go install github.com/nicklvsa/construct/editor/gui@latest   # from a released tag
# or, from a checkout of this repo:
cd editor/gui && go install .
```

During development the module's `replace` directive points at the repo root,
so local changes to `pkg/` are picked up directly.

## Use

```bash
construct-gui [Constfile]
```

- Left: the commands of the open file (imported files show as their own
  section header; switch files by passing the file, or edit the import
  lines in the main file).
- Right: name, prerequisites (comma separated, file deps allowed), and the
  command body.
- **Apply** pushes the fields through the same validation gate as the web
  editor (invalid edits are rejected with the parse error), **Undo** steps
  back, **Save** formats and writes — refusing files that changed on disk.

# Constfile

Syntax highlighting and language intelligence for [Constfiles](https://github.com/nicklvsa/construct#readme) used by the `construct` build tool.

## Features

- **Syntax highlighting** for all Constfile constructs: variable declarations (`var`), `@env` and `&var` references, command headers with cloud markers (`|cmd|`), default commands (`_`), argument lists, prerequisite lists, strings, numbers, and comments (`//`, `#`).
- **Diagnostics** — live error squiggles for circular dependencies, missing prerequisites, unclosed command bodies, and empty variable names.
- **Hover** — hover over a `&varName` reference to see its value and scope; hover over a command header to see its arguments and dependencies.
- **Go to Definition** — jump from a `&varName` reference to its `var` declaration; jump from a prerequisite name to its command header.
- **Completion** — variable names after `&`; command names in prerequisite lists.

## Prerequisites

- VSCode 1.75+.

Go is **not** required to use the published extension — the language server ships as a prebuilt binary (one per platform). Go is only needed if you're building/packaging the extension from source.

## Installing the extension locally

### Option A — Install a prebuilt `.vsix`

Grab the `.vsix` for your platform and install it:

```bash
code --install-extension constfile-<platform>-0.2.0.vsix
```

where `<platform>` is one of `darwin-arm64`, `darwin-x64`, `linux-x64`, `linux-arm64`, `win32-x64`, `win32-arm64`.

### Option B — Package from source

The language server is a native Go binary, so each platform gets its own VSIX. A packaging script cross-compiles the server for the right `GOOS`/`GOARCH` and tags the package with `vsce --target` so VS Code auto-selects the correct one.

```bash
cd editor/vscode
npm install

# Build for every supported platform (produces 6 .vsix files):
npm run package:all

# Or a single platform:
./scripts/package.sh darwin-arm64

# Or a universal (untagged) VSIX using the host's native binary,
# handy for local dev installs:
npm run package:native
```

Go cross-compiles, so all 6 targets build from a single host (e.g. your Mac) — no target-platform toolchain needed.

After installation, open any `Constfile` or `Constfile-*` file. You should see syntax highlighting immediately; the language server starts automatically and provides diagnostics, hover, and go-to-definition.

## Development

- **Grammar changes** (`syntaxes/constfile.tmLanguage.json`): reload the VSCode window to pick up changes; no compilation needed.
- **Server changes** (`server/*.go`): rebuild the Go binary, then reload the VSCode window.
- **Client changes** (`src/extension.ts`): run `npm run compile`, then reload.

To debug, run the extension in an Extension Development Host via `F5` (requires a `.vscode/launch.json` in this folder, which you can generate with the "Extension Development" template).

## How it works

The extension has two parts:

1. A **TextMate grammar** (`syntaxes/`) provides tokenization for syntax coloring — pure declarative config, no code.
2. A **language server** (`server/`, written in Go) reuses the construct tool's own parser (`pkg/parser.go`) to provide diagnostics, hover, definition, and completion over the Language Server Protocol (LSP) via stdio. The TS client (`src/extension.ts`) launches the Go binary and wires up the stdio transport.

Reusing the real parser means the editor's understanding of Constfile syntax is always identical to what the `construct` CLI itself enforces.

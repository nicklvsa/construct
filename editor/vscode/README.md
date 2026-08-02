# Constfile

Syntax highlighting and language intelligence for [Constfiles](../../README.md) used by the `construct` build tool.

## Features

- **Syntax highlighting** for all Constfile constructs: variable declarations (`var`), `@env` and `&var` references, command headers with cloud markers (`|cmd|`), default commands (`_`), argument lists, prerequisite lists, strings, numbers, and comments (`//`, `#`).
- **Diagnostics** — live error squiggles for circular dependencies, missing prerequisites, unclosed command bodies, and empty variable names.
- **Hover** — hover over a `&varName` reference to see its value and scope; hover over a command header to see its arguments and dependencies.
- **Go to Definition** — jump from a `&varName` reference to its `var` declaration; jump from a prerequisite name to its command header.
- **Completion** — variable names after `&`; command names in prerequisite lists.

## Prerequisites

- [Go 1.26+](https://go.dev/dl/) installed and on your `PATH` (to build the language server).
- VSCode 1.75+.

## Setup

The language server is a small Go binary that ships as source in this folder. Build it once:

```bash
cd editor/vscode/server
go build -o construct-lsp .        # Windows: -o construct-lsp.exe
```

> On Windows, name the binary `construct-lsp.exe`. On macOS/Linux, name it `construct-lsp`.

## Installing the extension locally

### Option A — Install from this folder

1. Open VSCode.
2. Run the command **"Developer: Install Extension from Location..."**.
3. Select the `editor/vscode/` folder.

### Option B — Package as a `.vsix`

```bash
cd editor/vscode
npm install
npm run compile        # compile the TS client
npm run package        # produces constfile-0.1.0.vsix
code --install-extension constfile-0.1.0.vsix
```

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

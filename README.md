## Construct

A Make-like build tool written in Go for organizing command executions across platforms.

## Installation

```bash
go build -o construct
```

> Requires Go 1.26 or later.

## Usage

```bash
construct [options] [Constfile] [commands...]
```

### Commands

| Command | Description |
|---------|-------------|
| `list` | List all available commands |

### Options

| Flag | Description |
|------|-------------|
| `-h, --help` | Show help message |
| `-v, --version` | Show version information |
| `--debug` | Enable debug mode for verbose output |
| `--concurrent` | Execute commands and their prerequisites concurrently (DAG-parallel) |
| `--jobs N` | Cap parallel commands (implies `--concurrent`) |
| `--watch` | Rerun when the Constfile, its imports, or its dependencies change |
| `--choose` | Interactively select targets to run |
| `--dry-run` | Show commands without executing them |
| `--list` | List all available commands |
| `--env-file PATH` | Load environment variables from a dotenv-style file |

### Examples

```bash
construct                    # Run default command from Constfile
construct build test         # Run 'build' and 'test' commands
construct MyFile build       # Run 'build' from MyFile
construct --list             # List available commands
construct --dry-run build    # Show what 'build' would execute
construct --debug build      # Run 'build' with debug output
```

## Constfile Syntax

### Variables

```
var name = value
var envVar = @ENVIRONMENT_VAR    # Environment variable
var ref = &otherVar              # Reference another variable
```

### Commands

```
commandName (args) < prerequisites {
    body
}
```

- `|cloudcmd|` - Cloud-accessible command (can fetch remote definitions)
- `_` - Default command (runs first when no commands specified)
- Arguments: `arg1` (required), `opt arg2` (optional), `opt env=prod` (optional with default)

Arguments are passed as flags and referenced with the same `&` marker as variables:

```bash
construct deploy --deploy:env=prod --deploy:region=us-east
```

```
deploy (env, opt region) {
    $ echo "Deploying &env to &region"
}
```

An argument that isn't provided substitutes its default (if declared) or the
empty string, so references never leak into the shell.

Literal `&`/`@`/`$` text can be emitted with a backslash escape: `\&foo`,
`\@VAR`, `\$` are not substituted.

- Prerequisites: `cmd1, cmd2`
- A prerequisite can run in its own directory at the call site: `main < gen in src/`
- Error tolerance: prefix a body statement with `!` to let it fail without aborting the build

### Imports

Split a project across multiple Constfiles with `import`:

```
# lib.constfile
var version = 2

build {
    $ make
}

# Constfile
import "lib.constfile"

deploy < build {
    $ deploy --version &version
}
```

Import paths resolve relative to the importing file. Imports can import other
files; circular imports and duplicate command names are errors.

#### Namespaced imports

To merge two Constfiles that define same-named commands, give the import a
namespace. Every command, variable, and reference inside becomes
`ns.name`:

```
import "lib.constfile" as lib
import "other.constfile" as other

deploy < lib.build {
    $ echo "lib version: &lib.version"
    $ echo "other version: &other.version"
}
```

- Commands are renamed (`lib.build`), so both files can define `build`.
- Global variables are renamed (`lib.version`); reference them as `&lib.version`.
- Prerequisites, `invoke` targets, and prereq outputs inside the imported file
  are rewritten automatically (`&gen.0` becomes `&lib.gen.0`).
- References to *local* variables (scoped to a command or a loop) are left
  alone, so shadowing keeps working.
- The same file may be imported twice under different namespaces.
- Nested namespaces compose: `lib` importing `sub` as `sub` yields `lib.sub.*`.

### Doc Comments

A `#` or `//` comment directly above a command becomes its description,
shown by `construct --list`:

```
# Builds the app and bundles assets
build {
    $ go build -o app .
}
```

### Per-Command Environment

Set environment variables for the rest of the enclosing command body with an
`env` block — single-line (comma-separated) or multi-line:

```
test {
    env { CI=true, RETRIES=3 }
    $ pytest
}

deploy {
    env {
        REGION=us-east-1
        PROFILE=prod
    }
    if "@REGION" == "us-east-1" {
        $ aws deploy --region &REGION
    }
}
```

Values may reference `&variables` and `@env` refs; they are visible both to
child processes and to later `@NAME` references and conditions in the same
command. `env` blocks apply from their position to the end of the command
body (including nested `if`/`for` blocks).

### Invoke

`invoke <command>` runs another command's body inline, imperatively — unlike a
prerequisite, no dependency tracking happens and it can be placed anywhere,
including inside conditionals. The invoked body executes in the caller's
scope, so it can see the caller's variables, arguments, and loop variables:

```
gen {
    $ echo line-one
    $ echo line-two
}

use {
    invoke gen                    # streams to stdout
    invoke gen as lines           # captures output into &lines
    for l in &lines {
        $ echo "captured: &l"
    }
}
```

`invoke` only runs the body; prerequisites, file deps, and up-to-date checks
of the invoked command are ignored. Self-referential or circular invokes are
rejected.

### Variable Scopes

Variables can be global (defined outside commands) or local (defined inside commands):

```
var global = value

mycommand {
    var local = &global   # Can reference global vars
    $ echo &local
}
```

### Prerequisite Output

Access prerequisite output using `&prereqName.index` or named outputs:

```
prereq {
    $ echo "hello" as greeting
}

main < prereq {
    $ echo &prereq.0         # Outputs: hello (positional)
    $ echo &prereq.greeting  # Outputs: hello (named)
}
```

### For Loops

Iterate over comma-separated lists or file globs:

```
vet {
    for f in *.go {
        $ go vet &f
    }
}

deploy {
    for svc in api, web, worker {
        $ docker build -t &svc ./&svc
    }
}
```

The loop variable (`&f`, `&svc`) is available inside the loop body. Globs are expanded relative to the command's working directory.

### File Dependencies

Commands with file dependencies skip execution if nothing changed since the last run:

```
build < main.go, pkg/*.go {
    $ go build -o app .
}
```

File patterns (containing `/`, `*`, or `.`) are tracked separately from command prerequisites. A manifest is stored in `.construct-cache/`. Touch a file and re-run to trigger a rebuild.

### Produced Artifacts (make-style up-to-date checks)

Instead of hashing dependencies, declare what a command produces — the command
skips when every artifact exists and no dependency is newer:

```
build produces dist/app < src/*.go {
    $ go build -o dist/app .
}
```

The produced files may be globs, and the check is mtime-based like `make`.

### Environment Files

`construct` loads `KEY=VALUE` entries from a `.env` file next to the Constfile
(or from a path given with `--env-file PATH`). Existing environment variables
take precedence, `#` comments and blank lines are ignored, and values may be
quoted. Loaded variables are visible to `@VAR` refs and child processes.

### CLI Variable Overrides

Override any variable from the command line:

```bash
construct deploy -e env=prod -e region=us-east
```

### Conditionals

```
build {
    if "&version" >= "2" {
        $ make modern
    } else {
        $ make legacy
    }
}
```

Conditions support the comparison operators `==`, `!=`, `>`, `>=`, `<`, `<=`
(numeric when both sides are integers, otherwise lexicographic), plus the
`contains` operator for substring tests, the logical operators `&&`, `||`,
`!`, and parentheses. `@ENV` references resolve in shell lines and conditions;
in shell lines an unset variable stays literal (so scoped package names like
`@vscode/vsce` survive), while in variable values it resolves to empty.

```
deploy {
    if "&target" contains "windows" && "&version" >= "3" {
        $ build.exe
    } else if "&target" contains "linux" {
        $ build-linux
    } else {
        $ build
    }
}
```

`else if` chains are fully supported. Conditionals can be nested inside `for`
loops, where they're re-evaluated on each iteration with the current loop
variable:

```
for spec in darwin/arm64, windows/amd64 {
    if "&spec" contains "windows" {
        $ echo "windows target: &spec"
    }
}
```

Built-in file-test functions evaluate inside conditions (they are construct
functions, never passed to the shell):

```
deploy {
    if exists("dist/app.exe") {
        $ echo "artifact already built"
    }
    if missing("dist/app.exe") {
        $ go build -o dist/app.exe .
    }
    if glob("dist/*.exe") {
        $ echo "found a built binary"
    }
    if require("docker") {
        $ docker build -t app .
    }
}
```

`require("tool")` checks that a binary is available on `PATH`.

List membership uses the `in` operator — exact match against a comma-separated
list:

```
if "&os" in "windows, linux" {
    $ echo "supported platform"
}
```

Blocks may be written on a single line, which pairs naturally with loop
control:

```
for f in *.go {
    if "&f" == "main.go" { continue }
    $ go vet &f
}

for i, f in *.go {      # "for index, value in ..." — 0-based index variable
    $ echo "[&i] vetting &f"
}

for i in 1..10 {          # numeric ranges: N..M (ascending or descending)
    $ echo "attempt $i"
}
```

A prereq's captured outputs can be iterated with the `&prereq.*` reference
(a command with no outputs yields zero iterations):

```
gen {
    $ echo line-one
    $ echo line-two
}

use < gen {
    for line in &gen.* {
        $ echo "captured: &line"
    }
}
```

Working directories and file dependencies resolve relative to the Constfile's
directory, so `construct /path/to/Constfile` behaves the same from anywhere.

### Build Matrices

For multi-axis builds, `matrix` iterates the cross product of several
variable lists (it expands to nested `for` loops; the last variable varies
fastest):

```
build_matrix {
    matrix os in windows, linux; arch in amd64, arm64 {
        if "&os" == "windows" && "&arch" == "arm64" {
            $ echo "skip &os/&arch"
        } else {
            $ echo "building &os/&arch"
        }
    }
}
```

Each clause is `var in items` separated by `;`, and any axis supports globs.
Combinations can be filtered declaratively with `continue if` / `break if`
(also usable inside regular `for` loops):

```
build_matrix {
    matrix os in windows, linux; arch in amd64, arm64 {
        continue if "&os" == "windows" && "&arch" == "arm64"
        $ echo "building &os/&arch"
    }
}
```

Bare `continue` (skip the rest of this iteration) and `break` (exit the loop)
work inside any `for` or `matrix` body, including nested in `if` blocks. Use
`$ continue` if you actually need the shell builtin.

## Example Constfile

```
var name = @USER

setup {
    $ pip install requests
}

test < setup {
    $ pytest tests/
}

build (env, opt region) < test {
    $ echo "Building for &env"
    $ echo "Region: &region"
}

_ {
    $ echo "Welcome &name!"
}
```

## Platform-Specific Files

Construct automatically looks for platform-specific Constfiles:

- `Constfile-darwin` (macOS)
- `Constfile-linux` (Linux)
- `Constfile-windows` (Windows)

If `Constfile` doesn't exist but a platform-specific file does, it will be used automatically.

## Configuration

Construct runs each command body through a shell. The shell is chosen automatically (`cmd.exe` on Windows, otherwise the `SHELL` environment variable, falling back to `/bin/sh`), but you can override it. When `zsh` or `bash` is used, startup files (`.zshrc`, `.bashrc`, etc.) are skipped — command bodies are batch scripts, and this keeps per-statement startup in the single-digit milliseconds. If a command needs aliases or functions from your rc files, set `CONSTRUCT_SHELL` to a plain `sh` or export the setup yourself.

| Env var | Default | Purpose |
|---------|---------|---------|
| `CONSTRUCT_SHELL` | platform default | Override the shell binary used to run command bodies (e.g. `bash`, `/usr/local/bin/zsh`) |
| `CONSTRUCT_CLOUD_FILE` | `fakecloud.json` | Path to the JSON file holding cloud-accessible command definitions |
| `SHELL` | (system) | Used as the non-Windows shell when `CONSTRUCT_SHELL` is unset |

## Error Detection

Construct validates:
- Circular dependencies in prerequisites
- Missing prerequisite commands
- Unclosed command blocks

Failures are reported with their source location, e.g.
`Constfile:42: command '...' failed (exit 1)` — the file and line of the
failing statement — and parse errors include the offending line.

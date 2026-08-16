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
| `init [template]` | Scaffold a Constfile (`minimal`, `go`, `python`, `node`, `rust`, `monorepo`; `--force` to overwrite) |
| `import [Makefile] [out]` | Convert a Makefile to a Constfile (best-effort; `--force` to overwrite) |
| `shell [command]` | Start a shell with a command's env block, workdir, or container (`--container IMG` for ad-hoc) |
| `doctor` | Diagnose the environment, Constfile, tools, and cloud file |
| `stats` | Show per-command timing history from `.construct-cache/run-state.json` |
| `cloud list\|pull\|push` | Manage cloud command definitions (see Cloud Commands) |
| `cloud submit [targets...]` | Dispatch a build to GitHub Actions (`--wait` follows it) |
| `cloud status\|logs\|cancel <run-id>` | Inspect or cancel a dispatched run |
| `cloud init-actions` | Create `.github/workflows/construct.yml` |
| `clean [targets...]` | Remove files declared in `produces` (`--cache` also removes `.construct-cache`; respects `--dry-run`) |
| `lint [file]` | Static checks shared with the editor (`--strict` fails on warnings, `--json` for tools) |
| `graph [targets...]` | Print the dependency tree (`--dot` for Graphviz, `--json` for tools) |
| `completion <shell>` | Emit bash/zsh/fish completion (flags + your Constfile's commands) |
| `fmt [files...]` | Canonicalize Constfile indentation (`--check` for CI) |
| `ui [Constfile]` | Edit the Constfile (and its imports) in a drag-and-drop browser editor |

### Options

| Flag | Description |
|------|-------------|
| `-h, --help` | Show help message |
| `-v, --version` | Show version information |
| `--debug` | Enable debug mode for verbose output |
| `--concurrent` | Execute commands and their prerequisites concurrently (DAG-parallel) |
| `--jobs N` | Cap parallel commands (implies `--concurrent`) |
| `-k, --keep-going` | Continue other targets when one fails; report all failures |
| `--no-cache` | Ignore the file-dep cache and run everything |
| `--quiet`, `-q` | Suppress command output, keep errors |
| `--explain` | Print why commands run or are skipped |
| `--json` | Machine-readable output (with `--list`) |
| `--shell PATH` | Shell to run statements with (default: `$SHELL`) |
| `--watch` | Rerun when the Constfile, its imports, or its dependencies change |
| `--choose` | Interactively select targets (arrow-key menu; type to filter) |
| `--timing` | Print per-command elapsed time |
| `--dry-run` | Show commands without executing them |
| `--list` | List all available commands |
| `--env-file PATH` | Load environment variables from a dotenv-style file |
| `--resume`, `--only-failed` | Rerun only the commands that failed in the last run |
| `--repeat N` | Run the whole build N times (flaky detection) |
| `--flame` | Print a per-statement flame graph after the run |
| `--github-actions` | GitHub Actions native output (auto-enabled under `GITHUB_ACTIONS`) |
| `--yes` | Auto-approve `confirm` statements |
| `--force`, `-f` | Overwrite files (`init`) |
| `--notify` | Desktop notification when the run finishes (works with `--watch` and `--repeat`) |
| `--tui` | Live dashboard for the run (`q` detaches, Ctrl-C cancels) |
| `--container IMG` | `shell`: run in this container image instead of the command's |
| `--strict` | `lint`: fail on warnings too |
| `--cache` | `clean`: also remove `.construct-cache` |
| `--dot` | `graph`: emit Graphviz DOT |
| `--check` | `fmt`: exit 1 when files are not formatted |
| `--port N` | `ui`: serve on this port (default: a random free port) |
| `--no-open` | `ui`: print the URL without opening a browser |

### Examples

```bash
construct                    # Run default command from Constfile
construct build test         # Run 'build' and 'test' commands
construct MyFile build       # Run 'build' from MyFile
construct --list             # List available commands
construct --dry-run build    # Show what 'build' would execute
construct --debug build      # Run 'build' with debug output
construct --jobs 16 build    # Parallel build (independent commands run concurrently)
```

## Constfile Syntax

### Variables

```
var name = value
var envVar = @ENVIRONMENT_VAR    # Environment variable
var envOr = @PORT:-8080          # Env var, with a default when unset
var ref = &otherVar              # Reference another variable
```

`@ENV:-default` also works in shell lines and conditions: an unset variable
expands to the default instead of staying literal or becoming empty.

#### Lists and Expressions

A variable can hold a list of values, created with `[...]`:

```
var platforms = [linux, windows]
var more = &platforms + [macos]      # list concatenation
var first = &platforms.0             # index into a list
var count = len(&platforms)          # list length
var sorted = sort([b, a, c])         # sort and uniq
var joined = join(&platforms, "+")   # join/split
```

- In shell lines a list expands space-joined (`&platforms` → `linux windows`);
  in `for` loops, `in` conditions, and variable values it behaves as a list.
- `for x in &platforms` iterates the items; `"&x" in &platforms` tests
  membership.

Variable values are evaluated as expressions when they contain operators:

```
var total = &count * 2 + 1           # arithmetic: + - * / %
var tag = &count >= 5 ? "big" : "small"   # ternary
var combined = &a + &b               # numbers add, strings concatenate
var ok = &env == "prod" && &count > 0    # logical operators
```

Values without operators keep the literal substitution behavior.

#### Builtin Functions

Available in variable values, `env` blocks, `switch` expressions, and
`for ... in` item lists (use them in conditions where `exists`/`missing`/
`glob`/`require` already work):

| Function | Returns |
|----------|---------|
| `upper(s)`, `lower(s)`, `trim(s)`, `replace(s, old, new)` | string helpers |
| `sprintf(fmt, args...)` | printf-style formatting |
| `basename(s)`, `dirname(s)`, `ext(s)`, `stem(s)` | path helpers |
| `length(s)`, `len(x)` | rune count or list length |
| `abs(n)`, `min(a, b, ...)`, `max(a, b, ...)` | numeric helpers |
| `date("2006-01-02")` | current time (Go layout; defaults to `2006-01-02`) |
| `uuid()` | a random v4 UUID |
| `file(path)` | the file's contents (trailing newline trimmed) |
| `lines(path)` | the file's non-empty lines as a list |
| `sha256(path)` | hex digest of a file |
| `glob(pattern)` | matching files as a list |
| `sort(list)`, `uniq(list)`, `join(list, sep)`, `split(s, sep)` | list helpers |
| `env("NAME")` | an environment variable's value |
| `state("name")` / `@state("name")` | a value persisted by a `state` declaration |
| `exists(path)`, `missing(path)`, `require(tool)` | "true"/"false" checks |

Paths resolve relative to the Constfile's directory.

#### Persistent State

`state name = value` declares a variable that persists across runs in
`.construct-cache/state.json`:

```
state last_release = "0.0.0"

release {
    $ echo "previous: @state("last_release")"
    state last_release = "1.0.0"   # written to state.json
}
```

`state` declarations may appear at the top level or inside command bodies;
`@state("name")` and `state("name")` read the persisted value.

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
construct deploy env=prod region=us-east      # bare key=value shorthand
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

A `&ref` followed by a dot only resolves as a prereq output (or namespaced
variable) when the full dotted name exists; otherwise the first segment
resolves as a plain variable and the rest stays literal — so `&version.vsix`
with `var version = 0.2.0` produces `0.2.0.vsix`.

- Prerequisites: `cmd1, cmd2`
- A prerequisite can run in its own directory at the call site: `main < gen in src/`
- Error tolerance: prefix a body statement with `!` to let it fail without aborting the build
- A command body may be written on a single line — including an empty one:
  `|gen| { }` or `clean { rm -rf dist }` (nested single-line blocks work too)

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
command. Env-block variables also resolve as `&NAME` refs in subsequent
statements (including inside loops, e.g. `for i in 1..&N`). `env` blocks
apply from their position to the end of the command body (including nested
`if`/`for` blocks).

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
    invoke gen arg="value"        # passes args as variables in the caller's scope
    for l in &lines {
        $ echo "captured: &l"
    }
}
```

`invoke <cmd> key=value, key2="value 2"` passes arguments to the invoked body;
declared arguments (`arg="default"`) use their default when not passed.

`invoke` only runs the body; prerequisites, file deps, and up-to-date checks
of the invoked command are ignored. Self-referential or circular invokes are
rejected.

### Failing and Cleaning Up

```
deploy {
    onfail {
        $ echo "rolling back"
    }
    if &env == "prod" {
        fail "refusing to deploy to prod"
    }
    $ echo "deploying"
}
```

- `fail "message"` aborts the command with a readable error (`fail: message (file:line)`).
- `require_env KEY "message"` fails the command when an environment variable is unset.
- `global name = value` writes a global variable from inside a command body.
- `retry<3> $ cmd` reruns a statement up to 3 extra times before failing.
  `retry<3, 2s>` also waits between attempts, doubling each time
  (2s, 4s, 8s, ...).
- `onfail { ... }` registers a block that runs once if any later statement in
  the command fails — handy for cleanup and rollback. It runs after the
  failure; the original error is still reported. Inside the block,
  `&fail.message`, `&fail.line`, and `&fail.exit` (when the failure was a
  non-zero exit) describe what went wrong. `onfail` also runs when the build
  is interrupted (Ctrl-C) before exiting.

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

An `onchange` header modifier adds globs to the `--watch` set without
participating in the skip-cache:

```
gen in src onchange src/**.c, src/**.h {
    $ make generate
}
```

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
string operators `contains`, `starts_with`, `ends_with`, and `matches` (regular
expression), the logical operators `&&`, `||`, `!`, and parentheses. `@ENV`
references resolve in shell lines and conditions; in shell lines an unset
variable stays literal (so scoped package names like `@vscode/vsce` survive),
while in variable values it resolves to empty.

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

### Parallel Loops

`parallel for` runs the loop body for every item concurrently — each
iteration gets its own variable scope, environment, and output prefix
(`[cmd/item]`):

```
build {
    parallel for spec in darwin/arm64, windows/amd64, linux/arm64 {
        $ go build -o dist/construct-&spec .
    }
}
```

- The iteration cap defaults to `--jobs` (or the CPU count); an explicit cap
  can be given with the `<N>` keyword modifier: `parallel<4> for x in ...`.
  `--jobs` still bounds global process concurrency, so the two compose.
- `continue` (and `continue if`) skips a single iteration; `break` is an
  error — it cannot stop other in-flight iterations.
- Variables assigned inside a parallel body are shared across iterations;
  the loop variable itself is always iteration-local.
- On failure the loop stops launching new iterations, waits for in-flight
  ones, and reports the first error.

`parallel matrix` parallelizes across the cross product (the outer axis
varies concurrently, inner axes stay nested):

```
build_matrix {
    parallel matrix os in windows, linux; arch in amd64, arm64 {
        continue if "&os" == "windows" && "&arch" == "arm64"
        $ echo "building &os/&arch"
    }
}
```

The `<...>` modifier slot is shared by keyword modifiers: `parallel<N>`
(iteration cap), `lock<5m>` (bounded lock wait), `retry<3, 2s>` (retry
count plus optional backoff base), `switch<strict>` (fail on no match),
and `timeout<30s>` (statement timeout).

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

### Switch

Multi-arm dispatch over an expression; the first matching case runs:

```
deploy {
    switch "&env" {
        case "prod", "staging" {
            $ aws deploy
        }
        case "dev" {
            $ local-deploy
        }
        default {
            $ echo "unknown env &env"
        }
    }
}
```

Case values are comma-separated and matched exactly; `default` runs when
nothing matches. `switch<strict>` fails the command when nothing matches
and there is no `default` — useful when a missing case is a configuration
error. `case`/`default` outside a `switch` is a parse error.

### Scoped Working Directories

`in <dir> { ... }` runs a block with its working directory set (created if
missing), returning afterwards:

```
test {
    in api {
        $ go test ./...
    }
    in web {
        $ npm test
    }
}
```

### Locks

`lock "name" { ... }` holds an exclusive advisory lock (stored in
`.construct-cache/locks/`) while the block runs. Concurrent `construct`
processes wait for it:

```
deploy {
    lock "deploy" {
        $ aws deploy
    }
}
```

The wait can be bounded with a duration modifier — when it expires, the
command fails instead of hanging:

```
deploy {
    lock<5m> "deploy" {
        $ aws deploy
    }
}
```

### Confirmations and Input

- `confirm "deploy to prod?"` — asks y/N and aborts the command when declined.
  `--yes` auto-approves; non-TTY stdin aborts unless `--yes` is given.
- `prompt "press enter"` — prints the message and waits for Enter on a TTY
  (skips the wait otherwise).
- `input name "question?"` — reads a line from stdin into the variable `name`.

### Timeouts

A command or a single statement can be capped with a duration, written with
the `timeout<...>` modifier in both places - the header caps the whole
command, a body statement caps just that line:

```
build timeout<120s> {
    $ go build
    timeout<30s> $ go test
}
```

A hit kills the statement's process group and reports
`command '...' timed out after 30s (exit 124)`.

### Container Isolation

A command can run its shell statements inside a container image (docker, or
podman when docker is absent):

```
build container "golang:1.26" < **.go {
    $ go build ./...
    $ go test ./...
}
```

The Constfile's directory is mounted at `/work` and statements run there with
`/bin/sh`; the environment is passed through via `--env-file`, and the
command's `in <dir>` workdir maps under `/work`. Builtins (`cp`, `rm`,
`download`, ...) still run on the host. Statement timeouts kill the container
CLI; the container itself is removed via `--rm`.

### Linting

`construct lint` runs the same checks the editor shows inline: out-of-bounds
`&cmd.N` indexes, unknown named outputs, duplicate prerequisites, missing
file dependencies, unused globals, and unreferenced commands. It also flags
misused keywords before they silently misparse: `produces`/`container`/
`timeout` written after the prerequisite list (where they are treated as
prerequisites instead of modifiers), timeouts written with the removed
space form (`timeout 30s $ cmd` or `build timeout 30s {`), `break`/`continue` outside a loop, `break` inside a `parallel`
loop (it cannot stop concurrent iterations), references like `&svc-`
whose trailing `-` is swallowed into the name, `&name` references that
resolve to nothing (typos — they substitute to empty silently), `case`
arms without values, and duplicate named outputs. `parallel<4>` on a
non-loop statement is a parse error. Exit code 1 on errors (or warnings
with `--strict`); `--json` emits machine-readable issues — both are
CI-friendly.

Commands that are meant to be invoked by hand (or via `--choose`) can opt out
of the unreferenced check with the `manual` marker:

```
manual build-construct {
    $ go build -o construct .
}
```

### Cleaning

`construct clean [targets...]` deletes the files a command declares in
`produces` (globs expand, `--dry-run` previews) and refuses to delete
directories or anything outside the Constfile's directory. Add `--cache` to
also drop `.construct-cache` (file-dep hashes, run state, locks).

### Dependency Graph

`construct graph [targets...]` prints the transitive prerequisite tree with
file dependencies marked; `--dot` emits Graphviz DOT for
`construct graph --dot | dot -Tsvg -o graph.svg`, and `--json` for tooling.

### Formatting

`construct fmt` re-indents Constfiles to four spaces per nesting level,
trims trailing whitespace, and normalizes the trailing newline — statement
text (shell lines especially) is preserved byte-for-byte. `construct fmt
--check` exits non-zero on unformatted files for CI.

### Shell Completions

`construct completion bash|zsh|fish` prints a completion script that
completes flags and your Constfile's command names (via a hidden
`construct __targets` helper). Source it from your shell profile.

### Notifications

`--notify` sends a desktop notification (macOS Notification Center, Linux
`notify-send`, Windows balloon tips) when a run finishes — including each
`--watch` rerun — with the duration or the first line of the failure.

### Builtin Commands

Bare lines starting with a builtin name run cross-platform (use `$ cp ...` to
force the shell version):

```
provision {
    mkdir dist
    cp src/app dist/app
    touch dist/version.txt
    download "https://example.com/data.zip" dist/data.zip
    extract dist/data.zip dist/data
    rm -rf dist/tmp
}
```

- `cp src dst` — copies a file or directory (recursive)
- `rm path...` — removes recursively; refuses to remove the base directory or
  its ancestors. `rm<kill> path...` also terminates any process still running
  from the file first (Windows refuses to delete a running executable;
  elsewhere the modifier is a no-op)
- `mkdir path...`, `touch path...` — directory/file helpers
- `download url dst` — fetches with a progress bar on TTYs
- `extract archive dir` — `.zip`, `.tar`, `.tar.gz`/`.tgz`, `.tar.bz2`
  (path-traversal entries are refused)

A leading `!` makes a builtin error-tolerant, and `&last.exit` reports the
result.

### Statement Results: &last.exit and &last.output

After every shell or builtin statement, `&last.exit` and `&last.output` hold
the previous statement's exit code and captured output — most useful after an
error-tolerant `!` statement:

```
deploy {
    ! $ aws cloudfront create-invalidation
    if "&last.exit" != "0" {
        $ echo "invalidation failed, continuing anyway"
    }
}
```

### Cloud Commands

Commands marked `|name|` can fetch their body from a cloud definitions file
(JSON map of name → command). The file comes from `$CONSTRUCT_CLOUD_FILE`,
or `construct-cloud.json` next to the Constfile:

```
|deploy| {
    $ echo "local fallback body"
}

use {
    invoke deploy          # uses the remote body when the local body is empty
}
```

- A cloud-accessible command with an empty local body runs the remote
  definition; `invoke` falls back to the cloud when the command isn't local;
  running `construct name` also works for pure cloud commands.
- `construct cloud list` — list definitions.
- `construct cloud pull [names...]` — write definitions into
  `construct-cloud.json` (auto-loaded).
- `construct cloud push [names...]` — upload local (cloud-accessible) bodies
  into the cloud file (`--file` to choose the target).

### Cloud Jobs (GitHub Actions)

`construct cloud submit` runs a build on GitHub Actions instead of locally:

```bash
construct cloud submit build                     # dispatch
construct cloud submit --wait test               # dispatch + follow logs
construct cloud submit --wait -e CI=true deploy --deploy:env=prod
construct cloud status 12345
construct cloud logs 12345
construct cloud cancel 12345
```

- The repository is inferred from `git remote get-url origin` (override with
  `--repo owner/repo`), the branch from the current checkout (`--ref` to
  override), and the workflow defaults to `.github/workflows/construct.yml`.
- If the workflow file doesn't exist, `cloud submit` creates it (a
  `workflow_dispatch`-driven runner that installs construct, restores the
  previous `.construct-cache` artifact, runs the targets, and uploads the new
  state). Commit and push it, then re-submit.
- Authentication: `GITHUB_TOKEN` (or `CONSTRUCT_GITHUB_TOKEN`), falling back
  to `gh auth token`. The API base is overridable with
  `CONSTRUCT_GITHUB_API`.
- `-e KEY=value` overrides travel to the runner as workflow inputs. Keys that
  look like secrets (token, password, api_key, ...) are warned about and
  their values are redacted from locally streamed logs — but workflow inputs
  are visible to repo collaborators, so prefer GitHub repo secrets for real
  secrets.
- `--wait` polls the run, streams new job logs with secrets redacted, and
  exits non-zero when the run fails. `--json` prints machine-readable
  dispatch/status output.
- Run records, `--resume`, `--repeat`, and `--flame` all behave the same on
  the runner as locally; the runner's `.construct-cache` is uploaded as an
  artifact between runs.

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

## Importing a Makefile

`construct import [Makefile] [output]` converts an existing Makefile to a
Constfile (defaults: `Makefile` → `Constfile`, refusing to overwrite without
`--force`):

```bash
construct import                # Makefile -> Constfile
construct import GNUmakefile    # explicit input
construct import Makefile lib/Constfile
```

What converts mechanically:

- `VAR = ...` / `:=` / `::=` / `?=` → `var NAME = ...` with `$(VAR)` refs
  rewritten to `&NAME` (forward references included)
- rules → commands; prereqs keep make ordering; `.PHONY` targets keep their
  names; file-like targets (`main.o`, `dist/app`) get a safe command name
  plus `produces <original>` so the up-to-date check covers the file
- automatic variables: `$@` → the target, `$<` → the first prereq,
  `$^`/`$?` → all prereqs, `$(MAKE)` → `construct`, `$$` → `$`
- recipe prefixes: `@` is dropped (construct never echoes commands),
  `-` becomes the error-tolerant `!`
- `include` → `import`, doc comments above rules become command
  descriptions, `.DEFAULT_GOAL` (or the first target) becomes `_`

What is flagged instead of guessed: conditionals (`ifeq`/`ifdef`/...),
`$(shell ...)`, pattern rules (`%.o: %.c`), `define` blocks, `export`,
`+=` on existing variables, target-specific variables, order-only
prereqs, and double-colon rules each get a
`# construct-import: ...` comment at the site, and the summary counts
them. The generated file is formatted and parse-checked; run
`construct lint` after importing.

## Interactive Shell

`construct shell [command]` starts an interactive shell with that
command's environment — the resolved `env` block, header workdir
(created if missing), and container image when one is declared:

```bash
construct shell dev                     # env + workdir of 'dev'
construct shell --container golang:1.26 # ad-hoc container shell
construct shell                         # bare shell at the Constfile's dir
```

With `container "image"` (or `--container`), construct runs
`docker/podman run --rm -it` with the Constfile mounted at `/work`, the
environment passed via `--env-file`, and `bash` if the image has it
(falling back to `sh`). Without a container, the shell is `CONSTRUCT_SHELL`,
`$SHELL`, or `/bin/sh`, and the child's exit code becomes construct's.

## Live Dashboard

`--tui` replaces the scrolling output with a full-screen dashboard while
the build runs: one row per command with a live spinner, duration, and
exit codes, plus a rolling log pane for the selected command.

```
construct --tui --jobs 8 build
```

- `j`/`k` (or arrows) select a command to watch; `f` follows the
  currently running one (the default)
- `q` detaches — the terminal is restored and the run continues with
  normal output; nothing is killed
- `Ctrl-C` cancels the run as usual (exit 130)
- when commands fail, their captured output tails print after the
  dashboard closes so the error context isn't lost

The dashboard requires a terminal and is disabled automatically (with a
notice) for `--watch`, `--repeat`, `--choose`, `--dry-run`, `--debug`,
`--explain`, `--quiet`, `--resume`, GitHub Actions output, or Constfiles
using `confirm`/`prompt`/`input`.

## Web Editor

`construct ui` opens a drag-and-drop editor for the Constfile and its whole
import closure in your browser:

```bash
construct ui                  # edit Constfile (or Constfile-$GOOS)
construct ui --port 8080      # pick the port
construct ui --no-open        # print the URL without launching a browser
```

- Commands render as cards — drag to reorder, drop one card onto another's
  prerequisite area to connect them (`deploy < build` without touching the
  text), edit headers as forms (arguments, prerequisites, `produces`,
  `container`, `timeout`, `onchange`, workdir), and edit bodies in a
  monospace editor with Constfile syntax highlighting and a statement
  palette (`if`, `for`, `parallel for`, `env`, `invoke`, `retry<3>`, …).
- Bodies also have a **Structure** view: the parsed statement tree as an
  outline — drag statements to reorder them, drop one onto a block's
  "nest inside" zone to move it into `if`/`for`/`env`/… bodies, click a
  row to jump to its code.
- Imported files get their own tab and are fully editable; namespaced
  imports show the name they are referenced by. Adding an import line
  registers the file immediately.
- The graph view lays out the dependency DAG per file — drag nodes to
  arrange them, drag the ● handle of one node onto another to connect a
  prerequisite, click an edge and press Delete to remove it.
- A lint panel surfaces the same checks as `construct lint` with
  click-to-jump.
- Variables, state, and import lines keep their raw text — the editor never
  re-serializes lines it did not change, so comments and expressions are
  preserved byte-for-byte. Saving formats the file like `construct fmt`.
- Every edit is validated through the real parser before it applies
  (circular dependencies, duplicate names, and broken references are
  rejected with the parse error), with undo/redo and Ctrl-S to save.
  `Dry-run` on a command shows what it would execute without running it.
- The server binds to 127.0.0.1 only and requires a per-session token from
  the printed URL; it refuses to overwrite files that changed on disk since
  they were loaded.

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
| `CONSTRUCT_CLOUD_FILE` | `construct-cloud.json` | Path to the JSON file holding cloud-accessible command definitions |
| `SHELL` | (system) | Used as the non-Windows shell when `CONSTRUCT_SHELL` is unset |

## Error Detection

Construct validates:
- Circular dependencies in prerequisites
- Missing prerequisite commands
- Unclosed command blocks
- `case`/`default` outside a `switch`, duplicate case values, switches without cases
- Header-only keywords (`manual`, `produces`, `container`, `onchange`, `import`)
  used as body statements — with a hint to prefix `$` when a shell command of
  that name was intended
- Unrecognized top-level statements (anything that is not `var`, `import`,
  `state`, or a command header) instead of silently dropping the line
- Top-level blocks like `env { ... }` or `onfail { ... }` that would parse as
  commands literally named after the statement keyword (a lint error, since
  renaming the command is the fix)

Failures are reported with their source location, e.g.
`Constfile:42: command '...' failed (exit 1)` — the file and line of the
failing statement — and parse errors include the offending line.

## Editor Support

The VSCode extension (`editor/vscode`) ships a language server with hover
(variables, arguments, prereq outputs, keywords, builtins, functions, and
`&fail.*`/`&last.*` context refs), go-to-definition (variables, commands,
prereqs, file deps, workdirs, and `state("...")` refs), completions
(variables, command names, statement keywords, builtins, and functions),
document symbols (commands and state declarations), and diagnostics
(parse errors, duplicate prereqs, out-of-range output indexes, named-output
hints). The TextMate grammar highlights all statements, blocks, list
literals, and expressions.

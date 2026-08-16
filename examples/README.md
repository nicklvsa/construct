# Constfile Examples

Each example is a self-contained build file demonstrating different construct features. Run any of them with:

```bash
construct Constfile-<name> --list       # see available commands
construct Constfile-<name> --dry-run    # preview execution
construct Constfile-<name> <command>    # run a command
```

## Examples

| File | Language | Features Demonstrated |
|------|----------|----------------------|
| `Constfile-go-webapp` | Go | File deps, for loops, workdir, named outputs, conditionals, lazy vars |
| `Constfile-python-project` | Python | Lazy vars, env refs, prereq outputs, CLI overrides, conditionals |
| `Constfile-monorepo` | Multi-service | For loops over services, workdir per service, concurrent builds |
| `Constfile-c-cpp` | C/C++ | File deps, for loops over source files, platform conditionals |
| `Constfile-rust` | Rust | Lazy vars, conditionals, named outputs, prereq chaining, cross-compile loops |
| `Constfile-nodejs` | Node.js | Workdir, for loops, env refs, CLI overrides, conditional deploy |
| `Constfile-static-site` | Static site | File deps, workdir, named outputs, for loops over assets |
| `Constfile-imports` (+ `lib.constfile`) | Multi-file | `import` keyword, cross-file prereqs/variables, named outputs, argument defaults |
| `Constfile-advanced-control` | Any | `else if` chains, logical operators (`&&`, `\|\|`, `!`, parens), error-tolerance marker (`!`), matrices, loop control, single-line blocks, `exists()`/`missing()`/`glob()`, loop index, numeric ranges, escape hatch |
| `Constfile-parallel-build` | Any | DAG-parallel prerequisites (`--concurrent`), call-site `in <dir>` workdir overrides |
| `Constfile-artifacts` (+ `example.env`) | Any | `produces` (make-style up-to-date checks), `--env-file` loading, `require()`, `--jobs`, `--watch` |
| `Constfile-modern-features` (+ `Constfile-shared-lib`, `Constfile-shared-other`) | Multi-file | Namespaced imports (`import ... as ns`), doc comments (`--list`), per-command `env { }` blocks, `invoke` with output capture |
| `Constfile-robustness` | Any | `@ENV:-default` env defaults, `fail "message"` guards, `onfail { }` cleanup blocks, `invoke` with arguments, `-k/--keep-going`, `--no-cache`, `--shell` |
| `Constfile-modern-flags` | Any | `require_env`, `global` writes, `retry N`, `starts_with`/`ends_with`/`matches`, `onchange` watch globs, bare `key=value` overrides, `--explain`, `--quiet`, `--list --json`, `--jobs auto`, concurrent output prefixing, SIGINT cleanup |
| `Constfile-expressions` | Any | List values (`[...]`), arithmetic (`+ - * / %`), ternaries, `in` membership, builtin function library (`sort`, `join`, `len`, `upper`, `replace`, `basename`, `lines`, `date`, ...) |
| `Constfile-control-flow` | Any | `switch`/`case`/`default`, `in <dir> { }` scoped workdirs, `lock`, `timeout` (command + statement), `confirm`/`prompt`/`input`, `&last.exit`/`&last.output` |
| `Constfile-builtins` | Any | Cross-platform builtin commands `cp`/`rm`/`mkdir`/`touch`/`download`/`extract`, error-tolerant builtins, `&last.exit` reporting |
| `Constfile-state` | Any | Persistent `state` variables, `@state("name")` reads, runtime state expressions, `--resume`, `stats` |
| `Constfile-cloud` (+ `example-cloud.json`) | Any | Cloud commands (`\|name\|`), `invoke` cloud fallback, pure cloud commands, `cloud list/pull/push` |
| `Constfile-locks` | Any | Real-world `lock` usage: a release/rollback pipeline serialized on a shared named lock (`lock<5s>` bounded waits), composed with `confirm` and `onfail` |
| `Constfile-container` (+ `src/`) | Any | Container isolation (`container "image"`): containerized builds, cross-compile matrices, host/container composition, timeouts |
| `Constfile-parallel` | Any | `parallel for` / `parallel<N>` / `parallel matrix`: concurrent iterations, per-item output prefixes, `continue if`, composition with env/workdirs/nested loops |
| `Makefile` | Any | Import demo: run `construct import examples/Makefile` to convert it (vars, `.PHONY`, automatic vars, file targets; ifeq/`$(shell)`/pattern rule show up as flagged comments) |
| `Constfile-shell` | Any | `construct shell`: interactive shells with a command's env block, workdir, or container; ad-hoc `--container` |
| `Constfile-dashboard` | Any | `--tui` live dashboard over a multi-command DAG: statuses, durations, output tails, detach, cached skips, failure tails |

## Feature Coverage

- **File dependency tracking** — `Constfile-go-webapp`, `Constfile-c-cpp`, `Constfile-rust`, `Constfile-static-site`
- **For loops** — all examples
- **CLI variable overrides** — `Constfile-python-project`, `Constfile-nodejs`, `Constfile-advanced-control`
- **Named outputs (`as`)** — `Constfile-go-webapp`, `Constfile-python-project`, `Constfile-rust`, `Constfile-imports`, `Constfile-parallel-build`
- **Conditionals (`if/else`)** — `Constfile-go-webapp`, `Constfile-python-project`, `Constfile-c-cpp`, `Constfile-monorepo`, `Constfile-nodejs`
- **Logical operators + `else if`** — `Constfile-advanced-control`
- **Build matrices (`matrix`)** — `Constfile-advanced-control`
- **Loop control (`continue`/`break`)** — `Constfile-advanced-control`
- **Single-line blocks** — `Constfile-advanced-control`
- **Built-in file-test functions** — `Constfile-advanced-control`
- **Loop index + numeric ranges** — `Constfile-advanced-control`
- **Escape hatch (`\&`, `\@`)** — `Constfile-advanced-control`
- **Argument defaults (`opt env=prod`)** — `Constfile-imports`
- **Produced artifacts (`produces`)** — `Constfile-artifacts`
- **Environment files (`--env-file`)** — `Constfile-artifacts`
- **Toolchain guards (`require()`)** — `Constfile-artifacts`
- **Working directory (`in`)** — `Constfile-go-webapp`, `Constfile-monorepo`, `Constfile-static-site`, `Constfile-parallel-build` (call-site overrides)
- **Lazy variables (`$`)** — `Constfile-go-webapp`, `Constfile-python-project`, `Constfile-rust`
- **Environment refs (`@`)** — `Constfile-python-project`, `Constfile-c-cpp`
- **Imports (`import`)** — `Constfile-imports`
- **Namespaced imports (`import ... as ns`)** — `Constfile-modern-features`
- **Doc comments (`--list`)** — `Constfile-modern-features`
- **Per-command `env { }` blocks** — `Constfile-modern-features`
- **`invoke` with output capture** — `Constfile-modern-features`
- **Error tolerance (`!`)** — `Constfile-advanced-control`
- **DAG-parallel prerequisites** — `Constfile-parallel-build`
- **List values + list operators** — `Constfile-expressions`
- **Arithmetic + ternaries** — `Constfile-expressions`
- **Builtin function library** — `Constfile-expressions`
- **`switch`/`case`/`default`** — `Constfile-control-flow`
- **Scoped workdir blocks (`in`)** — `Constfile-control-flow`
- **`lock` mutual exclusion** — `Constfile-control-flow`, `Constfile-locks` (real-world release/rollback)
- **Keyword modifiers (`parallel<N>`, `lock<5s>`, `retry<2s>`, `switch<strict>`)** — `Constfile-parallel`, `Constfile-locks`, `Constfile-modern-flags`, `Constfile-control-flow`
- **`timeout` (command + statement)** — `Constfile-control-flow`
- **`confirm`/`prompt`/`input` + `--yes`** — `Constfile-control-flow`
- **`&last.exit`/`&last.output`** — `Constfile-control-flow`, `Constfile-builtins`
- **Builtin commands (`cp`/`rm`/`mkdir`/`touch`/`download`/`extract`)** — `Constfile-builtins`
- **Persistent `state` variables** — `Constfile-state`
- **`--resume` / `--only-failed`** — `Constfile-state`
- **`--repeat`, `--flame`, `--github-actions`** — `Constfile-state`, any example
- **Cloud commands (`|name|`, `invoke` fallback)** — `Constfile-cloud`
- **Parallel loops (`parallel for`, `parallel<N>`, `parallel matrix`)** — `Constfile-parallel`
- **`retry<2s>` backoff** — `Constfile-modern-flags`
- **`switch<strict>`** — `Constfile-control-flow`
- **Makefile import (`construct import`)** — `Makefile`
- **Interactive shells (`construct shell`, `--container`)** — `Constfile-shell`
- **Live dashboard (`--tui`)** — `Constfile-dashboard`
- **`init`/`doctor`/`stats`/`cloud` subcommands** — any example

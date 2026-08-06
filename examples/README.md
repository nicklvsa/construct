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

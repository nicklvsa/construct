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

## Feature Coverage

- **File dependency tracking** — `Constfile-go-webapp`, `Constfile-c-cpp`, `Constfile-rust`, `Constfile-static-site`
- **For loops** — all examples
- **CLI variable overrides** — `Constfile-python-project`, `Constfile-nodejs`
- **Named outputs (`as`)** — `Constfile-go-webapp`, `Constfile-python-project`, `Constfile-rust`
- **Conditionals (`if/else`)** — `Constfile-go-webapp`, `Constfile-python-project`, `Constfile-c-cpp`, `Constfile-monorepo`, `Constfile-nodejs`
- **Working directory (`in`)** — `Constfile-go-webapp`, `Constfile-monorepo`, `Constfile-static-site`
- **Lazy variables (`$`)** — `Constfile-go-webapp`, `Constfile-python-project`, `Constfile-rust`
- **Environment refs (`@`)** — `Constfile-python-project`, `Constfile-c-cpp`

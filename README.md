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
| `--concurrent` | Execute commands concurrently |
| `--dry-run` | Show commands without executing them |
| `--list` | List all available commands |

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
- Arguments: `arg1` (required), `opt arg2` (optional)
- Prerequisites: `cmd1, cmd2`

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

Construct runs each command body through a shell. The shell is chosen automatically (`cmd.exe` on Windows, otherwise the `SHELL` environment variable, falling back to `/bin/sh`), but you can override it:

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

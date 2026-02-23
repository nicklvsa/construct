## Construct

A Make-like build tool written in Go for organizing command executions across platforms.

## Installation

```bash
go build -o construct
```

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

Access prerequisite output using `&prereqName.index`:

```
prereq {
    $ echo "hello"
}

main < prereq {
    $ echo &prereq.0    # Outputs: hello
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

## Error Detection

Construct validates:
- Circular dependencies in prerequisites
- Missing prerequisite commands
- Unclosed command blocks

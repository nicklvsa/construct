#!/usr/bin/env bash
# Build and install the Constfile VSCode extension in one command.
#
#   ./build.sh            build + install the extension into VSCode
#   ./build.sh --package  build + produce a .vsix file (no install)
#
# Run from the repo root. Requires Go and npm on your PATH.
set -euo pipefail

EXT_DIR="editor/vscode"
SERVER_DIR="$EXT_DIR/server"

# Detect the server binary name per platform.
case "$(uname -s)" in
    MINGW*|MSYS*|CYGWIN*) SERVER_BIN="construct-lsp.exe" ;;
    Darwin)               SERVER_BIN="construct-lsp" ;;
    *)                    SERVER_BIN="construct-lsp" ;;
esac

# Pick the vsce version that matches the installed Node. @vscode/vsce >= 3
# requires Node 18+; older Node uses vsce 2.x.
NODE_MAJOR=$(node -p "process.versions.node.split('.')[0]" 2>/dev/null || echo "0")
if [[ "$NODE_MAJOR" -ge 18 ]]; then
    VSCE_CMD="npx @vscode/vsce@latest"
else
    VSCE_CMD="npx vsce@2.19.0"
fi

echo "==> Building Go language server..."
( cd "$SERVER_DIR" && go build -o "$SERVER_BIN" . )
echo "    -> $SERVER_DIR/$SERVER_BIN"

echo "==> Installing npm dependencies (if needed)..."
( cd "$EXT_DIR" && npm install --silent )

echo "==> Compiling TypeScript client..."
( cd "$EXT_DIR" && npm run compile )

# Package to .vsix (required — `code --install-extension` only accepts a .vsix
# or a published extension ID, not a raw folder).
echo "==> Packaging extension as .vsix (using $VSCE_CMD, Node $NODE_MAJOR)..."
( cd "$EXT_DIR" && $VSCE_CMD package --no-git-tag-version --allow-star-registration )

VSIX=$(ls "$EXT_DIR"/*.vsix 2>/dev/null | head -1)
if [[ -z "$VSIX" ]]; then
    echo "ERROR: no .vsix produced" >&2
    exit 1
fi

if [[ "${1:-}" == "--package" ]]; then
    echo ""
    echo "Done. Extension packaged at: $VSIX"
    echo "Install it manually with:  code --install-extension \"$VSIX\""
else
    echo "==> Installing extension into VSCode..."
    code --install-extension "$VSIX" --force
    echo ""
    echo "Done. Reload VSCode ('Developer: Reload Window') to activate."
    echo "Open any Constfile to see syntax highlighting + language intelligence."
fi

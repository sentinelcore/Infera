#!/bin/sh
# Infera node installer for macOS and Linux.
#   curl -fsSL https://raw.githubusercontent.com/sentinelcore/Infera/main/install.sh | sh
set -eu
REPO="${INFERA_REPO:-sentinelcore/Infera}"
DEST="${INFERA_BIN:-$HOME/.infera/bin}"
os=$(uname -s); arch=$(uname -m)
case "$os-$arch" in
  Linux-x86_64)            name=infera-linux-x64 ;;
  Linux-aarch64|Linux-arm64) name=infera-linux-arm64 ;;
  Darwin-arm64)            name=infera-macos-arm64 ;;
  Darwin-x86_64)           name=infera-macos-x64 ;;
  *) echo "Infera doesn't have a build for $os $arch yet." >&2; exit 1 ;;
esac
url="https://github.com/$REPO/releases/latest/download/$name.tar.gz"
echo "Downloading $name …"
mkdir -p "$DEST"
tmp=$(mktemp -d)
curl -fsSL "$url" -o "$tmp/infera.tar.gz"
tar -xzf "$tmp/infera.tar.gz" -C "$DEST"
chmod +x "$DEST/infera"
rm -rf "$tmp"
echo "Installed to $DEST/infera"
case ":$PATH:" in *":$DEST:"*) ;; *) echo "Add it to your PATH:  export PATH=\"$DEST:\$PATH\"" ;; esac
cat <<'NEXT'

Next steps:
  1. Get a 6-digit code on the dashboard (Contribute a device > Get pairing code)
  2. infera pair <code> --server <dashboard url>
  3. Pick a model (see TESTING.md), e.g. with Ollama running:
       infera engine --url http://127.0.0.1:11434 --model qwen3-1.7b --engine-model qwen3:1.7b --params-b 1.7
  4. infera run      (local page: http://127.0.0.1:7777)
NEXT

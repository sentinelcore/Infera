#!/bin/sh
# Infera node installer for macOS and Linux. One command does everything: installs Infera and
# Ollama, pairs, picks and downloads the right models, benchmarks and starts at login.
#   curl -fsSL "https://infera.valanor.network/install.sh?code=123456" | sh   # the dashboard shows this line
#   curl -fsSL https://<your Infera server>/install.sh | sh                 # install only
#   curl -fsSL https://<your Infera server>/install.sh | sh -s -- 123456    # install, pair with this code,
#   … | sh -s -- 123456 --model deepseek-r1-distill-llama-8b               # choose the model
#                                                                            # download a model, start at login
set -eu
CODE="${1:-${INFERA_CODE:-}}"
[ $# -gt 0 ] && shift  # anything after the code (e.g. --model …) goes to infera setup
REPO="${INFERA_REPO:-sentinelcore/Infera}"
TAG="${INFERA_VERSION:-test-build}"
DEST="${INFERA_BIN:-$HOME/.infera/bin}"
os=$(uname -s); arch=$(uname -m)
case "$os-$arch" in
  Linux-x86_64)            name=infera-linux-x64 ;;
  Linux-aarch64|Linux-arm64) name=infera-linux-arm64 ;;
  Darwin-arm64)
    # Apple Silicon: download the ready-made build (curl downloads aren't quarantined).
    url="https://github.com/$REPO/releases/download/$TAG/infera-macos-arm64.tar.gz"
    tmp=$(mktemp -d)
    if curl -fsSL "$url" -o "$tmp/infera.tar.gz" 2>/dev/null; then
      echo "Downloading infera-macos-arm64 …"
      mkdir -p "$DEST"; tar -xzf "$tmp/infera.tar.gz" -C "$DEST"; chmod +x "$DEST/infera"; rm -rf "$tmp"
      echo "Installed to $DEST/infera"
      if [ -n "$CODE" ]; then exec "$DEST/infera" setup "$CODE" "$@"; fi
      echo "Next: get a pairing code on the dashboard, then run:  $DEST/infera setup <code>"
      exit 0
    fi
    rm -rf "$tmp"
    INFERA_FROM_SOURCE=1 ;;
esac
case "$os-${INFERA_FROM_SOURCE:-}" in
  Darwin-*)
    # Intel Mac (or no download yet): build it here once (about 5 minutes).
    BRANCH="${INFERA_BRANCH:-ccr-13fb360c-2cnfc4}"
    if ! xcode-select -p >/dev/null 2>&1; then
      echo "Installing Apple's command line tools (a window will open; click Install), then run this command again."
      xcode-select --install || true
      exit 1
    fi
    if ! command -v cargo >/dev/null 2>&1 && [ ! -x "$HOME/.cargo/bin/cargo" ]; then
      echo "Installing Rust (needed once to build Infera) …"
      curl -fsSL https://sh.rustup.rs | sh -s -- -y --profile minimal >/dev/null
    fi
    PATH="$HOME/.cargo/bin:$PATH"
    SRC="$HOME/.infera/src"
    echo "Getting the Infera source …"
    if [ -d "$SRC/.git" ]; then
      git -C "$SRC" fetch -q --depth 1 origin "$BRANCH" && git -C "$SRC" reset -q --hard FETCH_HEAD
    else
      rm -rf "$SRC"; mkdir -p "$HOME/.infera"
      git clone -q --depth 1 -b "$BRANCH" "https://github.com/$REPO.git" "$SRC" || {
        echo "Couldn't download the source. If the repository is private, sign in to GitHub first (e.g. 'gh auth login'), then run this again." >&2; exit 1; }
    fi
    echo "Building Infera (about 5 minutes, only this once) …"
    (cd "$SRC/agent" && cargo build --release -q)
    mkdir -p "$DEST"
    cp "$SRC/agent/target/release/infera" "$DEST/infera"
    chmod +x "$DEST/infera"
    echo "Installed to $DEST/infera"
    if [ -n "$CODE" ]; then exec "$DEST/infera" setup "$CODE" "$@"; fi
    echo "Next: get a pairing code on the dashboard, then run:  $DEST/infera setup <code>"
    exit 0 ;;
esac
[ -n "${name:-}" ] || { echo "Infera doesn't have a build for $os $arch yet." >&2; exit 1; }
url="https://github.com/$REPO/releases/download/$TAG/$name.tar.gz"
echo "Downloading $name …"
mkdir -p "$DEST"
tmp=$(mktemp -d)
curl -fsSL "$url" -o "$tmp/infera.tar.gz"
tar -xzf "$tmp/infera.tar.gz" -C "$DEST"
chmod +x "$DEST/infera"
rm -rf "$tmp"
echo "Installed to $DEST/infera"
if [ -n "$CODE" ]; then exec "$DEST/infera" setup "$CODE" "$@"; fi
case ":$PATH:" in *":$DEST:"*) ;; *) echo "Add it to your PATH:  export PATH=\"$DEST:\$PATH\"" ;; esac
cat <<'NEXT'

Next steps:
  1. Get a 6-digit code on the dashboard (Contribute a device > Get pairing code)
  2. infera pair <code> --server <dashboard url>
  3. Pick a model (see TESTING.md), e.g. with Ollama running:
       infera engine --url http://127.0.0.1:11434 --model qwen3-1.7b --engine-model qwen3:1.7b --params-b 1.7
  4. infera run      (local page: http://127.0.0.1:7777)
NEXT

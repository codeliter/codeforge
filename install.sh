#!/bin/sh
# Installs the latest codeforge release for this machine into ~/.local/bin (or $CODEFORGE_BIN).
set -eu

repo="codeliter/codeforge"
bin_dir="${CODEFORGE_BIN:-$HOME/.local/bin}"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) echo "codeforge: unsupported CPU $(uname -m)" >&2; exit 1 ;;
esac
case "$os" in
  darwin | linux) ;;
  *) echo "codeforge: unsupported OS $os" >&2; exit 1 ;;
esac

asset="codeforge_${os}_${arch}.tar.gz"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

base="https://github.com/$repo/releases/latest/download"
curl -fsSL "$base/$asset" -o "$tmp/$asset"
curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt"

(cd "$tmp" && grep " $asset\$" checksums.txt | shasum -a 256 -c -) >/dev/null
tar -xzf "$tmp/$asset" -C "$tmp" codeforge
mkdir -p "$bin_dir"
install -m 0755 "$tmp/codeforge" "$bin_dir/codeforge"

echo "codeforge installed to $bin_dir/codeforge"
case ":$PATH:" in
  *":$bin_dir:"*) ;;
  *) echo "Add $bin_dir to your PATH, then run: codeforge init" ;;
esac

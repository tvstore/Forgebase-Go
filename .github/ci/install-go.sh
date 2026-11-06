#!/usr/bin/env bash
# Install the Go toolchain named by the `toolchain` line of go.mod, without any third-party action.
#
# Downloads https://go.dev/dl/<version>.<os>-<arch>.tar.gz, verifies its sha256 against the JSON
# index at https://go.dev/dl/?mode=json&include=all, extracts it into $RUNNER_TEMP/go, adds its bin
# directory to $GITHUB_PATH and sets GOTOOLCHAIN=local in $GITHUB_ENV (so that no job downloads
# another toolchain behind the pin). Needs bash, curl, tar and python3 (all on the runner images).
#
# Usage: .github/ci/install-go.sh        (from the repository root; GitHub Actions environment)
set -euo pipefail

: "${RUNNER_TEMP:?RUNNER_TEMP is not set}"
: "${GITHUB_PATH:?GITHUB_PATH is not set}"
: "${GITHUB_ENV:?GITHUB_ENV is not set}"

version="$(sed -n 's/^toolchain[[:space:]]\{1,\}\(go[0-9][0-9.]*\)[[:space:]]*$/\1/p' go.mod)"
if [ -z "$version" ]; then
  echo "install-go: no 'toolchain goX.Y.Z' line in go.mod" >&2
  exit 1
fi

case "$(uname -s)" in
  Linux) os=linux ;;
  *) echo "install-go: unsupported OS $(uname -s)" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) echo "install-go: unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac
file="$version.$os-$arch.tar.gz"

work="$RUNNER_TEMP/go-download"
rm -rf "$work"
mkdir -p "$work"

curl -fsSL --retry 3 -o "$work/index.json" "https://go.dev/dl/?mode=json&include=all"
expected="$(python3 - "$work/index.json" "$file" <<'PY'
import json, sys
index, name = sys.argv[1], sys.argv[2]
with open(index) as f:
    for release in json.load(f):
        for entry in release.get("files", []):
            if entry.get("filename") == name:
                print(entry["sha256"])
                sys.exit(0)
sys.exit("install-go: " + name + " is not in the Go download index")
PY
)"
if [ -z "$expected" ]; then
  echo "install-go: empty checksum for $file" >&2
  exit 1
fi

curl -fsSL --retry 3 -o "$work/$file" "https://go.dev/dl/$file"
actual="$(sha256sum "$work/$file" | cut -d' ' -f1)"
if [ "$actual" != "$expected" ]; then
  echo "install-go: sha256 mismatch for $file: expected $expected, got $actual" >&2
  exit 1
fi

rm -rf "$RUNNER_TEMP/go"
tar -C "$RUNNER_TEMP" -xzf "$work/$file"   # the archive contains the directory go/
rm -rf "$work"

echo "$RUNNER_TEMP/go/bin" >> "$GITHUB_PATH"
echo "GOTOOLCHAIN=local" >> "$GITHUB_ENV"
"$RUNNER_TEMP/go/bin/go" version

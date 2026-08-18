#!/usr/bin/env bash

set -euo pipefail

: "${RELEASE_COMMIT:?RELEASE_COMMIT is required}"

release_tag="${RELEASE_TAG:-v0.0.0-dev}"
case "$release_tag" in
  v*) ;;
  *) printf 'maze CLI release tags must use v*: %s\n' "$release_tag" >&2; exit 1 ;;
esac
version="${release_tag#v}"

dist_dir="${DIST_DIR:-dist}"
mkdir -p "$dist_dir"
dist_path="$(cd "$dist_dir" && pwd)"

if command -v sha256sum >/dev/null 2>&1; then
  sha256_tool=(sha256sum)
else
  sha256_tool=(shasum -a 256)
fi

build_date="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
ldflags="-s -w -X github.com/packagemaze/maze-cli/internal/version.Version=${version} -X github.com/packagemaze/maze-cli/internal/version.Commit=${RELEASE_COMMIT} -X github.com/packagemaze/maze-cli/internal/version.Date=${build_date}"

for target in linux/amd64 linux/arm64 darwin/arm64 windows/amd64; do
  goos="${target%/*}"
  goarch="${target#*/}"
  platform="${goos}_${goarch}"
  binary="maze"
  if [ "$goos" = "windows" ]; then
    binary="maze.exe"
  fi
  work_dir="$(mktemp -d)"
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -trimpath -ldflags "$ldflags" -o "${work_dir}/${binary}" ./cmd/maze
  if [ "$goos" = "windows" ]; then
    (cd "$work_dir" && zip -q -X "${dist_path}/maze_${platform}.zip" "$binary")
  else
    tar -C "$work_dir" -czf "${dist_path}/maze_${platform}.tar.gz" "$binary"
  fi
  rm -rf "$work_dir"
done

(cd "$dist_path" && "${sha256_tool[@]}" maze_*.tar.gz maze_*.zip > maze_checksums.txt)

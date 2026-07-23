#!/usr/bin/env bash

set -euo pipefail

: "${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}"
: "${GITHUB_SHA:?GITHUB_SHA is required}"
: "${RELEASE_TAG:?RELEASE_TAG is required}"
: "${VERSION:?VERSION is required}"

dist_dir="${DIST_DIR:-dist}"
asset_paths=()
shopt -s nullglob
for path in "$dist_dir"/*; do
  if [[ -f "$path" ]]; then
    asset_paths+=("$path")
  fi
done
shopt -u nullglob

if [[ ${#asset_paths[@]} -eq 0 ]]; then
  printf 'maze release has no rebuilt assets in %s\n' "$dist_dir" >&2
  exit 1
fi

notes_file="$(mktemp)"
release_json="$(mktemp)"
release_error="$(mktemp)"
expected_names="$(mktemp)"
published_names="$(mktemp)"
trap 'rm -f "$notes_file" "$release_json" "$release_error" "$expected_names" "$published_names"' EXIT

cat > "$notes_file" <<EOF
PackageMaze maze CLI ${VERSION}

Assets:
- maze_linux_amd64.tar.gz
- maze_linux_arm64.tar.gz
- maze_darwin_arm64.tar.gz
- maze_checksums.txt

Windows binaries are intentionally deferred.
EOF

set +e
gh release view "$RELEASE_TAG" \
  --repo "$GITHUB_REPOSITORY" \
  --json isDraft,isImmutable,targetCommitish,tagName,assets \
  >"$release_json" 2>"$release_error"
release_view_status=$?
set -e

if [[ $release_view_status -ne 0 ]]; then
  if grep -Fq 'release not found' "$release_error"; then
    gh release create "$RELEASE_TAG" "${asset_paths[@]}" \
      --target "$GITHUB_SHA" \
      --title "maze ${VERSION}" \
      --notes-file "$notes_file" \
      --repo "$GITHUB_REPOSITORY"
    exit 0
  fi

  printf 'maze release could not inspect %s before publication:\n' "$RELEASE_TAG" >&2
  cat "$release_error" >&2
  exit 1
fi

is_draft="$(jq -r '.isDraft' "$release_json")"
is_immutable="$(jq -r '.isImmutable' "$release_json")"

if [[ "$is_draft" == "true" ]]; then
  gh release upload "$RELEASE_TAG" "${asset_paths[@]}" \
    --clobber \
    --repo "$GITHUB_REPOSITORY"
  exit 0
fi

if [[ "$is_immutable" != "true" ]]; then
  gh release upload "$RELEASE_TAG" "${asset_paths[@]}" \
    --clobber \
    --repo "$GITHUB_REPOSITORY"
  exit 0
fi

published_tag="$(jq -r '.tagName // empty' "$release_json")"
release_target="$(jq -r '.targetCommitish // empty' "$release_json")"
if [[ "$published_tag" != "$RELEASE_TAG" ]]; then
  printf 'immutable release tag mismatch: expected %s, found %s\n' \
    "$RELEASE_TAG" "${published_tag:-<missing>}" >&2
  exit 1
fi
if [[ "$release_target" != "$GITHUB_SHA" ]]; then
  printf 'immutable release target mismatch for %s: expected %s, found %s\n' \
    "$RELEASE_TAG" "$GITHUB_SHA" "${release_target:-<missing>}" >&2
  exit 1
fi

if ! tag_target="$(gh api \
  "repos/${GITHUB_REPOSITORY}/commits/${RELEASE_TAG}" \
  --jq '.sha')"; then
  printf 'maze release could not resolve the remote tag target for %s\n' \
    "$RELEASE_TAG" >&2
  exit 1
fi
if [[ "$tag_target" != "$GITHUB_SHA" ]]; then
  printf 'immutable release tag target mismatch for %s: expected %s, found %s\n' \
    "$RELEASE_TAG" "$GITHUB_SHA" "${tag_target:-<missing>}" >&2
  exit 1
fi

for path in "${asset_paths[@]}"; do
  basename "$path"
done | LC_ALL=C sort >"$expected_names"
jq -r '.assets[].name' "$release_json" | LC_ALL=C sort >"$published_names"

if ! cmp -s "$expected_names" "$published_names"; then
  printf 'immutable release asset set mismatch for %s; rebuilt and published assets differ:\n' \
    "$RELEASE_TAG" >&2
  diff -u "$expected_names" "$published_names" >&2 || true
  exit 1
fi

for path in "${asset_paths[@]}"; do
  name="$(basename "$path")"
  expected_digest="sha256:$(shasum -a 256 "$path" | awk '{print $1}')"
  published_digest="$(jq -r --arg name "$name" \
    '.assets[] | select(.name == $name) | .digest // empty' \
    "$release_json")"
  if [[ "$published_digest" != "$expected_digest" ]]; then
    printf 'immutable release asset digest mismatch for %s: expected %s, found %s\n' \
      "$name" "$expected_digest" "${published_digest:-<missing>}" >&2
    exit 1
  fi
done

printf 'Verified immutable release %s at %s with %d unchanged assets.\n' \
  "$RELEASE_TAG" "$GITHUB_SHA" "${#asset_paths[@]}"

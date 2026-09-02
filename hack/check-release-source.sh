#!/usr/bin/env bash
set -euo pipefail

release_tag=""

usage() {
  printf 'Usage: %s --tag vVERSION\n' "$0"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --tag)
      [[ $# -ge 2 ]] || { echo "error: --tag requires a value" >&2; exit 2; }
      release_tag="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      printf 'error: unknown argument: %s\n' "$1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ -z $release_tag ]]; then
  echo "error: --tag is required" >&2
  exit 2
fi

tag_pattern='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$'
if [[ ! $release_tag =~ $tag_pattern ]]; then
  printf 'error: release tag is not a supported SemVer tag: %s\n' "$release_tag" >&2
  exit 1
fi

major="${BASH_REMATCH[1]}"
minor="${BASH_REMATCH[2]}"

if [[ "$(git cat-file -t "refs/tags/$release_tag" 2>/dev/null || true)" != "tag" ]]; then
  printf 'error: release tag must be annotated: %s\n' "$release_tag" >&2
  exit 1
fi
tag_commit="$(git rev-parse --verify "refs/tags/$release_tag^{commit}")"

fetch_branch() {
  local branch="$1"
  local tracking_ref="refs/remotes/origin/$branch"

  git update-ref -d "$tracking_ref"
  git fetch --no-tags --force origin "+refs/heads/$branch:$tracking_ref"
}

if ! fetch_branch main; then
  echo "error: could not fetch required branch origin/main" >&2
  exit 1
fi

candidate_branches=(main "release/$major.$minor")
for branch in "${candidate_branches[@]}"; do
  if [[ $branch != main ]] && ! fetch_branch "$branch" >/dev/null 2>&1; then
    continue
  fi
  if git merge-base --is-ancestor "$tag_commit" "refs/remotes/origin/$branch"; then
    printf 'release source valid: %s is reachable from %s\n' "$release_tag" "$branch"
    exit 0
  fi
done

printf 'error: %s must be reachable from main or release/%s.%s\n' \
  "$release_tag" "$major" "$minor" >&2
exit 1

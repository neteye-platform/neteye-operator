#!/usr/bin/env bash
# Bump the neteye-operator release version across every synchronized file,
# regenerate the OLM bundle, and (optionally) create the release tag.
#
# Usage: hack/bump-version.sh <new-version> [--tag] [--repo-root PATH]
#
# <new-version> must be a SemVer version (e.g. 0.1.1 or 0.2.0-alpha.1).
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
new_version=""
create_tag=false

usage() {
  printf 'Usage: %s <new-version> [--tag] [--repo-root PATH]\n' "$0"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --tag)
      create_tag=true
      shift
      ;;
    --repo-root)
      [[ $# -ge 2 ]] || { echo "error: --repo-root requires a path" >&2; exit 2; }
      repo_root="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      if [[ -n $new_version ]]; then
        printf 'error: unexpected argument: %s\n' "$1" >&2
        usage >&2
        exit 2
      fi
      new_version="$1"
      shift
      ;;
  esac
done

if [[ -z $new_version ]]; then
  usage >&2
  exit 2
fi

semver_re='^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$'
if [[ ! $new_version =~ $semver_re ]]; then
  printf 'error: %s is not a valid SemVer version\n' "$new_version" >&2
  exit 1
fi

channel="stable"
[[ $new_version == *-* ]] && channel="alpha"

cd "$repo_root"

sed -i -E "s/^VERSION \?= .+$/VERSION ?= ${new_version}/" src/Makefile

make -C src bundle VERSION="${new_version}"

./hack/check-version-consistency.sh --repo-root "$repo_root"

echo "Bumped neteye-operator to ${new_version} (channel: ${channel})."

if [[ $create_tag == true ]]; then
  git add -A
  git commit -m "chore: bump version to ${new_version}"
  git tag -a "v${new_version}" -m "v${new_version}"
  echo "Created annotated tag v${new_version}. Push it with: git push origin main v${new_version}"
fi

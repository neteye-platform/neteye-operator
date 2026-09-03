#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
validator="$repo_root/hack/check-release-source.sh"
tmp_root="$(mktemp -d)"
trap 'rm -rf "$tmp_root"' EXIT

work=""
remote=""

new_repo() {
  local name="$1"
  remote="$tmp_root/$name.git"
  work="$tmp_root/$name"
  git init --bare --quiet "$remote"
  git init --quiet --initial-branch=main "$work"
  git -C "$work" config user.name "Release Test"
  git -C "$work" config user.email "release-test@example.com"
  git -C "$work" config commit.gpgsign false
  printf 'base\n' > "$work/content"
  git -C "$work" add content
  git -C "$work" commit --quiet -m base
  git -C "$work" remote add origin "$remote"
  git -C "$work" push --quiet --set-upstream origin main
}

commit_on_branch() {
  local branch="$1"
  local content="$2"
  git -C "$work" switch --quiet -c "$branch"
  printf '%s\n' "$content" >> "$work/content"
  git -C "$work" add content
  git -C "$work" commit --quiet -m "$content"
  git -C "$work" push --quiet --set-upstream origin "$branch"
}

expect_success() {
  local name="$1"
  local tag="$2"
  if output=$(cd "$work" && "$validator" --tag "$tag" 2>&1); then
    printf 'ok - %s\n' "$name"
  else
    printf 'not ok - %s\n%s\n' "$name" "$output" >&2
    return 1
  fi
}

expect_failure() {
  local name="$1"
  local tag="$2"
  local expected="$3"
  if output=$(cd "$work" && "$validator" --tag "$tag" 2>&1); then
    printf 'not ok - %s (unexpected success)\n' "$name" >&2
    return 1
  elif [[ $output == *"$expected"* ]]; then
    printf 'ok - %s\n' "$name"
  else
    printf 'not ok - %s (missing %q)\n%s\n' "$name" "$expected" "$output" >&2
    return 1
  fi
}

new_repo main-tag
git -C "$work" tag -a v1.2.0 -m v1.2.0
expect_success "annotated tag on main" v1.2.0

new_repo release-train
commit_on_branch release/1.2 patch
git -C "$work" tag -a v1.2.1 -m v1.2.1
expect_success "tag on matching release train" v1.2.1

new_repo prerelease-train
commit_on_branch release/1.2 candidate
git -C "$work" tag -a v1.2.3-rc1 -m v1.2.3-rc1
expect_success "prerelease tag uses core major and minor" v1.2.3-rc1

new_repo wrong-train
commit_on_branch release/1.3 wrong
git -C "$work" tag -a v1.2.3 -m v1.2.3
expect_failure "tag on wrong release train" v1.2.3 "must be reachable"

new_repo missing-train
git -C "$work" switch --quiet -c feature
printf 'feature\n' >> "$work/content"
git -C "$work" commit --quiet -am feature
git -C "$work" tag -a v1.2.4 -m v1.2.4
expect_failure "missing matching release train" v1.2.4 "must be reachable"

new_repo lightweight
git -C "$work" tag v1.2.5
expect_failure "lightweight tag" v1.2.5 "must be annotated"

new_repo multi-digit
commit_on_branch release/10.20 patch
git -C "$work" tag -a v10.20.1 -m v10.20.1
expect_success "multi-digit release train" v10.20.1

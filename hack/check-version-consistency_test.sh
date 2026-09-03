#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
validator="$repo_root/hack/check-version-consistency.sh"
tmp_root="$(mktemp -d)"
trap 'rm -rf "$tmp_root"' EXIT

fixture=""
new_fixture() {
  local version="${1:-0.1.0-alpha3}"
  local version_placeholder='$'
  version_placeholder+='(VERSION)'
  fixture="$(mktemp -d "$tmp_root/fixture.XXXXXX")"
  mkdir -p "$fixture/src/bundle/manifests" "$fixture/src/bundle/metadata" "$fixture/charts"

  printf 'IMG ?= ghcr.io/neteye-platform/neteye-operator:%s\nVERSION ?= %s\n' \
    "$version_placeholder" "$version" \
    > "$fixture/src/Makefile"
  printf 'apiVersion: v2\nname: neteye-operator\nversion: %s\n' "$version" \
    > "$fixture/charts/Chart.yaml"
  printf 'operator:\n  versionRange: ">=%s"\n  channel: %s\nnamespace:\n  operators: neteye-system\n' \
    "$version" "$([[ $version == *-* ]] && printf alpha || printf stable)" \
    > "$fixture/charts/values.yaml"
  printf 'annotations:\n  operators.operatorframework.io.bundle.version.v1: %s\n' "$version" \
    > "$fixture/src/bundle/metadata/annotations.yaml"
  printf 'apiVersion: operators.coreos.com/v1alpha1\nkind: ClusterServiceVersion\nmetadata:\n  name: neteye-operator.v%s\nspec:\n  install:\n    spec:\n      deployments:\n      - spec:\n          template:\n            spec:\n              containers:\n              - image: ghcr.io/neteye-platform/neteye-operator:%s\n  version: %s\n' \
    "$version" "$version" "$version" \
    > "$fixture/src/bundle/manifests/neteye-operator.v${version}.clusterserviceversion.yaml"
}

pass_count=0
fail_count=0

expect_success() {
  local name="$1"
  shift
  if output=$("$validator" --repo-root "$fixture" "$@" 2>&1); then
    printf 'ok - %s\n' "$name"
    pass_count=$((pass_count + 1))
  else
    printf 'not ok - %s\n%s\n' "$name" "$output" >&2
    fail_count=$((fail_count + 1))
  fi
}

expect_failure() {
  local name="$1"
  local expected="$2"
  shift 2
  if output=$("$validator" --repo-root "$fixture" "$@" 2>&1); then
    printf 'not ok - %s (unexpected success)\n' "$name" >&2
    fail_count=$((fail_count + 1))
  elif [[ $output == *"$expected"* ]]; then
    printf 'ok - %s\n' "$name"
    pass_count=$((pass_count + 1))
  else
    printf 'not ok - %s (missing %q)\n%s\n' "$name" "$expected" "$output" >&2
    fail_count=$((fail_count + 1))
  fi
}

new_fixture
expect_success "consistent metadata"
expect_success "matching release tag" --tag v0.1.0-alpha3

new_fixture
expect_failure "mismatched release tag" "release tag v0.1.0-alpha4 does not match" --tag v0.1.0-alpha4

new_fixture "01.2.3"
expect_failure "invalid release SemVer" "not a supported release SemVer"

new_fixture "1.2.3-01"
expect_failure "invalid numeric prerelease SemVer" "not a supported release SemVer"

new_fixture
sed -i 's/version: 0.1.0-alpha3$/version: 0.1.0-alpha2/' "$fixture/charts/Chart.yaml"
expect_failure "chart version mismatch" "charts/Chart.yaml version"

new_fixture
sed -i 's/bundle.version.v1: 0.1.0-alpha3/bundle.version.v1: 0.1.0-alpha2/' \
  "$fixture/src/bundle/metadata/annotations.yaml"
expect_failure "bundle annotation mismatch" "bundle metadata annotation"

new_fixture
sed -i 's/channel: alpha/channel: stable/' "$fixture/charts/values.yaml"
expect_failure "chart channel mismatch" "operator.channel"

new_fixture
sed -i 's/>=0.1.0-alpha3/>=0.1.0-alpha2/' "$fixture/charts/values.yaml"
expect_failure "chart version range mismatch" "versionRange"

new_fixture
sed -i 's/neteye-operator:0.1.0-alpha3/neteye-operator:0.1.0-alpha2/' \
  "$fixture/src/bundle/manifests/"*.clusterserviceversion.yaml
expect_failure "operator image mismatch" "bundle CSV operator image"

new_fixture
cp "$fixture/src/bundle/manifests/"*.clusterserviceversion.yaml \
  "$fixture/src/bundle/manifests/neteye-operator.v0.1.0-alpha2.clusterserviceversion.yaml"
expect_failure "multiple bundle CSV files" "expected exactly one bundle CSV"

printf '%d checks passed; %d failed\n' "$pass_count" "$fail_count"
(( fail_count == 0 ))

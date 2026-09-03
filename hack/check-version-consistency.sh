#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
release_tag=""

usage() {
  printf 'Usage: %s [--repo-root PATH] [--tag vVERSION]\n' "$0"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --repo-root)
      [[ $# -ge 2 ]] || { echo "error: --repo-root requires a path" >&2; exit 2; }
      repo_root="$2"
      shift 2
      ;;
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

cd "$repo_root"

required_files=(
  src/Makefile
  charts/Chart.yaml
  charts/values.yaml
  src/bundle/metadata/annotations.yaml
)
for path in "${required_files[@]}"; do
  if [[ ! -f $path ]]; then
    printf 'error: required release metadata file is missing: %s\n' "$path" >&2
    exit 1
  fi
done

mapfile -t mk_versions < <(sed -nE 's/^VERSION \?= (.+)$/\1/p' src/Makefile)
mapfile -t chart_versions < <(sed -nE 's/^version: (.+)$/\1/p' charts/Chart.yaml)
mapfile -t image_templates < <(sed -nE 's/^IMG \?= (.+)$/\1/p' src/Makefile)
mapfile -t chart_version_ranges < <(
  sed -n '/^operator:/,/^[^ ]/{s/^  versionRange: "\(.*\)"$/\1/p;}' charts/values.yaml
)
mapfile -t chart_channels < <(
  sed -n '/^operator:/,/^[^ ]/{s/^  channel: \(.*\)$/\1/p;}' charts/values.yaml
)
mapfile -t annotation_versions < <(
  sed -nE 's/^  operators\.operatorframework\.io\.bundle\.version\.v1: (.+)$/\1/p' \
    src/bundle/metadata/annotations.yaml
)

shopt -s nullglob
csv_files=(src/bundle/manifests/neteye-operator.v*.clusterserviceversion.yaml)
shopt -u nullglob

errors=0
value=""

require_single_value() {
  local label="$1"
  local count="$2"
  local first_value="$3"

  if [[ $count -ne 1 || -z $first_value ]]; then
    printf 'error: expected exactly one %s, found %d\n' "$label" "$count" >&2
    errors=$((errors + 1))
    value=""
    return
  fi

  value="$first_value"
}

require_single_value "src/Makefile VERSION" "${#mk_versions[@]}" "${mk_versions[0]-}"
mk_version="$value"
require_single_value "charts/Chart.yaml version" "${#chart_versions[@]}" "${chart_versions[0]-}"
chart_version="$value"
require_single_value "src/Makefile IMG" "${#image_templates[@]}" "${image_templates[0]-}"
image_template="$value"
require_single_value "charts/values.yaml operator.versionRange" "${#chart_version_ranges[@]}" "${chart_version_ranges[0]-}"
chart_version_range="$value"
require_single_value "charts/values.yaml operator.channel" "${#chart_channels[@]}" "${chart_channels[0]-}"
chart_channel="$value"
require_single_value "bundle metadata version annotation" "${#annotation_versions[@]}" "${annotation_versions[0]-}"
annotation_version="$value"

if [[ ${#csv_files[@]} -ne 1 ]]; then
  printf 'error: expected exactly one bundle CSV, found %d\n' "${#csv_files[@]}" >&2
  errors=$((errors + 1))
  csv_version=""
  csv_name_version=""
  csv_spec_version=""
  csv_operator_image=""
else
  csv_file="${csv_files[0]}"
  csv_basename="$(basename "$csv_file")"
  csv_version="${csv_basename#neteye-operator.v}"
  csv_version="${csv_version%.clusterserviceversion.yaml}"

  mapfile -t csv_name_versions < <(sed -nE 's/^  name: neteye-operator\.v(.+)$/\1/p' "$csv_file")
  mapfile -t csv_spec_versions < <(sed -nE 's/^  version: (.+)$/\1/p' "$csv_file")
  mapfile -t csv_operator_images < <(
    sed -nE 's/^[[:space:]]+(- )?image: (ghcr\.io\/neteye-platform\/neteye-operator:.+)$/\2/p' "$csv_file"
  )

  require_single_value "bundle CSV metadata.name version" "${#csv_name_versions[@]}" "${csv_name_versions[0]-}"
  csv_name_version="$value"
  require_single_value "bundle CSV spec.version" "${#csv_spec_versions[@]}" "${csv_spec_versions[0]-}"
  csv_spec_version="$value"
  require_single_value "bundle CSV operator image" "${#csv_operator_images[@]}" "${csv_operator_images[0]-}"
  csv_operator_image="$value"
fi

semver_re='^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$'
valid_semver=true
if [[ -n $mk_version && ! $mk_version =~ $semver_re ]]; then
  valid_semver=false
elif [[ $mk_version == *-* ]]; then
  prerelease="${mk_version#*-}"
  IFS='.' read -r -a prerelease_identifiers <<< "$prerelease"
  for identifier in "${prerelease_identifiers[@]}"; do
    if [[ $identifier =~ ^[0-9]+$ && $identifier == 0[0-9]* ]]; then
      valid_semver=false
      break
    fi
  done
fi
if [[ -n $mk_version && $valid_semver == false ]]; then
  printf 'error: src/Makefile VERSION is not a supported release SemVer: %s\n' "$mk_version" >&2
  errors=$((errors + 1))
fi

expected_operator_image=""
if [[ -n $image_template && -n $mk_version ]]; then
  version_placeholder='$'
  version_placeholder+='(VERSION)'
  expected_operator_image="${image_template//$version_placeholder/$mk_version}"
fi

compare_version() {
  local label="$1"
  local actual="$2"

  if [[ -n $mk_version && $actual != "$mk_version" ]]; then
    printf 'error: %-34s = %s (expected %s)\n' "$label" "${actual:-<missing>}" "$mk_version" >&2
    errors=$((errors + 1))
  fi
}

compare_version "charts/Chart.yaml version" "$chart_version"
compare_version "bundle CSV filename" "$csv_version"
compare_version "bundle CSV metadata.name" "$csv_name_version"
compare_version "bundle CSV spec.version" "$csv_spec_version"
compare_version "bundle metadata annotation" "$annotation_version"

if [[ -n $mk_version ]]; then
  expected_chart_channel="stable"
  [[ $mk_version == *-* ]] && expected_chart_channel="alpha"
  expected_chart_version_range=">=$mk_version"

  if [[ $chart_channel != "$expected_chart_channel" ]]; then
    printf 'error: charts/values.yaml operator.channel = %s (expected %s)\n' \
      "${chart_channel:-<missing>}" "$expected_chart_channel" >&2
    errors=$((errors + 1))
  fi
  if [[ $chart_version_range != "$expected_chart_version_range" ]]; then
    printf 'error: charts/values.yaml versionRange     = %s (expected %s)\n' \
      "${chart_version_range:-<missing>}" "$expected_chart_version_range" >&2
    errors=$((errors + 1))
  fi
fi

if [[ -n $expected_operator_image && $csv_operator_image != "$expected_operator_image" ]]; then
  printf 'error: bundle CSV operator image          = %s (expected %s)\n' \
    "${csv_operator_image:-<missing>}" "$expected_operator_image" >&2
  errors=$((errors + 1))
fi

if [[ -n $release_tag ]]; then
  if [[ $release_tag != "v${mk_version}" ]]; then
    printf 'error: release tag %s does not match src/Makefile VERSION %s (expected v%s)\n' \
      "$release_tag" "$mk_version" "$mk_version" >&2
    errors=$((errors + 1))
  fi
fi

if (( errors > 0 )); then
  printf 'error: release metadata validation failed with %d error(s)\n' "$errors" >&2
  exit 1
fi

if [[ -n $release_tag ]]; then
  printf 'release metadata consistent: %s (%s)\n' "$mk_version" "$release_tag"
else
  printf 'release metadata consistent: %s\n' "$mk_version"
fi

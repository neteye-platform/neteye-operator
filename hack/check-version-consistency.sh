#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

mk_version="$(sed -n 's/^VERSION ?= //p' src/Makefile)"
chart_version="$(sed -n 's/^version: //p' charts/Chart.yaml)"

shopt -s nullglob
csv_files=(src/bundle/manifests/neteye-operator.v*.clusterserviceversion.yaml)
shopt -u nullglob
if [[ ${#csv_files[@]} -ne 1 ]]; then
  printf 'error: expected exactly one bundle CSV, found %d\n' "${#csv_files[@]}" >&2
  exit 1
fi
csv_version="$(basename "${csv_files[0]}" | sed -E 's/^neteye-operator\.v(.*)\.clusterserviceversion\.yaml$/\1/')"

if [[ -z $mk_version || -z $chart_version ]]; then
  echo "error: could not read version from src/Makefile or charts/Chart.yaml" >&2
  exit 1
fi

if [[ $mk_version == "$chart_version" && $mk_version == "$csv_version" ]]; then
  echo "version consistent: $mk_version"
else
  {
    echo "error: operator version mismatch:"
    echo "  src/Makefile VERSION      = $mk_version"
    echo "  charts/Chart.yaml version = $chart_version"
    echo "  bundle CSV filename       = $csv_version"
    echo "Bump all three together: edit VERSION and charts/Chart.yaml, then run 'make bundle'."
  } >&2
  exit 1
fi

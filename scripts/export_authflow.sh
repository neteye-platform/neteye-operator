#!/bin/bash
#
# export-keycloak-flow.sh
#
# Recursively exports a Keycloak authentication flow, including all
# nested subflows, from the Admin REST API.
#
# Usage:
#   ./export-keycloak-flow.sh <top-level-flow-alias>
#
# Example:
#   ./export-keycloak-flow.sh neteye-idp-discovery-flow
#
# Requires: curl, jq
#
# Before running, fill in the settings below.

set -euo pipefail

# ---------------------------------------------------------------------------
# Settings - edit these for your environment
# ---------------------------------------------------------------------------
KEYCLOAK_URL="https://rdneteye.si.wp.lan/auth"     # base URL, no trailing slash
REALM="master"                       # realm that contains the flow
ADMIN_USER="neteye-internal-keycloak-admin"
ADMIN_PASSWORD="orSM7tpBz3o2KTcmVQdiKWcvuJY0YUz2"
OUTPUT_DIR="./flow-export"           # where the JSON files will be saved

# ---------------------------------------------------------------------------
# Arguments
# ---------------------------------------------------------------------------
TOP_LEVEL_FLOW_ALIAS="${1:-}"

if [[ -z "$TOP_LEVEL_FLOW_ALIAS" ]]; then
  echo "Usage: $0 <top-level-flow-alias>"
  exit 1
fi

mkdir -p "$OUTPUT_DIR"

# ---------------------------------------------------------------------------
# Step 1: authenticate and get an access token
# ---------------------------------------------------------------------------
echo "Logging in to Keycloak as $ADMIN_USER ..."

TOKEN=$(curl -s -X POST \
  "$KEYCLOAK_URL/realms/master/protocol/openid-connect/token" \
  -d "client_id=admin-cli" \
  -d "username=$ADMIN_USER" \
  -d "password=$ADMIN_PASSWORD" \
  -d "grant_type=password" \
  | jq -r '.access_token')

if [[ "$TOKEN" == "null" || -z "$TOKEN" ]]; then
  echo "Login failed - check ADMIN_USER / ADMIN_PASSWORD / KEYCLOAK_URL."
  exit 1
fi

# ---------------------------------------------------------------------------
# Step 2: recursive function to fetch a flow and all its subflows
# ---------------------------------------------------------------------------
FLOWS_API="$KEYCLOAK_URL/admin/realms/$REALM/authentication/flows"

# keep track of flows we already fetched, to avoid loops/duplicates
declare -A already_fetched

fetch_flow() {
  local flow_alias="$1"

  # skip if we already did this one
  if [[ -n "${already_fetched[$flow_alias]:-}" ]]; then
    return
  fi
  already_fetched["$flow_alias"]=1

  # turn the alias into a safe file name
  local safe_name
  safe_name=$(echo "$flow_alias" | tr ' /' '_')
  local output_file="$OUTPUT_DIR/flow_${safe_name}.json"

  echo "Fetching flow: $flow_alias"

  curl -s -X GET \
    "$FLOWS_API/$flow_alias/executions" \
    -H "Authorization: Bearer $TOKEN" \
    -o "$output_file"

  # find any nested subflows (authenticatorFlow == true) and recurse into them
  local subflow_aliases
  subflow_aliases=$(jq -r '.[] | select(.authenticatorFlow==true) | .flowAlias' "$output_file")

  for subflow_alias in $subflow_aliases; do
    fetch_flow "$subflow_alias"
  done
}

# ---------------------------------------------------------------------------
# Step 3: run it, starting from the top-level flow
# ---------------------------------------------------------------------------
fetch_flow "$TOP_LEVEL_FLOW_ALIAS"

echo ""
echo "Done. Exported flow files are in: $OUTPUT_DIR"
ls -1 "$OUTPUT_DIR"
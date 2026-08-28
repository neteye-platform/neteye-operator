#!/bin/bash
#
# export-keycloak-realm.sh
#
# Exports a Keycloak realm from the Admin REST API.
#
# Usage:
#   ./export-keycloak-realm.sh <realm>
#
# Example:
#   ./export-keycloak-realm.sh master
#
# Requires: curl, jq
#
# Before running, fill in the settings below.

set -euo pipefail

# ---------------------------------------------------------------------------
# Settings - edit these for your environment
# ---------------------------------------------------------------------------
KEYCLOAK_URL="https://rdneteye.si.wp.lan/auth"     # base URL, no trailing slash
ADMIN_USER="neteye-internal-keycloak-admin"
ADMIN_PASSWORD="orSM7tpBz3o2KTcmVQdiKWcvuJY0YUz2"
OUTPUT_DIR="./realm-export"                         # where the JSON files will be saved

# ---------------------------------------------------------------------------
# Arguments
# ---------------------------------------------------------------------------
REALM="${1:-}"

if [[ -z "$REALM" ]]; then
  echo "Usage: $0 <realm>"
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
# Step 2: fetch the realm
# ---------------------------------------------------------------------------
REALM_API="$KEYCLOAK_URL/admin/realms/$REALM"

OUTPUT_FILE="$OUTPUT_DIR/realm_${REALM}.json"

echo "Fetching realm: $REALM"

curl -s -X GET \
  "$REALM_API" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Accept: application/json" \
  -o "$OUTPUT_FILE"

# ---------------------------------------------------------------------------
# Step 3: validate and pretty-print the result
# ---------------------------------------------------------------------------
if ! jq empty "$OUTPUT_FILE" 2>/dev/null; then
  echo "Failed to retrieve a valid JSON representation of realm: $REALM"
  rm -f "$OUTPUT_FILE"
  exit 1
fi

TMP_FILE="${OUTPUT_FILE}.tmp"

jq '.' "$OUTPUT_FILE" > "$TMP_FILE"
mv "$TMP_FILE" "$OUTPUT_FILE"

echo ""
echo "Done. Realm exported to:"
echo "  $OUTPUT_FILE"
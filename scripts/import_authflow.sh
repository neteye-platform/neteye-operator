#!/bin/bash
#
# import-keycloak-flow.sh
#
# Import a Keycloak authentication flow tree from a flattened JSON export.
#
# Export format:
#
#   GET /admin/realms/{realm}/authentication/flows/{alias}/executions
#
# The export contains executions and subflows in a flattened array.
# "level" describes the nesting depth and "index" describes sibling order.
#
# Requires:
#   curl
#   jq
#
# Usage:
#
#   ./import-keycloak-flow.sh <export.json> <new-top-level-flow-alias> [description]
#
# Example:
#
#   ./import-keycloak-flow.sh \
#       flow_neteye-idp-discovery-flow.json \
#       neteye-idp-discovery-flow2-import \
#       "Login with binded idp"
#

set -euo pipefail

# ---------------------------------------------------------------------------
# Settings
# ---------------------------------------------------------------------------

KEYCLOAK_URL="https://rdneteye.si.wp.lan/auth"     # base URL, no trailing slash
REALM="master"                       # realm that contains the flow
ADMIN_USER="neteye-internal-keycloak-admin"
ADMIN_PASSWORD="orSM7tpBz3o2KTcmVQdiKWcvuJY0YUz2"

API="$KEYCLOAK_URL/admin/realms/$REALM/authentication"

# ---------------------------------------------------------------------------
# Arguments
# ---------------------------------------------------------------------------

EXPORT_FILE="${1:-}"
TOP_FLOW_ALIAS="${2:-}"
TOP_FLOW_DESCRIPTION="${3:-}"

if [[ -z "$EXPORT_FILE" || -z "$TOP_FLOW_ALIAS" ]]; then
    echo "Usage: $0 <export.json> <new-top-level-flow-alias> [description]"
    exit 1
fi

if [[ ! -f "$EXPORT_FILE" ]]; then
    echo "File not found: $EXPORT_FILE"
    exit 1
fi

# ---------------------------------------------------------------------------
# Basic JSON validation
# ---------------------------------------------------------------------------

if ! jq -e 'type == "array"' "$EXPORT_FILE" >/dev/null 2>&1; then
    echo "ERROR: $EXPORT_FILE is not a valid JSON array."
    exit 1
fi

# ---------------------------------------------------------------------------
# Step 1: Login
# ---------------------------------------------------------------------------

echo "Logging in to Keycloak as $ADMIN_USER ..."

TOKEN=$(
    curl -sS -X POST \
        "$KEYCLOAK_URL/realms/master/protocol/openid-connect/token" \
        -d "client_id=admin-cli" \
        -d "username=$ADMIN_USER" \
        -d "password=$ADMIN_PASSWORD" \
        -d "grant_type=password" |
        jq -r '.access_token // empty'
)

if [[ -z "$TOKEN" ]]; then
    echo "Login failed - check ADMIN_USER / ADMIN_PASSWORD / KEYCLOAK_URL."
    exit 1
fi

AUTH_HEADER="Authorization: Bearer $TOKEN"

# ---------------------------------------------------------------------------
# Helper: URL encode a path segment
# ---------------------------------------------------------------------------

urlencode() {
    jq -rn --arg s "$1" '$s | @uri'
}

# ---------------------------------------------------------------------------
# Helper: get all flows
# ---------------------------------------------------------------------------

get_all_flows() {
    curl -sS -X GET \
        "$API/flows" \
        -H "$AUTH_HEADER"
}

# ---------------------------------------------------------------------------
# Helper: find flow ID by alias
# ---------------------------------------------------------------------------

get_flow_id_by_alias() {
    local alias="$1"

    get_all_flows |
        jq -r --arg alias "$alias" '
            .[]
            | select(.alias == $alias)
            | .id
        ' |
        head -n1
}

# ---------------------------------------------------------------------------
# Helper: delete a flow if it exists
#
# Deleting a top-level flow also removes its child executions/subflows.
# ---------------------------------------------------------------------------

delete_flow_if_exists() {
    local alias="$1"
    local flow_id

    [[ -z "$alias" ]] && return 0

    flow_id=$(get_flow_id_by_alias "$alias")

    if [[ -n "$flow_id" && "$flow_id" != "null" ]]; then
        echo "Deleting existing flow '$alias' (id $flow_id) ..."

        local http_code
        http_code=$(
            curl -sS -o /dev/null -w "%{http_code}" \
                -X DELETE \
                "$API/flows/$flow_id" \
                -H "$AUTH_HEADER"
        )

        if [[ "$http_code" -lt 200 || "$http_code" -ge 300 ]]; then
            echo "ERROR deleting flow '$alias' (HTTP $http_code)"
            exit 1
        fi
    fi
}

# ---------------------------------------------------------------------------
# Determine the original top-level alias from the export.
#
# Subflow names produced by Keycloak's Duplicate functionality commonly look
# like:
#
#   neteye-idp-discovery-flow2 Block LDAP logins
#   neteye-idp-discovery-flow2 username-password
#   neteye-idp-discovery-flow2 adfs
#
# We need to replace that original prefix with TOP_FLOW_ALIAS.
#
# We first try to derive the original prefix from level-0 subflows/executions.
# The caller's new TOP_FLOW_ALIAS is intentionally NOT used as the source
# prefix.
# ---------------------------------------------------------------------------

detect_original_flow_alias() {
    local candidate

    # Look for a level-0 item whose displayName looks like:
    #
    #   "<something> <suffix>"
    #
    # and whose alias/displayName is available.
    #
    # For this export format, the most reliable source is a level-0
    # authenticationFlow item with an alias.
    candidate=$(
        jq -r '
            .[]
            | select((.level // -1) == 0)
            | select(.authenticationFlow == true)
            | (.alias // .displayName // empty)
        ' "$EXPORT_FILE" |
        head -n1
    )

    if [[ -n "$candidate" ]]; then
        echo "$candidate"
        return 0
    fi

    # Fallback: inspect level-0 names. If no subflow is present there is
    # nothing to map and aliases can be preserved as-is.
    echo ""
}

ORIGINAL_FLOW_ALIAS=$(detect_original_flow_alias)

# ---------------------------------------------------------------------------
# Helper: convert an exported subflow alias/name to the new imported alias.
#
# IMPORTANT:
#
# Old behavior:
#
#   neteye-idp-discovery-flow2 Block LDAP logins
#       ->
#   Block LDAP logins
#
# New behavior:
#
#   neteye-idp-discovery-flow2 Block LDAP logins
#       ->
#   <NEW_TOP_FLOW_ALIAS> Block LDAP logins
#
# This prevents the accidental loss of the parent-flow prefix.
# ---------------------------------------------------------------------------

build_imported_subflow_alias() {
    local exported_alias="$1"
    local exported_display_name="$2"

    local value="$exported_alias"

    if [[ -z "$value" ]]; then
        value="$exported_display_name"
    fi

    # If the exported name starts with the original top-level flow alias,
    # replace ONLY that prefix.
    if [[ -n "$ORIGINAL_FLOW_ALIAS" &&
          "$value" == "$ORIGINAL_FLOW_ALIAS "* ]]; then

        local suffix="${value#"$ORIGINAL_FLOW_ALIAS "}"

        echo "$TOP_FLOW_ALIAS $suffix"
        return 0
    fi

    # Some Keycloak exports don't preserve the alias but do preserve the
    # prefixed displayName.
    if [[ -n "$ORIGINAL_FLOW_ALIAS" &&
          "$exported_display_name" == "$ORIGINAL_FLOW_ALIAS "* ]]; then

        local suffix="${exported_display_name#"$ORIGINAL_FLOW_ALIAS "}"

        echo "$TOP_FLOW_ALIAS $suffix"
        return 0
    fi

    # If no original prefix can be identified, keep the exported alias/name.
    echo "$value"
}

# ---------------------------------------------------------------------------
# Helper: get execution/subflow ID by displayName.
#
# We use the newest matching item because the item was just created.
# ---------------------------------------------------------------------------

get_item_id() {
    local flow_alias="$1"
    local display_name="$2"

    local encoded_alias
    encoded_alias=$(urlencode "$flow_alias")

    curl -sS -X GET \
        "$API/flows/$encoded_alias/executions" \
        -H "$AUTH_HEADER" |
        jq -r --arg name "$display_name" '
            [
                .[]
                | select(.displayName == $name)
            ]
            | last
            | .id // empty
        '
}

# ---------------------------------------------------------------------------
# Helper: set execution requirement and alias.
# ---------------------------------------------------------------------------

set_requirement() {
    local flow_alias="$1"
    local item_id="$2"
    local requirement="$3"
    local alias="${4:-}"

    if [[ -z "$item_id" ]]; then
        echo "ERROR: Could not find newly-created execution."
        echo "       Flow: $flow_alias"
        echo "       Requirement: $requirement"
        exit 1
    fi

    local body

    if [[ -n "$alias" ]]; then
        body=$(
            jq -n \
                --arg id "$item_id" \
                --arg req "$requirement" \
                --arg alias "$alias" \
                '{
                    id: $id,
                    requirement: $req,
                    alias: $alias
                }'
        )
    else
        body=$(
            jq -n \
                --arg id "$item_id" \
                --arg req "$requirement" \
                '{
                    id: $id,
                    requirement: $req
                }'
        )
    fi

    local encoded_alias
    encoded_alias=$(urlencode "$flow_alias")

    local http_code response_body

    response_body=$(
        curl -sS \
            -w "\n%{http_code}" \
            -X PUT \
            "$API/flows/$encoded_alias/executions" \
            -H "$AUTH_HEADER" \
            -H "Content-Type: application/json" \
            -d "$body"
    )

    http_code=$(echo "$response_body" | tail -n1)
    response_body=$(echo "$response_body" | sed '$d')

    if [[ "$http_code" -lt 200 || "$http_code" -ge 300 ]]; then
        echo "ERROR setting requirement for '$item_id' (HTTP $http_code)"
        echo "Response: $response_body"
        exit 1
    fi
}

# ---------------------------------------------------------------------------
# Helper: create a flow.
# ---------------------------------------------------------------------------

create_flow() {
    local alias="$1"
    local description="$2"
    local top_level="$3"

    local body
    body=$(
        jq -n \
            --arg alias "$alias" \
            --arg desc "$description" \
            --argjson top "$top_level" \
            '{
                alias: $alias,
                description: $desc,
                providerId: "basic-flow",
                topLevel: $top,
                builtIn: false
            }'
    )

    local http_code response_body

    response_body=$(
        curl -sS \
            -w "\n%{http_code}" \
            -X POST \
            "$API/flows" \
            -H "$AUTH_HEADER" \
            -H "Content-Type: application/json" \
            -d "$body"
    )

    http_code=$(echo "$response_body" | tail -n1)
    response_body=$(echo "$response_body" | sed '$d')

    if [[ "$http_code" -lt 200 || "$http_code" -ge 300 ]]; then
        echo "ERROR creating flow '$alias' (HTTP $http_code)"
        echo "Response: $response_body"
        exit 1
    fi
}

# ---------------------------------------------------------------------------
# Helper: add plain execution.
# ---------------------------------------------------------------------------

add_execution() {
    local flow_alias="$1"
    local provider_id="$2"

    local encoded_alias
    encoded_alias=$(urlencode "$flow_alias")

    local body
    body=$(
        jq -n \
            --arg provider "$provider_id" \
            '{
                provider: $provider
            }'
    )

    local http_code response_body

    response_body=$(
        curl -sS \
            -w "\n%{http_code}" \
            -X POST \
            "$API/flows/$encoded_alias/executions/execution" \
            -H "$AUTH_HEADER" \
            -H "Content-Type: application/json" \
            -d "$body"
    )

    http_code=$(echo "$response_body" | tail -n1)
    response_body=$(echo "$response_body" | sed '$d')

    if [[ "$http_code" -lt 200 || "$http_code" -ge 300 ]]; then
        echo "ERROR creating execution '$provider_id' under '$flow_alias' (HTTP $http_code)"
        echo "Response: $response_body"
        exit 1
    fi
}

# ---------------------------------------------------------------------------
# Helper: add subflow.
# ---------------------------------------------------------------------------

add_subflow() {
    local parent_flow_alias="$1"
    local subflow_alias="$2"
    local subflow_description="$3"

    local encoded_parent_alias
    encoded_parent_alias=$(urlencode "$parent_flow_alias")

    local body
    body=$(
        jq -n \
            --arg alias "$subflow_alias" \
            --arg desc "$subflow_description" \
            '{
                alias: $alias,
                description: $desc,
                provider: "basic-flow",
                type: "basic-flow"
            }'
    )

    local http_code response_body

    response_body=$(
        curl -sS \
            -w "\n%{http_code}" \
            -X POST \
            "$API/flows/$encoded_parent_alias/executions/flow" \
            -H "$AUTH_HEADER" \
            -H "Content-Type: application/json" \
            -d "$body"
    )

    http_code=$(echo "$response_body" | tail -n1)
    response_body=$(echo "$response_body" | sed '$d')

    if [[ "$http_code" -lt 200 || "$http_code" -ge 300 ]]; then
        echo "ERROR creating subflow '$subflow_alias' under '$parent_flow_alias' (HTTP $http_code)"
        echo "Response: $response_body"
        echo ""
        echo "This usually means the alias already exists somewhere in the realm."
        exit 1
    fi
}

# ---------------------------------------------------------------------------
# Helper: delete possible aliases left behind by an older version of this
# script.
#
# The old script stripped:
#
#   <original-top> Block LDAP logins
#
# into:
#
#   Block LDAP logins
#
# So we clean that legacy alias too, but ONLY when it can be derived from
# the exported data.
# ---------------------------------------------------------------------------

cleanup_legacy_aliases() {
    local item exported_alias exported_display_name legacy_alias imported_alias

    while IFS= read -r item; do
        exported_alias=$(echo "$item" | jq -r '.alias // empty')
        exported_display_name=$(echo "$item" | jq -r '.displayName // empty')

        # Only authenticationFlow items are actual flow aliases.
        if [[ "$(echo "$item" | jq -r '.authenticationFlow // false')" != "true" ]]; then
            continue
        fi

        imported_alias=$(
            build_imported_subflow_alias \
                "$exported_alias" \
                "$exported_display_name"
        )

        # Remove the correctly mapped alias if a previous failed import left
        # it behind.
        if [[ -n "$imported_alias" && "$imported_alias" != "$TOP_FLOW_ALIAS" ]]; then
            delete_flow_if_exists "$imported_alias"
        fi

        # Remove the old broken alias produced by the previous script:
        #
        #   neteye-idp-discovery-flow2 Block LDAP logins
        #
        # became:
        #
        #   Block LDAP logins
        #
        local source_value="$exported_alias"

        if [[ -z "$source_value" ]]; then
            source_value="$exported_display_name"
        fi

        if [[ -n "$ORIGINAL_FLOW_ALIAS" &&
              "$source_value" == "$ORIGINAL_FLOW_ALIAS "* ]]; then

            legacy_alias="${source_value#"$ORIGINAL_FLOW_ALIAS "}"

            if [[ -n "$legacy_alias" ]]; then
                delete_flow_if_exists "$legacy_alias"
            fi
        fi

    done < <(jq -c '.[]' "$EXPORT_FILE")
}

# ---------------------------------------------------------------------------
# Build the tree.
#
# parent_stack[N] = alias of the flow that owns items at level N.
#
# Example:
#
#   level 0  Cookie
#   level 0  Block LDAP logins       <-- subflow
#   level 1    Condition
#   level 1    Deny access
#   level 0  username-password       <-- subflow
#   level 1    Username Password Form
#
# becomes:
#
#   TOP
#   ├── Cookie
#   ├── Block LDAP logins
#   │   ├── Condition
#   │   └── Deny access
#   └── username-password
#       └── Username Password Form
# ---------------------------------------------------------------------------

build_tree() {
    local total
    total=$(jq 'length' "$EXPORT_FILE")

    declare -A parent_stack

    parent_stack[0]="$TOP_FLOW_ALIAS"

    for ((i = 0; i < total; i++)); do

        local item
        item=$(jq --argjson idx "$i" '.[$idx]' "$EXPORT_FILE")

        local level
        level=$(echo "$item" | jq -r '.level // 0')

        local display_name
        display_name=$(echo "$item" | jq -r '.displayName // empty')

        local requirement
        requirement=$(echo "$item" | jq -r '.requirement // empty')

        local alias
        alias=$(echo "$item" | jq -r '.alias // empty')

        local provider_id
        provider_id=$(echo "$item" | jq -r '.providerId // empty')

        local description
        description=$(echo "$item" | jq -r '.description // empty')

        local is_flow
        is_flow=$(echo "$item" | jq -r '.authenticationFlow // false')

        local parent_flow_alias="${parent_stack[$level]:-}"

        if [[ -z "$parent_flow_alias" ]]; then
            echo "ERROR: No parent flow found for:"
            echo "$item"
            echo ""
            echo "level=$level"
            echo "parent_stack contents:"

            for key in "${!parent_stack[@]}"; do
                echo "  [$key] = ${parent_stack[$key]}"
            done

            exit 1
        fi

        # -------------------------------------------------------------------
        # Subflow
        # -------------------------------------------------------------------

        if [[ "$is_flow" == "true" ]]; then

            local subflow_alias

            subflow_alias=$(
                build_imported_subflow_alias \
                    "$alias" \
                    "$display_name"
            )

            echo ""
            echo "  $(printf '%*s' "$level" '')[subflow]"
            echo "    Exported displayName : $display_name"
            echo "    Exported alias       : ${alias:-<none>}"
            echo "    Imported alias      : $subflow_alias"
            echo "    Parent               : $parent_flow_alias"
            echo "    Requirement          : $requirement"

            add_subflow \
                "$parent_flow_alias" \
                "$subflow_alias" \
                "$description"

            # The POST endpoint doesn't reliably return the new execution ID,
            # so look it up again.
            local subflow_id

            subflow_id=$(
                get_item_id \
                    "$parent_flow_alias" \
                    "$display_name"
            )

            # If Keycloak reports the imported alias as displayName instead of
            # the original displayName, try the imported alias too.
            if [[ -z "$subflow_id" ]]; then
                subflow_id=$(
                    get_item_id \
                        "$parent_flow_alias" \
                        "$subflow_alias"
                )
            fi

            if [[ -z "$subflow_id" ]]; then
                echo "ERROR: Could not find created subflow '$subflow_alias'."
                exit 1
            fi

            set_requirement \
                "$parent_flow_alias" \
                "$subflow_id" \
                "$requirement"

            # This subflow becomes the parent for level+1.
            parent_stack[$((level + 1))]="$subflow_alias"

            # Clear deeper stack entries. Otherwise a later sibling could
            # accidentally inherit an old parent.
            local deeper_level
            for deeper_level in "${!parent_stack[@]}"; do
                if (( deeper_level > level + 1 )); then
                    unset 'parent_stack[$deeper_level]'
                fi
            done

        # -------------------------------------------------------------------
        # Plain authenticator execution
        # -------------------------------------------------------------------

        else

            echo ""
            echo "  $(printf '%*s' "$level" '')[execution]"
            echo "    Display name          : $display_name"
            echo "    Provider              : $provider_id"
            echo "    Parent                : $parent_flow_alias"
            echo "    Requirement           : $requirement"
            echo "    Alias                 : ${alias:-<none>}"

            if [[ -z "$provider_id" ]]; then
                echo "ERROR: Execution '$display_name' has no providerId."
                exit 1
            fi

            add_execution \
                "$parent_flow_alias" \
                "$provider_id"

            local exec_id

            exec_id=$(
                get_item_id \
                    "$parent_flow_alias" \
                    "$display_name"
            )

            if [[ -z "$exec_id" ]]; then
                echo "ERROR: Could not find created execution '$display_name'."
                exit 1
            fi

            set_requirement \
                "$parent_flow_alias" \
                "$exec_id" \
                "$requirement" \
                "$alias"
        fi
    done
}

# ---------------------------------------------------------------------------
# Show what we're about to do
# ---------------------------------------------------------------------------

echo ""
echo "============================================================"
echo " Keycloak Authentication Flow Import"
echo "============================================================"
echo "Export file       : $EXPORT_FILE"
echo "Realm             : $REALM"
echo "New top-level     : $TOP_FLOW_ALIAS"
echo "Original flow     : ${ORIGINAL_FLOW_ALIAS:-<not detected>}"
echo "Description       : ${TOP_FLOW_DESCRIPTION:-<empty>}"
echo "============================================================"
echo ""

# ---------------------------------------------------------------------------
# Cleanup
#
# First remove an existing top-level flow with the requested destination
# alias. This also removes its child executions/subflows.
# ---------------------------------------------------------------------------

echo "Checking for existing destination flow ..."

delete_flow_if_exists "$TOP_FLOW_ALIAS"

# ---------------------------------------------------------------------------
# Clean aliases created by the older broken version.
#
# This is important if you previously ran the original script and it created:
#
#   Block LDAP logins
#   username-password
#   adfs
#
# instead of:
#
#   neteye-idp-discovery-flow2-import Block LDAP logins
#   neteye-idp-discovery-flow2-import username-password
#   neteye-idp-discovery-flow2-import adfs
# ---------------------------------------------------------------------------

echo ""
echo "Checking for aliases left by previous import attempts ..."

cleanup_legacy_aliases

# ---------------------------------------------------------------------------
# Create top-level flow
# ---------------------------------------------------------------------------

echo ""
echo "Creating top-level flow: $TOP_FLOW_ALIAS"

create_flow \
    "$TOP_FLOW_ALIAS" \
    "$TOP_FLOW_DESCRIPTION" \
    "true"

# ---------------------------------------------------------------------------
# Rebuild tree
# ---------------------------------------------------------------------------

echo ""
echo "Rebuilding authentication flow tree ..."
echo ""

build_tree

# ---------------------------------------------------------------------------
# Finish
# ---------------------------------------------------------------------------

echo ""
echo "============================================================"
echo " Flow structure created successfully."
echo "============================================================"
echo ""
echo "Top-level flow:"
echo "  $TOP_FLOW_ALIAS"
echo ""
echo "IMPORTANT:"
echo "The 'authenticationConfig' values in the export are IDs of separate"
echo "Keycloak authenticator configuration objects."
echo ""
echo "They are NOT recreated by this script yet."
echo ""
echo "For example:"
echo ""
echo "  authenticationConfig:"
echo "    27ffb094-57c4-4a88-8bc7-c41b4eee8579"
echo ""
echo "is an ID belonging to the OLD execution/configuration."
echo ""
echo "The newly-created execution has a different ID and therefore the"
echo "configuration must be recreated against the new execution."
echo ""
echo "Done."
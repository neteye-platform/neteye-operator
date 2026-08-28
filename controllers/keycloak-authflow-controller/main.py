#!/usr/bin/env python3

import os
import sys
import time
from urllib.parse import quote

import requests
from kubernetes import client, config


# =============================================================================
# Configuration
# =============================================================================

KEYCLOAK_URL = os.environ.get(
    "KEYCLOAK_URL",
    "https://rdneteye.si.wp.lan/auth",
).rstrip("/")

KEYCLOAK_TOKEN_REALM = os.environ.get(
    "KEYCLOAK_TOKEN_REALM",
    "master",
)

KEYCLOAK_USERNAME = os.environ["KEYCLOAK_USERNAME"]
KEYCLOAK_PASSWORD = os.environ["KEYCLOAK_PASSWORD"]

KEYCLOAK_VERIFY_SSL = (
    os.environ.get("KEYCLOAK_VERIFY_SSL", "true").lower() == "true"
)

KEYCLOAK_TIMEOUT = int(
    os.environ.get("KEYCLOAK_TIMEOUT", "30")
)

RECONCILE_INTERVAL = int(
    os.environ.get("RECONCILE_INTERVAL", "30")
)

NAMESPACE = os.environ.get(
    "NAMESPACE",
    "default",
)

CRD_GROUP = "neteye.cloud"
CRD_VERSION = "v1"
CRD_PLURAL = "keycloakauthflows"

TOKEN = None


# =============================================================================
# Kubernetes
# =============================================================================

def load_kubernetes():
    try:
        config.load_incluster_config()
        print("Using in-cluster Kubernetes configuration")
    except config.ConfigException:
        config.load_kube_config()
        print("Using local Kubernetes configuration")


def get_authflows():
    api = client.CustomObjectsApi()

    result = api.list_namespaced_custom_object(
        group=CRD_GROUP,
        version=CRD_VERSION,
        namespace=NAMESPACE,
        plural=CRD_PLURAL,
    )

    print(
        f"[DEBUG] Kubernetes namespace={NAMESPACE}, "
        f"found {len(result.get('items', []))} KeycloakAuthFlow(s)"
    )

    return result.get("items", [])


# =============================================================================
# Keycloak
# =============================================================================

def authenticate():
    global TOKEN

    url = (
        f"{KEYCLOAK_URL}/realms/"
        f"{quote(KEYCLOAK_TOKEN_REALM, safe='')}"
        "/protocol/openid-connect/token"
    )

    print(f"[DEBUG] Authenticating against: {url}")

    response = requests.post(
        url,
        data={
            "client_id": "admin-cli",
            "username": KEYCLOAK_USERNAME,
            "password": KEYCLOAK_PASSWORD,
            "grant_type": "password",
        },
        timeout=KEYCLOAK_TIMEOUT,
        verify=KEYCLOAK_VERIFY_SSL,
    )

    response.raise_for_status()

    TOKEN = response.json()["access_token"]

    print("Authenticated to Keycloak")


def kc_request(method, path, **kwargs):
    global TOKEN

    if TOKEN is None:
        authenticate()

    headers = kwargs.pop("headers", {})
    headers["Authorization"] = f"Bearer {TOKEN}"

    url = f"{KEYCLOAK_URL}{path}"

    # Don't print Authorization header.
    print(f"[DEBUG] Keycloak request: {method} {url}")

    if "json" in kwargs:
        print(f"[DEBUG] Request JSON: {kwargs['json']}")

    response = requests.request(
        method,
        url,
        headers=headers,
        timeout=KEYCLOAK_TIMEOUT,
        verify=KEYCLOAK_VERIFY_SSL,
        **kwargs,
    )

    print(
        f"[DEBUG] Keycloak response: "
        f"{response.status_code} {response.reason}"
    )

    if response.text:
        print(f"[DEBUG] Response body: {response.text}")

    # Re-authenticate once if the token expired.
    if response.status_code == 401:
        print("[DEBUG] Token expired, authenticating again")

        authenticate()

        headers["Authorization"] = f"Bearer {TOKEN}"

        response = requests.request(
            method,
            url,
            headers=headers,
            timeout=KEYCLOAK_TIMEOUT,
            verify=KEYCLOAK_VERIFY_SSL,
            **kwargs,
        )

        print(
            f"[DEBUG] Retry response: "
            f"{response.status_code} {response.reason}"
        )

        if response.text:
            print(f"[DEBUG] Retry response body: {response.text}")

    if not response.ok:
        raise RuntimeError(
            f"{method} {path} failed: "
            f"{response.status_code}: {response.text}"
        )

    return response


def authentication_path(realm, suffix=""):
    return (
        f"/admin/realms/"
        f"{quote(realm, safe='')}"
        f"/authentication{suffix}"
    )


# =============================================================================
# Flows
# =============================================================================

def get_flows(realm):
    print(f"[DEBUG] Getting authentication flows for realm '{realm}'")

    return kc_request(
        "GET",
        authentication_path(realm, "/flows"),
    ).json()


def get_flow(realm, alias):
    print(
        f"[DEBUG] Looking for flow "
        f"realm='{realm}', alias='{alias}'"
    )

    flows = get_flows(realm)

    for flow in flows:
        print(
            f"[DEBUG] Found flow: "
            f"alias='{flow.get('alias')}', "
            f"id='{flow.get('id')}', "
            f"providerId='{flow.get('providerId')}'"
        )

        if flow.get("alias") == alias:
            print(f"[DEBUG] Matched flow '{alias}'")
            return flow

    print(f"[DEBUG] Flow '{alias}' not found")

    return None


def create_root_flow(realm, desired):
    alias = desired["alias"]
    provider = desired.get("provider", "basic-flow")

    print(
        f"[DEBUG] Creating root flow: "
        f"alias='{alias}', provider='{provider}'"
    )

    kc_request(
        "POST",
        authentication_path(realm, "/flows"),
        json={
            "alias": alias,
            "providerId": provider,
            "topLevel": True,
            "builtIn": False,
        },
    )


def ensure_root_flow(realm, desired):
    alias = desired["alias"]

    flow = get_flow(realm, alias)

    if not flow:
        create_root_flow(realm, desired)

        flow = get_flow(realm, alias)

        if not flow:
            raise RuntimeError(
                f"Flow '{alias}' was created but cannot be found"
            )

    return flow


def create_subflow(realm, parent_alias, desired):
    alias = desired["alias"]
    provider = desired.get("provider", "basic-flow")

    print(
        f"[DEBUG] Creating subflow:"
        f" parent='{parent_alias}',"
        f" alias='{alias}',"
        f" provider='{provider}'"
    )

    kc_request(
        "POST",
        authentication_path(
            realm,
            f"/flows/{quote(parent_alias, safe='')}/executions/flow",
        ),
        json={
            "alias": alias,
            "type": provider,
            "provider": provider,
        },
    )


# =============================================================================
# Executions
# =============================================================================

def get_executions(realm, flow_alias):
    """
    Get all executions belonging to a flow.

    flow_alias is the Keycloak flow alias. This is also valid for
    subflows once the subflow already exists.
    """

    print(
        f"[DEBUG] Getting executions for flow "
        f"realm='{realm}', alias='{flow_alias}'"
    )

    response = kc_request(
        "GET",
        authentication_path(
            realm,
            f"/flows/{quote(flow_alias, safe='')}/executions",
        ),
    )

    executions = response.json()

    print(
        f"[DEBUG] Found {len(executions)} execution(s) "
        f"for flow '{flow_alias}'"
    )

    for execution in executions:
        print(
            f"[DEBUG]   Execution: "
            f"id='{execution.get('id')}', "
            f"providerId='{execution.get('providerId')}', "
            f"displayName='{execution.get('displayName')}', "
            f"alias='{execution.get('alias')}', "
            f"authenticationFlow='{execution.get('authenticationFlow')}', "
            f"flowId='{execution.get('flowId')}'"
        )

    return executions


def execution_name(execution):
    return (
        execution.get("alias")
        or execution.get("displayName")
        or execution.get("providerId")
        or execution.get("id")
    )


def desired_execution_name(desired):
    if "flow" in desired:
        return desired["flow"]["alias"]

    return desired.get("alias") or desired.get("authenticator")


def find_matching_execution(executions, desired):
    print(
        f"[DEBUG] Searching for matching execution:"
        f" desired={desired}"
    )

    if "flow" in desired:
        alias = desired["flow"]["alias"]

        print(
            f"[DEBUG] Looking for subflow execution "
            f"with alias/displayName='{alias}'"
        )

        for execution in executions:
            if not execution.get("authenticationFlow"):
                continue

            if execution.get("displayName") == alias:
                print(
                    f"[DEBUG] Matched subflow by displayName: "
                    f"id='{execution.get('id')}'"
                )
                return execution

            if execution.get("alias") == alias:
                print(
                    f"[DEBUG] Matched subflow by alias: "
                    f"id='{execution.get('id')}'"
                )
                return execution

        print("[DEBUG] No matching subflow execution found")

        return None

    alias = desired.get("alias")

    if alias:
        print(
            f"[DEBUG] Looking for normal execution "
            f"with alias='{alias}'"
        )

        for execution in executions:
            if execution.get("alias") == alias:
                print(
                    f"[DEBUG] Matched execution by alias: "
                    f"id='{execution.get('id')}'"
                )
                return execution

    authenticator = desired.get("authenticator")

    print(
        f"[DEBUG] Looking for normal execution "
        f"with providerId='{authenticator}'"
    )

    for execution in executions:
        if execution.get("authenticationFlow"):
            continue

        if execution.get("providerId") == authenticator:
            print(
                f"[DEBUG] Matched execution by providerId: "
                f"id='{execution.get('id')}'"
            )
            return execution

    print("[DEBUG] No matching execution found")

    return None


def create_execution(realm, flow_alias, desired):
    authenticator = desired["authenticator"]

    print()
    print("[DEBUG] ============================================================")
    print("[DEBUG] CREATE EXECUTION")
    print("[DEBUG] ============================================================")
    print(f"[DEBUG] Realm:       {realm}")
    print(f"[DEBUG] Flow alias:  {flow_alias}")
    print(f"[DEBUG] Authenticator/provider: {authenticator}")
    print(f"[DEBUG] Desired execution: {desired}")
    print("[DEBUG] ============================================================")

    path = authentication_path(
        realm,
        f"/flows/{quote(flow_alias, safe='')}/executions/execution",
    )

    payload = {
        "provider": authenticator,
    }

    print(f"[DEBUG] POST path: {path}")
    print(f"[DEBUG] POST payload: {payload}")

    response = kc_request(
        "POST",
        path,
        json=payload,
    )

    print(
        f"[DEBUG] Execution creation succeeded: "
        f"status={response.status_code}"
    )

    if response.text:
        print(
            f"[DEBUG] Execution creation response: "
            f"{response.text}"
        )

    print("[DEBUG] ============================================================")

    return response


# =============================================================================
# Requirements
# =============================================================================

def reconcile_requirement(
    realm,
    flow_alias,
    execution,
    desired,
):
    if "requirement" not in desired:
        print(
            f"[DEBUG] No requirement specified for "
            f"{execution_name(execution)}"
        )
        return

    desired_requirement = desired["requirement"]
    current_requirement = execution.get("requirement")

    print(
        f"[DEBUG] Requirement check for "
        f"{execution_name(execution)}: "
        f"current='{current_requirement}', "
        f"desired='{desired_requirement}'"
    )

    if current_requirement == desired_requirement:
        return

    print(
        f"      Updating requirement for "
        f"{execution_name(execution)}: "
        f"{current_requirement} -> "
        f"{desired_requirement}"
    )

    kc_request(
        "PUT",
        authentication_path(
            realm,
            f"/flows/{quote(flow_alias, safe='')}/executions",
        ),
        json={
            "id": execution["id"],
            "requirement": desired_requirement,
        },
    )


# =============================================================================
# Authenticator configuration
# =============================================================================

def get_authenticator_config(realm, config_id):
    print(
        f"[DEBUG] Getting authenticator config "
        f"id='{config_id}'"
    )

    return kc_request(
        "GET",
        authentication_path(
            realm,
            f"/config/{quote(config_id, safe='')}",
        ),
    ).json()


def create_authenticator_config(
    realm,
    execution,
    desired_config,
):
    execution_id = execution["id"]

    print()
    print("[DEBUG] ============================================================")
    print("[DEBUG] CREATE AUTHENTICATOR CONFIG")
    print("[DEBUG] ============================================================")
    print(f"[DEBUG] Execution ID: {execution_id}")
    print(f"[DEBUG] Config alias: {desired_config['alias']}")
    print(
        f"[DEBUG] Config values: "
        f"{desired_config.get('values', {})}"
    )
    print("[DEBUG] ============================================================")

    response = kc_request(
        "POST",
        authentication_path(
            realm,
            f"/executions/{quote(execution_id, safe='')}/config",
        ),
        json={
            "alias": desired_config["alias"],
            "config": desired_config.get("values", {}),
        },
    )

    return response.json()


def reconcile_authenticator_config(
    realm,
    execution,
    desired,
):
    print(
        f"[DEBUG] Checking authenticator config for "
        f"execution id='{execution.get('id')}', "
        f"provider='{execution.get('providerId')}', "
        f"alias='{execution.get('alias')}'"
    )

    desired_config = desired.get("config")

    if not desired_config:
        print("[DEBUG] No config declared in CR")
        return

    config_alias = desired_config["alias"]
    desired_values = desired_config.get("values", {})

    print(
        f"[DEBUG] Desired config alias='{config_alias}'"
    )
    print(
        f"[DEBUG] Desired config values={desired_values}"
    )

    execution_id = execution["id"]

    config_id = execution.get("authenticationConfig")

    print(
        f"[DEBUG] Execution id='{execution_id}' "
        f"has authenticationConfig='{config_id}'"
    )

    if not config_id:
        print(
            "[DEBUG] Execution has no authenticationConfig. "
            "Creating one."
        )

        create_authenticator_config(
            realm,
            execution,
            desired_config,
        )

        return

    print(
        f"[DEBUG] Existing authenticationConfig found: "
        f"{config_id}"
    )

    current_config = get_authenticator_config(
        realm,
        config_id,
    )

    print(
        f"[DEBUG] Current authenticator config: "
        f"{current_config}"
    )

    current_alias = current_config.get("alias")
    current_values = current_config.get("config", {})

    print(
        f"[DEBUG] Current config alias='{current_alias}'"
    )
    print(
        f"[DEBUG] Current config values={current_values}"
    )

    if (
        current_alias != config_alias
        or current_values != desired_values
    ):

        print(
            f"      Updating authenticator config: "
            f"{config_alias}"
        )

        kc_request(
            "PUT",
            authentication_path(
                realm,
                f"/config/{quote(config_id, safe='')}",
            ),
            json={
                "alias": config_alias,
                "config": desired_values,
            },
        )

    else:
        print(
            "[DEBUG] Authenticator config already matches desired state"
        )


# =============================================================================
# Delete
# =============================================================================

def delete_execution(realm, execution):
    name = execution_name(execution)

    print(
        f"      Deleting undesired execution: "
        f"{name}"
    )

    print(
        f"[DEBUG] Deleting execution "
        f"id='{execution.get('id')}', "
        f"providerId='{execution.get('providerId')}', "
        f"alias='{execution.get('alias')}', "
        f"displayName='{execution.get('displayName')}'"
    )

    kc_request(
        "DELETE",
        authentication_path(
            realm,
            f"/executions/{quote(execution['id'], safe='')}",
        ),
    )


# =============================================================================
# Ordering
# =============================================================================

def sort_executions(executions):
    return sorted(
        executions,
        key=lambda execution: (
            execution.get(
                "priority",
                execution.get("index", 0),
            )
        ),
    )


def reconcile_order(
    realm,
    flow_alias,
    desired_executions,
):
    print(
        f"[DEBUG] Reconciling execution order "
        f"for flow '{flow_alias}'"
    )

    for desired_index, desired in enumerate(desired_executions):

        current = sort_executions(
            get_executions(
                realm,
                flow_alias,
            )
        )

        execution = find_matching_execution(
            current,
            desired,
        )

        if not execution:
            print(
                f"[DEBUG] Cannot order desired execution "
                f"'{desired_execution_name(desired)}' "
                f"because it was not found"
            )
            continue

        current_index = next(
            (
                index
                for index, item in enumerate(current)
                if item["id"] == execution["id"]
            ),
            None,
        )

        if current_index is None:
            continue

        execution_id = execution["id"]

        print(
            f"[DEBUG] Execution "
            f"'{execution_name(execution)}' "
            f"id='{execution_id}' "
            f"current_index={current_index}, "
            f"desired_index={desired_index}"
        )

        while current_index > desired_index:
            kc_request(
                "POST",
                authentication_path(
                    realm,
                    f"/executions/"
                    f"{quote(execution_id, safe='')}"
                    "/raise-priority",
                ),
            )

            current_index -= 1

        while current_index < desired_index:
            kc_request(
                "POST",
                authentication_path(
                    realm,
                    f"/executions/"
                    f"{quote(execution_id, safe='')}"
                    "/lower-priority",
                ),
            )

            current_index += 1


# =============================================================================
# Recursive reconciliation
# =============================================================================

def reconcile_flow(
    realm,
    desired,
    parent_alias=None,
):
    alias = desired["alias"]

    print()
    print("=" * 80)
    print(f"[DEBUG] Reconciling flow: {alias}")
    print(f"[DEBUG] Realm: {realm}")
    print(f"[DEBUG] Parent flow: {parent_alias}")
    print("=" * 80)

    # -------------------------------------------------------------------------
    # Make sure flow exists
    # -------------------------------------------------------------------------

    if parent_alias is None:
        # Root flow.
        flow = ensure_root_flow(
            realm,
            desired,
        )

    else:
        # ---------------------------------------------------------------------
        # IMPORTANT:
        #
        # For a subflow, do NOT blindly create the flow just because
        # get_flow() cannot find it.
        #
        # The parent flow may already contain a subflow execution pointing
        # to this flow. Keycloak also requires flow aliases to be globally
        # unique, so blindly calling create_subflow() can result in:
        #
        #   409 New flow alias name already exists
        #
        # The parent execution is the authoritative way to determine whether
        # this subflow is already attached.
        # ---------------------------------------------------------------------

        print(
            f"[DEBUG] Resolving subflow execution "
            f"'{alias}' inside parent '{parent_alias}'"
        )

        parent_executions = get_executions(
            realm,
            parent_alias,
        )

        nested_execution = None

        for execution in parent_executions:
            if not execution.get("authenticationFlow"):
                continue

            execution_alias = (
                execution.get("alias")
                or execution.get("displayName")
            )

            print(
                f"[DEBUG] Parent subflow candidate: "
                f"id='{execution.get('id')}', "
                f"alias='{execution.get('alias')}', "
                f"displayName='{execution.get('displayName')}', "
                f"authenticationFlow='{execution.get('authenticationFlow')}'"
            )

            if execution_alias == alias:
                nested_execution = execution
                break

        # ---------------------------------------------------------------------
        # Existing subflow execution found.
        # ---------------------------------------------------------------------

        if nested_execution:

            print(
                f"[DEBUG] Subflow '{alias}' is already attached to "
                f"parent '{parent_alias}'"
            )

            print(
                f"[DEBUG] Subflow execution id="
                f"'{nested_execution.get('id')}'"
            )

            # We deliberately do NOT call create_subflow() here.
            #
            # The subflow already exists and is already attached.
            flow = get_flow(
                realm,
                alias,
            )

            if flow:
                print(
                    f"[DEBUG] Existing subflow definition found: "
                    f"id='{flow.get('id')}', "
                    f"alias='{flow.get('alias')}'"
                )
            else:
                print(
                    f"[DEBUG] WARNING: subflow execution exists, "
                    f"but flow definition '{alias}' was not returned "
                    f"by GET /authentication/flows"
                )

        else:

            # -----------------------------------------------------------------
            # No subflow execution exists under the parent.
            #
            # Before creating anything, check whether a flow with this alias
            # already exists globally.
            # -----------------------------------------------------------------

            print(
                f"[DEBUG] No existing subflow execution found under "
                f"'{parent_alias}'"
            )

            existing_flow = get_flow(
                realm,
                alias,
            )

            if existing_flow:

                print(
                    f"[DEBUG] Flow '{alias}' already exists globally "
                    f"but is not attached to '{parent_alias}'"
                )

                print(
                    "[DEBUG] Creating it again would cause a Keycloak "
                    "409 alias conflict."
                )

                raise RuntimeError(
                    f"Flow '{alias}' already exists in Keycloak but is "
                    f"not attached as a subflow of '{parent_alias}'. "
                    f"Manual cleanup or explicit adoption is required."
                )

            # -----------------------------------------------------------------
            # Completely new subflow.
            # -----------------------------------------------------------------

            print(
                f"[DEBUG] Subflow '{alias}' does not exist. "
                f"Creating it under '{parent_alias}'."
            )

            create_subflow(
                realm,
                parent_alias,
                desired,
            )

            # Re-read parent executions because Keycloak generated
            # the execution ID.
            parent_executions = get_executions(
                realm,
                parent_alias,
            )

            nested_execution = None

            for execution in parent_executions:

                if not execution.get("authenticationFlow"):
                    continue

                execution_alias = (
                    execution.get("alias")
                    or execution.get("displayName")
                )

                if execution_alias == alias:
                    nested_execution = execution
                    break

            if not nested_execution:
                raise RuntimeError(
                    f"Subflow '{alias}' was created under "
                    f"'{parent_alias}', but its execution "
                    f"could not be found"
                )

            print(
                f"[DEBUG] Newly created subflow execution: "
                f"id='{nested_execution.get('id')}'"
            )

            flow = get_flow(
                realm,
                alias,
            )

            if not flow:
                raise RuntimeError(
                    f"Subflow '{alias}' was created but its "
                    f"flow definition cannot be found"
                )

    # -------------------------------------------------------------------------
    # Desired executions
    # -------------------------------------------------------------------------

    desired_executions = desired.get(
        "executions",
        [],
    )

    print(
        f"[DEBUG] Desired executions for '{alias}': "
        f"{len(desired_executions)}"
    )

    for index, desired_execution in enumerate(
        desired_executions
    ):
        print(
            f"[DEBUG]   Desired execution #{index}: "
            f"{desired_execution}"
        )

    current_executions = get_executions(
        realm,
        alias,
    )

    desired_ids = set()

    # -------------------------------------------------------------------------
    # Reconcile desired executions
    # -------------------------------------------------------------------------

    for desired_index, desired_execution in enumerate(
        desired_executions
    ):

        print()
        print(
            f"[DEBUG] Processing desired execution "
            f"#{desired_index}: {desired_execution}"
        )

        # =====================================================================
        # Subflow
        # =====================================================================

        if "flow" in desired_execution:

            nested = desired_execution["flow"]

            print(
                f"[DEBUG] Desired execution is a subflow: "
                f"{nested}"
            )

            execution = find_matching_execution(
                current_executions,
                desired_execution,
            )

            if not execution:

                print(
                    f"[DEBUG] Subflow execution "
                    f"'{nested['alias']}' does not exist. "
                    f"Creating it."
                )

                create_subflow(
                    realm,
                    alias,
                    nested,
                )

                current_executions = get_executions(
                    realm,
                    alias,
                )

                execution = find_matching_execution(
                    current_executions,
                    desired_execution,
                )

            if not execution:
                raise RuntimeError(
                    f"Could not find subflow "
                    f"'{nested['alias']}' "
                    f"inside '{alias}'"
                )

            print(
                f"[DEBUG] Subflow execution resolved: "
                f"id='{execution.get('id')}'"
            )

            desired_ids.add(
                execution["id"]
            )

            reconcile_requirement(
                realm,
                alias,
                execution,
                desired_execution,
            )

            # Recursively reconcile the contents of the existing subflow.
            reconcile_flow(
                realm,
                nested,
                parent_alias=alias,
            )

            continue

        # =====================================================================
        # Normal authenticator execution
        # =====================================================================

        print(
            f"[DEBUG] Desired execution is an authenticator: "
            f"provider='{desired_execution.get('authenticator')}', "
            f"alias='{desired_execution.get('alias')}'"
        )

        execution = find_matching_execution(
            current_executions,
            desired_execution,
        )

        if not execution:

            print(
                "[DEBUG] Execution does not exist. "
                "Calling create_execution()."
            )

            create_execution(
                realm,
                alias,
                desired_execution,
            )

            print(
                "[DEBUG] Re-reading executions after creation"
            )

            current_executions = get_executions(
                realm,
                alias,
            )

            execution = find_matching_execution(
                current_executions,
                desired_execution,
            )

        if not execution:
            raise RuntimeError(
                f"Could not find execution "
                f"'{desired_execution_name(desired_execution)}' "
                f"inside '{alias}'"
            )

        print(
            f"[DEBUG] Final execution selected: "
            f"id='{execution.get('id')}', "
            f"providerId='{execution.get('providerId')}', "
            f"alias='{execution.get('alias')}', "
            f"displayName='{execution.get('displayName')}', "
            f"authenticationConfig='{execution.get('authenticationConfig')}'"
        )

        desired_ids.add(
            execution["id"]
        )

        reconcile_requirement(
            realm,
            alias,
            execution,
            desired_execution,
        )

        reconcile_authenticator_config(
            realm,
            execution,
            desired_execution,
        )

    # -------------------------------------------------------------------------
    # PRUNE
    # -------------------------------------------------------------------------

    print(
        f"[DEBUG] Checking for undesired executions "
        f"attached to '{alias}'"
    )

    current_executions = get_executions(
        realm,
        alias,
    )

    for execution in current_executions:

        if execution["id"] not in desired_ids:

            print(
                f"[DEBUG] Execution id='{execution['id']}' "
                f"is not in desired state"
            )

            delete_execution(
                realm,
                execution,
            )

    # -------------------------------------------------------------------------
    # Ordering
    # -------------------------------------------------------------------------

    reconcile_order(
        realm,
        alias,
        desired_executions,
    )

def reconcile():
    resources = get_authflows()

    print(
        f"Found {len(resources)} "
        f"KeycloakAuthFlow resource(s)"
    )

    success = True

    for resource in resources:

        metadata = resource.get(
            "metadata",
            {},
        )

        spec = resource.get(
            "spec",
            {},
        )

        resource_name = metadata.get(
            "name",
            "<unknown>",
        )

        realm = spec.get("realm")
        alias = spec.get("alias")

        print()
        print("=" * 80)
        print(f"Resource: {resource_name}")
        print(f"Realm:    {realm}")
        print(f"Flow:     {alias}")
        print("=" * 80)

        if not realm:
            print(
                "ERROR: spec.realm is missing",
                file=sys.stderr,
            )
            success = False
            continue

        if not alias:
            print(
                "ERROR: spec.alias is missing",
                file=sys.stderr,
            )
            success = False
            continue

        try:
            reconcile_flow(
                realm,
                spec,
            )

            print(
                f"Successfully reconciled "
                f"{resource_name}"
            )

        except Exception as exc:
            print(
                f"ERROR reconciling "
                f"{resource_name}: {exc}",
                file=sys.stderr,
            )

            success = False

    return success


# =============================================================================
# Main
# =============================================================================

def main():
    load_kubernetes()

    while True:

        try:
            authenticate()
            reconcile()

        except Exception as exc:
            print(
                f"Reconciliation failed: {exc}",
                file=sys.stderr,
            )

        print(
            f"Sleeping for "
            f"{RECONCILE_INTERVAL} seconds..."
        )

        time.sleep(
            RECONCILE_INTERVAL
        )


if __name__ == "__main__":
    main()
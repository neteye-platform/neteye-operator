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

FINALIZER = "neteye.cloud/keycloakauthflow"

TOKEN = None


# =============================================================================
# Helpers
# =============================================================================

def string_id(value):
    if value is None:
        raise ValueError("Expected identifier, got None")

    if isinstance(value, bytes):
        return value.decode("utf-8")

    if not isinstance(value, str):
        value = str(value)

    return value


def url_part(value):
    return quote(
        string_id(value),
        safe="",
    )


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

    return (
        desired.get("alias")
        or desired.get("authenticator")
    )


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


def get_kubernetes_api():
    return client.CustomObjectsApi()


def get_authflows():
    api = get_kubernetes_api()

    result = api.list_namespaced_custom_object(
        group=CRD_GROUP,
        version=CRD_VERSION,
        namespace=NAMESPACE,
        plural=CRD_PLURAL,
    )

    items = result.get("items", [])

    print(
        f"[DEBUG] Kubernetes namespace={NAMESPACE}, "
        f"found {len(items)} KeycloakAuthFlow(s)"
    )

    return items


def ensure_finalizer(resource):
    api = get_kubernetes_api()

    metadata = resource.get(
        "metadata",
        {},
    )

    name = metadata.get("name")

    if not name:
        raise RuntimeError(
            "Cannot add finalizer: resource has no metadata.name"
        )

    finalizers = list(
        metadata.get("finalizers") or []
    )

    if FINALIZER in finalizers:
        print(
            f"[DEBUG] Finalizer '{FINALIZER}' already exists "
            f"on '{name}'"
        )
        return

    print(
        f"[DEBUG] Adding finalizer '{FINALIZER}' "
        f"to '{name}'"
    )

    finalizers.append(FINALIZER)

    body = {
        "metadata": {
            "finalizers": finalizers,
        }
    }

    api.patch_namespaced_custom_object(
        group=CRD_GROUP,
        version=CRD_VERSION,
        namespace=NAMESPACE,
        plural=CRD_PLURAL,
        name=name,
        body=body,
    )

    print(
        f"[DEBUG] Finalizer '{FINALIZER}' added "
        f"to '{name}'"
    )


def remove_finalizer(resource):
    api = get_kubernetes_api()

    metadata = resource.get(
        "metadata",
        {},
    )

    name = metadata.get("name")

    if not name:
        raise RuntimeError(
            "Cannot remove finalizer: resource has no metadata.name"
        )

    finalizers = list(
        metadata.get("finalizers") or []
    )

    if FINALIZER not in finalizers:
        print(
            f"[DEBUG] Finalizer '{FINALIZER}' is already absent "
            f"from '{name}'"
        )
        return

    print(
        f"[DEBUG] Removing finalizer '{FINALIZER}' "
        f"from '{name}'"
    )

    new_finalizers = [
        item
        for item in finalizers
        if item != FINALIZER
    ]

    body = {
        "metadata": {
            "finalizers": new_finalizers,
        }
    }

    api.patch_namespaced_custom_object(
        group=CRD_GROUP,
        version=CRD_VERSION,
        namespace=NAMESPACE,
        plural=CRD_PLURAL,
        name=name,
        body=body,
    )

    print(
        f"[DEBUG] Finalizer '{FINALIZER}' removed "
        f"from '{name}'"
    )


# =============================================================================
# Keycloak authentication
# =============================================================================

def authenticate():
    global TOKEN

    url = (
        f"{KEYCLOAK_URL}/realms/"
        f"{url_part(KEYCLOAK_TOKEN_REALM)}"
        "/protocol/openid-connect/token"
    )

    print(
        f"[DEBUG] Authenticating against: {url}"
    )

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

    headers = kwargs.pop(
        "headers",
        {},
    )

    headers["Authorization"] = f"Bearer {TOKEN}"

    url = f"{KEYCLOAK_URL}{path}"

    print(
        f"[DEBUG] Keycloak request: "
        f"{method} {url}"
    )

    if "json" in kwargs:
        print(
            f"[DEBUG] Request JSON: "
            f"{kwargs['json']}"
        )

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
        f"{response.status_code} "
        f"{response.reason}"
    )

    if response.text:
        print(
            f"[DEBUG] Response body: "
            f"{response.text}"
        )

    if response.status_code == 401:

        print(
            "[DEBUG] Token expired, authenticating again"
        )

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
            f"{response.status_code} "
            f"{response.reason}"
        )

        if response.text:
            print(
                f"[DEBUG] Retry response body: "
                f"{response.text}"
            )

    if not response.ok:
        raise RuntimeError(
            f"{method} {path} failed: "
            f"{response.status_code}: "
            f"{response.text}"
        )

    return response


def authentication_path(realm, suffix=""):
    return (
        f"/admin/realms/"
        f"{url_part(realm)}"
        f"/authentication{suffix}"
    )


# =============================================================================
# Authentication flows
# =============================================================================

def get_flows(realm):
    return kc_request(
        "GET",
        authentication_path(
            realm,
            "/flows",
        ),
    ).json()


def get_flow(realm, alias):
    alias = string_id(alias)

    print(
        f"[DEBUG] Looking for flow "
        f"realm='{realm}', alias='{alias}'"
    )

    flows = get_flows(realm)

    for flow in flows:

        if flow.get("alias") == alias:

            print(
                f"[DEBUG] Matched flow "
                f"alias='{alias}', "
                f"id='{flow.get('id')}', "
                f"topLevel='{flow.get('topLevel')}'"
            )

            return flow

    print(
        f"[DEBUG] Flow '{alias}' was not found"
    )

    return None


def get_flow_by_id(realm, flow_id):
    flow_id = string_id(flow_id)

    print(
        f"[DEBUG] Getting authentication flow "
        f"by ID='{flow_id}'"
    )

    response = kc_request(
        "GET",
        authentication_path(
            realm,
            f"/flows/{url_part(flow_id)}",
        ),
    )

    flow = response.json()

    print(
        f"[DEBUG] Flow by ID: "
        f"id='{flow.get('id')}', "
        f"alias='{flow.get('alias')}', "
        f"providerId='{flow.get('providerId')}', "
        f"topLevel='{flow.get('topLevel')}', "
        f"builtIn='{flow.get('builtIn')}'"
    )

    return flow


def create_root_flow(realm, desired):
    alias = string_id(
        desired["alias"]
    )

    provider = desired.get(
        "provider",
        "basic-flow",
    )

    print(
        f"[DEBUG] Creating root flow "
        f"alias='{alias}', "
        f"provider='{provider}'"
    )

    kc_request(
        "POST",
        authentication_path(
            realm,
            "/flows",
        ),
        json={
            "alias": alias,
            "providerId": provider,
            "topLevel": True,
            "builtIn": False,
        },
    )


def ensure_root_flow(realm, desired):
    alias = string_id(
        desired["alias"]
    )

    flow = get_flow(
        realm,
        alias,
    )

    if flow is None:

        create_root_flow(
            realm,
            desired,
        )

        flow = get_flow(
            realm,
            alias,
        )

        if flow is None:
            raise RuntimeError(
                f"Flow '{alias}' was created "
                f"but cannot be found"
            )

    return flow


# =============================================================================
# RAW execution retrieval
# =============================================================================

def get_raw_executions(realm, flow_alias):
    flow_alias = string_id(flow_alias)

    print(
        f"[DEBUG] Getting RAW executions "
        f"for flow realm='{realm}', "
        f"alias='{flow_alias}'"
    )

    response = kc_request(
        "GET",
        authentication_path(
            realm,
            f"/flows/{url_part(flow_alias)}/executions",
        ),
    )

    executions = response.json()

    print(
        f"[DEBUG] Raw executions returned "
        f"for '{flow_alias}': {len(executions)}"
    )

    for execution in executions:

        print(
            f"[DEBUG]   id='{execution.get('id')}', "
            f"providerId='{execution.get('providerId')}', "
            f"displayName='{execution.get('displayName')}', "
            f"alias='{execution.get('alias')}', "
            f"authenticationFlow='{execution.get('authenticationFlow')}', "
            f"flowId='{execution.get('flowId')}', "
            f"requirement='{execution.get('requirement')}', "
            f"level='{execution.get('level')}', "
            f"index='{execution.get('index')}', "
            f"priority='{execution.get('priority')}'"
        )

    return executions


def get_direct_executions(realm, flow_alias):
    raw = get_raw_executions(
        realm,
        flow_alias,
    )

    direct = [
        execution
        for execution in raw
        if execution.get("level", 0) == 0
    ]

    print(
        f"[DEBUG] Direct executions of "
        f"'{flow_alias}': {len(direct)} execution(s)"
    )

    for execution in direct:

        print(
            f"[DEBUG]   DIRECT: "
            f"id='{execution.get('id')}', "
            f"displayName='{execution.get('displayName')}', "
            f"providerId='{execution.get('providerId')}', "
            f"alias='{execution.get('alias')}', "
            f"authenticationFlow='{execution.get('authenticationFlow')}', "
            f"flowId='{execution.get('flowId')}', "
            f"requirement='{execution.get('requirement')}', "
            f"index='{execution.get('index')}'"
        )

    return direct


# =============================================================================
# Subflow handling
# =============================================================================

def find_subflow_execution(
    executions,
    alias,
):
    alias = string_id(alias)

    print(
        f"[DEBUG] Looking for subflow execution "
        f"with alias/displayName='{alias}'"
    )

    for execution in executions:

        if not execution.get("authenticationFlow"):
            continue

        display_name = execution.get(
            "displayName"
        )

        execution_alias = execution.get(
            "alias"
        )

        print(
            f"[DEBUG]   Subflow candidate: "
            f"id='{execution.get('id')}', "
            f"displayName='{display_name}', "
            f"alias='{execution_alias}', "
            f"flowId='{execution.get('flowId')}'"
        )

        if display_name == alias:
            print(
                f"[DEBUG] Matched subflow by displayName: "
                f"id='{execution.get('id')}'"
            )
            return execution

        if execution_alias == alias:
            print(
                f"[DEBUG] Matched subflow by alias: "
                f"id='{execution.get('id')}'"
            )
            return execution

    return None


def create_subflow(
    realm,
    parent_alias,
    desired,
):
    parent_alias = string_id(parent_alias)

    alias = string_id(
        desired["alias"]
    )

    provider = desired.get(
        "provider",
        "basic-flow",
    )

    print(
        f"[DEBUG] Creating subflow "
        f"parent='{parent_alias}', "
        f"alias='{alias}', "
        f"provider='{provider}'"
    )

    kc_request(
        "POST",
        authentication_path(
            realm,
            f"/flows/{url_part(parent_alias)}"
            "/executions/flow",
        ),
        json={
            "alias": alias,
            "type": provider,
            "provider": provider,
        },
    )


def ensure_subflow(
    realm,
    parent_alias,
    desired,
):
    parent_alias = string_id(parent_alias)

    alias = string_id(
        desired["alias"]
    )

    print(
        f"[DEBUG] Ensuring subflow "
        f"parent='{parent_alias}', "
        f"alias='{alias}'"
    )

    parent_executions = get_direct_executions(
        realm,
        parent_alias,
    )

    nested_execution = find_subflow_execution(
        parent_executions,
        alias,
    )

    if nested_execution:

        flow_id = nested_execution.get(
            "flowId"
        )

        if not flow_id:
            raise RuntimeError(
                f"Subflow '{alias}' exists under "
                f"'{parent_alias}' but has no flowId"
            )

        flow = get_flow_by_id(
            realm,
            flow_id,
        )

        actual_alias = flow.get(
            "alias"
        )

        if actual_alias != alias:
            raise RuntimeError(
                f"Subflow execution '{alias}' points "
                f"to flow '{actual_alias}'"
            )

        return flow

    existing_flow = get_flow(
        realm,
        alias,
    )

    if existing_flow:

        raise RuntimeError(
            f"Flow '{alias}' already exists globally "
            f"but is not attached to '{parent_alias}'"
        )

    create_subflow(
        realm,
        parent_alias,
        desired,
    )

    parent_executions = get_direct_executions(
        realm,
        parent_alias,
    )

    nested_execution = find_subflow_execution(
        parent_executions,
        alias,
    )

    if not nested_execution:
        raise RuntimeError(
            f"Subflow '{alias}' was created but "
            f"its execution was not found"
        )

    flow_id = nested_execution.get(
        "flowId"
    )

    if not flow_id:
        raise RuntimeError(
            f"Subflow '{alias}' has no flowId"
        )

    flow = get_flow_by_id(
        realm,
        flow_id,
    )

    if flow.get("alias") != alias:

        raise RuntimeError(
            f"Created subflow alias mismatch: "
            f"expected='{alias}', "
            f"actual='{flow.get('alias')}'"
        )

    return flow


# =============================================================================
# Execution matching
# =============================================================================

def find_matching_execution(
    executions,
    desired,
):
    print(
        f"[DEBUG] Searching for matching execution: "
        f"desired={desired}"
    )

    # -------------------------------------------------------------------------
    # Subflow
    # -------------------------------------------------------------------------

    if "flow" in desired:

        alias = string_id(
            desired["flow"]["alias"]
        )

        return find_subflow_execution(
            executions,
            alias,
        )

    # -------------------------------------------------------------------------
    # Normal authenticator
    # -------------------------------------------------------------------------

    authenticator = desired.get(
        "authenticator"
    )

    desired_alias = desired.get(
        "alias"
    )

    desired_alias = (
        string_id(desired_alias)
        if desired_alias
        else None
    )

    # -------------------------------------------------------------------------
    # First try execution alias.
    # -------------------------------------------------------------------------

    if desired_alias:

        for execution in executions:

            if execution.get(
                "authenticationFlow"
            ):
                continue

            if execution.get(
                "alias"
            ) == desired_alias:

                print(
                    f"[DEBUG] Matched normal execution "
                    f"by execution alias: "
                    f"id='{execution.get('id')}'"
                )

                return execution

    # -------------------------------------------------------------------------
    # Then match by providerId.
    # -------------------------------------------------------------------------

    if authenticator:

        for execution in executions:

            if execution.get(
                "authenticationFlow"
            ):
                continue

            if execution.get(
                "providerId"
            ) != authenticator:
                continue

            print(
                f"[DEBUG] Matched normal execution "
                f"by providerId='{authenticator}': "
                f"id='{execution.get('id')}'"
            )

            return execution

    print(
        "[DEBUG] No matching execution found"
    )

    return None


# =============================================================================
# Create authenticator execution
# =============================================================================

def create_execution(
    realm,
    flow_alias,
    desired,
):
    flow_alias = string_id(
        flow_alias
    )

    authenticator = string_id(
        desired["authenticator"]
    )

    print(
        f"[DEBUG] Creating authenticator "
        f"'{authenticator}' in flow '{flow_alias}'"
    )

    return kc_request(
        "POST",
        authentication_path(
            realm,
            f"/flows/{url_part(flow_alias)}"
            "/executions/execution",
        ),
        json={
            "provider": authenticator,
        },
    )


# =============================================================================
# Requirement
# =============================================================================

def reconcile_requirement(
    realm,
    flow_alias,
    execution,
    desired,
):
    desired_requirement = desired.get(
        "requirement"
    )

    if desired_requirement is None:
        return

    current_requirement = execution.get(
        "requirement"
    )

    if current_requirement == desired_requirement:
        return

    print(
        f"[DEBUG] Updating requirement for "
        f"'{execution_name(execution)}': "
        f"{current_requirement} -> "
        f"{desired_requirement}"
    )

    kc_request(
        "PUT",
        authentication_path(
            realm,
            f"/flows/{url_part(flow_alias)}"
            "/executions",
        ),
        json={
            "id": execution["id"],
            "requirement": desired_requirement,
        },
    )


# =============================================================================
# Authenticator configuration
# =============================================================================

def get_authenticator_config(
    realm,
    config_id,
):
    config_id = string_id(
        config_id
    )

    return kc_request(
        "GET",
        authentication_path(
            realm,
            f"/config/{url_part(config_id)}",
        ),
    ).json()


def create_authenticator_config(
    realm,
    execution,
    desired_config,
):
    execution_id = string_id(
        execution["id"]
    )

    config_alias = string_id(
        desired_config["alias"]
    )

    values = desired_config.get(
        "values",
        {},
    )

    print(
        f"[DEBUG] Creating authenticator config "
        f"execution='{execution_id}', "
        f"alias='{config_alias}'"
    )

    response = kc_request(
        "POST",
        authentication_path(
            realm,
            f"/executions/{url_part(execution_id)}"
            "/config",
        ),
        json={
            "alias": config_alias,
            "config": values,
        },
    )

    if response.text:

        try:
            return response.json()

        except ValueError:
            return None

    return None


def reconcile_authenticator_config(
    realm,
    execution,
    desired,
):
    desired_config = desired.get(
        "config"
    )

    if not desired_config:
        return

    desired_config_alias = string_id(
        desired_config["alias"]
    )

    desired_values = desired_config.get(
        "values",
        {},
    )

    execution_id = execution.get(
        "id"
    )

    config_id = execution.get(
        "authenticationConfig"
    )

    print(
        f"[DEBUG] Config reconciliation: "
        f"execution='{execution_id}', "
        f"provider='{execution.get('providerId')}', "
        f"authenticationConfig='{config_id}', "
        f"desiredAlias='{desired_config_alias}'"
    )

    # -------------------------------------------------------------------------
    # Existing configuration
    # -------------------------------------------------------------------------

    if config_id:

        current_config = get_authenticator_config(
            realm,
            config_id,
        )

        current_alias = current_config.get(
            "alias"
        )

        current_values = current_config.get(
            "config",
            {},
        )

        print(
            f"[DEBUG] Existing config: "
            f"alias='{current_alias}', "
            f"values={current_values}"
        )

        if (
            current_alias != desired_config_alias
            or current_values != desired_values
        ):

            print(
                "[DEBUG] Updating existing "
                "authenticator config"
            )

            kc_request(
                "PUT",
                authentication_path(
                    realm,
                    f"/config/{url_part(config_id)}",
                ),
                json={
                    "alias": desired_config_alias,
                    "config": desired_values,
                },
            )

        else:

            print(
                "[DEBUG] Authenticator config "
                "already matches desired state"
            )

        return

    # -------------------------------------------------------------------------
    # No configuration ID.
    # -------------------------------------------------------------------------

    print(
        "[DEBUG] Execution has no authenticationConfig"
    )

    try:

        create_authenticator_config(
            realm,
            execution,
            desired_config,
        )

    except RuntimeError as exc:

        error_text = str(exc)

        if "already exists" not in error_text.lower():
            raise

        print(
            "[DEBUG] Config already exists according to "
            "Keycloak; will refresh execution state"
        )


# =============================================================================
# Delete execution
# =============================================================================

def delete_execution(
    realm,
    execution,
):
    execution_id = string_id(
        execution["id"]
    )

    print(
        f"[DEBUG] Deleting execution "
        f"id='{execution_id}', "
        f"name='{execution_name(execution)}', "
        f"provider='{execution.get('providerId')}', "
        f"flowId='{execution.get('flowId')}'"
    )

    kc_request(
        "DELETE",
        authentication_path(
            realm,
            f"/executions/{url_part(execution_id)}",
        ),
    )


# =============================================================================
# Delete flow
# =============================================================================

def delete_keycloak_flow(
    realm,
    alias,
):
    alias = string_id(alias)

    print()
    print("=" * 80)
    print("[DEBUG] DELETING KEYCLOAK FLOW")
    print("=" * 80)
    print(f"[DEBUG] Realm: {realm}")
    print(f"[DEBUG] Flow:  {alias}")
    print("=" * 80)

    # -------------------------------------------------------------------------
    # Find flow by alias.
    #
    # IMPORTANT:
    # Keycloak's DELETE endpoint expects the FLOW ID,
    # not the alias.
    # -------------------------------------------------------------------------

    flow = get_flow(
        realm,
        alias,
    )

    if flow is None:

        print(
            f"[DEBUG] Flow '{alias}' does not exist "
            f"in Keycloak."
        )

        print(
            "[DEBUG] Nothing to delete."
        )

        return True

    flow_id = flow.get(
        "id"
    )

    if not flow_id:

        raise RuntimeError(
            f"Flow '{alias}' exists but has no ID"
        )

    print(
        f"[DEBUG] Found flow to delete: "
        f"alias='{flow.get('alias')}', "
        f"id='{flow_id}', "
        f"topLevel='{flow.get('topLevel')}', "
        f"builtIn='{flow.get('builtIn')}'"
    )

    # -------------------------------------------------------------------------
    # Never delete built-in Keycloak flows.
    # -------------------------------------------------------------------------

    if flow.get("builtIn"):

        raise RuntimeError(
            f"Refusing to delete built-in Keycloak "
            f"flow '{alias}'"
        )

    # -------------------------------------------------------------------------
    # DELETE USING FLOW ID.
    # -------------------------------------------------------------------------

    print(
        f"[DEBUG] Sending DELETE for flow ID='{flow_id}'"
    )

    kc_request(
        "DELETE",
        authentication_path(
            realm,
            f"/flows/{url_part(flow_id)}",
        ),
    )

    print(
        f"[DEBUG] DELETE request completed for "
        f"flow ID='{flow_id}'"
    )

    # -------------------------------------------------------------------------
    # Verify deletion.
    #
    # We deliberately search by alias again.
    # If Keycloak still returns the flow, we do NOT remove
    # the Kubernetes finalizer.
    # -------------------------------------------------------------------------

    print(
        f"[DEBUG] Verifying deletion of "
        f"flow alias='{alias}'"
    )

    remaining_flow = get_flow(
        realm,
        alias,
    )

    if remaining_flow is not None:

        raise RuntimeError(
            f"Keycloak flow '{alias}' still exists "
            f"after DELETE"
        )

    print(
        f"[DEBUG] Confirmed Keycloak flow "
        f"'{alias}' has been deleted"
    )

    return True


# =============================================================================
# Ordering
# =============================================================================

def sort_executions(executions):
    return sorted(
        executions,
        key=lambda execution: (
            execution.get(
                "index",
                0,
            )
        ),
    )


def reconcile_order(
    realm,
    flow_alias,
    desired_executions,
):
    flow_alias = string_id(flow_alias)

    print(
        f"[DEBUG] Reconciling execution order "
        f"for flow '{flow_alias}'"
    )

    for desired_index, desired in enumerate(
        desired_executions
    ):

        current = sort_executions(
            get_direct_executions(
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
                f"[DEBUG] Cannot order "
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

        execution_id = string_id(
            execution["id"]
        )

        while current_index > desired_index:

            kc_request(
                "POST",
                authentication_path(
                    realm,
                    f"/executions/"
                    f"{url_part(execution_id)}"
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
                    f"{url_part(execution_id)}"
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
    alias = string_id(
        desired["alias"]
    )

    print()
    print("=" * 80)
    print("[DEBUG] RECONCILING FLOW")
    print("=" * 80)
    print(f"[DEBUG] Alias:  {alias}")
    print(f"[DEBUG] Parent: {parent_alias}")
    print("=" * 80)

    # -------------------------------------------------------------------------
    # Ensure flow exists.
    # -------------------------------------------------------------------------

    if parent_alias is None:

        flow = ensure_root_flow(
            realm,
            desired,
        )

    else:

        flow = ensure_subflow(
            realm,
            parent_alias,
            desired,
        )

    flow_id = flow.get(
        "id"
    )

    print(
        f"[DEBUG] Flow ID: {flow_id}"
    )

    # -------------------------------------------------------------------------
    # Get ONLY executions directly belonging to this flow.
    # -------------------------------------------------------------------------

    current_executions = get_direct_executions(
        realm,
        alias,
    )

    desired_executions = desired.get(
        "executions",
        [],
    )

    print(
        f"[DEBUG] Desired DIRECT executions "
        f"for '{alias}': "
        f"{len(desired_executions)}"
    )

    desired_ids = set()

    # =========================================================================
    # Reconcile desired executions
    # =========================================================================

    for desired_execution in desired_executions:

        print()
        print("=" * 80)

        # =====================================================================
        # SUBFLOW
        # =====================================================================

        if "flow" in desired_execution:

            nested = desired_execution[
                "flow"
            ]

            nested_alias = string_id(
                nested["alias"]
            )

            print(
                f"[DEBUG] Desired execution is "
                f"SUBFLOW '{nested_alias}'"
            )

            execution = find_matching_execution(
                current_executions,
                desired_execution,
            )

            if not execution:

                print(
                    f"[DEBUG] Subflow '{nested_alias}' "
                    f"is missing from '{alias}'. "
                    f"Creating it."
                )

                create_subflow(
                    realm,
                    alias,
                    nested,
                )

                current_executions = get_direct_executions(
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
                    f"'{nested_alias}' inside '{alias}'"
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

            reconcile_flow(
                realm,
                nested,
                parent_alias=alias,
            )

            continue

        # =====================================================================
        # NORMAL AUTHENTICATOR
        # =====================================================================

        authenticator = desired_execution.get(
            "authenticator"
        )

        print(
            f"[DEBUG] Desired execution is "
            f"AUTHENTICATOR '{authenticator}'"
        )

        execution = find_matching_execution(
            current_executions,
            desired_execution,
        )

        if not execution:

            print(
                f"[DEBUG] Authenticator "
                f"'{authenticator}' is missing "
                f"from '{alias}'. Creating it."
            )

            create_execution(
                realm,
                alias,
                desired_execution,
            )

            current_executions = get_direct_executions(
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
            f"authenticationConfig="
            f"'{execution.get('authenticationConfig')}'"
        )

        desired_ids.add(
            execution["id"]
        )

        # ---------------------------------------------------------------------
        # Requirement
        # ---------------------------------------------------------------------

        reconcile_requirement(
            realm,
            alias,
            execution,
            desired_execution,
        )

        # ---------------------------------------------------------------------
        # Configuration
        # ---------------------------------------------------------------------

        reconcile_authenticator_config(
            realm,
            execution,
            desired_execution,
        )

    # =========================================================================
    # PRUNE DIRECT EXECUTIONS
    # =========================================================================

    print()
    print(
        f"[DEBUG] Pruning undesired DIRECT executions "
        f"from '{alias}'"
    )

    current_executions = get_direct_executions(
        realm,
        alias,
    )

    for execution in current_executions:

        execution_id = execution.get(
            "id"
        )

        if execution_id in desired_ids:
            continue

        print(
            f"[DEBUG] Execution is NOT desired: "
            f"id='{execution_id}', "
            f"name='{execution_name(execution)}', "
            f"provider='{execution.get('providerId')}', "
            f"authenticationFlow="
            f"'{execution.get('authenticationFlow')}', "
            f"flowId='{execution.get('flowId')}'"
        )

        delete_execution(
            realm,
            execution,
        )

    # =========================================================================
    # ORDER
    # =========================================================================

    reconcile_order(
        realm,
        alias,
        desired_executions,
    )


# =============================================================================
# Resource deletion reconciliation
# =============================================================================

def reconcile_resource_deletion(
    resource,
):
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

    deletion_timestamp = metadata.get(
        "deletionTimestamp"
    )

    realm = spec.get(
        "realm"
    )

    alias = spec.get(
        "alias"
    )

    print()
    print("=" * 80)
    print("[DEBUG] RESOURCE DELETION REQUESTED")
    print("=" * 80)
    print(f"[DEBUG] Resource: {resource_name}")
    print(f"[DEBUG] Realm:    {realm}")
    print(f"[DEBUG] Flow:     {alias}")
    print(
        f"[DEBUG] DeletionTimestamp: "
        f"{deletion_timestamp}"
    )
    print(
        f"[DEBUG] Finalizers: "
        f"{metadata.get('finalizers', [])}"
    )
    print("=" * 80)

    if not realm:

        raise RuntimeError(
            f"Cannot delete Keycloak resource "
            f"'{resource_name}': spec.realm is missing"
        )

    if not alias:

        raise RuntimeError(
            f"Cannot delete Keycloak resource "
            f"'{resource_name}': spec.alias is missing"
        )

    # -------------------------------------------------------------------------
    # Delete from Keycloak.
    #
    # This function:
    #   1. Finds the flow by alias.
    #   2. Gets its ID.
    #   3. Deletes using the ID.
    #   4. Verifies that the flow disappeared.
    #
    # If any step fails, an exception is raised and the finalizer remains.
    # -------------------------------------------------------------------------

    delete_keycloak_flow(
        realm,
        alias,
    )

    # -------------------------------------------------------------------------
    # Only remove finalizer AFTER Keycloak deletion is confirmed.
    # -------------------------------------------------------------------------

    print(
        f"[DEBUG] Keycloak deletion confirmed "
        f"for resource '{resource_name}'"
    )

    remove_finalizer(
        resource
    )

    print(
        f"[DEBUG] Finalizer removed. "
        f"Kubernetes can now delete '{resource_name}'."
    )


# =============================================================================
# Main reconciliation
# =============================================================================

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

        deletion_timestamp = metadata.get(
            "deletionTimestamp"
        )

        realm = spec.get(
            "realm"
        )

        alias = spec.get(
            "alias"
        )

        print()
        print("=" * 80)
        print(
            f"Resource: {resource_name}"
        )

        if deletion_timestamp:

            print(
                f"[DEBUG] Resource '{resource_name}' "
                f"is being deleted"
            )

        else:

            print(
                f"Realm:    {realm}"
            )

            print(
                f"Flow:     {alias}"
            )

        print("=" * 80)

        # =====================================================================
        # DELETION PATH
        # =====================================================================

        if deletion_timestamp:

            try:

                reconcile_resource_deletion(
                    resource
                )

                print(
                    f"Successfully deleted "
                    f"Keycloak resources for "
                    f"'{resource_name}'"
                )

            except Exception as exc:

                print(
                    f"ERROR deleting "
                    f"{resource_name}: {exc}",
                    file=sys.stderr,
                )

                print(
                    f"[DEBUG] Finalizer will remain. "
                    f"Deletion will be retried."
                )

                success = False

            continue

        # =====================================================================
        # NORMAL RECONCILIATION
        # =====================================================================

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

            # -----------------------------------------------------------------
            # Add finalizer BEFORE managing Keycloak.
            # -----------------------------------------------------------------

            ensure_finalizer(
                resource
            )

            # -----------------------------------------------------------------
            # Reconcile Keycloak.
            # -----------------------------------------------------------------

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
# Entry point
# =============================================================================

def main():

    load_kubernetes()

    while True:

        try:

            authenticate()

            reconcile()

        except Exception as exc:

            print(
                f"Reconciliation failed: "
                f"{exc}",
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
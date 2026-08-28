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

# -------------------------------------------------------------------------
# KeycloakRealm CRD
# -------------------------------------------------------------------------

CRD_GROUP = "neteye.cloud"
CRD_VERSION = "v1"
CRD_PLURAL = "keycloakrealms"

FINALIZER = "keycloakrealm.v1.edp.epam.com/finalizer"

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


def custom_objects_api():
    return client.CustomObjectsApi()


def get_realms():
    api = custom_objects_api()

    result = api.list_namespaced_custom_object(
        group=CRD_GROUP,
        version=CRD_VERSION,
        namespace=NAMESPACE,
        plural=CRD_PLURAL,
    )

    items = result.get("items", [])

    print(
        f"[DEBUG] Kubernetes namespace={NAMESPACE}, "
        f"found {len(items)} KeycloakRealm(s)"
    )

    return items


# =============================================================================
# Finalizers
# =============================================================================

def add_finalizer(resource):
    api = custom_objects_api()

    metadata = resource.get("metadata", {})
    name = metadata["name"]
    namespace = metadata.get("namespace", NAMESPACE)

    finalizers = list(
        metadata.get("finalizers", [])
    )

    if FINALIZER in finalizers:
        return

    finalizers.append(FINALIZER)

    print(
        f"[DEBUG] Adding finalizer '{FINALIZER}' "
        f"to KeycloakRealm '{name}'"
    )

    api.patch_namespaced_custom_object(
        group=CRD_GROUP,
        version=CRD_VERSION,
        namespace=namespace,
        plural=CRD_PLURAL,
        name=name,
        body={
            "metadata": {
                "finalizers": finalizers,
            }
        },
    )


def remove_finalizer(resource):
    api = custom_objects_api()

    metadata = resource.get("metadata", {})
    name = metadata["name"]
    namespace = metadata.get("namespace", NAMESPACE)

    finalizers = [
        value
        for value in metadata.get("finalizers", [])
        if value != FINALIZER
    ]

    print(
        f"[DEBUG] Removing finalizer '{FINALIZER}' "
        f"from KeycloakRealm '{name}'"
    )

    api.patch_namespaced_custom_object(
        group=CRD_GROUP,
        version=CRD_VERSION,
        namespace=namespace,
        plural=CRD_PLURAL,
        name=name,
        body={
            "metadata": {
                "finalizers": finalizers,
            }
        },
    )


# =============================================================================
# Status
# =============================================================================

def update_status(resource, available, value):
    api = custom_objects_api()

    metadata = resource.get(
        "metadata",
        {},
    )

    name = metadata["name"]
    namespace = metadata.get(
        "namespace",
        NAMESPACE,
    )

    body = {
        "status": {
            "available": available,
            "value": value,
        }
    }

    print(
        f"[DEBUG] Updating status for '{name}': "
        f"available={available}, value='{value}'"
    )

    try:
        api.patch_namespaced_custom_object_status(
            group=CRD_GROUP,
            version=CRD_VERSION,
            namespace=namespace,
            plural=CRD_PLURAL,
            name=name,
            body=body,
        )

    except Exception as exc:
        # Status must never prevent reconciliation.
        print(
            f"[DEBUG] Failed to update status for '{name}': {exc}",
            file=sys.stderr,
        )


# =============================================================================
# Keycloak authentication
# =============================================================================

def authenticate():
    global TOKEN

    url = (
        f"{KEYCLOAK_URL}/realms/"
        f"{quote(KEYCLOAK_TOKEN_REALM, safe='')}"
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
        f"{response.status_code} {response.reason}"
    )

    if response.text:
        print(
            f"[DEBUG] Response body: "
            f"{response.text}"
        )

    # Re-authenticate once if the token expired.
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
            f"{response.status_code} {response.reason}"
        )

        if response.text:
            print(
                f"[DEBUG] Retry response body: "
                f"{response.text}"
            )

    return response


# =============================================================================
# Keycloak Realm API
# =============================================================================

def realm_path(realm):
    return (
        f"/admin/realms/"
        f"{quote(realm, safe='')}"
    )


def get_realm(realm):
    response = kc_request(
        "GET",
        realm_path(realm),
    )

    if response.status_code == 404:
        print(
            f"[DEBUG] Realm '{realm}' does not exist"
        )
        return None

    if not response.ok:
        raise RuntimeError(
            f"GET realm '{realm}' failed: "
            f"{response.status_code}: {response.text}"
        )

    return response.json()


def create_realm(realm, desired):
    print()
    print("=" * 80)
    print("[DEBUG] CREATE REALM")
    print("=" * 80)
    print(f"[DEBUG] Realm: {realm}")

    # spec.realm identifies the Keycloak realm.
    #
    # All other fields declared in spec are copied into the
    # Keycloak RealmRepresentation.
    payload = {
        "realm": realm,
    }

    apply_desired_fields(
        payload,
        desired,
    )

    print(
        f"[DEBUG] Realm creation payload: {payload}"
    )

    response = kc_request(
        "POST",
        "/admin/realms",
        json=payload,
    )

    if response.status_code == 409:
        print(
            f"[DEBUG] Realm '{realm}' already exists "
            f"(409)"
        )
        return

    if not response.ok:
        raise RuntimeError(
            f"Creating realm '{realm}' failed: "
            f"{response.status_code}: {response.text}"
        )

    print(
        f"[DEBUG] Realm '{realm}' created successfully"
    )


def delete_realm(realm):
    print()
    print("=" * 80)
    print("[DEBUG] DELETE REALM")
    print("=" * 80)
    print(f"[DEBUG] Realm: {realm}")

    response = kc_request(
        "DELETE",
        realm_path(realm),
    )

    # Already gone is a successful desired state.
    if response.status_code == 404:
        print(
            f"[DEBUG] Realm '{realm}' is already deleted"
        )
        return

    if not response.ok:
        raise RuntimeError(
            f"Deleting realm '{realm}' failed: "
            f"{response.status_code}: {response.text}"
        )

    print(
        f"[DEBUG] Realm '{realm}' deleted successfully"
    )


# =============================================================================
# Desired-state conversion
# =============================================================================

def apply_desired_fields(payload, spec):
    """
    Convert the CR spec into a Keycloak RealmRepresentation.

    The CR is the source of truth.

    spec.realm identifies the Keycloak realm and is handled separately.

    Every other field in spec is considered desired Keycloak realm state.

    There is intentionally no hard-coded whitelist here.

    Therefore, if the CRD contains a new Keycloak RealmRepresentation
    field, the controller will automatically pass it to Keycloak.
    """

    for key, value in spec.items():

        # spec.realm identifies the realm and is handled separately.
        if key == "realm":
            continue

        payload[key] = value


def desired_payload(spec):
    """
    Build the desired Keycloak RealmRepresentation.

    Every field declared in spec, except spec.realm, is considered
    desired state.
    """

    payload = {}

    apply_desired_fields(
        payload,
        spec,
    )

    return payload


# =============================================================================
# Realm reconciliation
# =============================================================================

def reconcile_realm(realm, spec):
    print()
    print("=" * 80)
    print("[DEBUG] RECONCILE REALM")
    print("=" * 80)
    print(f"[DEBUG] Realm: {realm}")
    print(f"[DEBUG] Desired spec: {spec}")
    print("=" * 80)

    current = get_realm(realm)

    # -------------------------------------------------------------------------
    # CREATE
    # -------------------------------------------------------------------------

    if current is None:
        create_realm(
            realm,
            spec,
        )

        current = get_realm(
            realm
        )

        if current is None:
            raise RuntimeError(
                f"Realm '{realm}' was created but "
                f"cannot be read afterwards"
            )

        return

    # -------------------------------------------------------------------------
    # UPDATE
    # -------------------------------------------------------------------------

    desired = desired_payload(
        spec
    )

    changes = {}

    for key, desired_value in desired.items():

        current_value = current.get(
            key
        )

        if current_value != desired_value:
            changes[key] = {
                "current": current_value,
                "desired": desired_value,
            }

    if not changes:
        print(
            f"[DEBUG] Realm '{realm}' is already "
            f"in the desired state"
        )
        return

    print(
        f"[DEBUG] Realm '{realm}' requires "
        f"{len(changes)} change(s)"
    )

    for key, change in changes.items():
        print(
            f"[DEBUG]   {key}: "
            f"{change['current']} -> "
            f"{change['desired']}"
        )

    # -------------------------------------------------------------------------
    # Build the updated RealmRepresentation.
    #
    # Start with the current Keycloak representation and overwrite only
    # fields explicitly declared in the CR.
    #
    # This preserves Keycloak-managed/unmanaged fields that are not part
    # of the CR.
    # -------------------------------------------------------------------------

    updated = dict(
        current
    )

    for key, desired_value in desired.items():
        updated[key] = desired_value

    # Keep important server-managed fields if Keycloak returned them.
    #
    # They are not part of the desired state, but retaining them in the
    # RealmRepresentation avoids accidentally losing information during
    # the PUT.
    for field in (
        "id",
        "realm",
        "notBefore",
    ):
        if field in current:
            updated[field] = current[field]

    print(
        f"[DEBUG] Updated realm payload: "
        f"{updated}"
    )

    response = kc_request(
        "PUT",
        realm_path(realm),
        json=updated,
    )

    if not response.ok:
        raise RuntimeError(
            f"Updating realm '{realm}' failed: "
            f"{response.status_code}: {response.text}"
        )

    print(
        f"[DEBUG] Realm '{realm}' updated successfully"
    )


# =============================================================================
# Individual CR reconciliation
# =============================================================================

def reconcile_resource(resource):
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

    # spec.realm is the only field that has special meaning to the
    # controller. It identifies which Keycloak realm this CR manages.
    realm = spec.get(
        "realm"
    )

    print()
    print("=" * 80)
    print(f"Resource: {resource_name}")
    print(f"Realm:    {realm}")
    print("=" * 80)

    if not realm:
        raise RuntimeError(
            "spec.realm is missing"
        )

    # -------------------------------------------------------------------------
    # Deletion
    # -------------------------------------------------------------------------

    deletion_timestamp = metadata.get(
        "deletionTimestamp"
    )

    if deletion_timestamp:

        print(
            f"[DEBUG] Resource '{resource_name}' "
            f"is being deleted"
        )

        try:
            delete_realm(
                realm
            )

            remove_finalizer(
                resource
            )

            print(
                f"Successfully deleted realm "
                f"'{realm}' for resource "
                f"'{resource_name}'"
            )

        except Exception:
            # Keep the finalizer.
            #
            # Kubernetes will retain the resource and the controller
            # will retry deletion during the next reconciliation.
            raise

        return

    # -------------------------------------------------------------------------
    # Normal reconciliation
    # -------------------------------------------------------------------------

    if FINALIZER not in metadata.get(
        "finalizers",
        [],
    ):
        add_finalizer(
            resource
        )

    reconcile_realm(
        realm,
        spec,
    )

    update_status(
        resource,
        available=True,
        value="Ready",
    )

    print(
        f"Successfully reconciled "
        f"{resource_name}"
    )


# =============================================================================
# Reconciliation loop
# =============================================================================

def reconcile():
    resources = get_realms()

    print(
        f"Found {len(resources)} "
        f"KeycloakRealm resource(s)"
    )

    success = True

    for resource in resources:

        metadata = resource.get(
            "metadata",
            {},
        )

        resource_name = metadata.get(
            "name",
            "<unknown>",
        )

        try:
            reconcile_resource(
                resource
            )

        except Exception as exc:

            print(
                f"ERROR reconciling "
                f"{resource_name}: {exc}",
                file=sys.stderr,
            )

            update_status(
                resource,
                available=False,
                value="Error",
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
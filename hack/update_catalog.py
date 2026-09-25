#!/usr/bin/env python3

import argparse
import http.client
import json
import os
import re
import tempfile
from pathlib import Path

PACKAGE_NAME = "neteye-operator"
NETEYE_VERSION_HOST = "api.neteye.cloud"
NETEYE_VERSION_PATH = "/v2/config/version/latest"
SEMVER_PATTERN = re.compile(
    r"^(0|[1-9][0-9]*)\."
    r"(0|[1-9][0-9]*)\."
    r"(0|[1-9][0-9]*)"
    r"(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$"
)
BUNDLE_IMAGE_PATTERN = re.compile(
    r"^ghcr\.io/neteye-platform/neteye-operator-bundle@sha256:[0-9a-f]{64}$"
)
NIGHTLY_TAG_PATTERN = re.compile(r"^(?P<version>.+)-nightly-(?P<hash>[0-9a-f]{7,40})$")
NIGHTLY_IMAGE_PATTERN = re.compile(
    r"^ghcr\.io/neteye-platform/neteye-operator-bundle:(.+-nightly-[0-9a-f]{7,40})$"
)


def document_field(document: str, field: str) -> str | None:
    match = re.search(rf"^{re.escape(field)}: (.+)$", document, re.MULTILINE)
    return match.group(1) if match else None


def valid_semver(version: str) -> bool:
    if not SEMVER_PATTERN.fullmatch(version):
        return False
    if "-" not in version:
        return True
    prerelease = version.split("-", maxsplit=1)[1]
    return all(
        not (identifier.isdigit() and len(identifier) > 1 and identifier[0] == "0")
        for identifier in prerelease.split(".")
    )


def compare_semver(left: str, right: str) -> int:
    left_core, _, left_prerelease = left.partition("-")
    right_core, _, right_prerelease = right.partition("-")
    left_numbers = tuple(int(part) for part in left_core.split("."))
    right_numbers = tuple(int(part) for part in right_core.split("."))
    if left_numbers != right_numbers:
        return 1 if left_numbers > right_numbers else -1
    if not left_prerelease or not right_prerelease:
        if left_prerelease == right_prerelease:
            return 0
        return -1 if left_prerelease else 1

    left_identifiers = left_prerelease.split(".")
    right_identifiers = right_prerelease.split(".")
    for left_identifier, right_identifier in zip(
        left_identifiers, right_identifiers, strict=False
    ):
        if left_identifier == right_identifier:
            continue
        left_numeric = left_identifier.isdigit()
        right_numeric = right_identifier.isdigit()
        if left_numeric and right_numeric:
            return 1 if int(left_identifier) > int(right_identifier) else -1
        if left_numeric != right_numeric:
            return -1 if left_numeric else 1
        return 1 if left_identifier > right_identifier else -1

    if len(left_identifiers) == len(right_identifiers):
        return 0
    return 1 if len(left_identifiers) > len(right_identifiers) else -1


def atomic_write(path: Path, content: str) -> None:
    temporary_path: Path | None = None
    try:
        with tempfile.NamedTemporaryFile(
            mode="w", encoding="utf-8", dir=path.parent, delete=False
        ) as temporary_file:
            temporary_path = Path(temporary_file.name)
            temporary_file.write(content)
            temporary_file.flush()
            os.fsync(temporary_file.fileno())
        os.chmod(temporary_path, path.stat().st_mode)
        os.replace(temporary_path, path)
    finally:
        if temporary_path is not None:
            temporary_path.unlink(missing_ok=True)


def fetch_latest_neteye_version() -> str:
    connection = http.client.HTTPSConnection(NETEYE_VERSION_HOST, timeout=10)
    try:
        connection.request(
            "GET",
            NETEYE_VERSION_PATH,
            headers={"User-Agent": "neteye-operator-update-catalog"},
        )
        response = connection.getresponse()
        if response.status != http.HTTPStatus.OK:
            raise RuntimeError(
                f"failed to fetch latest NetEye version: HTTP {response.status}"
            )
        payload = json.load(response)
    finally:
        connection.close()
    return re.sub(r"-sr[0-9]+$", "", payload["version"])


def read_operator_version() -> str:
    makefile_path = Path(__file__).resolve().parent.parent / "src" / "Makefile"
    match = re.search(
        r"^VERSION \?= (\S+)$", makefile_path.read_text(encoding="utf-8"), re.MULTILINE
    )
    if not match:
        raise ValueError(f"VERSION not found in {makefile_path}")
    return match.group(1)


def update_catalog(
    catalog_path: Path,
    version: str,
    bundle_image: str,
    neteye_version: str | None = None,
) -> bool:
    if not valid_semver(version):
        raise ValueError(f"invalid release version: {version}")
    if not BUNDLE_IMAGE_PATTERN.fullmatch(bundle_image):
        raise ValueError(f"bundle image must be pinned by GHCR digest: {bundle_image}")

    channel = (
        f"{neteye_version}-stable"
        if neteye_version
        else ("alpha" if "-" in version else "stable")
    )
    bundle_name = f"{PACKAGE_NAME}.{version}"
    original = catalog_path.read_text(encoding="utf-8")
    documents = original.rstrip("\n").split("\n---\n")

    package_documents = [
        document
        for document in documents
        if document_field(document, "schema") == "olm.package"
        and document_field(document, "name") == PACKAGE_NAME
    ]
    if len(package_documents) != 1:
        raise ValueError(f"expected exactly one {PACKAGE_NAME} package document")

    existing_bundles = [
        document
        for document in documents
        if document_field(document, "schema") == "olm.bundle"
        and document_field(document, "name") == bundle_name
    ]
    if existing_bundles:
        if len(existing_bundles) != 1:
            raise ValueError(f"multiple bundle documents found for {bundle_name}")
        existing_image = document_field(existing_bundles[0], "image")
        if existing_image != bundle_image:
            raise ValueError(
                f"{bundle_name} already references {existing_image}; refusing to replace it"
            )
        return False

    channel_indexes = [
        index
        for index, document in enumerate(documents)
        if document_field(document, "schema") == "olm.channel"
        and document_field(document, "package") == PACKAGE_NAME
        and document_field(document, "name") == channel
    ]
    if len(channel_indexes) > 1:
        raise ValueError(f"expected at most one {PACKAGE_NAME} {channel} channel")
    if channel_indexes:
        channel_index = channel_indexes[0]
        channel_document = documents[channel_index]
    else:
        channel_index = len(documents)
        channel_document = "\n".join(
            [
                "schema: olm.channel",
                f"package: {PACKAGE_NAME}",
                f"name: {channel}",
                "entries:",
            ]
        )
        documents.append(channel_document)

    if neteye_version:
        package_index = next(
            index
            for index, document in enumerate(documents)
            if document_field(document, "schema") == "olm.package"
            and document_field(document, "name") == PACKAGE_NAME
        )
        documents[package_index] = re.sub(
            r"^defaultChannel: \S+$",
            f"defaultChannel: {channel}",
            documents[package_index],
            flags=re.MULTILINE,
        )

    previous_entries = re.findall(r"^  - name: (\S+)$", channel_document, re.MULTILINE)
    if bundle_name in previous_entries:
        raise ValueError(f"channel {channel} already contains {bundle_name}")

    prefix = f"{PACKAGE_NAME}."
    for entry in previous_entries:
        if not entry.startswith(prefix):
            raise ValueError(f"unexpected bundle in {channel} channel: {entry}")
        entry_version = entry.removeprefix(prefix)
        if not valid_semver(entry_version):
            raise ValueError(
                f"invalid bundle version in {channel} channel: {entry_version}"
            )

    previous_version = None
    if previous_entries:
        if re.search(r"^    skipRange:", channel_document, re.MULTILINE):
            raise ValueError(
                f"channel {channel} uses unsupported skipRange entries; "
                "use explicit replaces or skips edges"
            )
        replaced_entries = set(
            re.findall(r"^    replaces: (\S+)$", channel_document, re.MULTILINE)
        )
        skipped_entries = set(
            re.findall(r"^      - (\S+)$", channel_document, re.MULTILINE)
        )
        channel_heads = set(previous_entries) - replaced_entries - skipped_entries
        if len(channel_heads) != 1:
            raise ValueError(
                f"expected exactly one {channel} channel head, found: "
                f"{', '.join(sorted(channel_heads)) or 'none'}"
            )
        previous_entry = channel_heads.pop()
        previous_version = previous_entry.removeprefix(prefix)
        if compare_semver(version, previous_version) <= 0:
            raise ValueError(
                f"release {version} must be newer than {channel} channel version "
                f"{previous_version}"
            )

    entry_lines = [f"  - name: {bundle_name}"]
    if previous_version is not None:
        entry_lines.append(f"    replaces: {PACKAGE_NAME}.{previous_version}")
    documents[channel_index] = f"{channel_document}\n" + "\n".join(entry_lines)

    bundle_document = "\n".join(
        [
            "schema: olm.bundle",
            f"name: {bundle_name}",
            f"package: {PACKAGE_NAME}",
            f"image: {bundle_image}",
            "properties:",
            "  - type: olm.gvk",
            "    value:",
            "      group: operators.coreos.com",
            "      kind: ClusterServiceVersion",
            "      version: v1alpha1",
            "  - type: olm.package",
            "    value:",
            f"      packageName: {PACKAGE_NAME}",
            f"      version: {version}",
        ]
    )
    documents.append(bundle_document)

    atomic_write(catalog_path, "\n---\n".join(documents) + "\n")
    return True


def release_minor(version: str) -> str:
    core = version.partition("-")[0]
    major, minor, _ = core.split(".", maxsplit=2)
    return f"{major}.{minor}"


def update_catalog_backport(
    catalog_path: Path, version: str, bundle_image: str
) -> bool:
    """Add a bugfix release to every channel whose head is on the same minor line."""
    if not valid_semver(version):
        raise ValueError(f"invalid release version: {version}")
    if not BUNDLE_IMAGE_PATTERN.fullmatch(bundle_image):
        raise ValueError(f"bundle image must be pinned by GHCR digest: {bundle_image}")

    prefix = f"{PACKAGE_NAME}."
    bundle_name = f"{prefix}{version}"
    minor = release_minor(version)
    original = catalog_path.read_text(encoding="utf-8")
    documents = original.rstrip("\n").split("\n---\n")

    existing_bundles = [
        document
        for document in documents
        if document_field(document, "schema") == "olm.bundle"
        and document_field(document, "name") == bundle_name
    ]
    if existing_bundles:
        if len(existing_bundles) != 1:
            raise ValueError(f"multiple bundle documents found for {bundle_name}")
        existing_image = document_field(existing_bundles[0], "image")
        if existing_image != bundle_image:
            raise ValueError(
                f"{bundle_name} already references {existing_image}; refusing to replace it"
            )
        return False

    matched_channels: list[tuple[int, str]] = []
    for index, document in enumerate(documents):
        if (
            document_field(document, "schema") != "olm.channel"
            or document_field(document, "package") != PACKAGE_NAME
        ):
            continue
        name = document_field(document, "name")
        entries = re.findall(r"^  - name: (\S+)$", document, re.MULTILINE)
        if not entries:
            continue
        replaced_entries = set(
            re.findall(r"^    replaces: (\S+)$", document, re.MULTILINE)
        )
        skipped_entries = set(re.findall(r"^      - (\S+)$", document, re.MULTILINE))
        heads = set(entries) - replaced_entries - skipped_entries
        if len(heads) != 1:
            raise ValueError(
                f"expected exactly one {name} channel head, found: "
                f"{', '.join(sorted(heads)) or 'none'}"
            )
        head = heads.pop()
        if not head.startswith(prefix):
            raise ValueError(f"unexpected bundle in {name} channel: {head}")
        head_version = head.removeprefix(prefix)
        if not valid_semver(head_version):
            raise ValueError(
                f"invalid bundle version in {name} channel: {head_version}"
            )
        if release_minor(head_version) != minor:
            continue
        if compare_semver(version, head_version) <= 0:
            raise ValueError(
                f"backport {version} must be newer than {name} channel head {head_version}"
            )
        matched_channels.append((index, head))

    if not matched_channels:
        raise ValueError(
            f"no channel currently has a {PACKAGE_NAME} {minor}.x head; nothing to backport"
        )

    for index, head in matched_channels:
        entry_lines = [f"  - name: {bundle_name}", f"    replaces: {head}"]
        documents[index] = documents[index].rstrip("\n") + "\n" + "\n".join(entry_lines)

    bundle_document = "\n".join(
        [
            "schema: olm.bundle",
            f"name: {bundle_name}",
            f"package: {PACKAGE_NAME}",
            f"image: {bundle_image}",
            "properties:",
            "  - type: olm.gvk",
            "    value:",
            "      group: operators.coreos.com",
            "      kind: ClusterServiceVersion",
            "      version: v1alpha1",
            "  - type: olm.package",
            "    value:",
            f"      packageName: {PACKAGE_NAME}",
            f"      version: {version}",
        ]
    )
    documents.append(bundle_document)

    atomic_write(catalog_path, "\n---\n".join(documents) + "\n")
    return True


def update_nightly_channel(
    catalog_path: Path, bundle_image: str, neteye_version: str
) -> bool:
    """Append the newest bundle to the rolling nightly channel.

    Stable channels only advance when a tagged release is promoted by the
    build-and-test workflow, never by the nightly build.
    """
    match = NIGHTLY_IMAGE_PATTERN.fullmatch(bundle_image)
    if not match:
        raise ValueError(
            "nightly bundle image must match "
            "ghcr.io/neteye-platform/neteye-operator-bundle:<version>-nightly-<hash>: "
            f"{bundle_image}"
        )
    tag = match.group(1)
    tag_match = NIGHTLY_TAG_PATTERN.fullmatch(tag)
    if not tag_match or not valid_semver(tag_match.group("version")):
        raise ValueError(f"invalid nightly tag: {tag}")

    channel = f"{neteye_version}-nightly"
    bundle_name = f"{PACKAGE_NAME}.{tag}"
    original = catalog_path.read_text(encoding="utf-8")
    documents = original.rstrip("\n").split("\n---\n")

    changed = False
    channel_indexes = [
        index
        for index, document in enumerate(documents)
        if document_field(document, "schema") == "olm.channel"
        and document_field(document, "package") == PACKAGE_NAME
        and document_field(document, "name") == channel
    ]
    if len(channel_indexes) > 1:
        raise ValueError(f"expected at most one {channel} channel document")

    if channel_indexes:
        channel_index = channel_indexes[0]
        previous_entries = re.findall(
            r"^  - name: (\S+)$", documents[channel_index], re.MULTILINE
        )
        if previous_entries != [bundle_name]:
            new_entry_lines = [
                f"  - name: {bundle_name}",
                f"    replaces: {previous_entries[-1]}",
            ]
            if len(previous_entries) > 1:
                new_entry_lines.extend(
                    [
                        "    skips:",
                        *[f"      - {entry}" for entry in previous_entries[:-1]],
                    ]
                )
            documents[channel_index] = (
                documents[channel_index].rstrip("\n")
                + "\n"
                + "\n".join(new_entry_lines)
            )
            changed = True
    else:
        documents.append(
            "\n".join(
                [
                    "schema: olm.channel",
                    f"package: {PACKAGE_NAME}",
                    f"name: {channel}",
                    "entries:",
                    f"  - name: {bundle_name}",
                ]
            )
        )
        changed = True

    existing_bundles = [
        document
        for document in documents
        if document_field(document, "schema") == "olm.bundle"
        and document_field(document, "name") == bundle_name
    ]
    if not existing_bundles:
        bundle_document = "\n".join(
            [
                "schema: olm.bundle",
                f"name: {bundle_name}",
                f"package: {PACKAGE_NAME}",
                f"image: {bundle_image}",
                "properties:",
                "  - type: olm.gvk",
                "    value:",
                "      group: operators.coreos.com",
                "      kind: ClusterServiceVersion",
                "      version: v1alpha1",
                "  - type: olm.package",
                "    value:",
                f"      packageName: {PACKAGE_NAME}",
                f"      version: {tag}",
            ]
        )
        documents.append(bundle_document)

    atomic_write(catalog_path, "\n---\n".join(documents) + "\n")
    return changed


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Add a release to the NetEye OLM catalog"
    )
    parser.add_argument("--catalog", required=True, type=Path)
    parser.add_argument("--version")
    parser.add_argument("--bundle-image", required=True)
    parser.add_argument(
        "--neteye-version",
        required=False,
        help=(
            "NetEye release line for the versioned channel, e.g. 4.50; "
            "defaults to querying https://api.neteye.cloud/v2/config/version/latest"
        ),
    )
    parser.add_argument(
        "--nightly",
        action="store_true",
        help="update the rolling nightly channel instead of stable/alpha",
    )
    parser.add_argument(
        "--backport",
        action="store_true",
        help=(
            "add a bugfix release only to channels whose current head is on the "
            "same major.minor line, instead of advancing stable/alpha"
        ),
    )
    args = parser.parse_args()
    if args.nightly and args.backport:
        parser.error("--nightly and --backport are mutually exclusive")
    if not args.nightly and not args.version:
        parser.error("--version is required unless --nightly is set")
    return args


def main() -> None:
    args = parse_args()
    if args.nightly:
        neteye_version = args.neteye_version or fetch_latest_neteye_version()
        changed = update_nightly_channel(
            args.catalog, args.bundle_image, neteye_version
        )
        status = "updated" if changed else "already current"
        print(f"{neteye_version}-nightly catalog {status}: {args.bundle_image}")
        return
    if args.backport:
        changed = update_catalog_backport(args.catalog, args.version, args.bundle_image)
        status = "updated" if changed else "already current"
        print(f"backport catalog {status}: {PACKAGE_NAME}.{args.version}")
        return
    neteye_version = args.neteye_version or fetch_latest_neteye_version()
    changed = update_catalog(
        args.catalog, args.version, args.bundle_image, neteye_version
    )
    status = "updated" if changed else "already current"
    print(f"catalog {status}: {PACKAGE_NAME}.{args.version}")


if __name__ == "__main__":
    main()

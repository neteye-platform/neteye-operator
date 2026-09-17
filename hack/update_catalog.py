#!/usr/bin/env python3

import argparse
import json
import os
import re
import tempfile
import urllib.request
from pathlib import Path

PACKAGE_NAME = "neteye-operator"
NETEYE_LATEST_VERSION_URL = "https://api.neteye.cloud/v2/config/version/latest"
SEMVER_PATTERN = re.compile(
    r"^(0|[1-9][0-9]*)\."
    r"(0|[1-9][0-9]*)\."
    r"(0|[1-9][0-9]*)"
    r"(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$"
)
BUNDLE_IMAGE_PATTERN = re.compile(
    r"^ghcr\.io/neteye-platform/neteye-operator-bundle@sha256:[0-9a-f]{64}$"
)
NIGHTLY_TAG_PATTERN = re.compile(r"^nightly-[0-9a-f]{7,40}$")
NIGHTLY_IMAGE_PATTERN = re.compile(
    r"^ghcr\.io/neteye-platform/neteye-operator-bundle:(nightly-[0-9a-f]{7,40})$"
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


def fetch_latest_neteye_version(url: str = NETEYE_LATEST_VERSION_URL) -> str:
    with urllib.request.urlopen(url, timeout=10) as response:
        payload = json.load(response)
    return re.sub(r"-sr[0-9]+$", "", payload["version"])


def update_catalog(catalog_path: Path, version: str, bundle_image: str) -> bool:
    if not valid_semver(version):
        raise ValueError(f"invalid release version: {version}")
    if not BUNDLE_IMAGE_PATTERN.fullmatch(bundle_image):
        raise ValueError(f"bundle image must be pinned by GHCR digest: {bundle_image}")

    channel = "alpha" if "-" in version else "stable"
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
    if len(channel_indexes) != 1:
        raise ValueError(f"expected exactly one {PACKAGE_NAME} {channel} channel")

    channel_index = channel_indexes[0]
    channel_document = documents[channel_index]
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


def update_nightly_channel(
    catalog_path: Path, bundle_image: str, neteye_version: str
) -> bool:
    """Point the rolling nightly-<neteye_version> channel at the newest bundle.

    Unlike `stable`/`alpha`, this is not an upgrade graph: the channel always
    has exactly one entry (no `replaces` edges). Older nightly bundle
    documents are kept around (not pruned) for now.
    """
    match = NIGHTLY_IMAGE_PATTERN.fullmatch(bundle_image)
    if not match:
        raise ValueError(
            "nightly bundle image must match "
            f"ghcr.io/neteye-platform/neteye-operator-bundle:nightly-<hash>: {bundle_image}"
        )
    tag = match.group(1)
    if not NIGHTLY_TAG_PATTERN.fullmatch(tag):
        raise ValueError(f"invalid nightly tag: {tag}")

    channel = f"nightly-{neteye_version}"
    bundle_name = f"{PACKAGE_NAME}.{tag}"
    original = catalog_path.read_text(encoding="utf-8")
    documents = original.rstrip("\n").split("\n---\n")

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
        if previous_entries == [bundle_name]:
            return False
        documents[channel_index] = "\n".join(
            [
                "schema: olm.channel",
                f"package: {PACKAGE_NAME}",
                f"name: {channel}",
                "entries:",
                f"  - name: {bundle_name}",
            ]
        )
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
    return True


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
            "NetEye release line the nightly channel is scoped to, e.g. 4.50; "
            f"defaults to querying {NETEYE_LATEST_VERSION_URL}"
        ),
    )
    parser.add_argument(
        "--nightly",
        action="store_true",
        help="update the rolling nightly channel instead of stable/alpha",
    )
    args = parser.parse_args()
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
        print(f"nightly-{neteye_version} catalog {status}: {args.bundle_image}")
        return
    changed = update_catalog(args.catalog, args.version, args.bundle_image)
    status = "updated" if changed else "already current"
    print(f"catalog {status}: {PACKAGE_NAME}.{args.version}")


if __name__ == "__main__":
    main()

#!/usr/bin/env python3

import argparse
import os
import re
import tempfile
from functools import cmp_to_key
from pathlib import Path

PACKAGE_NAME = "neteye-operator"
SEMVER_PATTERN = re.compile(
    r"^(0|[1-9][0-9]*)\."
    r"(0|[1-9][0-9]*)\."
    r"(0|[1-9][0-9]*)"
    r"(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$"
)
BUNDLE_IMAGE_PATTERN = re.compile(
    r"^ghcr\.io/neteye-platform/neteye-operator-bundle@sha256:[0-9a-f]{64}$"
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

    previous_versions = []
    prefix = f"{PACKAGE_NAME}."
    for entry in previous_entries:
        if not entry.startswith(prefix):
            raise ValueError(f"unexpected bundle in {channel} channel: {entry}")
        entry_version = entry.removeprefix(prefix)
        if not valid_semver(entry_version):
            raise ValueError(
                f"invalid bundle version in {channel} channel: {entry_version}"
            )
        previous_versions.append(entry_version)

    previous_version = None
    if previous_versions:
        previous_version = max(previous_versions, key=cmp_to_key(compare_semver))
        if compare_semver(version, previous_version) <= 0:
            raise ValueError(
                f"release {version} must be newer than {channel} channel version "
                f"{previous_version}"
            )

    entry_lines = [f"  - name: {bundle_name}"]
    if previous_version is not None:
        entry_lines.extend(["    skips:", f"      - {PACKAGE_NAME}.{previous_version}"])
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


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Add a release to the NetEye OLM catalog"
    )
    parser.add_argument("--catalog", required=True, type=Path)
    parser.add_argument("--version", required=True)
    parser.add_argument("--bundle-image", required=True)
    return parser.parse_args()


def main() -> None:
    args = parse_args()
    changed = update_catalog(args.catalog, args.version, args.bundle_image)
    status = "updated" if changed else "already current"
    print(f"catalog {status}: {PACKAGE_NAME}.{args.version}")


if __name__ == "__main__":
    main()

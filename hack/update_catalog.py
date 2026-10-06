#!/usr/bin/env python3

import argparse
import os
import re
import tempfile
from dataclasses import dataclass, field
from pathlib import Path

import requests

PACKAGE_NAME = "neteye-operator"
NETEYE_VERSION_URL = "https://api.neteye.cloud/v2/config/version/latest"
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
NIGHTLY_VERSION_PATTERN = re.compile(r"-nightly-[0-9a-f]{7,40}$")
NIGHTLY_BUNDLES_FILE = "nightly.yaml"


def document_field(document: str, field_name: str) -> str | None:
    match = re.search(rf"^{re.escape(field_name)}: (.+)$", document, re.MULTILINE)
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
    path.parent.mkdir(parents=True, exist_ok=True)
    mode = path.stat().st_mode if path.exists() else None
    temporary_path: Path | None = None
    try:
        with tempfile.NamedTemporaryFile(
            mode="w", encoding="utf-8", dir=path.parent, delete=False
        ) as temporary_file:
            temporary_path = Path(temporary_file.name)
            temporary_file.write(content)
            temporary_file.flush()
            os.fsync(temporary_file.fileno())
        os.chmod(temporary_path, 0o644 if mode is None else mode)
        os.replace(temporary_path, path)
    finally:
        if temporary_path is not None:
            temporary_path.unlink(missing_ok=True)


def is_nightly_version(version: str) -> bool:
    return NIGHTLY_VERSION_PATTERN.search(version) is not None


def bundle_document(version: str, image: str) -> str:
    return "\n".join(
        [
            "schema: olm.bundle",
            f"name: {PACKAGE_NAME}.{version}",
            f"package: {PACKAGE_NAME}",
            f"image: {image}",
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


def fetch_latest_neteye_version() -> str:
    response = requests.get(
        NETEYE_VERSION_URL,
        headers={"User-Agent": "neteye-operator-update-catalog"},
        timeout=10,
    )
    response.raise_for_status()
    payload = response.json()
    return re.sub(r"-sr[0-9]+$", "", payload["version"])


@dataclass
class Entry:
    """One item of an olm.channel `entries` list."""

    name: str
    replaces: str | None = None
    skips: list[str] = field(default_factory=list)

    @property
    def version(self) -> str:
        return self.name.removeprefix(f"{PACKAGE_NAME}.")


@dataclass
class Channel:
    """An olm.channel blob, stored as one file in the catalog tree."""

    name: str
    entries: list[Entry] = field(default_factory=list)
    package: str = PACKAGE_NAME

    @classmethod
    def parse(cls, document: str, name: str) -> "Channel":
        if re.search(r"^    skipRange:", document, re.MULTILINE):
            raise ValueError(
                f"channel {name} uses unsupported skipRange entries; "
                "use explicit replaces or skips edges"
            )
        package = document_field(document, "package")
        if package is None:
            raise ValueError(f"channel {name} document is missing package")
        entries: list[Entry] = []
        for line in document.splitlines():
            if match := re.fullmatch(r"  - name: (\S+)", line):
                entries.append(Entry(match.group(1)))
            elif match := re.fullmatch(r"    replaces: (\S+)", line):
                entries[-1].replaces = match.group(1)
            elif match := re.fullmatch(r"      - (\S+)", line):
                entries[-1].skips.append(match.group(1))
        return cls(name=name, entries=entries, package=package)

    def render(self) -> str:
        lines = [
            "schema: olm.channel",
            f"package: {self.package}",
            f"name: {self.name}",
            "entries:",
        ]
        for entry in self.entries:
            lines.append(f"  - name: {entry.name}")
            if entry.replaces:
                lines.append(f"    replaces: {entry.replaces}")
            if entry.skips:
                lines.append("    skips:")
                lines += [f"      - {skip}" for skip in entry.skips]
        return "\n".join(lines) + "\n"

    def head(self) -> Entry | None:
        """The single entry that no other entry replaces or skips."""
        if not self.entries:
            return None
        replaced = {entry.replaces for entry in self.entries if entry.replaces}
        skipped = {skip for entry in self.entries for skip in entry.skips}
        heads = [
            entry
            for entry in self.entries
            if entry.name not in replaced and entry.name not in skipped
        ]
        if len(heads) != 1:
            raise ValueError(
                f"expected exactly one {self.name} channel head, found: "
                f"{', '.join(sorted(entry.name for entry in heads)) or 'none'}"
            )
        return heads[0]

    def validate_entry_versions(self) -> None:
        for entry in self.entries:
            if not entry.name.startswith(f"{PACKAGE_NAME}."):
                raise ValueError(
                    f"unexpected bundle in {self.name} channel: {entry.name}"
                )
            if not valid_semver(entry.version):
                raise ValueError(
                    f"invalid bundle version in {self.name} channel: {entry.version}"
                )


class Catalog:
    """A file-based catalog tree: <root>/<package>/{package.yaml,channels,bundles}."""

    def __init__(self, root: Path) -> None:
        self.root = root
        self.package_dir = root / PACKAGE_NAME
        self.package_file = self.package_dir / "package.yaml"
        self.channels_dir = self.package_dir / "channels"
        self.bundles_dir = self.package_dir / "bundles"
        self.nightly_bundles_file = self.bundles_dir / NIGHTLY_BUNDLES_FILE

    def read_package(self) -> str:
        if not self.package_file.is_file():
            raise ValueError(f"missing package document: {self.package_file}")
        return self.package_file.read_text(encoding="utf-8")

    def set_default_channel(self, channel: str) -> None:
        document = self.read_package()
        updated, count = re.subn(
            r"^defaultChannel: \S+$",
            f"defaultChannel: {channel}",
            document,
            flags=re.MULTILINE,
        )
        if count != 1:
            raise ValueError(f"expected one defaultChannel in {self.package_file}")
        if updated != document:
            atomic_write(self.package_file, updated)

    def channel_names(self) -> list[str]:
        if not self.channels_dir.is_dir():
            return []
        return sorted(path.stem for path in self.channels_dir.glob("*.yaml"))

    def channel_text(self, name: str) -> str | None:
        path = self.channels_dir / f"{name}.yaml"
        if not path.is_file():
            return None
        return path.read_text(encoding="utf-8")

    def read_channel(self, name: str) -> Channel | None:
        document = self.channel_text(name)
        if document is None:
            return None
        return Channel.parse(document, name)

    def write_channel(self, channel: Channel) -> None:
        atomic_write(self.channels_dir / f"{channel.name}.yaml", channel.render())

    def nightly_documents(self) -> list[str]:
        """Every olm.bundle blob in the shared nightly file, in order."""
        if not self.nightly_bundles_file.is_file():
            return []
        text = self.nightly_bundles_file.read_text(encoding="utf-8").rstrip("\n")
        return text.split("\n---\n") if text else []

    def bundle_path(self, version: str) -> Path:
        """The file holding this bundle.

        Releases are permanent and get a file each, so their history is
        reviewable on its own. Nightlies are high-volume and share one
        multi-document file, which keeps the directory from growing by a file
        per night. The release workflow never writes that file and the nightly
        workflow never writes a release file, so the two cannot conflict.
        """
        if is_nightly_version(version):
            return self.nightly_bundles_file
        return self.bundles_dir / f"{version}.yaml"

    def bundle_image(self, version: str) -> str | None:
        name = f"{PACKAGE_NAME}.{version}"
        if is_nightly_version(version):
            documents = [
                document
                for document in self.nightly_documents()
                if document_field(document, "name") == name
            ]
            if not documents:
                return None
            if len(documents) > 1:
                raise ValueError(f"multiple bundle documents found for {name}")
        else:
            path = self.bundle_path(version)
            if not path.is_file():
                return None
            documents = [path.read_text(encoding="utf-8")]
        image = document_field(documents[0], "image")
        if image is None:
            raise ValueError(f"bundle document has no image: {name}")
        return image

    def write_bundle(self, version: str, image: str) -> None:
        document = bundle_document(version, image)
        if is_nightly_version(version):
            documents = self.nightly_documents() + [document]
            atomic_write(self.nightly_bundles_file, "\n---\n".join(documents) + "\n")
        else:
            atomic_write(self.bundle_path(version), document + "\n")


def bundle_is_new(catalog: Catalog, version: str, bundle_image: str) -> bool:
    """False when the bundle already exists with exactly this image."""
    existing = catalog.bundle_image(version)
    if existing is None:
        return True
    if existing != bundle_image:
        raise ValueError(
            f"{PACKAGE_NAME}.{version} already references {existing}; "
            "refusing to replace it"
        )
    return False


def update_catalog(
    catalog_root: Path,
    version: str,
    bundle_image: str,
    neteye_version: str | None = None,
) -> bool:
    if not valid_semver(version):
        raise ValueError(f"invalid release version: {version}")
    if not BUNDLE_IMAGE_PATTERN.fullmatch(bundle_image):
        raise ValueError(f"bundle image must be pinned by GHCR digest: {bundle_image}")

    catalog = Catalog(catalog_root)
    catalog.read_package()
    channel_name = (
        f"{neteye_version}-stable"
        if neteye_version
        else ("alpha" if "-" in version else "stable")
    )
    bundle_name = f"{PACKAGE_NAME}.{version}"

    if not bundle_is_new(catalog, version, bundle_image):
        return False

    channel = catalog.read_channel(channel_name) or Channel(channel_name)
    if any(entry.name == bundle_name for entry in channel.entries):
        raise ValueError(f"channel {channel_name} already contains {bundle_name}")
    channel.validate_entry_versions()

    head = channel.head()
    if head is not None and compare_semver(version, head.version) <= 0:
        raise ValueError(
            f"release {version} must be newer than {channel_name} channel version "
            f"{head.version}"
        )

    channel.entries.append(
        Entry(bundle_name, replaces=head.name if head is not None else None)
    )
    catalog.write_channel(channel)
    catalog.write_bundle(version, bundle_image)
    if neteye_version:
        catalog.set_default_channel(channel_name)
    return True


def release_minor(version: str) -> str:
    core = version.partition("-")[0]
    major, minor, _ = core.split(".", maxsplit=2)
    return f"{major}.{minor}"


def update_catalog_backport(
    catalog_root: Path, version: str, bundle_image: str
) -> bool:
    """Add a bugfix release to every channel whose head is on the same minor line."""
    if not valid_semver(version):
        raise ValueError(f"invalid release version: {version}")
    if not BUNDLE_IMAGE_PATTERN.fullmatch(bundle_image):
        raise ValueError(f"bundle image must be pinned by GHCR digest: {bundle_image}")

    catalog = Catalog(catalog_root)
    bundle_name = f"{PACKAGE_NAME}.{version}"
    minor = release_minor(version)

    if not bundle_is_new(catalog, version, bundle_image):
        return False

    matched: list[Channel] = []
    for name in catalog.channel_names():
        channel = catalog.read_channel(name)
        if channel is None or not channel.entries:
            continue
        channel.validate_entry_versions()
        head = channel.head()
        if head is None or release_minor(head.version) != minor:
            continue
        if compare_semver(version, head.version) <= 0:
            raise ValueError(
                f"backport {version} must be newer than {name} channel head "
                f"{head.version}"
            )
        matched.append(channel)

    if not matched:
        raise ValueError(
            f"no channel currently has a {PACKAGE_NAME} {minor}.x head; "
            "nothing to backport"
        )

    for channel in matched:
        channel.entries.append(Entry(bundle_name, replaces=channel.head().name))
        catalog.write_channel(channel)
    catalog.write_bundle(version, bundle_image)
    return True


def update_nightly_channel(
    catalog_root: Path, bundle_image: str, neteye_version: str
) -> bool:
    """Append the newest bundle to the rolling nightly channel.

    The head is the only entry carrying upgrade edges: it replaces the previous
    head and skips everything before that, so an installation can move to the
    newest nightly from any older one. Earlier entries are rewritten as plain
    names, which keeps the channel linear in size instead of repeating the full
    predecessor list on every entry.

    Stable channels only advance when a tagged release is promoted by the
    release workflow, never by the nightly build.
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

    catalog = Catalog(catalog_root)
    channel_name = f"{neteye_version}-nightly"
    bundle_name = f"{PACKAGE_NAME}.{tag}"
    channel = catalog.read_channel(channel_name) or Channel(channel_name)

    names = [entry.name for entry in channel.entries if entry.name != bundle_name]
    names.append(bundle_name)
    entries = [Entry(name) for name in names]
    if len(names) >= 2:
        entries[-1].replaces = names[-2]
    if len(names) >= 3:
        entries[-1].skips = names[:-2]
    rewritten = Channel(channel_name, entries)

    changed = rewritten.render() != catalog.channel_text(channel_name)
    if changed:
        catalog.write_channel(rewritten)

    if catalog.bundle_image(tag) is None:
        catalog.write_bundle(tag, bundle_image)
    return changed


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Add a release to the NetEye OLM catalog"
    )
    parser.add_argument(
        "--catalog",
        required=True,
        type=Path,
        help="path to the file-based catalog root, e.g. ./catalog/catalog",
    )
    parser.add_argument("--version")
    parser.add_argument("--bundle-image", required=True)
    parser.add_argument(
        "--neteye-version",
        required=False,
        help=(
            "NetEye release line for the versioned channel, e.g. 4.51; "
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

#!/usr/bin/env python3
"""Advance the operator's supported NetEye release window."""

from __future__ import annotations

import argparse
import re
from pathlib import Path

VERSION_PATTERN = re.compile(r"^(?P<major>\d+)\.(?P<minor>\d+)$")


def replace_once(source: str, old: str, new: str, description: str) -> str:
    if source.count(old) != 1:
        raise ValueError(f"expected exactly one {description} reference")
    return source.replace(old, new, 1)


def update_source(source: str, target_version: str) -> str:
    target_match = VERSION_PATTERN.fullmatch(target_version)
    if target_match is None:
        raise ValueError(f"invalid NetEye release line: {target_version}")

    current_matches = re.findall(
        r'^\s*CurrentNetEyeVersion\s*=\s*"([0-9]+\.[0-9]+)"\s*$',
        source,
        re.MULTILINE,
    )
    previous_matches = re.findall(
        r'^\s*PreviousNetEyeVersion\s*=\s*"([0-9]+\.[0-9]+)"\s*$',
        source,
        re.MULTILINE,
    )
    if len(current_matches) != 1 or len(previous_matches) != 1:
        raise ValueError(
            "expected exactly one CurrentNetEyeVersion and PreviousNetEyeVersion"
        )

    current_version = current_matches[0]
    current_match = VERSION_PATTERN.fullmatch(current_version)
    assert current_match is not None
    current_key = (int(current_match["major"]), int(current_match["minor"]))
    target_key = (int(target_match["major"]), int(target_match["minor"]))
    if target_key == current_key:
        return source
    if target_key < current_key:
        raise ValueError(
            f"target NetEye version {target_version} must be newer than {current_version}"
        )

    map_match = re.search(
        r"(?ms)(^var netEyeVersionMap = map\[string\]NetEyeComponents\{\n)"
        r"(.*?)"
        r"(^\})",
        source,
    )
    if map_match is None:
        raise ValueError("could not locate netEyeVersionMap")

    entries = re.findall(
        r"^\s*(CurrentNetEyeVersion|PreviousNetEyeVersion):\s*(\{[^\n]+\}),?\s*$",
        map_match.group(2),
        re.MULTILINE,
    )
    current_images = [
        value for name, value in entries if name == "CurrentNetEyeVersion"
    ]
    if len(current_images) != 1:
        raise ValueError(
            "expected exactly one current component image set in netEyeVersionMap"
        )

    updated_map = (
        f"{map_match.group(1)}"
        f"\tPreviousNetEyeVersion: {current_images[0]},\n"
        f"\tCurrentNetEyeVersion: {current_images[0]},\n"
        f"{map_match.group(3)}"
    )
    updated_source = (
        source[: map_match.start()] + updated_map + source[map_match.end() :]
    )
    updated_source, current_count = re.subn(
        r'(^\s*CurrentNetEyeVersion\s*=\s*)"[0-9]+\.[0-9]+"\s*$',
        rf'\g<1>"{target_version}"',
        updated_source,
        count=1,
        flags=re.MULTILINE,
    )
    updated_source, previous_count = re.subn(
        r'(^\s*PreviousNetEyeVersion\s*=\s*)"[0-9]+\.[0-9]+"\s*$',
        rf'\g<1>"{current_version}"',
        updated_source,
        count=1,
        flags=re.MULTILINE,
    )
    if current_count != 1 or previous_count != 1:
        raise ValueError("failed to update supported NetEye version constants")
    return updated_source


def update_repository(repo_root: Path, target_version: str) -> bool:
    source_path = repo_root / "src/api/v1alpha1/neteyeconfig_types.go"
    source = source_path.read_text(encoding="utf-8")
    current_match = re.search(
        r'^\s*CurrentNetEyeVersion\s*=\s*"([0-9]+\.[0-9]+)"\s*$',
        source,
        re.MULTILINE,
    )
    if current_match is None:
        raise ValueError("could not locate CurrentNetEyeVersion")
    current_version = current_match.group(1)
    updated_source = update_source(source, target_version)
    if updated_source == source:
        return False
    updated_source = replace_once(
        updated_source,
        f'e.g. "{current_version}"',
        f'e.g. "{target_version}"',
        "Go version example",
    )
    source_path.write_text(updated_source, encoding="utf-8")

    target_match = VERSION_PATTERN.fullmatch(target_version)
    assert target_match is not None
    next_version = f"{target_match['major']}.{int(target_match['minor']) + 1}"
    adr_path = repo_root / "docs/adr/0003-neteye-and-operator-version-model.md"
    adr = adr_path.read_text(encoding="utf-8")
    adr = replace_once(
        adr,
        f"`stable-{current_version}`",
        f"`stable-{target_version}`",
        "stable channel",
    )
    adr = replace_once(
        adr,
        f"`experimental-{target_version}`",
        f"`experimental-{next_version}`",
        "experimental channel",
    )
    adr_path.write_text(adr, encoding="utf-8")

    catalog_path = repo_root / "hack/update_catalog.py"
    catalog = catalog_path.read_text(encoding="utf-8")
    catalog = replace_once(
        catalog,
        f"e.g. {current_version};",
        f"e.g. {target_version};",
        "catalog channel help",
    )
    catalog_path.write_text(catalog, encoding="utf-8")

    sample_path = repo_root / "src/config/samples/neteye_v1alpha1_neteye.yaml"
    sample = sample_path.read_text(encoding="utf-8")
    sample = replace_once(
        sample,
        f'version: "{current_version}"',
        f'version: "{target_version}"',
        "NetEye sample version",
    )
    sample_path.write_text(sample, encoding="utf-8")

    csv_metadata_path = repo_root / "src/config/manifests/patches/metadata.yaml"
    csv_metadata = csv_metadata_path.read_text(encoding="utf-8")
    csv_metadata = replace_once(
        csv_metadata,
        f'"version":"{current_version}"',
        f'"version":"{target_version}"',
        "CSV NetEye sample version",
    )
    csv_metadata_path.write_text(csv_metadata, encoding="utf-8")

    return True


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("neteye_version", help="New major.minor NetEye release line")
    parser.add_argument(
        "--repo-root",
        type=Path,
        default=Path(__file__).resolve().parents[1],
    )
    args = parser.parse_args()
    if update_repository(args.repo_root.resolve(), args.neteye_version):
        print(f"Updated operator NetEye support to {args.neteye_version}")
    else:
        print(f"Operator already supports NetEye {args.neteye_version}")


if __name__ == "__main__":
    main()

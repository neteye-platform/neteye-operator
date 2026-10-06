import tempfile
import unittest
from pathlib import Path
from unittest.mock import Mock, patch

from update_catalog import (
    NETEYE_VERSION_URL,
    fetch_latest_neteye_version,
    update_catalog,
    update_catalog_backport,
    update_nightly_channel,
)

PACKAGE = "neteye-operator"
STABLE_ENTRIES = "  - name: neteye-operator.0.1.0\n"
ALPHA_ENTRIES = "  - name: neteye-operator.0.2.0-alpha1\n"


def digest(character: str) -> str:
    return f"ghcr.io/neteye-platform/neteye-operator-bundle@sha256:{character * 64}"


def nightly(tag: str) -> str:
    return f"ghcr.io/neteye-platform/neteye-operator-bundle:{tag}"


def channel_document(name: str, entries: str) -> str:
    return f"schema: olm.channel\npackage: {PACKAGE}\nname: {name}\nentries:\n{entries}"


def bundle_document(version: str, image: str) -> str:
    return (
        f"schema: olm.bundle\nname: {PACKAGE}.{version}\npackage: {PACKAGE}\n"
        f"image: {image}\nproperties:\n  - type: olm.package\n    value:\n"
        f"      packageName: {PACKAGE}\n      version: {version}\n"
    )


class CatalogTreeTestCase(unittest.TestCase):
    """A catalog tree with a `stable` and an `alpha` channel, as files."""

    default_channel = "stable"

    def setUp(self) -> None:
        self.temp_dir = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp_dir.cleanup)
        self.catalog_path = Path(self.temp_dir.name) / "catalog"
        package_dir = self.catalog_path / PACKAGE
        (package_dir / "channels").mkdir(parents=True)
        (package_dir / "bundles").mkdir(parents=True)
        (package_dir / "package.yaml").write_text(
            f"schema: olm.package\nname: {PACKAGE}\n"
            f"defaultChannel: {self.default_channel}\n",
            encoding="utf-8",
        )
        self.write_channel("stable", STABLE_ENTRIES)
        self.write_channel("alpha", ALPHA_ENTRIES)
        self.write_bundle("0.2.0-alpha1", digest("a"))

    # -- fixture helpers ---------------------------------------------------
    def channel_path(self, name: str) -> Path:
        return self.catalog_path / PACKAGE / "channels" / f"{name}.yaml"

    def bundle_path(self, version: str) -> Path:
        return self.catalog_path / PACKAGE / "bundles" / f"{version}.yaml"

    @property
    def nightly_bundles_path(self) -> Path:
        return self.catalog_path / PACKAGE / "bundles" / "nightly.yaml"

    def nightly_bundle_names(self) -> list[str]:
        if not self.nightly_bundles_path.is_file():
            return []
        return [
            line.removeprefix("name: ")
            for line in self.nightly_bundles_path.read_text(
                encoding="utf-8"
            ).splitlines()
            if line.startswith("name: ")
        ]

    def bundle_file_count(self) -> int:
        return len(list((self.catalog_path / PACKAGE / "bundles").glob("*.yaml")))

    def write_channel(self, name: str, entries: str) -> None:
        self.channel_path(name).write_text(
            channel_document(name, entries), encoding="utf-8"
        )

    def write_bundle(self, version: str, image: str) -> None:
        self.bundle_path(version).write_text(
            bundle_document(version, image), encoding="utf-8"
        )

    # -- assertion helpers -------------------------------------------------
    def channel(self, name: str) -> str:
        return self.channel_path(name).read_text(encoding="utf-8")

    def package(self) -> str:
        return (self.catalog_path / PACKAGE / "package.yaml").read_text(
            encoding="utf-8"
        )

    def entry_names(self, name: str) -> list[str]:
        return [
            line.removeprefix("  - name: ")
            for line in self.channel(name).splitlines()
            if line.startswith("  - name: ")
        ]

    def channel_exists(self, name: str) -> bool:
        return self.channel_path(name).is_file()

    def bundle_exists(self, version: str) -> bool:
        return self.bundle_path(version).is_file()

    def nightly_bundle_exists(self, tag: str) -> bool:
        return f"{PACKAGE}.{tag}" in self.nightly_bundle_names()


class FetchLatestNetEyeVersionTest(unittest.TestCase):
    @patch("update_catalog.requests.get")
    def test_uses_verified_requests_client(self, get: Mock) -> None:
        response = get.return_value
        response.json.return_value = {"version": "4.50.0-sr1"}

        version = fetch_latest_neteye_version()

        self.assertEqual(version, "4.50.0")
        get.assert_called_once_with(
            NETEYE_VERSION_URL,
            headers={"User-Agent": "neteye-operator-update-catalog"},
            timeout=10,
        )
        response.raise_for_status.assert_called_once_with()


class UpdateCatalogTest(CatalogTreeTestCase):
    def test_adds_prerelease_to_alpha_channel(self) -> None:
        image = digest("b")

        changed = update_catalog(self.catalog_path, "0.2.0-alpha2", image)

        self.assertTrue(changed)
        alpha = self.channel("alpha")
        self.assertIn("name: neteye-operator.0.2.0-alpha2", alpha)
        self.assertIn("replaces: neteye-operator.0.2.0-alpha1", alpha)
        self.assertIn(f"image: {image}", self.bundle_path("0.2.0-alpha2").read_text())
        self.assertNotIn("0.2.0-alpha2", self.channel("stable"))

    def test_adds_ga_release_to_stable_channel(self) -> None:
        changed = update_catalog(self.catalog_path, "0.2.0", digest("c"))

        self.assertTrue(changed)
        stable = self.channel("stable")
        self.assertIn("name: neteye-operator.0.2.0", stable)
        self.assertIn("replaces: neteye-operator.0.1.0", stable)
        self.assertNotIn("0.2.0\n", self.channel("alpha"))

    def test_writes_bundle_file_with_gvk_property(self) -> None:
        update_catalog(self.catalog_path, "0.2.0", digest("c"))

        bundle = self.bundle_path("0.2.0").read_text(encoding="utf-8")
        self.assertIn("kind: ClusterServiceVersion", bundle)
        self.assertIn("      packageName: neteye-operator", bundle)
        self.assertIn("      version: 0.2.0", bundle)

    def test_adds_release_to_versioned_stable_channel(self) -> None:
        changed = update_catalog(
            self.catalog_path, "0.2.0", digest("f"), neteye_version="4.50"
        )

        self.assertTrue(changed)
        self.assertIn("defaultChannel: 4.50-stable", self.package())
        self.assertTrue(self.channel_exists("4.50-stable"))
        self.assertEqual(self.entry_names("4.50-stable"), ["neteye-operator.0.2.0"])

    def test_leaves_other_channel_files_untouched(self) -> None:
        before = self.channel("stable")

        update_catalog(self.catalog_path, "0.2.0-alpha2", digest("b"))

        self.assertEqual(self.channel("stable"), before)

    def test_is_idempotent_for_same_digest(self) -> None:
        image = digest("d")
        update_catalog(self.catalog_path, "0.2.0-alpha2", image)

        changed = update_catalog(self.catalog_path, "0.2.0-alpha2", image)

        self.assertFalse(changed)

    def test_rejects_replacing_existing_release_digest(self) -> None:
        with self.assertRaisesRegex(ValueError, "refusing to replace"):
            update_catalog(self.catalog_path, "0.2.0-alpha1", digest("e"))

    def test_rejects_tagged_bundle_image(self) -> None:
        with self.assertRaisesRegex(ValueError, "pinned by GHCR digest"):
            update_catalog(
                self.catalog_path,
                "0.2.0-alpha2",
                "ghcr.io/neteye-platform/neteye-operator-bundle:0.2.0-alpha2",
            )

    def test_rejects_numeric_prerelease_with_leading_zero(self) -> None:
        with self.assertRaisesRegex(ValueError, "invalid release version"):
            update_catalog(self.catalog_path, "0.2.0-01", digest("f"))

    def test_rejects_out_of_order_release(self) -> None:
        with self.assertRaisesRegex(ValueError, "must be newer"):
            update_catalog(self.catalog_path, "0.1.0-alpha1", digest("1"))

    def test_rejects_missing_package_document(self) -> None:
        (self.catalog_path / PACKAGE / "package.yaml").unlink()

        with self.assertRaisesRegex(ValueError, "missing package document"):
            update_catalog(self.catalog_path, "0.2.0-alpha2", digest("b"))

    def test_rejects_skip_range_with_actionable_error(self) -> None:
        self.write_channel(
            "alpha",
            "  - name: neteye-operator.0.2.0-alpha1\n"
            '    skipRange: ">=0.1.0 <0.2.0-alpha1"\n',
        )

        with self.assertRaisesRegex(ValueError, "unsupported skipRange"):
            update_catalog(self.catalog_path, "0.2.0-alpha2", digest("2"))

    def test_uses_channel_head_as_previous_release(self) -> None:
        self.write_channel(
            "alpha",
            "  - name: neteye-operator.0.1.0-alpha9\n"
            "  - name: neteye-operator.0.1.0-alpha10\n"
            "    skips:\n"
            "      - neteye-operator.0.1.0-alpha9\n",
        )

        update_catalog(self.catalog_path, "0.1.1-alpha.2", digest("3"))

        alpha = self.channel("alpha")
        new_entry = alpha.split("name: neteye-operator.0.1.1-alpha.2", maxsplit=1)[1]
        self.assertTrue(
            new_entry.startswith("\n    replaces: neteye-operator.0.1.0-alpha10")
        )

    def test_rejects_channel_with_multiple_heads(self) -> None:
        self.write_channel(
            "alpha",
            "  - name: neteye-operator.0.1.0-alpha9\n"
            "  - name: neteye-operator.0.1.0-alpha10\n",
        )

        with self.assertRaisesRegex(ValueError, "expected exactly one .* channel head"):
            update_catalog(self.catalog_path, "0.1.1-alpha.2", digest("3"))

    def test_rejects_channel_with_no_head(self) -> None:
        self.write_channel(
            "alpha",
            "  - name: neteye-operator.0.1.0-alpha9\n"
            "  - name: neteye-operator.0.1.0-alpha10\n"
            "    replaces: neteye-operator.0.1.0-alpha9\n"
            "    skips:\n"
            "      - neteye-operator.0.1.0-alpha10\n",
        )

        with self.assertRaisesRegex(ValueError, "found: none"):
            update_catalog(self.catalog_path, "0.1.1-alpha.2", digest("4"))


class UpdateCatalogBackportTest(CatalogTreeTestCase):
    def test_adds_bugfix_only_to_matching_minor_channel(self) -> None:
        changed = update_catalog_backport(self.catalog_path, "0.1.1", digest("5"))

        self.assertTrue(changed)
        self.assertIn("name: neteye-operator.0.1.1", self.channel("stable"))
        self.assertIn("replaces: neteye-operator.0.1.0", self.channel("stable"))
        self.assertNotIn("0.1.1", self.channel("alpha"))

    def test_adds_bugfix_to_every_matching_channel(self) -> None:
        self.write_channel("alpha", "  - name: neteye-operator.0.1.0-alpha1\n")

        changed = update_catalog_backport(self.catalog_path, "0.1.1", digest("6"))

        self.assertTrue(changed)
        self.assertIn("name: neteye-operator.0.1.1", self.channel("stable"))
        self.assertIn("name: neteye-operator.0.1.1", self.channel("alpha"))

    def test_rejects_when_no_channel_matches_minor(self) -> None:
        with self.assertRaisesRegex(ValueError, "nothing to backport"):
            update_catalog_backport(self.catalog_path, "0.3.1", digest("7"))

    def test_rejects_out_of_order_backport(self) -> None:
        with self.assertRaisesRegex(ValueError, "must be newer"):
            update_catalog_backport(self.catalog_path, "0.1.0-alpha1", digest("8"))

    def test_is_idempotent_for_same_digest(self) -> None:
        image = digest("9")
        update_catalog_backport(self.catalog_path, "0.1.1", image)

        changed = update_catalog_backport(self.catalog_path, "0.1.1", image)

        self.assertFalse(changed)

    def test_rejects_replacing_existing_release_digest(self) -> None:
        update_catalog_backport(self.catalog_path, "0.1.1", digest("c"))

        with self.assertRaisesRegex(ValueError, "refusing to replace"):
            update_catalog_backport(
                self.catalog_path,
                "0.1.1",
                f"ghcr.io/neteye-platform/neteye-operator-bundle@sha256:{'a' * 63}b",
            )


class UpdateNightlyChannelTest(CatalogTreeTestCase):
    first = "0.2.0-alpha1-nightly-abc1234"
    second = "0.2.0-alpha1-nightly-def5678"
    third = "0.2.0-alpha1-nightly-789abcd"

    def test_creates_nightly_channel_when_absent(self) -> None:
        changed = update_nightly_channel(self.catalog_path, nightly(self.first), "4.50")

        self.assertTrue(changed)
        self.assertTrue(self.channel_exists("4.50-nightly"))
        self.assertFalse(self.channel_exists("4.50-stable"))
        self.assertTrue(self.nightly_bundle_exists(self.first))

    def test_replaces_previous_nightly_head_but_keeps_old_bundle(self) -> None:
        update_nightly_channel(self.catalog_path, nightly(self.first), "4.50")

        changed = update_nightly_channel(
            self.catalog_path, nightly(self.second), "4.50"
        )

        self.assertTrue(changed)
        # the superseded bundle file is kept; it is only no longer the head
        self.assertTrue(self.nightly_bundle_exists(self.first))
        self.assertTrue(self.nightly_bundle_exists(self.second))
        self.assertEqual(
            self.entry_names("4.50-nightly"),
            [f"{PACKAGE}.{self.first}", f"{PACKAGE}.{self.second}"],
        )
        self.assertIn(
            f"    replaces: {PACKAGE}.{self.first}", self.channel("4.50-nightly")
        )

    def test_keeps_skips_on_the_head_only(self) -> None:
        for tag in (self.first, self.second, self.third):
            update_nightly_channel(self.catalog_path, nightly(tag), "4.50")

        self.assertEqual(
            self.channel("4.50-nightly"),
            channel_document(
                "4.50-nightly",
                f"  - name: {PACKAGE}.{self.first}\n"
                f"  - name: {PACKAGE}.{self.second}\n"
                f"  - name: {PACKAGE}.{self.third}\n"
                f"    replaces: {PACKAGE}.{self.second}\n"
                "    skips:\n"
                f"      - {PACKAGE}.{self.first}\n",
            ),
        )

    def test_collapses_legacy_cumulative_skips(self) -> None:
        """A channel written by the previous scheme is rewritten head-only."""
        self.write_channel(
            "4.50-nightly",
            f"  - name: {PACKAGE}.{self.first}\n"
            f"  - name: {PACKAGE}.{self.second}\n"
            f"    replaces: {PACKAGE}.{self.first}\n"
            f"  - name: {PACKAGE}.{self.third}\n"
            f"    replaces: {PACKAGE}.{self.second}\n"
            "    skips:\n"
            f"      - {PACKAGE}.{self.first}\n",
        )
        fourth = "0.2.0-alpha1-nightly-fed4321"

        changed = update_nightly_channel(self.catalog_path, nightly(fourth), "4.50")

        self.assertTrue(changed)
        text = self.channel("4.50-nightly")
        # exactly one entry carries edges, and it is the new head
        self.assertEqual(text.count("    replaces: "), 1)
        self.assertEqual(text.count("    skips:"), 1)
        self.assertIn(f"    replaces: {PACKAGE}.{self.third}", text)
        self.assertEqual(
            self.entry_names("4.50-nightly"),
            [
                f"{PACKAGE}.{tag}"
                for tag in (self.first, self.second, self.third, fourth)
            ],
        )

    def test_grows_linearly_with_nightly_count(self) -> None:
        tags = [f"0.2.0-nightly-{index:07x}" for index in range(1, 21)]
        for tag in tags:
            update_nightly_channel(self.catalog_path, nightly(tag), "4.50")

        lines = len(self.channel("4.50-nightly").splitlines())
        # 4 header lines + 20 names + 1 replaces + 1 "skips:" + 18 skipped entries
        self.assertEqual(lines, 44)

    def test_scopes_channel_to_neteye_release_line(self) -> None:
        update_nightly_channel(self.catalog_path, nightly(self.first), "4.50")

        changed = update_nightly_channel(self.catalog_path, nightly(self.first), "4.51")

        self.assertTrue(changed)
        self.assertTrue(self.channel_exists("4.50-nightly"))
        self.assertTrue(self.channel_exists("4.51-nightly"))
        self.assertFalse(self.channel_exists("4.50-stable"))
        self.assertFalse(self.channel_exists("4.51-stable"))

    def test_is_idempotent_for_same_tag(self) -> None:
        update_nightly_channel(self.catalog_path, nightly(self.first), "4.50")

        changed = update_nightly_channel(self.catalog_path, nightly(self.first), "4.50")

        self.assertFalse(changed)

    def test_is_idempotent_when_head_of_a_longer_channel(self) -> None:
        update_nightly_channel(self.catalog_path, nightly(self.first), "4.50")
        update_nightly_channel(self.catalog_path, nightly(self.second), "4.50")
        before = self.channel("4.50-nightly")

        changed = update_nightly_channel(
            self.catalog_path, nightly(self.second), "4.50"
        )

        self.assertFalse(changed)
        self.assertEqual(self.channel("4.50-nightly"), before)

    def test_leaves_stable_channels_untouched(self) -> None:
        before = self.channel("stable")

        update_nightly_channel(self.catalog_path, nightly(self.first), "4.50")

        self.assertEqual(self.channel("stable"), before)

    def test_shares_one_file_for_every_nightly_bundle(self) -> None:
        before = self.bundle_file_count()

        for tag in (self.first, self.second, self.third):
            update_nightly_channel(self.catalog_path, nightly(tag), "4.50")

        # one new file in bundles/ no matter how many nightlies are published
        self.assertEqual(self.bundle_file_count(), before + 1)
        self.assertEqual(
            self.nightly_bundle_names(),
            [f"{PACKAGE}.{tag}" for tag in (self.first, self.second, self.third)],
        )
        self.assertFalse(self.bundle_path(self.first).is_file())

    def test_appends_without_rewriting_earlier_bundles(self) -> None:
        update_nightly_channel(self.catalog_path, nightly(self.first), "4.50")
        before = self.nightly_bundles_path.read_text(encoding="utf-8")

        update_nightly_channel(self.catalog_path, nightly(self.second), "4.50")

        after = self.nightly_bundles_path.read_text(encoding="utf-8")
        self.assertTrue(after.startswith(before.rstrip("\n") + "\n---\n"))

    def test_does_not_duplicate_a_known_tag(self) -> None:
        update_nightly_channel(self.catalog_path, nightly(self.first), "4.50")
        update_nightly_channel(self.catalog_path, nightly(self.second), "4.50")

        update_nightly_channel(self.catalog_path, nightly(self.first), "4.50")

        self.assertEqual(
            self.nightly_bundle_names(),
            [f"{PACKAGE}.{self.first}", f"{PACKAGE}.{self.second}"],
        )

    def test_keeps_release_bundles_in_their_own_files(self) -> None:
        update_nightly_channel(self.catalog_path, nightly(self.first), "4.50")
        update_catalog(self.catalog_path, "0.2.0", digest("c"))

        self.assertTrue(self.bundle_path("0.2.0").is_file())
        self.assertNotIn(f"{PACKAGE}.0.2.0", self.nightly_bundle_names())

    def test_rejects_non_nightly_image(self) -> None:
        with self.assertRaisesRegex(ValueError, "nightly bundle image must match"):
            update_nightly_channel(
                self.catalog_path,
                "ghcr.io/neteye-platform/neteye-operator-bundle:0.2.0-alpha2",
                "4.50",
            )

    def test_rejects_unversioned_nightly_image(self) -> None:
        with self.assertRaisesRegex(ValueError, "nightly bundle image must match"):
            update_nightly_channel(
                self.catalog_path,
                "ghcr.io/neteye-platform/neteye-operator-bundle:nightly-abc1234",
                "4.50",
            )


if __name__ == "__main__":
    unittest.main()

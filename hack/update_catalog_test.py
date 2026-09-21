import tempfile
import unittest
from pathlib import Path

from update_catalog import update_catalog, update_nightly_channel

CATALOG = """schema: olm.package
name: neteye-operator
defaultChannel: stable
---
schema: olm.channel
package: neteye-operator
name: stable
entries:
  - name: neteye-operator.0.1.0
---
schema: olm.channel
package: neteye-operator
name: alpha
entries:
  - name: neteye-operator.0.2.0-alpha1
---
schema: olm.bundle
name: neteye-operator.0.2.0-alpha1
package: neteye-operator
image: ghcr.io/neteye-platform/neteye-operator-bundle@sha256:{old_digest}
properties:
  - type: olm.package
    value:
      packageName: neteye-operator
      version: 0.2.0-alpha1
"""


class UpdateCatalogTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temp_dir = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp_dir.cleanup)
        self.catalog_path = Path(self.temp_dir.name) / "index.yaml"
        self.catalog_path.write_text(
            CATALOG.format(old_digest="a" * 64), encoding="utf-8"
        )

    def test_adds_prerelease_to_alpha_channel(self) -> None:
        image = f"ghcr.io/neteye-platform/neteye-operator-bundle@sha256:{'b' * 64}"

        changed = update_catalog(self.catalog_path, "0.2.0-alpha2", image)

        self.assertTrue(changed)
        updated = self.catalog_path.read_text(encoding="utf-8")
        self.assertIn("name: neteye-operator.0.2.0-alpha2", updated)
        self.assertIn("replaces: neteye-operator.0.2.0-alpha1", updated)
        self.assertIn(f"image: {image}", updated)
        stable_document = updated.split("\n---\n")[1]
        self.assertNotIn("0.2.0-alpha2", stable_document)

    def test_adds_ga_release_to_stable_channel(self) -> None:
        image = f"ghcr.io/neteye-platform/neteye-operator-bundle@sha256:{'c' * 64}"

        changed = update_catalog(self.catalog_path, "0.2.0", image)

        self.assertTrue(changed)
        stable_document = self.catalog_path.read_text(encoding="utf-8").split(
            "\n---\n"
        )[1]
        self.assertIn("name: neteye-operator.0.2.0", stable_document)
        self.assertIn("replaces: neteye-operator.0.1.0", stable_document)

    def test_is_idempotent_for_same_digest(self) -> None:
        image = f"ghcr.io/neteye-platform/neteye-operator-bundle@sha256:{'d' * 64}"
        update_catalog(self.catalog_path, "0.2.0-alpha2", image)

        changed = update_catalog(self.catalog_path, "0.2.0-alpha2", image)

        self.assertFalse(changed)

    def test_rejects_replacing_existing_release_digest(self) -> None:
        image = f"ghcr.io/neteye-platform/neteye-operator-bundle@sha256:{'e' * 64}"

        with self.assertRaisesRegex(ValueError, "refusing to replace"):
            update_catalog(self.catalog_path, "0.2.0-alpha1", image)

    def test_rejects_tagged_bundle_image(self) -> None:
        with self.assertRaisesRegex(ValueError, "pinned by GHCR digest"):
            update_catalog(
                self.catalog_path,
                "0.2.0-alpha2",
                "ghcr.io/neteye-platform/neteye-operator-bundle:0.2.0-alpha2",
            )

    def test_rejects_numeric_prerelease_with_leading_zero(self) -> None:
        image = f"ghcr.io/neteye-platform/neteye-operator-bundle@sha256:{'f' * 64}"

        with self.assertRaisesRegex(ValueError, "invalid release version"):
            update_catalog(self.catalog_path, "0.2.0-01", image)

    def test_rejects_out_of_order_release(self) -> None:
        image = f"ghcr.io/neteye-platform/neteye-operator-bundle@sha256:{'1' * 64}"

        with self.assertRaisesRegex(ValueError, "must be newer"):
            update_catalog(self.catalog_path, "0.1.0-alpha1", image)

    def test_rejects_skip_range_with_actionable_error(self) -> None:
        image = f"ghcr.io/neteye-platform/neteye-operator-bundle@sha256:{'2' * 64}"
        catalog = self.catalog_path.read_text(encoding="utf-8")
        catalog = catalog.replace(
            "  - name: neteye-operator.0.2.0-alpha1",
            "  - name: neteye-operator.0.2.0-alpha1\n"
            '    skipRange: ">=0.1.0 <0.2.0-alpha1"',
        )
        self.catalog_path.write_text(catalog, encoding="utf-8")

        with self.assertRaisesRegex(ValueError, "unsupported skipRange"):
            update_catalog(self.catalog_path, "0.2.0-alpha2", image)

    def test_uses_channel_head_as_previous_release(self) -> None:
        image = f"ghcr.io/neteye-platform/neteye-operator-bundle@sha256:{'3' * 64}"
        catalog = self.catalog_path.read_text(encoding="utf-8")
        catalog = catalog.replace(
            "  - name: neteye-operator.0.2.0-alpha1",
            "  - name: neteye-operator.0.1.0-alpha9\n"
            "  - name: neteye-operator.0.1.0-alpha10\n"
            "    skips:\n"
            "      - neteye-operator.0.1.0-alpha9",
        )
        self.catalog_path.write_text(catalog, encoding="utf-8")

        update_catalog(self.catalog_path, "0.1.1-alpha.2", image)

        updated = self.catalog_path.read_text(encoding="utf-8")
        new_entry = updated.split("name: neteye-operator.0.1.1-alpha.2", maxsplit=1)[1]
        self.assertTrue(
            new_entry.startswith("\n    replaces: neteye-operator.0.1.0-alpha10")
        )

    def test_rejects_channel_with_multiple_heads(self) -> None:
        image = f"ghcr.io/neteye-platform/neteye-operator-bundle@sha256:{'3' * 64}"
        catalog = self.catalog_path.read_text(encoding="utf-8")
        catalog = catalog.replace(
            "  - name: neteye-operator.0.2.0-alpha1",
            "  - name: neteye-operator.0.1.0-alpha9\n"
            "  - name: neteye-operator.0.1.0-alpha10",
        )
        self.catalog_path.write_text(catalog, encoding="utf-8")

        with self.assertRaisesRegex(ValueError, "expected exactly one .* channel head"):
            update_catalog(self.catalog_path, "0.1.1-alpha.2", image)

    def test_rejects_channel_with_no_head(self) -> None:
        image = f"ghcr.io/neteye-platform/neteye-operator-bundle@sha256:{'4' * 64}"
        catalog = self.catalog_path.read_text(encoding="utf-8")
        catalog = catalog.replace(
            "  - name: neteye-operator.0.2.0-alpha1",
            "  - name: neteye-operator.0.1.0-alpha9\n"
            "  - name: neteye-operator.0.1.0-alpha10\n"
            "    replaces: neteye-operator.0.1.0-alpha9\n"
            "    skips:\n"
            "      - neteye-operator.0.1.0-alpha10",
        )
        self.catalog_path.write_text(catalog, encoding="utf-8")

        with self.assertRaisesRegex(ValueError, "found: none"):
            update_catalog(self.catalog_path, "0.1.1-alpha.2", image)


class UpdateNightlyChannelTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temp_dir = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp_dir.cleanup)
        self.catalog_path = Path(self.temp_dir.name) / "index.yaml"
        self.catalog_path.write_text(
            CATALOG.format(old_digest="a" * 64), encoding="utf-8"
        )

    def test_creates_nightly_channel_when_absent(self) -> None:
        image = "ghcr.io/neteye-platform/neteye-operator-bundle:0.2.0-alpha1-nightly-abc1234"

        changed = update_nightly_channel(self.catalog_path, image, "4.50")

        self.assertTrue(changed)
        updated = self.catalog_path.read_text(encoding="utf-8")
        self.assertIn("name: nightly-4.50", updated)
        self.assertIn("name: neteye-operator.0.2.0-alpha1-nightly-abc1234", updated)

    def test_replaces_previous_nightly_head_but_keeps_old_bundle(self) -> None:
        first_image = "ghcr.io/neteye-platform/neteye-operator-bundle:0.2.0-alpha1-nightly-abc1234"
        update_nightly_channel(self.catalog_path, first_image, "4.50")
        second_image = "ghcr.io/neteye-platform/neteye-operator-bundle:0.2.0-alpha1-nightly-def5678"

        changed = update_nightly_channel(self.catalog_path, second_image, "4.50")

        self.assertTrue(changed)
        updated = self.catalog_path.read_text(encoding="utf-8")
        # old bundle document is kept (no pruning), just no longer the channel head
        self.assertIn("name: neteye-operator.0.2.0-alpha1-nightly-abc1234", updated)
        self.assertIn("name: neteye-operator.0.2.0-alpha1-nightly-def5678", updated)
        nightly_channel = next(
            document
            for document in updated.split("\n---\n")
            if "name: nightly-4.50" in document and "schema: olm.channel" in document
        )
        self.assertIn("0.2.0-alpha1-nightly-abc1234", nightly_channel)
        self.assertIn("0.2.0-alpha1-nightly-def5678", nightly_channel)
        self.assertIn(
            "replaces: neteye-operator.0.2.0-alpha1-nightly-abc1234",
            nightly_channel,
        )

        third_image = "ghcr.io/neteye-platform/neteye-operator-bundle:0.2.0-alpha1-nightly-789abcd"
        update_nightly_channel(self.catalog_path, third_image, "4.50")
        updated = self.catalog_path.read_text(encoding="utf-8")
        nightly_channel = next(
            document
            for document in updated.split("\n---\n")
            if "name: nightly-4.50" in document and "schema: olm.channel" in document
        )
        self.assertIn(
            "replaces: neteye-operator.0.2.0-alpha1-nightly-def5678",
            nightly_channel,
        )
        self.assertIn(
            "      - neteye-operator.0.2.0-alpha1-nightly-abc1234", nightly_channel
        )

    def test_scopes_channel_to_neteye_release_line(self) -> None:
        image = "ghcr.io/neteye-platform/neteye-operator-bundle:0.2.0-alpha1-nightly-abc1234"

        update_nightly_channel(self.catalog_path, image, "4.50")
        changed = update_nightly_channel(self.catalog_path, image, "4.51")

        self.assertTrue(changed)
        updated = self.catalog_path.read_text(encoding="utf-8")
        self.assertIn("name: nightly-4.50", updated)
        self.assertIn("name: nightly-4.51", updated)

    def test_is_idempotent_for_same_tag(self) -> None:
        image = "ghcr.io/neteye-platform/neteye-operator-bundle:0.2.0-alpha1-nightly-abc1234"
        update_nightly_channel(self.catalog_path, image, "4.50")

        changed = update_nightly_channel(self.catalog_path, image, "4.50")

        self.assertFalse(changed)

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

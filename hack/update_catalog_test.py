import tempfile
import unittest
from pathlib import Path

from update_catalog import update_catalog

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
        self.assertIn("- neteye-operator.0.2.0-alpha1", updated)
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
        self.assertIn("- neteye-operator.0.1.0", stable_document)

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

    def test_uses_semantic_maximum_as_previous_release(self) -> None:
        image = f"ghcr.io/neteye-platform/neteye-operator-bundle@sha256:{'2' * 64}"
        catalog = self.catalog_path.read_text(encoding="utf-8")
        catalog = catalog.replace(
            "  - name: neteye-operator.0.2.0-alpha1",
            "  - name: neteye-operator.0.2.0-alpha2\n"
            "  - name: neteye-operator.0.2.0-alpha1",
        )
        self.catalog_path.write_text(catalog, encoding="utf-8")

        update_catalog(self.catalog_path, "0.2.0-alpha3", image)

        updated = self.catalog_path.read_text(encoding="utf-8")
        new_entry = updated.split("name: neteye-operator.0.2.0-alpha3", maxsplit=1)[1]
        self.assertTrue(
            new_entry.startswith("\n    skips:\n      - neteye-operator.0.2.0-alpha2")
        )


if __name__ == "__main__":
    unittest.main()

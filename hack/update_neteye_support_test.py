import unittest
from pathlib import Path
from tempfile import TemporaryDirectory

from hack.update_neteye_support import update_repository, update_source

SOURCE = """\
var netEyeVersionMap = map[string]NetEyeComponents{
\tCurrentNetEyeVersion: {KeycloakImage: "keycloak:1", OTelCollectorImage: "otel:1", EDOTGatewayImage: "gateway:1", CABundleImage: "ca@sha256:abc"},
}

const (
\tCurrentNetEyeVersion  = "4.50"
\tPreviousNetEyeVersion = "4.49"
)
"""


class UpdateSourceTest(unittest.TestCase):
    def test_advances_versions_and_copies_component_images(self) -> None:
        updated = update_source(SOURCE, "4.51")

        self.assertIn('PreviousNetEyeVersion = "4.50"', updated)
        self.assertIn('CurrentNetEyeVersion  = "4.51"', updated)
        self.assertEqual(updated.count('KeycloakImage: "keycloak:1"'), 2)
        self.assertIn("PreviousNetEyeVersion: {KeycloakImage:", updated)
        self.assertIn("CurrentNetEyeVersion: {KeycloakImage:", updated)

    def test_second_advance_drops_the_older_supported_entry(self) -> None:
        updated = update_source(update_source(SOURCE, "4.51"), "4.52")

        self.assertIn('PreviousNetEyeVersion = "4.51"', updated)
        self.assertIn('CurrentNetEyeVersion  = "4.52"', updated)
        self.assertEqual(updated.count("NetEyeVersion:"), 2)

    def test_rejects_current_or_older_version(self) -> None:
        self.assertEqual(update_source(SOURCE, "4.50"), SOURCE)
        with self.assertRaises(ValueError):
            update_source(SOURCE, "4.49")

    def test_rejects_non_release_line(self) -> None:
        with self.assertRaises(ValueError):
            update_source(SOURCE, "4.51-sr1")


class UpdateRepositoryTest(unittest.TestCase):
    def test_updates_operator_and_release_references(self) -> None:
        with TemporaryDirectory() as temporary_directory:
            repo = Path(temporary_directory)
            files = {
                "src/api/v1alpha1/neteyeconfig_types.go": SOURCE.replace(
                    "var netEyeVersionMap",
                    '// NetEye version, e.g. "4.50".\nvar netEyeVersionMap',
                ),
                "docs/adr/0003-neteye-and-operator-version-model.md": "`stable-4.50`\n`experimental-4.51`\n",
                "hack/update_catalog.py": "NetEye release line, e.g. 4.50; defaults to latest\n",
                "src/config/samples/neteye_v1alpha1_neteye.yaml": 'version: "4.50"\n',
                "src/config/manifests/patches/metadata.yaml": '{"spec":{"version":"4.50"}}\n',
            }
            for relative_path, content in files.items():
                path = repo / relative_path
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(content, encoding="utf-8")

            self.assertTrue(update_repository(repo, "4.51"))
            self.assertFalse(update_repository(repo, "4.51"))
            self.assertIn(
                'CurrentNetEyeVersion  = "4.51"',
                (repo / "src/api/v1alpha1/neteyeconfig_types.go").read_text(),
            )
            self.assertIn(
                'PreviousNetEyeVersion = "4.50"',
                (repo / "src/api/v1alpha1/neteyeconfig_types.go").read_text(),
            )
            self.assertIn(
                "`stable-4.51`",
                (
                    repo / "docs/adr/0003-neteye-and-operator-version-model.md"
                ).read_text(),
            )
            self.assertIn(
                "`experimental-4.52`",
                (
                    repo / "docs/adr/0003-neteye-and-operator-version-model.md"
                ).read_text(),
            )
            self.assertIn(
                'version: "4.51"',
                (repo / "src/config/samples/neteye_v1alpha1_neteye.yaml").read_text(),
            )
            self.assertIn(
                '"version":"4.51"',
                (repo / "src/config/manifests/patches/metadata.yaml").read_text(),
            )


if __name__ == "__main__":
    unittest.main()

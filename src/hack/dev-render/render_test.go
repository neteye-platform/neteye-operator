// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderDiscoversAndRendersSourceArtifacts(t *testing.T) {
	source := testSource(t, "1.7.3", "example.io/team/sample-operator:old", "example.io/team/keycloak:old", "example.io/team/sample-operator-bundle:old")
	before := snapshotSource(t, source)
	output := filepath.Join(source, "bin", "dev", "context")
	version := "1.7.4-dev.20260819.abc1234"
	if err := Render(Options{SourceRoot: source, OutputContext: output, Registry: "registry.example:5000", Version: version, KeycloakImage: "registry.example:5000/custom-keycloak:dev", ImagePullSecret: "local-pull"}); err != nil {
		t.Fatal(err)
	}
	csvPath := filepath.Join(output, "bundle", "manifests", "sample-operator."+version+".clusterserviceversion.yaml")
	csv := readTestFile(t, csvPath)
	for _, want := range []string{"name: sample-operator." + version, "version: " + version, "image: registry.example:5000/sample-operator:" + version, "value: registry.example:5000/custom-keycloak:dev", "image: registry.example:5000/custom-keycloak:dev", "- name: local-pull"} {
		if !strings.Contains(csv, want) {
			t.Errorf("rendered CSV does not contain %q", want)
		}
	}
	if _, err := os.Stat(filepath.Join(output, "bundle", "manifests", "sample-operator.1.7.3.clusterserviceversion.yaml")); !os.IsNotExist(err) {
		t.Errorf("source-version CSV remains in output, stat error = %v", err)
	}
	metadata := readTestFile(t, filepath.Join(output, "bundle", "metadata", "annotations.yaml"))
	if !strings.Contains(metadata, "bundle.version.v1: "+version) {
		t.Error("bundle metadata version was not rendered")
	}
	catalog := readTestFile(t, filepath.Join(output, "catalog", "index.yaml"))
	for _, want := range []string{"sample-operator." + version, "registry.example:5000/sample-operator-bundle:" + version, "version: " + version} {
		if !strings.Contains(catalog, want) {
			t.Errorf("rendered catalog does not contain %q", want)
		}
	}
	for _, path := range []string{filepath.Join(output, ".dev-version"), filepath.Join(output, ".catalog-image"), filepath.Join(output, ".bundle-image")} {
		if strings.TrimSpace(readTestFile(t, path)) == "" {
			t.Errorf("state file %s is empty", path)
		}
	}
	if got, want := strings.TrimSpace(readTestFile(t, filepath.Join(output, ".dev-version"))), version; got != want {
		t.Errorf("state version = %q, want %q", got, want)
	}
	if got, want := strings.TrimSpace(readTestFile(t, filepath.Join(output, ".catalog-image"))), "registry.example:5000/sample-operator-catalog:"+version; got != want {
		t.Errorf("state catalog image = %q, want %q", got, want)
	}
	for _, text := range []string{csv, catalog, metadata} {
		if strings.Contains(text, "example.io/team") {
			t.Error("rendered output retains a source packaging reference")
		}
	}
	if got := snapshotSource(t, source); got != before {
		t.Error("source files were modified")
	}
}

func TestRenderRemovesImagePullSecretsAndDerivesKeycloakImage(t *testing.T) {
	source := testSource(t, "2.0.0", "example.io/sample-operator:old", "example.io/keycloak:old", "example.io/sample-operator-bundle:old")
	output := filepath.Join(source, "bin", "dev", "context")
	if err := Render(Options{SourceRoot: source, OutputContext: output, Registry: "registry.example:5000", Version: "2.0.1-dev.test"}); err != nil {
		t.Fatal(err)
	}
	csv := readTestFile(t, filepath.Join(output, "bundle", "manifests", "sample-operator.2.0.1-dev.test.clusterserviceversion.yaml"))
	if strings.Contains(csv, "imagePullSecrets:") {
		t.Error("imagePullSecrets was not removed")
	}
	if !strings.Contains(csv, "registry.example:5000/keycloak:old") {
		t.Error("derived Keycloak image was not rendered")
	}
}

func TestRenderRejectsInvalidRegistryOrVersion(t *testing.T) {
	source := testSource(t, "1.0.0", "example.io/sample-operator:old", "example.io/keycloak:old", "example.io/sample-operator-bundle:old")
	output := filepath.Join(source, "bin", "dev", "context")
	for _, options := range []Options{{SourceRoot: source, OutputContext: output, Registry: "https://registry.example", Version: "1.0.1-dev.test"}, {SourceRoot: source, OutputContext: output, Registry: "registry.example:5000/", Version: "1.0.1-dev.test"}, {SourceRoot: source, OutputContext: output, Registry: "registry.example:5000", Version: "1.0.1"}, {SourceRoot: source, OutputContext: output, Registry: "registry.example:5000", Version: "v1.0.1-dev.test"}, {SourceRoot: source, OutputContext: output, Registry: "registry.example:5000", Version: "1.0.1-dev.01"}, {SourceRoot: source, OutputContext: output, Registry: "registry.example:5000", Version: "1.0.1--"}} {
		if err := Render(options); err == nil {
			t.Errorf("Render(%+v) succeeded, want error", options)
		}
	}
}

func TestRenderRejectsUnsafeOutputContexts(t *testing.T) {
	source := testSource(t, "1.0.0", "example.io/sample-operator:old", "example.io/keycloak:old", "example.io/sample-operator-bundle:old")
	for _, output := range []string{source, filepath.Dir(source), filepath.Join(source, "bundle"), filepath.Join(source, "catalog"), filepath.Join(source, "bin", "dev"), filepath.Join(source, "bin", "dev", "..", "outside"), filepath.Join(t.TempDir(), "outside")} {
		if err := Render(Options{SourceRoot: source, OutputContext: output, Registry: "registry.example:5000", Version: "1.0.1-dev.test"}); err == nil {
			t.Errorf("Render output %q succeeded, want error", output)
		}
	}
}

func TestRenderRejectsSymlinkedManagedParents(t *testing.T) {
	for _, parent := range []string{"bin", filepath.Join("bin", "dev")} {
		t.Run(parent, func(t *testing.T) {
			source := testSource(t, "1.0.0", "example.io/sample-operator:old", "example.io/keycloak:old", "example.io/sample-operator-bundle:old")
			target := t.TempDir()
			link := filepath.Join(source, parent)
			if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			if err := Render(Options{SourceRoot: source, OutputContext: filepath.Join(source, "bin", "dev", "context"), Registry: "registry.example:5000", Version: "1.0.1-dev.test"}); err == nil {
				t.Fatal("Render succeeded through symlinked parent")
			}
		})
	}
}

func TestCleanOnlyRemovesManagedOutput(t *testing.T) {
	source := testSource(t, "1.0.0", "example.io/sample-operator:old", "example.io/keycloak:old", "example.io/sample-operator-bundle:old")
	output := filepath.Join(source, "bin", "dev", "context")
	if err := Render(Options{SourceRoot: source, OutputContext: output, Registry: "registry.example:5000", Version: "1.0.1-dev.test"}); err != nil {
		t.Fatal(err)
	}
	if err := Clean(source, output); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("managed output still exists after clean: %v", err)
	}
	if err := Clean(source, filepath.Dir(source)); err == nil {
		t.Fatal("Clean accepted a path outside the managed output root")
	}
	if _, err := os.Stat(filepath.Join(source, "bundle")); err != nil {
		t.Fatalf("source bundle was removed: %v", err)
	}
}

func TestValidationRejectsUnsafeValues(t *testing.T) {
	for _, registry := range []string{"registry.example,evil", "registry.example\nvalue: evil", "registry.example:0", "registry.example:65536", "registry.example:http", "[::1]:5000"} {
		if err := validateRegistry(registry); err == nil {
			t.Errorf("validateRegistry(%q) succeeded", registry)
		}
	}
	for _, image := range []string{"registry.example/repo", "registry.example/repo:tag\nvalue: evil", "registry.example/repo:tag,evil", "https://registry.example/repo:tag", "registry.example/:tag"} {
		if err := validateImage(image); err == nil {
			t.Errorf("validateImage(%q) succeeded", image)
		}
	}
	for _, secret := range []string{"Bad_Name", "bad\nsecret", "bad,secret", strings.Repeat("a", 254)} {
		if err := validatePullSecret(secret); err == nil {
			t.Errorf("validatePullSecret(%q) succeeded", secret)
		}
	}
}

func TestRenderRejectsInvalidPullSecretBeforeCreatingOutput(t *testing.T) {
	source := testSource(t, "1.0.0", "example.io/sample-operator:old", "example.io/keycloak:old", "example.io/sample-operator-bundle:old")
	output := filepath.Join(source, "bin", "dev", "context")
	err := Render(Options{
		SourceRoot:      source,
		OutputContext:   output,
		Registry:        "registry.example:5000",
		Version:         "1.0.1-dev.test",
		ImagePullSecret: "invalid\nsecret",
	})
	if err == nil {
		t.Fatal("Render succeeded with an invalid image pull secret")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("invalid input created output context: %v", err)
	}
}

func testSource(t *testing.T, version, manager, keycloak, bundle string) string {
	t.Helper()
	root := t.TempDir()
	manifests := filepath.Join(root, "bundle", "manifests")
	if err := os.MkdirAll(manifests, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "bundle", "metadata"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "catalog"), 0o755); err != nil {
		t.Fatal(err)
	}
	name := "sample-operator." + version
	writeTestFile(t, filepath.Join(manifests, name+".clusterserviceversion.yaml"), "metadata:\n  name: "+name+"\nspec:\n  install:\n    spec:\n      deployments:\n        - spec:\n            template:\n              spec:\n                containers:\n                  - image: "+manager+"\n                     imagePullPolicy: Always\n                     name: manager\n                     env:\n                      - name: RELATED_IMAGE_KEYCLOAK\n                        value: "+keycloak+"\n                imagePullSecrets:\n                  - name: source-secret\n  relatedImages:\n    - image: "+keycloak+"\n      name: neteye-keycloak\n  version: "+version+"\n")
	writeTestFile(t, filepath.Join(root, "bundle", "metadata", "annotations.yaml"), "annotations:\n  operators.operatorframework.io.bundle.version.v1: "+version+"\n")
	writeTestFile(t, filepath.Join(root, "catalog", "index.yaml"), "schema: olm.package\nname: sample-operator\n---\nschema: olm.channel\nentries:\n  - name: "+name+"\n---\nschema: olm.bundle\nname: "+name+"\nimage: "+bundle+"\nproperties:\n  - type: olm.package\n    value:\n      version: "+version+"\n")
	return root
}

func snapshotSource(t *testing.T, root string) string {
	t.Helper()
	return readTestFile(t, filepath.Join(root, "bundle", "manifests", mustCSV(t, root))) + readTestFile(t, filepath.Join(root, "bundle", "metadata", "annotations.yaml")) + readTestFile(t, filepath.Join(root, "catalog", "index.yaml"))
}
func mustCSV(t *testing.T, root string) string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(root, "bundle", "manifests", "*.clusterserviceversion.yaml"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("CSV discovery failed: %v, %v", paths, err)
	}
	return filepath.Base(paths[0])
}
func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
func readTestFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

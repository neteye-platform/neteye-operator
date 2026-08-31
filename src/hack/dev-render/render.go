// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

var (
	semverPrerelease  = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
	registryHostRE    = regexp.MustCompile(`^(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)(?:\.(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?))*$|^(?:25[0-5]|2[0-4][0-9]|1?[0-9]{1,2})(?:\.(?:25[0-5]|2[0-4][0-9]|1?[0-9]{1,2})){3}$`)
	imageRE           = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*(?::[0-9]+)?/[a-z0-9][a-z0-9._/-]*(?::[A-Za-z0-9_][A-Za-z0-9_.-]*|@sha256:[a-fA-F0-9]{64})$`)
	dns1123RE         = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$`)
	bundleVersionRE   = regexp.MustCompile(`(?m)^  operators\.operatorframework\.io\.bundle\.version\.v1: ([^\s]+)$`)
	csvNameRE         = regexp.MustCompile(`(?m)^  name: ([^\s]+)$`)
	csvVersionRE      = regexp.MustCompile(`(?m)^  version: ([^\s]+)$`)
	managerImageRE    = regexp.MustCompile(`(?m)^[ \t]+- image: ([^\s]+)\n[ \t]+imagePullPolicy: [^\n]+\n[ \t]+name: manager$`)
	keycloakEnvRE     = regexp.MustCompile(`(?m)^[ \t]+- name: RELATED_IMAGE_KEYCLOAK\n[ \t]+value: ([^\s]+)$`)
	keycloakRelatedRE = regexp.MustCompile(`(?m)^[ \t]+- image: ([^\s]+)\n[ \t]+name: neteye-keycloak$`)
	fbcDocumentRE     = regexp.MustCompile(`(?ms)^schema: olm\.bundle\n(.*?)(?:^---\n?|\z)`)
	fbcBundleNameRE   = regexp.MustCompile(`(?m)^name: ([^\s]+)$`)
	fbcBundleImageRE  = regexp.MustCompile(`(?m)^image: ([^\s]+)$`)
	fbcVersionRE      = regexp.MustCompile(`(?ms)^  - type: olm\.package\n    value:\n.*?^      version: ([^\s]+)$`)
)

// Options configures an isolated development bundle and catalog render.
type Options struct {
	SourceRoot      string
	OutputContext   string
	Registry        string
	Version         string
	KeycloakImage   string
	ImagePullSecret string
}

type sourceArtifacts struct {
	csvPath, csvName, version, managerImage, keycloakImage, bundleImage string
}

// Render copies and rewrites bundle and catalog artifacts without modifying source.
func Render(options Options) error {
	if err := validateRegistry(options.Registry); err != nil {
		return err
	}
	if err := validateVersion(options.Version); err != nil {
		return err
	}
	if err := validatePullSecret(options.ImagePullSecret); err != nil {
		return err
	}
	if options.SourceRoot == "" || options.OutputContext == "" {
		return errors.New("source root and output context are required")
	}
	sourceRoot, err := filepath.Abs(options.SourceRoot)
	if err != nil {
		return fmt.Errorf("resolve source root: %w", err)
	}
	outputContext, err := validateOutputContext(sourceRoot, options.OutputContext)
	if err != nil {
		return err
	}
	artifacts, err := discoverSource(sourceRoot)
	if err != nil {
		return err
	}
	keycloakImage := options.KeycloakImage
	if keycloakImage == "" {
		keycloakImage, err = imageInRegistry(options.Registry, artifacts.keycloakImage, "")
		if err != nil {
			return fmt.Errorf("derive Keycloak image: %w", err)
		}
	}
	if err := validateImage(keycloakImage); err != nil {
		return fmt.Errorf("validate Keycloak image: %w", err)
	}
	managerImage, err := imageInRegistry(options.Registry, artifacts.managerImage, options.Version)
	if err != nil {
		return fmt.Errorf("derive manager image: %w", err)
	}
	bundleImage, err := imageInRegistry(options.Registry, artifacts.bundleImage, options.Version)
	if err != nil {
		return fmt.Errorf("derive bundle image: %w", err)
	}
	if err := os.RemoveAll(outputContext); err != nil {
		return fmt.Errorf("remove output context: %w", err)
	}
	if err := copyTree(filepath.Join(sourceRoot, "bundle"), filepath.Join(outputContext, "bundle")); err != nil {
		return fmt.Errorf("copy bundle: %w", err)
	}
	if err := copyTree(filepath.Join(sourceRoot, "catalog"), filepath.Join(outputContext, "catalog")); err != nil {
		return fmt.Errorf("copy catalog: %w", err)
	}
	renderedCSV := filepath.Join(outputContext, "bundle", "manifests", artifacts.csvName[:len(artifacts.csvName)-len(artifacts.version)]+options.Version+".clusterserviceversion.yaml")
	copiedCSV := filepath.Join(outputContext, "bundle", "manifests", filepath.Base(artifacts.csvPath))
	if err := renderCSV(copiedCSV, artifacts, options.Version, managerImage, keycloakImage, options.ImagePullSecret); err != nil {
		return err
	}
	if err := os.Rename(copiedCSV, renderedCSV); err != nil {
		return fmt.Errorf("rename rendered CSV: %w", err)
	}
	if err := replaceExactly(filepath.Join(outputContext, "bundle", "metadata", "annotations.yaml"), artifacts.version, options.Version, 1); err != nil {
		return fmt.Errorf("render bundle metadata: %w", err)
	}
	if err := renderCatalog(filepath.Join(outputContext, "catalog", "index.yaml"), artifacts, options.Version, bundleImage); err != nil {
		return fmt.Errorf("render catalog: %w", err)
	}
	bundleRepository := imageRepository(bundleImage)
	operatorRepository, found := strings.CutSuffix(bundleRepository, "-bundle")
	if !found || operatorRepository == "" {
		return fmt.Errorf("derive catalog image: bundle repository must end in -bundle: %q", artifacts.bundleImage)
	}
	catalogRepository := operatorRepository + "-catalog"
	return writeState(outputContext, options.Version, registryImage(options.Registry, catalogRepository, options.Version), bundleImage)
}

// Clean removes a renderer-owned output context after applying the same path
// safety checks used before rendering.
func Clean(sourceRoot, output string) error {
	if sourceRoot == "" || output == "" {
		return errors.New("source root and output context are required")
	}
	root, err := filepath.Abs(sourceRoot)
	if err != nil {
		return fmt.Errorf("resolve source root: %w", err)
	}
	context, err := validateOutputContext(root, output)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(context); err != nil {
		return fmt.Errorf("remove output context: %w", err)
	}
	return nil
}

func discoverSource(root string) (sourceArtifacts, error) {
	metadata, err := os.ReadFile(filepath.Join(root, "bundle", "metadata", "annotations.yaml"))
	if err != nil {
		return sourceArtifacts{}, fmt.Errorf("read bundle metadata: %w", err)
	}
	version, err := exactlyOne(bundleVersionRE, string(metadata), "bundle metadata version")
	if err != nil {
		return sourceArtifacts{}, err
	}
	csvs, err := filepath.Glob(filepath.Join(root, "bundle", "manifests", "*.clusterserviceversion.yaml"))
	if err != nil {
		return sourceArtifacts{}, fmt.Errorf("discover CSV: %w", err)
	}
	if len(csvs) != 1 {
		return sourceArtifacts{}, fmt.Errorf("expected exactly one CSV, found %d", len(csvs))
	}
	csv, err := os.ReadFile(csvs[0])
	if err != nil {
		return sourceArtifacts{}, fmt.Errorf("read CSV: %w", err)
	}
	text := string(csv)
	name, err := exactlyOne(csvNameRE, text, "CSV metadata name")
	if err != nil {
		return sourceArtifacts{}, err
	}
	csvVersion, err := exactlyOne(csvVersionRE, text, "CSV spec version")
	if err != nil {
		return sourceArtifacts{}, err
	}
	if csvVersion != version || !strings.HasSuffix(name, "."+version) || filepath.Base(csvs[0]) != name+".clusterserviceversion.yaml" {
		return sourceArtifacts{}, errors.New("CSV filename, metadata name, spec.version, and bundle metadata version must agree")
	}
	manager, err := exactlyOne(managerImageRE, text, "manager image")
	if err != nil {
		return sourceArtifacts{}, err
	}
	keycloak, err := exactlyOne(keycloakEnvRE, text, "RELATED_IMAGE_KEYCLOAK")
	if err != nil {
		return sourceArtifacts{}, err
	}
	related, err := exactlyOne(keycloakRelatedRE, text, "Keycloak related image")
	if err != nil {
		return sourceArtifacts{}, err
	}
	if keycloak != related {
		return sourceArtifacts{}, errors.New("RELATED_IMAGE_KEYCLOAK and Keycloak related image must agree")
	}
	if err := validateImage(manager); err != nil {
		return sourceArtifacts{}, fmt.Errorf("validate manager source image: %w", err)
	}
	if err := validateImage(keycloak); err != nil {
		return sourceArtifacts{}, fmt.Errorf("validate Keycloak source image: %w", err)
	}
	catalog, err := os.ReadFile(filepath.Join(root, "catalog", "index.yaml"))
	if err != nil {
		return sourceArtifacts{}, fmt.Errorf("read catalog: %w", err)
	}
	catalogText := string(catalog)
	bundleDocument, err := exactlyOne(fbcDocumentRE, catalogText, "FBC bundle document")
	if err != nil {
		return sourceArtifacts{}, err
	}
	bundleName, err := exactlyOne(fbcBundleNameRE, bundleDocument, "FBC bundle name")
	if err != nil {
		return sourceArtifacts{}, err
	}
	if bundleName != name {
		return sourceArtifacts{}, errors.New("FBC bundle name must match CSV metadata name")
	}
	bundleImage, err := exactlyOne(fbcBundleImageRE, bundleDocument, "FBC bundle image")
	if err != nil {
		return sourceArtifacts{}, err
	}
	if err := validateImage(bundleImage); err != nil {
		return sourceArtifacts{}, fmt.Errorf("validate bundle source image: %w", err)
	}
	fbcVersion, err := exactlyOne(fbcVersionRE, bundleDocument, "FBC package version")
	if err != nil {
		return sourceArtifacts{}, err
	}
	if fbcVersion != version {
		return sourceArtifacts{}, errors.New("FBC package version must match bundle metadata version")
	}
	return sourceArtifacts{csvs[0], name, version, manager, keycloak, bundleImage}, nil
}

func exactlyOne(re *regexp.Regexp, text, description string) (string, error) {
	matches := re.FindAllStringSubmatch(text, -1)
	if len(matches) != 1 {
		return "", fmt.Errorf("expected exactly one %s, found %d", description, len(matches))
	}
	return matches[0][1], nil
}

func imageInRegistry(registry, source, version string) (string, error) {
	if err := validateImage(source); err != nil {
		return "", err
	}
	leaf := source[strings.LastIndex(source, "/")+1:]
	name, _, found := strings.Cut(leaf, ":")
	if !found || name == "" {
		return "", fmt.Errorf("source image must include a tag: %q", source)
	}
	if version == "" {
		return registry + "/" + leaf, nil
	}
	return registryImage(registry, name, version), nil
}

func imageRepository(image string) string {
	return image[strings.LastIndex(image, "/")+1 : strings.LastIndex(image, ":")]
}
func registryImage(registry, repository, version string) string {
	return registry + "/" + repository + ":" + version
}

func renderCatalog(path string, artifacts sourceArtifacts, version, bundleImage string) error {
	if err := replaceExactly(path, artifacts.csvName, artifacts.csvName[:len(artifacts.csvName)-len(artifacts.version)]+version, 2); err != nil {
		return err
	}
	if err := replaceExactly(path, artifacts.bundleImage, bundleImage, 1); err != nil {
		return err
	}
	return replaceExactly(path, "version: "+artifacts.version, "version: "+version, 1)
}

func validateRegistry(registry string) error {
	if invalidToken(registry) || strings.Contains(registry, "://") || strings.Contains(registry, "/") || strings.ContainsAny(registry, "?#@") {
		return fmt.Errorf("registry must be a whitespace-free host[:port] without scheme, path, or userinfo: %q", registry)
	}
	host, port, hasPort := strings.Cut(registry, ":")
	if strings.Contains(host, ":") || !registryHostRE.MatchString(host) {
		return fmt.Errorf("registry host must be a hostname or IPv4 address: %q", registry)
	}
	if hasPort {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return fmt.Errorf("registry port must be numeric and in 1..65535: %q", registry)
		}
	}
	return nil
}

func validateVersion(version string) error {
	matches := semverPrerelease.FindStringSubmatch(version)
	if matches == nil {
		return fmt.Errorf("version must be a plain valid SemVer with a prerelease component: %q", version)
	}
	for identifier := range strings.SplitSeq(matches[4], ".") {
		if !strings.ContainsFunc(identifier, unicode.IsLetter) && !strings.ContainsFunc(identifier, unicode.IsDigit) {
			return fmt.Errorf("version prerelease identifiers must contain an alphanumeric character: %q", version)
		}
		if len(identifier) > 1 && identifier[0] == '0' && strings.Trim(identifier, "0123456789") == "" {
			return fmt.Errorf("version contains a numeric prerelease identifier with a leading zero: %q", version)
		}
	}
	return nil
}

func renderCSV(path string, artifacts sourceArtifacts, version, managerImage, keycloakImage, imagePullSecret string) error {
	if err := validateImage(managerImage); err != nil {
		return err
	}
	if err := validateImage(keycloakImage); err != nil {
		return err
	}
	if err := replaceExactly(path, "name: "+artifacts.csvName, "name: "+artifacts.csvName[:len(artifacts.csvName)-len(artifacts.version)]+version, 1); err != nil {
		return err
	}
	if err := replaceExactly(path, "image: "+artifacts.managerImage, "image: "+managerImage, 1); err != nil {
		return err
	}
	if err := replaceExactly(path, "value: "+artifacts.keycloakImage, "value: "+keycloakImage, 1); err != nil {
		return err
	}
	if err := replaceExactly(path, "image: "+artifacts.keycloakImage, "image: "+keycloakImage, 1); err != nil {
		return err
	}
	if err := replaceExactly(path, "version: "+artifacts.version, "version: "+version, 1); err != nil {
		return err
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := string(contents)
	secretRE := regexp.MustCompile(`(?m)^[ \t]+imagePullSecrets:\n[ \t]+- name: [^\n]+\n`)
	if imagePullSecret == "" {
		if len(secretRE.FindAllString(text, -1)) != 1 {
			return errors.New("CSV expected one imagePullSecrets block")
		}
		text = secretRE.ReplaceAllString(text, "")
	} else {
		if len(secretRE.FindAllString(text, -1)) != 1 {
			return errors.New("CSV expected one imagePullSecrets block")
		}
		text = secretRE.ReplaceAllString(text, "                imagePullSecrets:\n                  - name: "+imagePullSecret+"\n")
	}
	return os.WriteFile(path, []byte(text), 0o644)
}

func validateOutputContext(sourceRoot, output string) (string, error) {
	outputContext, err := filepath.Abs(output)
	if err != nil {
		return "", fmt.Errorf("resolve output context: %w", err)
	}
	managedRoot := filepath.Join(sourceRoot, "bin", "dev")
	relative, err := filepath.Rel(managedRoot, outputContext)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("output context must be a descendant of %s", managedRoot)
	}
	fromSource, err := filepath.Rel(sourceRoot, outputContext)
	if err != nil {
		return "", err
	}
	current := sourceRoot
	for _, component := range strings.Split(fromSource, string(os.PathSeparator)) {
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		if statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("output context path contains symlinked component %s", current)
		}
		if statErr != nil && !os.IsNotExist(statErr) {
			return "", statErr
		}
	}
	return outputContext, nil
}

func validateImage(image string) error {
	if invalidToken(image) || strings.Contains(image, "://") || !imageRE.MatchString(image) {
		return fmt.Errorf("image must be a single-line OCI-like registry/repository reference with tag or sha256 digest: %q", image)
	}
	return nil
}

func validatePullSecret(secret string) error {
	if secret == "" {
		return nil
	}
	if len(secret) > 253 || invalidToken(secret) || !dns1123RE.MatchString(secret) {
		return fmt.Errorf("image pull secret must be a DNS-1123 subdomain up to 253 characters: %q", secret)
	}
	return nil
}

func invalidToken(value string) bool {
	return value == "" || strings.ContainsRune(value, ',') || strings.ContainsFunc(value, unicode.IsSpace) || strings.ContainsFunc(value, unicode.IsControl)
}

func replaceExactly(path, old, replacement string, want int) error {
	contents, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := string(contents)
	if count := strings.Count(text, old); count != want {
		return fmt.Errorf("expected %d %q values, found %d", want, old, count)
	}
	return os.WriteFile(path, []byte(strings.ReplaceAll(text, old, replacement)), 0o644)
}

func writeState(output, version, catalogImage, bundleImage string) error {
	for path, value := range map[string]string{".dev-version": version, ".catalog-image": catalogImage, ".bundle-image": bundleImage} {
		if err := os.WriteFile(filepath.Join(output, path), []byte(value+"\n"), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return nil
}

func copyTree(source, destination string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", source)
	}
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("unsupported source entry %s", path)
		}
		return copyFile(path, target)
	})
}

func copyFile(source, destination string) error {
	contents, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return os.WriteFile(destination, contents, 0o644)
}

// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
	corev1 "k8s.io/api/core/v1"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
)

// classisServerImageEnv turns on the tests that start the pinned Classis Server
// image. They need a container runtime and the image, which they pull, so
// `make test` leaves them out; `make test-classis-server-image` and the CI run
// them.
const classisServerImageEnv = "CLASSIS_SERVER_IMAGE_TEST"

// classisServerConfigMarker ends the message of every error the Server finds in
// its configuration, before it opens a store.
const classisServerConfigMarker = " and the environment: "

// The image refuses an unknown key, so a rendered section that it does not
// know stops every tenant's server at start. This starts the pinned image on
// the configuration the operator renders, with and without revoked signers,
// and checks that it reads the configuration.
//
// The server cannot reach a database from here, so a good configuration ends
// in an error about the database, after the configuration check. What the
// test looks for is the absence of an error about the configuration. Two runs
// show that the absence means something: the image names `revoked_signers`
// when an item is not a fingerprint, so it does read the key, and it refuses
// a section that it does not know.
func TestPinnedClassisServerImageAcceptsTheRenderedConfiguration(t *testing.T) {
	if os.Getenv(classisServerImageEnv) != "1" {
		t.Skipf("set %s=1 to start the Classis Server image", classisServerImageEnv)
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("%s=1 needs docker: %v", classisServerImageEnv, err)
	}
	// The image the operator pins, or the one that RELATED_IMAGE_CLASSIS_SERVER
	// names, to try an image before it is pinned.
	image := currentImages(t).ClassisServerImage
	if override := strings.TrimSpace(os.Getenv(neteye.RelatedImageClassisServerEnv)); override != "" {
		image = override
	}
	// Pulled once, so that no run below prints the progress of a pull.
	pull, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if output, err := exec.CommandContext(pull, "docker", "pull", "--quiet", image).CombinedOutput(); err != nil { //nolint:gosec // the image is the one the operator pins
		t.Fatalf("docker pull %s: %v\n%s", image, err, output)
	}
	tenant := classisServerTenant(neteye.TenantStorageExternal)
	signers := []string{strings.Repeat("a", 64), strings.Repeat("b", 64)}

	t.Run("without revoked signers", func(t *testing.T) {
		config := renderedClassisServerConfig(t, tenant, nil)
		if strings.Contains(config, "[packages]") {
			t.Fatalf("a configuration without revoked signers has a [packages] section:\n%s", config)
		}
		assertConfigurationAccepted(t, image, tenant, config)
	})

	t.Run("with revoked signers", func(t *testing.T) {
		config := renderedClassisServerConfig(t, tenant, signers)
		if !strings.Contains(config, "[packages]") {
			t.Fatalf("a configuration with revoked signers has no [packages] section:\n%s", config)
		}
		assertConfigurationAccepted(t, image, tenant, config)
	})

	t.Run("reads revoked_signers", func(t *testing.T) {
		config := renderedClassisServerConfig(t, tenant, []string{"not-a-fingerprint"})
		output := runClassisServerImage(t, image, tenant, config)
		if !strings.Contains(output, "revoked_signers") {
			t.Errorf("the image does not name [packages] revoked_signers for an item that is no fingerprint, so it does not read the key:\n%s", output)
		}
	})

	t.Run("refuses an unknown section", func(t *testing.T) {
		config := renderedClassisServerConfig(t, tenant, nil) + "\n[unknown_section]\nkey = 1\n"
		output := runClassisServerImage(t, image, tenant, config)
		if !strings.Contains(output, "unknown") {
			t.Errorf("the image accepted a section that it does not know, so this test cannot tell a section it refuses:\n%s", output)
		}
	})
}

func renderedClassisServerConfig(t *testing.T, tenant *neteye.NetEyeTenant, revokedSigners []string) string {
	t.Helper()
	config, err := classisServerConfig(tenant, revokedSigners)
	if err != nil {
		t.Fatal(err)
	}
	return config
}

// assertConfigurationAccepted checks that the server read the configuration,
// whatever it did next.
func assertConfigurationAccepted(t *testing.T, image string, tenant *neteye.NetEyeTenant, config string) {
	t.Helper()
	output := runClassisServerImage(t, image, tenant, config)
	t.Logf("the image said:\n%s", output)
	if strings.Contains(output, classisServerConfigMarker) {
		t.Errorf("the image refused the configuration the operator renders:\n%s\n--- configuration ---\n%s", output, config)
	}
}

// runClassisServerImage starts the image as the Deployment of the tenant does:
// its arguments, the environment, and the files that the volumes put under
// /etc/classis. The Secrets hold made-up values, and the container has no
// network. It returns what the server wrote before it stopped.
func runClassisServerImage(t *testing.T, image string, tenant *neteye.NetEyeTenant, config string) string {
	t.Helper()
	container := classisServerDeployment(tenant, image, "config", "secrets").Spec.Template.Spec.Containers[0]

	root := t.TempDir()
	writeClassisServerFile(t, root, "/etc/classis/config/"+classisServerConfigFile, config)
	for _, path := range configuredFiles(t, config) {
		content := "made-up\n"
		if strings.HasSuffix(path, "/basic-users") {
			content = "smoke = " + strconv.Quote(classisServerImageCredential(t, image)) + "\n"
		}
		writeClassisServerFile(t, root, path, content)
	}

	// The server demands that its secrets are not readable by others, so the
	// files are 0600 and only root of the container reads them whoever runs
	// docker: the user of the test with a rootless runtime, any file with the
	// daemon of docker.
	args := []string{"run", "--rm", "--network", "none", "--user", "0:0", "-v", root + ":/etc/classis:ro,z"}
	for _, env := range container.Env {
		args = append(args, "-e", env.Name+"="+envValue(env))
	}
	args = append(args, image)
	args = append(args, container.Args...)
	_, output := dockerRun(t, "", args...)
	return output
}

// configuredFiles lists the files that the configuration names under
// /etc/classis, in the keys that end in `_file`.
func configuredFiles(t *testing.T, config string) []string {
	t.Helper()
	var decoded map[string]any
	if _, err := toml.Decode(config, &decoded); err != nil {
		t.Fatalf("the rendered configuration is not TOML: %v", err)
	}
	var files []string
	var walk func(map[string]any)
	walk = func(table map[string]any) {
		for key, value := range table {
			switch v := value.(type) {
			case map[string]any:
				walk(v)
			case string:
				if strings.HasSuffix(key, "_file") && strings.HasPrefix(v, "/etc/classis/") {
					files = append(files, v)
				}
			}
		}
	}
	walk(decoded)
	return files
}

func envValue(env corev1.EnvVar) string {
	if env.ValueFrom != nil {
		return "made-up"
	}
	return env.Value
}

func writeClassisServerFile(t *testing.T, root, path, content string) {
	t.Helper()
	target := filepath.Join(root, strings.TrimPrefix(path, "/etc/classis/"))
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// classisServerImageCredential asks the image for the entry of a Basic user,
// so that the test holds no hash of its own.
func classisServerImageCredential(t *testing.T, image string) string {
	t.Helper()
	stdout, _ := dockerRun(t, "smoke-test", "run", "--rm", "-i", "--network", "none", image, "hash-credential", "--basic")
	return strings.TrimSpace(stdout)
}

// dockerRun runs docker and returns what it wrote on the standard output, and
// on both outputs together. A non-zero exit is no error here: the tests read
// what the server said.
func dockerRun(t *testing.T, stdin string, args ...string) (stdout, output string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...) //nolint:gosec // the arguments are made by this test
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, both bytes.Buffer
	cmd.Stdout, cmd.Stderr = io.MultiWriter(&out, &both), &both
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), ctx.Err(), both.String())
		}
		var exited *exec.ExitError
		if !errors.As(err, &exited) {
			t.Fatalf("docker %s: %v", strings.Join(args, " "), err)
		}
	}
	return out.String(), both.String()
}

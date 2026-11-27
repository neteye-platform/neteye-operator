// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"errors"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
)

// The images of the tenant components follow the NetEye version of the
// cluster, as the images of Keycloak do; the RELATED_IMAGE_* variables of the
// operator override them.
func TestTenantImagesFollowTheNetEyeVersion(t *testing.T) {
	// The defaults, whatever the environment of the developer overrides.
	for _, env := range []string{neteye.RelatedImageClassisServerEnv, neteye.RelatedImagePostgreSQLEnv, neteye.RelatedImageRustFSEnv, neteye.RelatedImageS3ClientEnv} {
		t.Setenv(env, "")
	}
	r, _ := newTenantReconciler(t, netEyeResource())
	images, err := r.tenantImages(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for name, image := range map[string]string{"classis-server": images.ClassisServerImage, "postgresql": images.PostgreSQLImage, "rustfs": images.RustFSImage, "s3-client": images.S3ClientImage} {
		if image == "" {
			t.Errorf("no %s image for NetEye %s", name, neteye.CurrentNetEyeVersion)
		}
	}
	for _, image := range []string{images.ClassisServerImage, images.PostgreSQLImage, images.RustFSImage, images.S3ClientImage} {
		if !strings.Contains(image, "@sha256:") {
			t.Errorf("image %q is not pinned by digest", image)
		}
	}

	t.Setenv(neteye.RelatedImageClassisServerEnv, "registry.example/classis-server:dev")
	t.Setenv(neteye.RelatedImagePostgreSQLEnv, "registry.example/postgresql:dev")
	t.Setenv(neteye.RelatedImageRustFSEnv, "registry.example/rustfs:dev")
	t.Setenv(neteye.RelatedImageS3ClientEnv, "registry.example/aws-cli:dev")
	images, err = r.tenantImages(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if images.ClassisServerImage != "registry.example/classis-server:dev" || images.PostgreSQLImage != "registry.example/postgresql:dev" || images.RustFSImage != "registry.example/rustfs:dev" || images.S3ClientImage != "registry.example/aws-cli:dev" {
		t.Errorf("overrides ignored: %#v", images)
	}
}

func TestTenantImagesNeedOneKnownNetEyeVersion(t *testing.T) {
	r, _ := newTenantReconciler(t)
	if _, err := r.tenantImages(context.Background()); !errors.Is(err, errNoNetEye) {
		t.Errorf("without a NetEye resource: %v, want errNoNetEye", err)
	}

	unknown := netEyeResource()
	unknown.Spec.Version = "9.99"
	r, _ = newTenantReconciler(t, unknown)
	if _, err := r.tenantImages(context.Background()); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Errorf("unknown version: %v", err)
	}

	other := netEyeResource()
	other.ObjectMeta = metav1.ObjectMeta{Namespace: "elsewhere", Name: "neteye"}
	other.Spec.Version = neteye.PreviousNetEyeVersion
	r, _ = newTenantReconciler(t, netEyeResource(), other)
	if _, err := r.tenantImages(context.Background()); err == nil || !strings.Contains(err.Error(), "different versions") {
		t.Errorf("disagreeing versions: %v", err)
	}
}

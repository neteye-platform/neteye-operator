// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/keycloak"
	"github.com/neteye-platform/neteye-operator/internal/resources"
)

var (
	certificateGVK = schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"}
	issuerGVK      = schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Issuer"}
	gatewayGVK     = schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "Gateway"}
	httpRouteGVK   = schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRoute"}
)

// startEnvtest boots a real API server via envtest. It skips the test when the
// envtest binaries are not installed (run "make envtest" / set KUBEBUILDER_ASSETS)
// so that a plain "go test ./..." stays green without them.
func startEnvtest(t *testing.T) (client.Client, *runtime.Scheme) {
	t.Helper()

	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("client-go scheme: %v", err)
	}
	if err := neteye.AddToScheme(s); err != nil {
		t.Fatalf("neteye scheme: %v", err)
	}

	env := &envtest.Environment{
		CRDInstallOptions: envtest.CRDInstallOptions{
			Paths: []string{
				filepath.Join("testdata", "crds"),
				filepath.Join("..", "bundle", "manifests", "neteyes.neteye.cloud.crd.yaml"),
			},
		},
	}
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("envtest binaries not available (run 'make envtest' / set KUBEBUILDER_ASSETS)")
	}
	cfg, err := env.Start()
	t.Cleanup(func() { _ = env.Stop() })
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}

	c, err := client.New(cfg, client.Options{Scheme: s})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	return c, s
}

func newUnstructured(gvk schema.GroupVersionKind, namespace, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	u.SetNamespace(namespace)
	u.SetName(name)
	return u
}

func requireExists(ctx context.Context, t *testing.T, c client.Client, gvk schema.GroupVersionKind, namespace, name string) *unstructured.Unstructured {
	t.Helper()
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, u); err != nil {
		t.Fatalf("expected %s %s/%s to exist: %v", gvk.Kind, namespace, name, err)
	}
	return u
}

func TestReconcileBaseResourcesAgainstAPIServer(t *testing.T) {
	c, s := startEnvtest(t)
	ctx := context.Background()
	const ns = "neteye-envtest"

	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	if err := c.Create(ctx, newUnstructured(issuerGVK, ns, "internal-issuer")); err != nil {
		t.Fatalf("create issuer: %v", err)
	}

	ne := &neteye.NetEye{
		ObjectMeta: metav1.ObjectMeta{Name: "platform", Namespace: ns},
		Spec: neteye.NetEyeSpec{
			Version:                      neteye.CurrentNetEyeVersion,
			InternalCertificateIssuerRef: "internal-issuer",
			Gateway: neteye.NetEyeGatewaySpec{
				Name:          "neteye-gw",
				ClassName:     "cilium",
				TLSSecretName: "gateway-tls",
			},
			Identity: neteye.NetEyeIdentitySpec{
				Hostname: "keycloak.example.com",
				DBConnection: neteye.NetEyeDBConnectionSpec{
					Host:           "mariadb.example.com",
					DBName:         "keycloak",
					UsernameSecret: neteye.NetEyeSecretKeySelector{Name: "kc-db", Key: "username"},
					PasswordSecret: neteye.NetEyeSecretKeySelector{Name: "kc-db", Key: "password"},
				},
			},
		},
	}
	if err := c.Create(ctx, ne); err != nil {
		t.Fatalf("create neteye: %v", err)
	}

	r := &NetEyeReconciler{
		Client:            c,
		Log:               logr.Discard(),
		Scheme:            s,
		KeycloakComponent: keycloak.NewComponent(c, logr.Discard()),
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "platform"}}

	// First pass: the gateway TLS certificate is not ready yet, so the operator
	// provisions the base resources and reports NotReady.
	res, err := r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res.RequeueAfter != waitForProgressingRequeueAfter {
		t.Errorf("requeueAfter = %v, want %v", res.RequeueAfter, waitForProgressingRequeueAfter)
	}

	cert := requireExists(ctx, t, c, certificateGVK, ns, "gateway-tls")
	if owner := cert.GetOwnerReferences(); len(owner) != 1 || owner[0].Name != "platform" {
		t.Errorf("gateway certificate owner references = %v", owner)
	}
	requireExists(ctx, t, c, gatewayGVK, ns, "neteye-gw")
	requireExists(ctx, t, c, httpRouteGVK, ns, resources.HTTPToHTTPSRedirectRouteName)

	got := &neteye.NetEye{}
	if err := c.Get(ctx, req.NamespacedName, got); err != nil {
		t.Fatalf("get neteye: %v", err)
	}
	if got.Status.Phase != neteye.PhaseNotReady {
		t.Errorf("phase = %q, want %q", got.Status.Phase, neteye.PhaseNotReady)
	}

	// Simulate cert-manager marking the gateway certificate Ready, then reconcile
	// again: the operator should progress into the Keycloak phase and provision
	// the identity TLS certificate.
	if err := unstructured.SetNestedSlice(cert.Object,
		[]any{map[string]any{"type": "Ready", "status": "True"}}, "status", "conditions"); err != nil {
		t.Fatal(err)
	}
	if err := c.Status().Update(ctx, cert); err != nil {
		t.Fatalf("update certificate status: %v", err)
	}

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	requireExists(ctx, t, c, certificateGVK, ns, keycloak.TLSCertificateName)
}

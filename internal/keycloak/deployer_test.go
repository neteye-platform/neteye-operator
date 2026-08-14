/*
Copyright (c) 2026 Würth IT Italy S.r.l.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package keycloak

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	neteyev1alpha1 "github.com/neteye/neteye-platform-operator/api/v1alpha1"
)

func testDeployer(t *testing.T, objects ...client.Object) (*Deployer, client.Client) {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := neteyev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	// The upstream Keycloak and OLM types are not compiled in; the operator
	// only ever writes a fixed shape and reads conditions back.
	scheme.AddKnownTypeWithName(upstreamKeycloakGVK(), &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(clusterExtensionGVK(), &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(crdGVK(), &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(certificateGVK(), &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(issuerGVK(), &unstructured.Unstructured{})

	owner := &neteyev1alpha1.Keycloak{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha", Namespace: "neteye-system", UID: "uid-alpha"},
		Spec: neteyev1alpha1.KeycloakSpec{
			Image:       "reg/kc:1",
			ConfigImage: "reg/cfg:1",
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(append(objects, owner)...).Build()
	return &Deployer{Client: c, Owner: owner}, c
}

// Everything the instance is made of has to go away with the CR that declares
// it; without the ownerReference an admin deleting a Keycloak would leave its
// Secrets and Job behind.
func TestNamespacedObjectsAreOwnedByTheCR(t *testing.T) {
	d, c := testDeployer(t)
	ctx := context.Background()

	if err := d.EnsureDBSecret(ctx); err != nil {
		t.Fatalf("EnsureDBSecret: %v", err)
	}
	if err := d.EnsureCertificate(ctx); err != nil {
		t.Fatalf("EnsureCertificate: %v", err)
	}
	if _, err := d.EnsureOperatorClientSecret(ctx); err != nil {
		t.Fatalf("EnsureOperatorClientSecret: %v", err)
	}
	if err := d.EnsureBootstrapJob(ctx, d.ConfigHash("secret")); err != nil {
		t.Fatalf("EnsureBootstrapJob: %v", err)
	}

	n := namesFor("alpha")
	for _, name := range []string{n.DBSecret, n.ClientSecret} {
		secret := &corev1.Secret{}
		if err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: "neteye-system"}, secret); err != nil {
			t.Fatalf("get %s: %v", name, err)
		}
		if len(secret.OwnerReferences) == 0 {
			t.Errorf("%s has no ownerReference, it would outlive its Keycloak CR", name)
		}
	}

	job := &batchv1.Job{}
	if err := c.Get(ctx, types.NamespacedName{Name: n.BootstrapJob, Namespace: "neteye-system"}, job); err != nil {
		t.Fatalf("get job: %v", err)
	}
	if len(job.OwnerReferences) == 0 {
		t.Error("the bootstrap Job has no ownerReference")
	}
}

// A credential that is still present must never be silently replaced: whatever
// already consumed it — Keycloak itself — would keep using the old value.
func TestGeneratedSecretsAreStableAcrossPasses(t *testing.T) {
	d, _ := testDeployer(t)
	ctx := context.Background()

	first, err := d.EnsureOperatorClientSecret(ctx)
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	second, err := d.EnsureOperatorClientSecret(ctx)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}

	if first == "" {
		t.Fatal("no client secret was generated")
	}
	if first != second {
		t.Error("the client secret was regenerated on the second pass, invalidating the registered one")
	}
}

// The operator issues nothing itself: a hand-rolled certificate always gets
// renewal wrong, and cert-manager already owns it.
func TestCertificateIsRequestedFromCertManager(t *testing.T) {
	d, c := testDeployer(t)
	ctx := context.Background()
	n := namesFor("alpha")

	if err := d.EnsureCertificate(ctx); err != nil {
		t.Fatalf("EnsureCertificate: %v", err)
	}

	cert := &unstructured.Unstructured{}
	cert.SetGroupVersionKind(certificateGVK())
	if err := c.Get(ctx, types.NamespacedName{Name: n.Certificate, Namespace: "neteye-system"}, cert); err != nil {
		t.Fatalf("no Certificate was requested: %v", err)
	}

	secretName, _, _ := unstructured.NestedString(cert.Object, "spec", "secretName")
	if secretName != n.TLSSecret {
		t.Errorf("spec.secretName = %q, want the Secret Keycloak mounts (%q)", secretName, n.TLSSecret)
	}
	dnsNames, _, _ := unstructured.NestedStringSlice(cert.Object, "spec", "dnsNames")
	if len(dnsNames) == 0 {
		t.Fatal("the certificate carries no dnsNames")
	}
	// The operator dials the in-cluster Service, so that name has to be on the
	// certificate or every enforcement pass fails the handshake.
	var hasServiceName bool
	for _, n := range dnsNames {
		if n == defaultHostname("alpha", "neteye-system") {
			hasServiceName = true
		}
	}
	if !hasServiceName {
		t.Errorf("dnsNames = %v, want the in-cluster service name among them", dnsNames)
	}
	if len(cert.GetOwnerReferences()) == 0 {
		t.Error("the Certificate has no ownerReference, it would outlive the instance")
	}
}

// An instance nothing outside this operator talks to needs no real CA, so the
// operator provisions an issuer rather than making one mandatory.
func TestSelfSignedIssuerIsProvisionedWhenTheSpecNamesNone(t *testing.T) {
	d, c := testDeployer(t)
	ctx := context.Background()

	if err := d.EnsureCertificate(ctx); err != nil {
		t.Fatalf("EnsureCertificate: %v", err)
	}

	issuer := &unstructured.Unstructured{}
	issuer.SetGroupVersionKind(issuerGVK())
	key := types.NamespacedName{Name: namesFor("alpha").SelfSignedIssuer, Namespace: "neteye-system"}
	if err := c.Get(ctx, key, issuer); err != nil {
		t.Fatalf("no issuer was provisioned: %v", err)
	}

	if _, found, _ := unstructured.NestedMap(issuer.Object, "spec", "selfSigned"); !found {
		t.Error("the provisioned issuer is not self-signed")
	}
}

// An installation that points at its own CA must get that CA, and must not
// have an unused issuer created in its namespace.
func TestDeclaredIssuerIsUsedAsIs(t *testing.T) {
	d, c := testDeployer(t)
	ctx := context.Background()
	d.Owner.Spec.IssuerRef = &neteyev1alpha1.IssuerReference{Name: "neteye-ca", Kind: "ClusterIssuer"}

	if err := d.EnsureCertificate(ctx); err != nil {
		t.Fatalf("EnsureCertificate: %v", err)
	}

	cert := &unstructured.Unstructured{}
	cert.SetGroupVersionKind(certificateGVK())
	key := types.NamespacedName{Name: namesFor("alpha").Certificate, Namespace: "neteye-system"}
	if err := c.Get(ctx, key, cert); err != nil {
		t.Fatal(err)
	}

	name, _, _ := unstructured.NestedString(cert.Object, "spec", "issuerRef", "name")
	kind, _, _ := unstructured.NestedString(cert.Object, "spec", "issuerRef", "kind")
	if name != "neteye-ca" || kind != "ClusterIssuer" {
		t.Errorf("issuerRef = %s/%s, want ClusterIssuer/neteye-ca", kind, name)
	}

	issuer := &unstructured.Unstructured{}
	issuer.SetGroupVersionKind(issuerGVK())
	issuerKey := types.NamespacedName{Name: namesFor("alpha").SelfSignedIssuer, Namespace: "neteye-system"}
	if err := c.Get(ctx, issuerKey, issuer); err == nil {
		t.Error("a self-signed issuer was provisioned although the spec names its own")
	}
}

// Pinning the CA rather than the leaf survives renewal: cert-manager replaces
// the leaf periodically and a pinned leaf would stop matching.
func TestTrustAnchorPrefersTheIssuingCA(t *testing.T) {
	tlsSecret := func(data map[string][]byte) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: namesFor("alpha").TLSSecret, Namespace: "neteye-system"},
			Data:       data,
		}
	}

	cases := []struct {
		name   string
		data   map[string][]byte
		want   string
		issued bool
	}{
		{"a CA was published", map[string][]byte{"ca.crt": []byte("CA"), "tls.crt": []byte("LEAF")}, "CA", true},
		{"self-signed, no CA", map[string][]byte{"tls.crt": []byte("LEAF")}, "LEAF", true},
		{"not issued yet", map[string][]byte{}, "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, _ := testDeployer(t, tlsSecret(tc.data))

			anchor, issued, err := d.TrustAnchor(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if issued != tc.issued {
				t.Errorf("issued = %v, want %v", issued, tc.issued)
			}
			if string(anchor) != tc.want {
				t.Errorf("anchor = %q, want %q", anchor, tc.want)
			}
		})
	}
}

// cert-manager issues asynchronously; the Secret simply is not there yet on
// the pass that requested it, and that is a wait, not a failure.
func TestTrustAnchorTreatsAnAbsentSecretAsNotIssued(t *testing.T) {
	d, _ := testDeployer(t)

	_, issued, err := d.TrustAnchor(context.Background())
	if err != nil {
		t.Fatalf("an absent Secret must not be an error: %v", err)
	}
	if issued {
		t.Error("issued = true with no Secret at all")
	}
}

// Job specs are immutable: a changed config image can only take effect if the
// Job is replaced, and an unchanged one must not be replaced on every pass.
func TestBootstrapJobIsRecreatedOnlyWhenItsInputsChange(t *testing.T) {
	d, c := testDeployer(t)
	ctx := context.Background()
	key := types.NamespacedName{Name: namesFor("alpha").BootstrapJob, Namespace: "neteye-system"}

	if err := d.EnsureBootstrapJob(ctx, d.ConfigHash("secret")); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	job := &batchv1.Job{}
	if err := c.Get(ctx, key, job); err != nil {
		t.Fatal(err)
	}
	firstUID := job.UID

	if err := d.EnsureBootstrapJob(ctx, d.ConfigHash("secret")); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if err := c.Get(ctx, key, job); err != nil {
		t.Fatal(err)
	}
	if job.UID != firstUID {
		t.Error("the Job was replaced although nothing changed")
	}

	const newConfigImage = "reg/cfg:2"
	d.Owner.Spec.ConfigImage = newConfigImage
	if err := d.EnsureBootstrapJob(ctx, d.ConfigHash("secret")); err != nil {
		t.Fatalf("third pass: %v", err)
	}
	if err := c.Get(ctx, key, job); err != nil {
		t.Fatal(err)
	}
	if job.Spec.Template.Spec.Containers[0].Image != newConfigImage {
		t.Error("the Job still runs the old config image")
	}
}

func TestBootstrapJobStateReadsTerminalConditions(t *testing.T) {
	cases := []struct {
		name string
		cond []batchv1.JobCondition
		want BootstrapState
	}{
		{"no conditions yet", nil, BootstrapRunning},
		{
			"complete",
			[]batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}},
			BootstrapSucceeded,
		},
		{
			"failed",
			[]batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}},
			BootstrapFailed,
		},
		{
			"a condition that is False says nothing",
			[]batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionFalse}},
			BootstrapRunning,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			job := &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{Name: namesFor("alpha").BootstrapJob, Namespace: "neteye-system"},
				Status:     batchv1.JobStatus{Conditions: tc.cond},
			}
			d, _ := testDeployer(t, job)

			got, err := d.BootstrapJobState(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("state = %v, want %v", got, tc.want)
			}
		})
	}
}

// The Job is garbage-collected once its TTL expires; treating its absence as a
// failure would flip a healthy instance to Failed an hour after it came up.
func TestBootstrapStateTreatsAnAbsentJobAsRunning(t *testing.T) {
	d, _ := testDeployer(t)

	got, err := d.BootstrapJobState(context.Background())
	if err != nil {
		t.Fatalf("an absent Job must not be an error: %v", err)
	}
	if got != BootstrapRunning {
		t.Errorf("state = %v, want BootstrapRunning", got)
	}
}

// The upstream Keycloak Operator is a cluster-wide singleton: the second
// instance must reuse the first one's install rather than fight it over the
// immutable installation namespace.
func TestExtensionIsInstalledOnceAndReused(t *testing.T) {
	d, c := testDeployer(t)
	ctx := context.Background()

	if err := d.EnsureExtension(ctx, "neteye-system"); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if err := d.EnsureExtension(ctx, "somewhere-else"); err != nil {
		t.Fatalf("second pass: %v", err)
	}

	ext := &unstructured.Unstructured{}
	ext.SetGroupVersionKind(clusterExtensionGVK())
	if err := c.Get(ctx, types.NamespacedName{Name: extensionName}, ext); err != nil {
		t.Fatal(err)
	}
	ns, _, _ := unstructured.NestedString(ext.Object, "spec", "namespace")
	if ns != "neteye-system" {
		t.Errorf("spec.namespace = %q, the first install must stand: it is immutable in OLM", ns)
	}
}

func TestReadinessReadsTheUpstreamCondition(t *testing.T) {
	cases := []struct {
		name       string
		conditions []any
		want       bool
	}{
		{"ready", []any{map[string]any{"type": "Ready", "status": "True"}}, true},
		{"not ready", []any{map[string]any{"type": "Ready", "status": "False"}}, false},
		{"another condition is not readiness", []any{map[string]any{"type": "Degraded", "status": "True"}}, false},
		{"no status yet", nil, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kc := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "k8s.keycloak.org/v2beta1",
				"kind":       "Keycloak",
				"metadata":   map[string]any{"name": "alpha", "namespace": "neteye-system"},
				"status":     map[string]any{"conditions": tc.conditions},
			}}
			d, _ := testDeployer(t, kc)

			got, err := d.IsUpstreamKeycloakReady(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("ready = %v, want %v", got, tc.want)
			}
		})
	}
}

// An upstream CR this pass just created is simply not there yet; that is a
// wait, not a failure.
func TestReadinessTreatsAnAbsentUpstreamCRAsNotReady(t *testing.T) {
	d, _ := testDeployer(t)

	ready, err := d.IsUpstreamKeycloakReady(context.Background())
	if err != nil {
		t.Fatalf("an absent CR must not be an error: %v", err)
	}
	if ready {
		t.Error("ready = true with no upstream CR at all")
	}
}

func TestUpstreamKeycloakIsCreatedFromTheSpec(t *testing.T) {
	d, c := testDeployer(t)
	ctx := context.Background()

	if err := d.EnsureUpstreamKeycloak(ctx); err != nil {
		t.Fatalf("EnsureUpstreamKeycloak: %v", err)
	}

	kc := &unstructured.Unstructured{}
	kc.SetGroupVersionKind(upstreamKeycloakGVK())
	key := types.NamespacedName{Name: namesFor("alpha").UpstreamKeycloak, Namespace: "neteye-system"}
	if err := c.Get(ctx, key, kc); err != nil {
		t.Fatal(err)
	}

	image, _, _ := unstructured.NestedString(kc.Object, "spec", "image")
	if image != "reg/kc:1" {
		t.Errorf("spec.image = %q, want the image resolved onto the CR", image)
	}
	if len(kc.GetOwnerReferences()) == 0 {
		t.Error("the upstream CR has no ownerReference, it would outlive the NetEye Keycloak CR")
	}
}

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
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"maps"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	neteyev1alpha1 "github.com/neteye/neteye-platform-operator/api/v1alpha1"
)

const (
	// clientSecretKey is the key holding the operator's service-account
	// credential inside its Secret.
	clientSecretKey = "client-secret"

	// The upstream Keycloak Operator is installed through OLM as one
	// cluster-scoped extension shared by every NetEye instance: OLM runs a
	// single copy of that controller for the whole cluster.
	extensionName    = "keycloak-operator"
	extensionChannel = "fast"
	extensionCatalog = "operatorhubio"

	// upstreamCRDName is the CRD the Keycloak Operator registers once it is
	// installed. Nothing here may touch an upstream Keycloak CR before this
	// CRD is Established.
	upstreamCRDName = "keycloaks.k8s.keycloak.org"
)

// Deployer creates and reconciles the Kubernetes objects one Keycloak instance
// is made of. It runs no control loop of its own: the KeycloakReconciler
// drives every step, so the ordering — install the operator, wait for its CRD,
// only then create a Keycloak CR — stays visible in one place rather than
// being implied by who happens to requeue first.
type Deployer struct {
	Client client.Client

	// Owner is the Keycloak CR every namespaced object is created for. It owns
	// them through an ownerReference, so deleting it takes the whole instance
	// with it; the objects live in its own namespace, which is what makes that
	// reference legal.
	Owner *neteyev1alpha1.Keycloak
}

func (d *Deployer) instance() instance {
	return instance{
		Name:        d.Owner.Name,
		Namespace:   d.Owner.Namespace,
		Image:       d.Owner.Spec.Image,
		ConfigImage: d.Owner.Spec.ConfigImage,
		Hostname:    d.Owner.Spec.Hostname,
		IssuerRef:   d.Owner.Spec.IssuerRef,
	}
}

// Target describes where this instance is reached and which hostname its
// certificate carries, so the enforcer verifies against the name the
// certificate was actually issued for.
func (d *Deployer) Target() Target {
	inst := d.instance()
	return Target{Instance: inst.Name, Namespace: inst.Namespace, Hostname: inst.hostname()}
}

// ConfigHash fingerprints the inputs of the bootstrap Job. The caller records
// the hash of the run that succeeded, which is what makes bootstrap a
// once-per-input event rather than once-per-Job.
func (d *Deployer) ConfigHash(clientSecret string) string {
	return configHash(d.instance(), clientSecret)
}

// EnsureDBSecret creates the credentials the Keycloak instance connects to its
// database with, generating the password on first call.
//
// TODO(db): the username is fixed and the database itself is not provisioned
// here; both change when the platform moves to a CNPG Cluster per service.
func (d *Deployer) EnsureDBSecret(ctx context.Context) error {
	_, err := d.ensureGeneratedSecret(ctx, d.instance().names().DBSecret, map[string][]byte{
		"username": []byte("keycloak"),
	}, "password")
	return err
}

// EnsureCertificate asks cert-manager for the certificate the instance serves
// on, provisioning a self-signed Issuer first when the spec names none.
//
// The operator issues nothing itself: renewal is the part a hand-rolled
// certificate always gets wrong, and cert-manager already owns it.
func (d *Deployer) EnsureCertificate(ctx context.Context) error {
	inst := d.instance()

	if inst.ownsIssuer() {
		issuer := &unstructured.Unstructured{}
		issuer.SetGroupVersionKind(issuerGVK())
		issuer.SetName(inst.names().SelfSignedIssuer)
		issuer.SetNamespace(inst.Namespace)

		if _, err := controllerutil.CreateOrUpdate(ctx, d.Client, issuer, func() error {
			issuer.SetLabels(inst.labels())
			if err := unstructured.SetNestedMap(issuer.Object, selfSignedIssuerSpec(), "spec"); err != nil {
				return err
			}
			return d.own(issuer)
		}); err != nil {
			return fmt.Errorf("ensure self-signed issuer: %w", err)
		}
	}

	cert := &unstructured.Unstructured{}
	cert.SetGroupVersionKind(certificateGVK())
	cert.SetName(inst.names().Certificate)
	cert.SetNamespace(inst.Namespace)

	_, err := controllerutil.CreateOrUpdate(ctx, d.Client, cert, func() error {
		cert.SetLabels(inst.labels())
		if err := unstructured.SetNestedMap(cert.Object, certificateSpec(inst), "spec"); err != nil {
			return err
		}
		return d.own(cert)
	})
	return err
}

// EnsureOperatorClientSecret returns the credential the operator authenticates
// with as its own service-account client, minting it on first call.
//
// The operator mints it and the bootstrap Job registers it in Keycloak, rather
// than the reverse: that way the Job needs no access to the Kubernetes API,
// and the operator is never left guessing where its own credential lives. The
// initial-admin Secret is deliberately not reused — upstream treats it as a
// bootstrap credential an admin may rotate or delete, which would silently
// stop enforcement.
func (d *Deployer) EnsureOperatorClientSecret(ctx context.Context) (string, error) {
	data, err := d.ensureGeneratedSecret(ctx, d.instance().names().ClientSecret, nil, clientSecretKey)
	if err != nil {
		return "", err
	}
	return string(data[clientSecretKey]), nil
}

// ensureGeneratedSecret returns an Opaque Secret holding fixed entries plus a
// randomly generated value under generatedKey, creating it if absent and
// regenerating only when that value is missing — a generated credential that
// is still there must never be silently replaced, since whatever consumed it
// would keep using the old one.
func (d *Deployer) ensureGeneratedSecret(
	ctx context.Context,
	name string,
	fixed map[string][]byte,
	generatedKey string,
) (map[string][]byte, error) {
	secret := &corev1.Secret{}
	key := types.NamespacedName{Name: name, Namespace: d.Owner.Namespace}
	err := d.Client.Get(ctx, key, secret)
	switch {
	case err == nil && len(secret.Data[generatedKey]) > 0:
		return secret.Data, nil
	case err != nil && !apierrors.IsNotFound(err):
		return nil, err
	}

	generated, err := randomHex(32)
	if err != nil {
		return nil, fmt.Errorf("generate %s: %w", generatedKey, err)
	}

	data := map[string][]byte{generatedKey: []byte(generated)}
	maps.Copy(data, fixed)

	if err := d.createOrUpdateSecret(ctx, name, corev1.SecretTypeOpaque, data); err != nil {
		return nil, err
	}

	// Re-read rather than returning what we just built: another reconcile may
	// have won the create, and its value is the one Keycloak will be told about.
	if err := d.Client.Get(ctx, key, secret); err != nil {
		return nil, err
	}
	return secret.Data, nil
}

func (d *Deployer) createOrUpdateSecret(
	ctx context.Context,
	name string,
	secretType corev1.SecretType,
	data map[string][]byte,
) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: d.Owner.Namespace},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, d.Client, secret, func() error {
		secret.Labels = d.instance().labels()
		secret.Type = secretType
		secret.Data = data
		return d.own(secret)
	})
	if apierrors.IsAlreadyExists(err) {
		// Lost a race with another reconcile; the winner's value stands.
		return nil
	}
	return err
}

// EnsureExtension installs the upstream Keycloak Operator as an OLM
// ClusterExtension.
//
// It is a cluster-scoped singleton shared by every NetEye instance: OLM runs
// one copy of that controller for the whole cluster and its installation
// namespace is immutable once set. The first instance reconciled on a cluster
// therefore wins that namespace and every later one reuses the install as-is,
// which is why installNamespace is the operator's own namespace and not any
// tenant's — otherwise the whole cluster's Keycloak Operator would end up
// anchored to whichever tenant reconciled first.
//
// No ownerReference: it outlives any single instance, and a namespaced object
// cannot own a cluster-scoped one anyway.
func (d *Deployer) EnsureExtension(ctx context.Context, installNamespace string) error {
	ext := &unstructured.Unstructured{}
	ext.SetGroupVersionKind(clusterExtensionGVK())

	err := d.Client.Get(ctx, types.NamespacedName{Name: extensionName}, ext)
	switch {
	case err == nil:
		return nil
	case !apierrors.IsNotFound(err):
		return err
	}

	ext.SetName(extensionName)
	ext.SetLabels(map[string]string{managedByLabel: managedByValue})
	if err := unstructured.SetNestedMap(ext.Object, map[string]any{
		"namespace": installNamespace,
		"serviceAccount": map[string]any{
			"name": extensionName + "-installer",
		},
		"source": map[string]any{
			"sourceType": "Catalog",
			"catalog": map[string]any{
				"packageName": extensionName,
				"channels":    []any{extensionChannel},
				"selector": map[string]any{
					"matchLabels": map[string]any{
						"olm.operatorframework.io/metadata.name": extensionCatalog,
					},
				},
			},
		},
	}, "spec"); err != nil {
		return err
	}

	if err := d.Client.Create(ctx, ext); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// IsExtensionCRDEstablished reports whether the Keycloak Operator has finished
// registering its CRD, i.e. whether it is safe to read or write upstream
// Keycloak CRs.
//
// reader must be an uncached client (mgr.GetAPIReader()): a cached read of a
// GroupVersionKind whose CRD does not exist yet starts an informer that can
// never sync, which stalls the caller instead of failing fast.
func (d *Deployer) IsExtensionCRDEstablished(ctx context.Context, reader client.Reader) (bool, error) {
	crd := &unstructured.Unstructured{}
	crd.SetGroupVersionKind(crdGVK())
	if err := reader.Get(ctx, types.NamespacedName{Name: upstreamCRDName}, crd); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return hasTrueCondition(crd, "Established"), nil
}

// EnsureUpstreamKeycloak creates or updates the k8s.keycloak.org/Keycloak CR
// that actually runs the workload.
func (d *Deployer) EnsureUpstreamKeycloak(ctx context.Context) error {
	inst := d.instance()

	kc := &unstructured.Unstructured{}
	kc.SetGroupVersionKind(upstreamKeycloakGVK())
	kc.SetName(inst.names().UpstreamKeycloak)
	kc.SetNamespace(inst.Namespace)

	_, err := controllerutil.CreateOrUpdate(ctx, d.Client, kc, func() error {
		kc.SetLabels(inst.labels())
		// Merged, not replaced: the upstream operator defaults fields into
		// this spec, and overwriting them would make every pass a write that
		// upstream immediately undoes.
		spec, _, err := unstructured.NestedMap(kc.Object, "spec")
		if err != nil {
			return err
		}
		if spec == nil {
			spec = map[string]any{}
		}
		maps.Copy(spec, upstreamKeycloakSpec(inst))
		if err := unstructured.SetNestedMap(kc.Object, spec, "spec"); err != nil {
			return err
		}
		return d.own(kc)
	})
	return err
}

// IsUpstreamKeycloakReady reports whether the upstream CR says the instance is
// serving. A missing CR is not an error: it means this pass created it and the
// upstream operator has not caught up yet.
func (d *Deployer) IsUpstreamKeycloakReady(ctx context.Context) (bool, error) {
	inst := d.instance()

	kc := &unstructured.Unstructured{}
	kc.SetGroupVersionKind(upstreamKeycloakGVK())
	key := types.NamespacedName{Name: inst.names().UpstreamKeycloak, Namespace: inst.Namespace}
	if err := d.Client.Get(ctx, key, kc); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return hasTrueCondition(kc, "Ready"), nil
}

// EnsureBootstrapJob creates the one-shot configuration Job, recreating it
// when its inputs change: Job specs are immutable, so an updated image can
// only be applied by replacing the object.
func (d *Deployer) EnsureBootstrapJob(ctx context.Context, hash string) error {
	inst := d.instance()

	existing := &batchv1.Job{}
	key := types.NamespacedName{Name: inst.names().BootstrapJob, Namespace: inst.Namespace}
	err := d.Client.Get(ctx, key, existing)
	switch {
	case err == nil && existing.Annotations[configHashAnnotation] == hash:
		return nil
	case err == nil:
		propagation := metav1.DeletePropagationBackground
		if err := d.Client.Delete(ctx, existing, &client.DeleteOptions{PropagationPolicy: &propagation}); err != nil &&
			!apierrors.IsNotFound(err) {
			return fmt.Errorf("delete outdated bootstrap job: %w", err)
		}
	case !apierrors.IsNotFound(err):
		return err
	}

	job := bootstrapJob(inst, hash)
	if err := d.own(job); err != nil {
		return err
	}
	if err := d.Client.Create(ctx, job); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// BootstrapState is what the one-shot configuration Job has reached.
type BootstrapState int

const (
	// BootstrapRunning covers both "still running" and "not there yet": the
	// Job is garbage-collected once its TTL expires, and an absent Job is
	// recreated by the next pass rather than being an error.
	BootstrapRunning BootstrapState = iota
	BootstrapSucceeded
	BootstrapFailed
)

// BootstrapJobState reports whether the configuration Job has reached a
// terminal state.
func (d *Deployer) BootstrapJobState(ctx context.Context) (BootstrapState, error) {
	inst := d.instance()

	job := &batchv1.Job{}
	key := types.NamespacedName{Name: inst.names().BootstrapJob, Namespace: inst.Namespace}
	if err := d.Client.Get(ctx, key, job); err != nil {
		if apierrors.IsNotFound(err) {
			return BootstrapRunning, nil
		}
		return BootstrapRunning, err
	}

	for _, c := range job.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete:
			return BootstrapSucceeded, nil
		case batchv1.JobFailed:
			return BootstrapFailed, nil
		}
	}
	return BootstrapRunning, nil
}

// TrustAnchor returns what the enforcer verifies the instance against: the
// issuing CA when cert-manager published one, and otherwise the leaf itself,
// which is what a self-signed issuer produces.
//
// The second return value is false while cert-manager has not issued yet —
// a wait, not an error, since the Certificate was only just requested.
func (d *Deployer) TrustAnchor(ctx context.Context) ([]byte, bool, error) {
	inst := d.instance()

	secret := &corev1.Secret{}
	key := types.NamespacedName{Name: inst.names().TLSSecret, Namespace: inst.Namespace}
	if err := d.Client.Get(ctx, key, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, false, nil
		}
		return nil, false, err
	}

	// Pinning the CA rather than the leaf survives renewal: cert-manager
	// replaces the leaf periodically, and a pinned leaf would stop matching.
	if ca := secret.Data[corev1.ServiceAccountRootCAKey]; len(ca) > 0 {
		return ca, true, nil
	}
	if leaf := secret.Data[corev1.TLSCertKey]; len(leaf) > 0 {
		return leaf, true, nil
	}
	return nil, false, nil
}

// own points obj back at the Keycloak CR, so deleting the CR takes the whole
// instance with it instead of leaving Secrets and Jobs behind.
func (d *Deployer) own(obj client.Object) error {
	return controllerutil.SetControllerReference(d.Owner, obj, d.Client.Scheme())
}

// hasTrueCondition reads the Kubernetes condition convention off an untyped
// object, which is how both the upstream Keycloak CR and a CRD report the two
// things this operator has to wait for.
func hasTrueCondition(obj *unstructured.Unstructured, condType string) bool {
	conditions, found, err := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if err != nil || !found {
		return false
	}
	for _, c := range conditions {
		cond, ok := c.(map[string]any)
		if !ok || cond["type"] != condType {
			continue
		}
		status, _ := cond["status"].(string)
		return status == "True"
	}
	return false
}

func randomHex(n int) (string, error) {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

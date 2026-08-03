/*
Copyright 2024 Wuerth Phoenix.

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

package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"fmt"
	"math/big"
	"os"
	"reflect"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	neteye "github.com/wuerth-phoenix/neteye-operator/api/v1alpha1"
)

const (
	keycloakNamespace = "neteye-system"
	keycloakHostname  = "keycloak.rke2.neteyelocal"
	dbSecretName      = "keycloak-db-secret"
	tlsSecretName     = "keycloak-tls-secret"
	keycloakCRName    = "neteye-kc"
	dbUser            = "testuser"
	dbPassword        = "testpassword"
	dbName            = "keycloak"

	// Keycloak ClusterExtension
	keycloakExtensionName = "keycloak-operator"
	keycloakChannel       = "fast"
	keycloakCatalogName   = "operatorhubio"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(neteye.AddToScheme(scheme))
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")

	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		MetricsBindAddress:     metricsAddr,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "neteye-operator.wuerth-phoenix.com",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	if err := mgr.Add(&keycloakDeployer{
		client: mgr.GetClient(),
		log:    ctrl.Log.WithName("keycloak-deployer"),
	}); err != nil {
		setupLog.Error(err, "unable to add keycloak deployer")
		os.Exit(1)
	}

	if err := (&NetEyeReconciler{
		Client: mgr.GetClient(),
		Log:    ctrl.Log.WithName("neteye-reconciler"),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create NetEye controller")
		os.Exit(1)
	}

	setupLog.Info("starting neteye-operator", "version", "v0.2.2")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

// keycloakDeployer runs after the manager starts and deploys Keycloak resources.
type keycloakDeployer struct {
	client client.Client
	log    logr.Logger
}

func (d *keycloakDeployer) NeedLeaderElection() bool {
	return true
}

func (d *keycloakDeployer) Start(ctx context.Context) error {
	log := d.log

	// Initial deployment with retry.
	for attempt := 1; ; attempt++ {
		err := d.deploy(ctx)
		if err == nil {
			log.Info("initial deployment completed successfully")
			break
		}
		log.Error(err, "deployment attempt failed, retrying", "attempt", attempt)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}

	// Continuous reconciliation loop — re-check every 30s.
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := d.deploy(ctx); err != nil {
				log.Error(err, "reconciliation failed")
			}
		}
	}
}

func (d *keycloakDeployer) deploy(ctx context.Context) error {
	if err := d.ensureKeycloakExtension(ctx); err != nil {
		return fmt.Errorf("ensure keycloak extension: %w", err)
	}
	return nil
}

func (d *keycloakDeployer) ensureNamespace(ctx context.Context, namespace string) error {
	ns := &corev1.Namespace{}
	if err := d.client.Get(ctx, types.NamespacedName{Name: namespace}, ns); err == nil {
		return nil
	}
	ns = &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "neteye-operator",
			},
		},
	}
	if err := d.client.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	d.log.Info("namespace created", "namespace", namespace)
	return nil
}

func (d *keycloakDeployer) ensureDBSecret(ctx context.Context, namespace string) error {
	desired := map[string][]byte{
		"username": []byte(dbUser),
		"password": []byte(dbPassword),
	}
	secret := &corev1.Secret{}
	key := types.NamespacedName{Name: dbSecretName, Namespace: namespace}
	if err := d.client.Get(ctx, key, secret); err == nil {
		if reflect.DeepEqual(secret.Data, desired) {
			return nil
		}
		secret.Data = desired
		if err := d.client.Update(ctx, secret); err != nil {
			return err
		}
		d.log.Info("db secret reconciled")
		return nil
	}
	secret = &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      dbSecretName,
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "neteye-operator",
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: desired,
	}
	if err := d.client.Create(ctx, secret); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	d.log.Info("db secret created")
	return nil
}

func (d *keycloakDeployer) ensureTLSSecret(ctx context.Context, namespace, hostname string) error {
	secret := &corev1.Secret{}
	key := types.NamespacedName{Name: tlsSecretName, Namespace: namespace}
	if err := d.client.Get(ctx, key, secret); err == nil {
		// Exists — verify it still has cert and key data.
		if len(secret.Data[corev1.TLSCertKey]) > 0 && len(secret.Data[corev1.TLSPrivateKeyKey]) > 0 {
			return nil
		}
		// Data was wiped — regenerate.
		d.log.Info("tls secret data is empty, regenerating")
		certPEM, keyPEM, err := generateSelfSignedCert(hostname)
		if err != nil {
			return fmt.Errorf("generate self-signed cert: %w", err)
		}
		secret.Data = map[string][]byte{
			corev1.TLSCertKey:       certPEM,
			corev1.TLSPrivateKeyKey: keyPEM,
		}
		return d.client.Update(ctx, secret)
	}

	certPEM, keyPEM, err := generateSelfSignedCert(hostname)
	if err != nil {
		return fmt.Errorf("generate self-signed cert: %w", err)
	}

	secret = &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tlsSecretName,
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "neteye-operator",
			},
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			corev1.TLSCertKey:       certPEM,
			corev1.TLSPrivateKeyKey: keyPEM,
		},
	}
	if err := d.client.Create(ctx, secret); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	d.log.Info("tls secret created")
	return nil
}

func (d *keycloakDeployer) ensureKeycloakExtension(ctx context.Context) error {
	desiredSpec := map[string]interface{}{
		"namespace": keycloakNamespace,
		"source": map[string]interface{}{
			"sourceType": "Catalog",
			"catalog": map[string]interface{}{
				"packageName": keycloakExtensionName,
				"channels":    []interface{}{keycloakChannel},
				"selector": map[string]interface{}{
					"matchLabels": map[string]interface{}{
						"olm.operatorframework.io/metadata.name": keycloakCatalogName,
					},
				},
			},
		},
	}

	ext := &unstructured.Unstructured{}
	ext.SetGroupVersionKind(clusterExtensionGVK())
	key := types.NamespacedName{Name: keycloakExtensionName}
	if err := d.client.Get(ctx, key, ext); err == nil {
		currentSpec, _, _ := unstructured.NestedMap(ext.Object, "spec")
		if reflect.DeepEqual(currentSpec, desiredSpec) {
			return nil
		}
		if err := unstructured.SetNestedMap(ext.Object, desiredSpec, "spec"); err != nil {
			return err
		}
		if err := d.client.Update(ctx, ext); err != nil {
			return err
		}
		d.log.Info("keycloak ClusterExtension reconciled")
		return nil
	}

	ext = &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "olm.operatorframework.io/v1",
			"kind":       "ClusterExtension",
			"metadata": map[string]interface{}{
				"name": keycloakExtensionName,
				"labels": map[string]interface{}{
					"app.kubernetes.io/managed-by": "neteye-operator",
				},
			},
			"spec": desiredSpec,
		},
	}
	if err := d.client.Create(ctx, ext); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	d.log.Info("keycloak ClusterExtension created")
	return nil
}

func (d *keycloakDeployer) ensureKeycloakCR(ctx context.Context, namespace, image string) error {
	desiredSpec := map[string]interface{}{
		"instances": int64(1),
		"image":     image,
		"db": map[string]interface{}{
			"vendor":   "postgres",
			"host":     dbHostForNamespace(namespace),
			"port":     int64(5432),
			"database": dbName,
			"usernameSecret": map[string]interface{}{
				"name": dbSecretName,
				"key":  "username",
			},
			"passwordSecret": map[string]interface{}{
				"name": dbSecretName,
				"key":  "password",
			},
		},
		"http": map[string]interface{}{
			"tlsSecret": tlsSecretName,
		},
		"hostname": map[string]interface{}{
			"hostname": keycloakHostname,
			"strict":   false,
		},
		"proxy": map[string]interface{}{
			"headers": "xforwarded",
		},
	}

	kc := &unstructured.Unstructured{}
	kc.SetGroupVersionKind(keycloakGVK())
	key := types.NamespacedName{Name: keycloakCRName, Namespace: namespace}
	if err := d.client.Get(ctx, key, kc); err == nil {
		currentSpec, _, _ := unstructured.NestedMap(kc.Object, "spec")
		if reflect.DeepEqual(currentSpec, desiredSpec) {
			return nil
		}
		if err := unstructured.SetNestedMap(kc.Object, desiredSpec, "spec"); err != nil {
			return err
		}
		if err := d.client.Update(ctx, kc); err != nil {
			return err
		}
		d.log.Info("keycloak CR reconciled", "namespace", namespace, "image", image)
		return nil
	}

	kc = &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "k8s.keycloak.org/v2beta1",
			"kind":       "Keycloak",
			"metadata": map[string]interface{}{
				"name":      keycloakCRName,
				"namespace": namespace,
				"labels": map[string]interface{}{
					"app.kubernetes.io/managed-by": "neteye-operator",
				},
			},
			"spec": desiredSpec,
		},
	}
	if err := d.client.Create(ctx, kc); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	d.log.Info("keycloak CR created", "namespace", namespace, "image", image)
	return nil
}

func dbHostForNamespace(namespace string) string {
	return "postgres-db." + namespace + ".svc.rke2.neteyelocal"
}

// NetEyeReconciler reconciles NetEye CRs and drives per-CR Keycloak deployment.
type NetEyeReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

func (r *NetEyeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("neteye", req.NamespacedName)

	ne := &neteye.NetEye{}
	if err := r.Get(ctx, req.NamespacedName, ne); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	components, ok := neteye.ComponentsForVersion(ne.Spec.NetEyeVersion)
	if !ok {
		log.Error(nil, "unsupported NetEye version", "version", ne.Spec.NetEyeVersion)
		ne.Status.Phase = "Failed"
		ne.Status.Message = fmt.Sprintf("unsupported NetEye version %q", ne.Spec.NetEyeVersion)
		_ = r.Status().Update(ctx, ne)
		return ctrl.Result{}, fmt.Errorf("unsupported NetEye version %q", ne.Spec.NetEyeVersion)
	}

	ns := ne.Spec.TargetNamespace
	d := &keycloakDeployer{client: r.Client, log: log}

	if err := d.ensureNamespace(ctx, ns); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensure namespace: %w", err)
	}
	if err := d.ensureDBSecret(ctx, ns); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensure db secret: %w", err)
	}
	if err := d.ensureTLSSecret(ctx, ns, keycloakHostname); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensure tls secret: %w", err)
	}
	if err := d.ensureKeycloakCR(ctx, ns, components.KeycloakImage); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensure keycloak cr: %w", err)
	}

	ne.Status.Phase = "Ready"
	ne.Status.ResolvedKeycloakImage = components.KeycloakImage
	ne.Status.Message = ""
	_ = r.Status().Update(ctx, ne)

	log.Info("NetEye reconciled", "namespace", ns, "image", components.KeycloakImage)
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func (r *NetEyeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&neteye.NetEye{}).
		Complete(r)
}

func keycloakGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   "k8s.keycloak.org",
		Version: "v2beta1",
		Kind:    "Keycloak",
	}
}

func clusterExtensionGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   "olm.operatorframework.io",
		Version: "v1",
		Kind:    "ClusterExtension",
	}
}

func generateSelfSignedCert(hostname string) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"NetEye"},
			CommonName:   hostname,
		},
		DNSNames:              []string{hostname},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	return certPEM, keyPEM, nil
}

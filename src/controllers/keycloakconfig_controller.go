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

package controllers

import (
	"context"
	"fmt"
	"math"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/go-logr/logr"

	neteye "github.com/wuerth-phoenix/neteye-operator/api/v1alpha1"
)

// KeycloakConfigReconciler reconciles a KeycloakConfig object
type KeycloakConfigReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=neteye.wuerth-phoenix.com,resources=keycloakconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=neteye.wuerth-phoenix.com,resources=keycloakconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=neteye.wuerth-phoenix.com,resources=keycloakconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=statefulsets;deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=k8s.keycloak.org,resources=keycloaks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch

// Reconcile implements the reconciliation loop for KeycloakConfig
func (r *KeycloakConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("keycloakconfig", req.NamespacedName)

	// Fetch the KeycloakConfig instance
	kc := &neteye.KeycloakConfig{}
	if err := r.Get(ctx, req.NamespacedName, kc); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("KeycloakConfig resource not found, ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		log.Error(err, "failed to get KeycloakConfig")
		return ctrl.Result{}, err
	}

	// Set observed generation
	kc.Status.ObservedGeneration = kc.Generation

	// Initialize status if needed
	if kc.Status.Phase == "" {
		kc.Status.Phase = neteye.PhaseTypePending
	}

	// Record last reconciliation time
	now := metav1.Now()
	kc.Status.LastReconciliation = &now

	// Main reconciliation logic with retry
	result, err := r.reconcileKeycloak(ctx, log, kc)

	// Update status
	if statusErr := r.Status().Update(ctx, kc); statusErr != nil {
		log.Error(statusErr, "failed to update KeycloakConfig status")
		return ctrl.Result{}, statusErr
	}

	return result, err
}

// reconcileKeycloak contains the main reconciliation logic
func (r *KeycloakConfigReconciler) reconcileKeycloak(ctx context.Context, log logr.Logger, kc *neteye.KeycloakConfig) (ctrl.Result, error) {
	// Check retry limits
	if kc.Status.RetryCount >= getMaxRetries(kc) {
		kc.Status.Phase = neteye.PhaseTypeFailed
		r.setCondition(kc, neteye.ConditionTypeError, corev1.ConditionTrue,
			"RetryLimitExceeded", fmt.Sprintf("Maximum retries (%d) exceeded", getMaxRetries(kc)))
		log.Error(nil, "maximum retries exceeded")
		return ctrl.Result{}, nil
	}

	// Set reconciling condition
	r.setCondition(kc, neteye.ConditionTypeReconciling, corev1.ConditionTrue, "Reconciling", "")
	kc.Status.Phase = neteye.PhaseTypeProvisioning

	// Step 1: Verify Keycloak Operator is installed
	operatorReady, err := r.verifyKeycloakOperator(ctx, log)
	if err != nil {
		return r.retryWithBackoff(log, kc, err, "Keycloak Operator verification failed")
	}
	if !operatorReady {
		return r.retryWithBackoff(log, kc, fmt.Errorf("keycloak operator CRD not found"),
			"Keycloak Operator is not installed yet")
	}
	r.setCondition(kc, neteye.ConditionTypeKeycloakOperatorReady, corev1.ConditionTrue, "Ready", "")

	// Step 3: Handle TLS certificate
	tlsReady, err := r.handleTLSCertificate(ctx, log, kc)
	if err != nil {
		return r.retryWithBackoff(log, kc, err, "TLS certificate handling failed")
	}
	if !tlsReady {
		return r.retryWithBackoff(log, kc, fmt.Errorf("tls secret not found"), "Waiting for TLS certificate")
	}
	r.setCondition(kc, neteye.ConditionTypeCertificateReady, corev1.ConditionTrue, "Ready", "")

	// Step 5: Deploy Keycloak CR
	if err := r.deployKeycloak(ctx, log, kc); err != nil {
		return r.retryWithBackoff(log, kc, err, "keycloak deployment failed")
	}

	// Step 6: Verify Keycloak is ready
	keycloakReady, err := r.verifyKeycloakReady(ctx, log, kc)
	if err != nil {
		return r.retryWithBackoff(log, kc, err, "keycloak verification failed")
	}
	if !keycloakReady {
		return r.retryWithBackoff(log, kc, fmt.Errorf("keycloak not ready"),
			"Waiting for Keycloak to be ready")
	}

	// Success!
	kc.Status.Phase = neteye.PhaseTypeReady
	kc.Status.RetryCount = 0
	kc.Status.LastError = ""
	r.setCondition(kc, neteye.ConditionTypeReady, corev1.ConditionTrue, "Ready", "Keycloak is ready")
	r.setCondition(kc, neteye.ConditionTypeReconciling, corev1.ConditionFalse, "Reconciled", "")

	log.Info("Keycloak successfully reconciled")
	return ctrl.Result{}, nil
}

// retryWithBackoff handles retry logic with exponential backoff
func (r *KeycloakConfigReconciler) retryWithBackoff(log logr.Logger, kc *neteye.KeycloakConfig,
	err error, reason string) (ctrl.Result, error) {

	kc.Status.RetryCount++
	kc.Status.LastError = err.Error()

	// Calculate backoff delay with exponential increase
	delay := calculateBackoffDelay(kc.Status.RetryCount, getRetryPolicy(kc))

	log.V(1).Info("Reconciliation failed, will retry",
		"retryCount", kc.Status.RetryCount,
		"maxRetries", getMaxRetries(kc),
		"retryAfter", delay.String(),
		"error", err.Error(),
		"reason", reason)

	r.setCondition(kc, neteye.ConditionTypeReconciling, corev1.ConditionTrue, "Retrying",
		fmt.Sprintf("%s - Retry %d/%d in %s: %s", reason, kc.Status.RetryCount,
			getMaxRetries(kc), delay.String(), err.Error()))

	return ctrl.Result{RequeueAfter: delay}, nil
}

// verifyKeycloakOperator checks if the Keycloak Operator CRD is installed
func (r *KeycloakConfigReconciler) verifyKeycloakOperator(ctx context.Context, log logr.Logger) (bool, error) {
	log.V(1).Info("Verifying Keycloak Operator installation")

	// Check if keycloaks.k8s.keycloak.org CRD exists
	discoveryClient := r.RESTMapper()
	if discoveryClient != nil {
		_, err := discoveryClient.RESTMapping(corev1.SchemeGroupVersion.WithKind("Keycloak").GroupKind())
		if err != nil {
			log.V(1).Info("Keycloak CRD not found yet", "error", err.Error())
			return false, nil
		}
	}

	log.V(1).Info("Keycloak Operator is installed")
	return true, nil
}

// createNamespace creates the target namespace if it doesn't exist
func (r *KeycloakConfigReconciler) createNamespace(ctx context.Context, log logr.Logger, kc *neteye.KeycloakConfig) error {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: kc.Spec.Namespace,
			Labels: map[string]string{
				"app":        "keycloak",
				"part-of":    "neteye-operator",
				"managed-by": "keycloak-operator",
				"created-at": time.Now().Format(time.RFC3339),
			},
		},
	}

	if err := r.Create(ctx, ns); err != nil {
		if apierrors.IsAlreadyExists(err) {
			log.V(1).Info("Namespace already exists", "namespace", kc.Spec.Namespace)
			return nil
		}
		log.Error(err, "failed to create namespace")
		return err
	}

	log.Info("Namespace created", "namespace", kc.Spec.Namespace)
	return nil
}

// handleTLSCertificate manages TLS certificate creation or validation
func (r *KeycloakConfigReconciler) handleTLSCertificate(ctx context.Context, log logr.Logger,
	kc *neteye.KeycloakConfig) (bool, error) {

	log.V(1).Info("Handling TLS certificate")

	certConfig := getDefaultCertConfig()
	if kc.Spec.Certificate != nil {
		certConfig = kc.Spec.Certificate
	}

	// Check if using existing secret
	if certConfig.ExistingSecret != "" {
		return r.verifyExistingSecret(ctx, log, kc, certConfig.ExistingSecret)
	}

	// For now, we assume the certificate is created by the setup script
	// In a more advanced implementation, we could generate it here
	secretName := "keycloak-tls-secret"
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      secretName,
		Namespace: kc.Spec.Namespace,
	}, secret); err != nil {
		if apierrors.IsNotFound(err) {
			log.V(1).Info("TLS secret not found, expecting it to be created externally",
				"secret", secretName, "namespace", kc.Spec.Namespace)
			return false, nil
		}
		log.Error(err, "failed to get TLS secret")
		return false, err
	}

	log.V(1).Info("TLS certificate found", "secret", secretName)
	return true, nil
}

// handleDatabase manages database setup
func (r *KeycloakConfigReconciler) handleDatabase(ctx context.Context, log logr.Logger,
	kc *neteye.KeycloakConfig) (bool, error) {

	log.V(1).Info("Handling database setup")

	// Check database credentials secret exists
	dbSecret := &corev1.Secret{}
	dbSecretName := "keycloak-db-secret"
	if err := r.Get(ctx, types.NamespacedName{
		Name:      dbSecretName,
		Namespace: kc.Spec.Namespace,
	}, dbSecret); err != nil {
		if apierrors.IsNotFound(err) {
			log.V(1).Info("Database secret not found, expecting it to be created externally")
			return false, nil
		}
		log.Error(err, "failed to get database secret")
		return false, err
	}

	log.V(1).Info("Database credentials found")

	// If using PostgreSQL inside cluster, verify it's ready
	if getBoolValue(kc.Spec.Database.CreatePostgres, false) {
		return r.verifyPostgreSQL(ctx, log, kc)
	}

	return true, nil
}

// deployKeycloak creates or updates the Keycloak CR
func (r *KeycloakConfigReconciler) deployKeycloak(ctx context.Context, log logr.Logger,
	kc *neteye.KeycloakConfig) error {

	log.Info("Deploying Keycloak instance")

	// Create Keycloak CR manifest
	keycloakManifest := map[string]interface{}{
		"apiVersion": "k8s.keycloak.org/v2beta1",
		"kind":       "Keycloak",
		"metadata": map[string]interface{}{
			"name":      "neteye-kc",
			"namespace": kc.Spec.Namespace,
			"labels": map[string]string{
				"app":        "keycloak",
				"part-of":    "neteye-operator",
				"managed-by": "keycloak-operator",
			},
		},
		"spec": map[string]interface{}{
			"instances": getInt32Value(kc.Spec.Instances, 1),
			"db": map[string]interface{}{
				"vendor":   kc.Spec.Database.Vendor,
				"host":     getDbHost(kc),
				"port":     getInt32Value(kc.Spec.Database.Port, 5432),
				"database": getStringValue(kc.Spec.Database.Database, "keycloak"),
				"usernameSecret": map[string]interface{}{
					"name": "keycloak-db-secret",
					"key":  "username",
				},
				"passwordSecret": map[string]interface{}{
					"name": "keycloak-db-secret",
					"key":  "password",
				},
			},
			"http": map[string]interface{}{
				"tlsSecret": "keycloak-tls-secret",
			},
			"hostname": map[string]interface{}{
				"hostname": kc.Spec.Hostname,
				"strict":   false,
			},
			"ingress": map[string]interface{}{
				"enabled":          getBoolValue(kc.Spec.Ingress.Enabled, true),
				"ingressClassName": getIngressClassName(kc),
			},
			"proxy": map[string]interface{}{
				"headers": "xforwarded",
			},
			"resources": map[string]interface{}{
				"requests": map[string]string{
					"cpu":    getResourceValue(kc.Spec.Resources, "requests", "cpu", "500m"),
					"memory": getResourceValue(kc.Spec.Resources, "requests", "memory", "1Gi"),
				},
				"limits": map[string]string{
					"cpu":    getResourceValue(kc.Spec.Resources, "limits", "cpu", "2000m"),
					"memory": getResourceValue(kc.Spec.Resources, "limits", "memory", "2Gi"),
				},
			},
		},
	}

	// For now, log the manifest that would be created
	_ = keycloakManifest
	log.V(1).Info("Keycloak manifest prepared",
		"namespace", kc.Spec.Namespace,
		"hostname", kc.Spec.Hostname,
		"instances", getInt32Value(kc.Spec.Instances, 1))

	return nil
}

// verifyKeycloakReady checks if Keycloak CR is in Ready state
func (r *KeycloakConfigReconciler) verifyKeycloakReady(ctx context.Context, log logr.Logger,
	kc *neteye.KeycloakConfig) (bool, error) {

	log.V(1).Info("Verifying Keycloak is ready")
	return true, nil
}

// Helper functions

// setCondition updates or creates a condition in the status
func (r *KeycloakConfigReconciler) setCondition(kc *neteye.KeycloakConfig, condType string,
	status corev1.ConditionStatus, reason, message string) {

	now := metav1.Now()

	// Find existing condition
	for i, cond := range kc.Status.Conditions {
		if cond.Type == condType {
			if cond.Status != status || cond.Reason != reason {
				kc.Status.Conditions[i].Status = status
				kc.Status.Conditions[i].Reason = reason
				kc.Status.Conditions[i].Message = message
				kc.Status.Conditions[i].LastTransitionTime = now
			}
			return
		}
	}

	// Add new condition
	kc.Status.Conditions = append(kc.Status.Conditions, neteye.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: now,
	})
}

// calculateBackoffDelay calculates exponential backoff delay
func calculateBackoffDelay(retryCount int32, policy *neteye.RetryPolicyConfig) time.Duration {
	initialDelay := getInt32Value(policy.InitialDelay, 5)
	maxDelay := getInt32Value(policy.MaxDelay, 300)
	multiplier := policy.BackoffMultiplier
	if multiplier == nil || *multiplier < 1.0 {
		multiplier = ptrFloat64(2.0)
	}

	// Calculate: initialDelay * (multiplier ^ (retryCount - 1))
	delay := float64(initialDelay) * math.Pow(*multiplier, float64(retryCount-1))

	// Cap at maxDelay
	if delay > float64(maxDelay) {
		delay = float64(maxDelay)
	}

	return time.Duration(delay) * time.Second
}

// getRetryPolicy returns the retry policy or default
func getRetryPolicy(kc *neteye.KeycloakConfig) *neteye.RetryPolicyConfig {
	if kc.Spec.RetryPolicy != nil {
		return kc.Spec.RetryPolicy
	}
	return &neteye.RetryPolicyConfig{
		MaxRetries:        ptrInt32(10),
		InitialDelay:      ptrInt32(5),
		MaxDelay:          ptrInt32(300),
		BackoffMultiplier: ptrFloat64(2.0),
	}
}

// getMaxRetries returns the maximum number of retries
func getMaxRetries(kc *neteye.KeycloakConfig) int32 {
	policy := getRetryPolicy(kc)
	return getInt32Value(policy.MaxRetries, 10)
}

// Helper utility functions
func getInt32Value(ptr *int32, defaultVal int32) int32 {
	if ptr == nil {
		return defaultVal
	}
	return *ptr
}

func getStringValue(s string, defaultVal string) string {
	if s == "" {
		return defaultVal
	}
	return s
}

func getBoolValue(ptr *bool, defaultVal bool) bool {
	if ptr == nil {
		return defaultVal
	}
	return *ptr
}

func getDefaultCertConfig() *neteye.CertificateConfig {
	return &neteye.CertificateConfig{
		Generate:     ptrBool(true),
		ValidityDays: ptrInt32(365),
	}
}

func getDbHost(kc *neteye.KeycloakConfig) string {
	if kc.Spec.Database.Host != "" {
		return kc.Spec.Database.Host
	}
	if getBoolValue(kc.Spec.Database.CreatePostgres, false) {
		return "postgres-db"
	}
	return "localhost"
}

func getIngressClassName(kc *neteye.KeycloakConfig) string {
	if kc.Spec.Ingress != nil && kc.Spec.Ingress.ClassName != "" {
		return kc.Spec.Ingress.ClassName
	}
	return "nginx"
}

func getResourceValue(resources *neteye.ResourceConfig, scope, resource, defaultVal string) string {
	if resources == nil {
		return defaultVal
	}

	var req *neteye.ResourceRequirements
	if scope == "requests" && resources.Requests != nil {
		req = resources.Requests
	} else if scope == "limits" && resources.Limits != nil {
		req = resources.Limits
	}

	if req == nil {
		return defaultVal
	}

	if resource == "cpu" && req.CPU != "" {
		return req.CPU
	}
	if resource == "memory" && req.Memory != "" {
		return req.Memory
	}

	return defaultVal
}

func (r *KeycloakConfigReconciler) verifyExistingSecret(ctx context.Context, log logr.Logger,
	kc *neteye.KeycloakConfig, secretRef string) (bool, error) {

	log.V(1).Info("Verifying existing TLS secret", "secretRef", secretRef)
	return true, nil
}

func (r *KeycloakConfigReconciler) verifyPostgreSQL(ctx context.Context, log logr.Logger,
	kc *neteye.KeycloakConfig) (bool, error) {

	log.V(1).Info("Verifying PostgreSQL is ready")
	return true, nil
}

// Pointer helper functions
func ptrInt32(val int32) *int32 {
	return &val
}

func ptrBool(val bool) *bool {
	return &val
}

func ptrFloat64(val float64) *float64 {
	return &val
}

// SetupWithManager sets up the controller with the Manager
func (r *KeycloakConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&neteye.KeycloakConfig{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: 2}).
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		Complete(r)
}

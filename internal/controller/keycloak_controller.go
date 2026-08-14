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

package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	neteyev1alpha1 "github.com/neteye/neteye-platform-operator/api/v1alpha1"
	"github.com/neteye/neteye-platform-operator/internal/keycloak"
)

const (
	// enforcementInterval is how often enforced settings are re-asserted
	// against a Ready instance. Enforcement is a poll by nature: drift is
	// caused from outside the cluster, through Keycloak's own admin console,
	// so there is no Kubernetes event to watch for.
	enforcementInterval = 30 * time.Second

	// waitInterval is how often a stage that is waiting on something outside
	// the operator — OLM installing an operator, Keycloak coming up — checks
	// back. Waiting is a normal state, not an error, so it requeues rather
	// than failing the reconcile.
	waitInterval = 10 * time.Second
)

// KeycloakReconciler reconciles one NetEye-managed Keycloak instance.
//
// Its spec is written by the NetEye controller; this controller owns
// everything downstream of it: the workloads, the one-shot bootstrap, and the
// continuous re-assertion of enforced settings.
type KeycloakReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// APIReader is an uncached reader (mgr.GetAPIReader()). Probing for a CRD
	// that may not be registered yet must not go through the manager's cache:
	// a cached read of a GroupVersionKind whose CRD does not exist spins up an
	// informer that can never sync, which stalls the caller instead of failing
	// fast.
	APIReader client.Reader

	// OperatorNamespace is the namespace this operator runs in (POD_NAMESPACE
	// via the downward API). Cluster-wide infrastructure installed on behalf
	// of every instance — the upstream Keycloak Operator's ClusterExtension —
	// is anchored here rather than to whichever tenant namespace happens to
	// reconcile first.
	OperatorNamespace string
}

// +kubebuilder:rbac:groups=neteye.com,resources=keycloaks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=neteye.com,resources=keycloaks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=neteye.com,resources=keycloaks/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch
// +kubebuilder:rbac:groups=olm.operatorframework.io,resources=clusterextensions,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=k8s.keycloak.org,resources=keycloaks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=k8s.keycloak.org,resources=keycloaks/status,verbs=get
// +kubebuilder:rbac:groups=cert-manager.io,resources=certificates;issuers,verbs=get;list;watch;create;update;patch;delete

func (r *KeycloakReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	kc := &neteyev1alpha1.Keycloak{}
	if err := r.Get(ctx, req.NamespacedName, kc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	kc.Status.ObservedGeneration = kc.Generation

	// The options allow-list is resolved first because it is pure logic: an
	// admin who mistyped an option name learns it immediately, without having
	// to wait for the deployment to come up.
	realm, unknown := keycloak.ResolveOptions(kc.Spec.AdditionalOptions)
	r.reportOptions(kc, unknown, log)

	result, err := r.deploy(ctx, kc, log)
	if err != nil {
		// The stage failed for a reason retrying might fix. Status is written
		// anyway, so the failure is visible while the backoff runs.
		r.setUnavailable(kc, "DeploymentFailed", err.Error())
		r.updateStatus(ctx, kc, log)
		return ctrl.Result{}, err
	}

	if kc.Status.Phase == neteyev1alpha1.PhaseReady {
		// Enforcement runs on every pass past this point, deliberately not
		// gated on the bootstrap Job still existing: that Job is
		// garbage-collected once its TTL expires and is then recreated, so
		// keying enforcement to it would suspend enforcement for the whole of
		// every re-bootstrap.
		r.enforce(ctx, kc, realm, log)
	}

	r.updateStatus(ctx, kc, log)
	return result, nil
}

// deploy walks the deployment stages in order, stopping at the first one that
// is still waiting. Each stage sets the phase it is waiting in, so the phase
// always names what the operator is currently blocked on.
func (r *KeycloakReconciler) deploy(
	ctx context.Context,
	kc *neteyev1alpha1.Keycloak,
	log logr.Logger,
) (ctrl.Result, error) {
	d := &keycloak.Deployer{Client: r.Client, Owner: kc}

	if err := d.EnsureDBSecret(ctx); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensure db secret: %w", err)
	}
	if err := d.EnsureCertificate(ctx); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensure certificate: %w", err)
	}
	// cert-manager issues asynchronously, and Keycloak cannot start without
	// the Secret it mounts.
	if _, issued, err := d.TrustAnchor(ctx); err != nil {
		return ctrl.Result{}, fmt.Errorf("read the instance certificate: %w", err)
	} else if !issued {
		r.setWaiting(kc, neteyev1alpha1.PhaseDeploying,
			"WaitingForCertificate", "waiting for cert-manager to issue the instance certificate")
		return ctrl.Result{RequeueAfter: waitInterval}, nil
	}

	// The upstream Keycloak Operator must be installed, and its CRD
	// registered, before an upstream Keycloak CR can be created. Waiting for
	// that is a normal state, not an error.
	if err := d.EnsureExtension(ctx, r.OperatorNamespace); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensure keycloak operator: %w", err)
	}
	established, err := d.IsExtensionCRDEstablished(ctx, r.APIReader)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("check keycloak crd: %w", err)
	}
	if !established {
		r.setWaiting(kc, neteyev1alpha1.PhaseDeploying,
			"WaitingForKeycloakOperator", "waiting for the Keycloak Operator to register its CRD")
		return ctrl.Result{RequeueAfter: waitInterval}, nil
	}

	if err := d.EnsureUpstreamKeycloak(ctx); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensure upstream keycloak: %w", err)
	}
	ready, err := d.IsUpstreamKeycloakReady(ctx)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("check keycloak readiness: %w", err)
	}
	if !ready {
		r.setWaiting(kc, neteyev1alpha1.PhaseDeploying,
			"WaitingForKeycloak", "waiting for Keycloak to become ready")
		return ctrl.Result{RequeueAfter: waitInterval}, nil
	}

	clientSecret, err := d.EnsureOperatorClientSecret(ctx)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("ensure operator client secret: %w", err)
	}
	// Bootstrap is keyed to its inputs, not to the Job object: the Job is
	// garbage-collected an hour after it finishes, and treating its absence as
	// "not bootstrapped" would drop the instance out of Ready — and suspend
	// enforcement — every time that happened.
	hash := d.ConfigHash(clientSecret)
	if kc.Status.BootstrapConfigHash != hash {
		if err := d.EnsureBootstrapJob(ctx, hash); err != nil {
			return ctrl.Result{}, fmt.Errorf("ensure bootstrap job: %w", err)
		}

		state, err := d.BootstrapJobState(ctx)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("check bootstrap job: %w", err)
		}
		switch state {
		case keycloak.BootstrapFailed:
			kc.Status.Phase = neteyev1alpha1.PhaseFailed
			kc.Status.Message = "the Keycloak configuration Job failed"
			r.setUnavailable(kc, "BootstrapFailed", kc.Status.Message)
			// Not an error: the Job has already exhausted its own retries, so
			// requeueing it faster would not help. A replacement Job is only
			// created once its inputs change.
			log.Info("bootstrap job failed", "namespace", kc.Namespace)
			return ctrl.Result{RequeueAfter: enforcementInterval}, nil

		case keycloak.BootstrapRunning:
			r.setWaiting(kc, neteyev1alpha1.PhaseBootstrapping,
				"Bootstrapping", "the Keycloak configuration Job is running")
			return ctrl.Result{RequeueAfter: waitInterval}, nil
		}

		kc.Status.BootstrapConfigHash = hash
	}

	kc.Status.Phase = neteyev1alpha1.PhaseReady
	kc.Status.Message = ""
	kc.Status.Endpoint = keycloak.ServiceURL(kc.Name, kc.Namespace)
	setCondition(&kc.Status.Conditions, kc.Generation, neteyev1alpha1.ConditionAvailable,
		true, "Available", "the Keycloak instance is serving")

	return ctrl.Result{RequeueAfter: enforcementInterval}, nil
}

// enforce re-asserts the settings NetEye owns and records the outcome on
// conditions. It never fails the reconcile: enforcement failing is a degraded
// instance, not a failed deploy, and the next pass retries anyway.
func (r *KeycloakReconciler) enforce(
	ctx context.Context,
	kc *neteyev1alpha1.Keycloak,
	realm keycloak.Realm,
	log logr.Logger,
) {
	d := &keycloak.Deployer{Client: r.Client, Owner: kc}

	anchor, issued, err := d.TrustAnchor(ctx)
	if err != nil {
		r.setEnforcementFailed(kc, fmt.Errorf("read the instance certificate: %w", err), log)
		return
	}
	if !issued {
		r.setEnforcementFailed(kc, fmt.Errorf("the instance certificate has not been issued yet"), log)
		return
	}

	clientSecret, err := d.EnsureOperatorClientSecret(ctx)
	if err != nil {
		r.setEnforcementFailed(kc, err, log)
		return
	}
	enforcer, err := keycloak.NewEnforcer(d.Target(), clientSecret, anchor)
	if err != nil {
		r.setEnforcementFailed(kc, err, log)
		return
	}

	corrected, err := enforcer.Enforce(ctx, realm)
	if err != nil {
		r.setEnforcementFailed(kc, err, log)
		return
	}
	if len(corrected) > 0 {
		log.Info("drift corrected", "fields", corrected)
	}

	setCondition(&kc.Status.Conditions, kc.Generation, neteyev1alpha1.ConditionSettingsEnforced,
		true, "InSync", "the enforced settings match the declared values")
}

func (r *KeycloakReconciler) setEnforcementFailed(kc *neteyev1alpha1.Keycloak, err error, log logr.Logger) {
	setCondition(&kc.Status.Conditions, kc.Generation, neteyev1alpha1.ConditionSettingsEnforced,
		false, "EnforcementFailed", err.Error())
	log.Error(err, "enforcing settings failed", "namespace", kc.Namespace)
}

// setWaiting records a stage that is blocked on something outside the
// operator. Waiting is not a failure, but it is not availability either.
func (r *KeycloakReconciler) setWaiting(kc *neteyev1alpha1.Keycloak, phase neteyev1alpha1.Phase, reason, message string) {
	kc.Status.Phase = phase
	kc.Status.Message = message
	r.setUnavailable(kc, reason, message)
}

func (r *KeycloakReconciler) setUnavailable(kc *neteyev1alpha1.Keycloak, reason, message string) {
	setCondition(&kc.Status.Conditions, kc.Generation, neteyev1alpha1.ConditionAvailable,
		false, reason, message)
}

// reportOptions records whether every declared option was recognised.
// Unrecognised names are a condition, never a failure: see ResolveOptions.
func (r *KeycloakReconciler) reportOptions(kc *neteyev1alpha1.Keycloak, unknown []string, log logr.Logger) {
	if len(unknown) == 0 {
		setCondition(&kc.Status.Conditions, kc.Generation, neteyev1alpha1.ConditionAdditionalOptionsAccepted,
			true, "AllRecognised", "every option in spec.additionalOptions is recognised")
		return
	}

	setCondition(&kc.Status.Conditions, kc.Generation, neteyev1alpha1.ConditionAdditionalOptionsAccepted,
		false, "UnknownOption", fmt.Sprintf("ignoring unrecognised options: %s", strings.Join(unknown, ", ")))
	log.Info("ignoring unrecognised additionalOptions", "names", unknown)
}

func (r *KeycloakReconciler) updateStatus(ctx context.Context, kc *neteyev1alpha1.Keycloak, log logr.Logger) {
	if err := r.Status().Update(ctx, kc); err != nil {
		// A lost status write is corrected on the next pass, and must not mask
		// the outcome of the reconcile itself.
		log.Error(err, "unable to update Keycloak status", "phase", kc.Status.Phase)
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *KeycloakReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&neteyev1alpha1.Keycloak{}).
		Named("keycloak").
		Complete(r)
}

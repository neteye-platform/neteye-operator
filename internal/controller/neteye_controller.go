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
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	neteyev1alpha1 "github.com/neteye/neteye-platform-operator/api/v1alpha1"
)

// netEyeFinalizer guards the managed-service CRs a NetEye owns.
//
// Kubernetes garbage collection cannot be relied on alone here: a NetEye may
// target a namespace other than its own, and cross-namespace ownerReferences
// are rejected by the API server. An ownerReference is still set whenever the
// namespaces do match — it is cheaper and reacts faster — but the finalizer is
// what guarantees teardown in every case.
const netEyeFinalizer = "neteye.com/services"

// NetEyeReconciler reconciles a NetEye object by fanning it out into one CR
// per managed service. It deploys nothing itself: each managed service has its
// own controller, so a service that fails does so visibly and on its own.
type NetEyeReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=neteye.com,resources=neteyes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=neteye.com,resources=neteyes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=neteye.com,resources=neteyes/finalizers,verbs=update
// +kubebuilder:rbac:groups=neteye.com,resources=keycloaks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=neteye.com,resources=keycloaks/status,verbs=get
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create

func (r *NetEyeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	ne := &neteyev1alpha1.NetEye{}
	if err := r.Get(ctx, req.NamespacedName, ne); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !ne.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.finalize(ctx, ne)
	}
	if controllerutil.AddFinalizer(ne, netEyeFinalizer) {
		if err := r.Update(ctx, ne); err != nil {
			return ctrl.Result{}, fmt.Errorf("add finalizer: %w", err)
		}
	}

	components, ok := neteyev1alpha1.ComponentsForVersion(ne.Spec.NetEyeVersion)
	if !ok {
		supported := neteyev1alpha1.SupportedVersions()
		sort.Strings(supported)
		ne.Status.Phase = neteyev1alpha1.PhaseFailed
		ne.Status.Message = fmt.Sprintf("unsupported NetEye version %q, this operator knows: %s",
			ne.Spec.NetEyeVersion, strings.Join(supported, ", "))
		r.updateStatus(ctx, ne)
		// Not returned as an error: no amount of retrying fixes an unsupported
		// version, only a spec change does, and that triggers a fresh event.
		log.Info("unsupported NetEye version", "version", ne.Spec.NetEyeVersion)
		return ctrl.Result{}, nil
	}

	// The target namespace may not exist yet: a NetEye is allowed to name one
	// it wants created, and every managed service CR lands in it.
	if err := r.ensureTargetNamespace(ctx, ne); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensure target namespace: %w", err)
	}

	// Every service is reconciled, then the failures are reported together.
	// One service that cannot be reconciled must not stop the others from
	// being: they fail independently, which is the whole reason each one has
	// an object of its own.
	serviceErrs := map[string]error{}
	if err := r.reconcileKeycloak(ctx, ne, components); err != nil {
		serviceErrs["Keycloak"] = err
		log.Error(err, "reconciling managed service failed", "kind", "Keycloak")
	}

	// The roll-up runs even when a service failed: a failure the admin cannot
	// see on status is a failure they cannot act on.
	if err := r.rollUpStatus(ctx, ne, serviceErrs); err != nil {
		return ctrl.Result{}, fmt.Errorf("roll up status: %w", err)
	}
	r.updateStatus(ctx, ne)

	// Returned so the failed services are retried with backoff. Status has
	// already been written, so the error costs no visibility.
	return ctrl.Result{}, errors.Join(slices.Collect(maps.Values(serviceErrs))...)
}

// ensureTargetNamespace creates spec.targetNamespace if it is absent. It is
// left behind on delete: a namespace may hold objects this operator knows
// nothing about, and removing it would take them too.
func (r *NetEyeReconciler) ensureTargetNamespace(ctx context.Context, ne *neteyev1alpha1.NetEye) error {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ne.Spec.TargetNamespace}}

	err := r.Get(ctx, client.ObjectKeyFromObject(ns), &corev1.Namespace{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}

	ns.Labels = withInstanceLabels(nil, ne)
	if err := r.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// reconcileKeycloak creates, updates or deletes the Keycloak CR to match
// spec.services.keycloak. The image fields are stamped from the resolved
// components rather than taken from the template: which images a product
// version is made of is the operator's decision, not the admin's.
func (r *NetEyeReconciler) reconcileKeycloak(ctx context.Context, ne *neteyev1alpha1.NetEye, components neteyev1alpha1.Components) error {
	kc := &neteyev1alpha1.Keycloak{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ne.Name,
			Namespace: ne.Spec.TargetNamespace,
		},
	}

	if ne.Spec.Services.Keycloak == nil {
		return client.IgnoreNotFound(r.Delete(ctx, kc))
	}

	tmpl := ne.Spec.Services.Keycloak
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, kc, func() error {
		kc.Labels = withInstanceLabels(kc.Labels, ne)
		kc.Spec.Image = components.KeycloakImage
		kc.Spec.ConfigImage = components.KeycloakConfigImage
		kc.Spec.Hostname = tmpl.Hostname
		kc.Spec.IssuerRef = tmpl.IssuerRef
		kc.Spec.AdditionalOptions = tmpl.AdditionalOptions
		return r.setOwnerIfSameNamespace(ne, kc)
	})
	return err
}

// setOwnerIfSameNamespace sets a controller reference only when owner and
// object share a namespace; a cross-namespace ownerReference is rejected by
// the API server, and teardown in that case is the finalizer's job.
func (r *NetEyeReconciler) setOwnerIfSameNamespace(ne *neteyev1alpha1.NetEye, obj client.Object) error {
	if obj.GetNamespace() != ne.Namespace {
		return nil
	}
	return controllerutil.SetControllerReference(ne, obj, r.Scheme)
}

// finalize deletes every managed service CR this NetEye owns, then releases
// the finalizer. Deletes are idempotent, so a partially completed teardown is
// simply retried on the next pass.
func (r *NetEyeReconciler) finalize(ctx context.Context, ne *neteyev1alpha1.NetEye) error {
	if !controllerutil.ContainsFinalizer(ne, netEyeFinalizer) {
		return nil
	}

	kc := &neteyev1alpha1.Keycloak{
		ObjectMeta: metav1.ObjectMeta{Name: ne.Name, Namespace: ne.Spec.TargetNamespace},
	}
	if err := client.IgnoreNotFound(r.Delete(ctx, kc)); err != nil {
		return fmt.Errorf("delete keycloak: %w", err)
	}

	controllerutil.RemoveFinalizer(ne, netEyeFinalizer)
	return r.Update(ctx, ne)
}

// rollUpStatus reads each managed service CR and reduces them to one phase:
// the least-advanced one, so a NetEye reads Ready only when all of it is.
//
// serviceErrs carries the services this pass could not reconcile, keyed by
// kind. Those are reported straight from the error: their CR may not exist at
// all, and "absent because we failed to create it" must not read the same as
// "created a moment ago".
func (r *NetEyeReconciler) rollUpStatus(ctx context.Context, ne *neteyev1alpha1.NetEye, serviceErrs map[string]error) error {
	ne.Status.Services = nil
	ne.Status.ObservedGeneration = ne.Generation

	if ne.Spec.Services.Keycloak != nil {
		key := client.ObjectKey{Name: ne.Name, Namespace: ne.Spec.TargetNamespace}
		ref, err := r.serviceRef(ctx, "Keycloak", key, serviceErrs)
		if err != nil {
			return err
		}
		ne.Status.Services = append(ne.Status.Services, ref)
	}

	ne.Status.Phase, ne.Status.Message = aggregatePhase(ne.Status.Services)

	ready := ne.Status.Phase == neteyev1alpha1.PhaseReady
	setCondition(&ne.Status.Conditions, ne.Generation, neteyev1alpha1.ConditionServicesReady,
		ready, reasonFor(ready, "AllServicesReady", "ServicesNotReady"), ne.Status.Message)

	return nil
}

// serviceRef reads one managed service CR and turns it into the entry cached
// on NetEye.status.
func (r *NetEyeReconciler) serviceRef(
	ctx context.Context,
	kind string,
	key client.ObjectKey,
	serviceErrs map[string]error,
) (neteyev1alpha1.ServiceReference, error) {
	ref := neteyev1alpha1.ServiceReference{Kind: kind, Name: key.Name, Namespace: key.Namespace}

	if err, failed := serviceErrs[kind]; failed {
		ref.Phase = neteyev1alpha1.PhaseFailed
		ref.Message = err.Error()
		return ref, nil
	}

	kc := &neteyev1alpha1.Keycloak{}
	switch err := r.Get(ctx, key, kc); {
	case apierrors.IsNotFound(err):
		// Just created: its own controller has not written a status yet.
		ref.Phase = neteyev1alpha1.PhasePending
	case err != nil:
		return ref, err
	default:
		ref.Phase, ref.Message = kc.Status.Phase, kc.Status.Message
	}

	return ref, nil
}

// aggregatePhase reduces per-service phases to the NetEye's own: any failure
// wins, then any service still working, and only an all-Ready set reads Ready.
// A NetEye with no services declared is Ready — it has nothing left to do.
func aggregatePhase(services []neteyev1alpha1.ServiceReference) (neteyev1alpha1.Phase, string) {
	rank := map[neteyev1alpha1.Phase]int{
		neteyev1alpha1.PhaseReady:         0,
		neteyev1alpha1.PhasePending:       1,
		neteyev1alpha1.PhaseDeploying:     2,
		neteyev1alpha1.PhaseBootstrapping: 3,
		neteyev1alpha1.PhaseFailed:        4,
	}

	worst := neteyev1alpha1.PhaseReady
	message := ""

	for _, s := range services {
		phase := s.Phase
		if phase == "" {
			phase = neteyev1alpha1.PhasePending
		}
		if rank[phase] > rank[worst] {
			worst = phase
			message = fmt.Sprintf("%s/%s is %s", s.Kind, s.Name, phase)
			if s.Message != "" {
				message += ": " + s.Message
			}
		}
	}

	return worst, message
}

func (r *NetEyeReconciler) updateStatus(ctx context.Context, ne *neteyev1alpha1.NetEye) {
	if err := r.Status().Update(ctx, ne); err != nil {
		// A lost status write is corrected on the next pass, and must not mask
		// the outcome of the reconcile itself.
		logf.Log.Error(err, "unable to update NetEye status", "neteye", client.ObjectKeyFromObject(ne))
	}
}

func withInstanceLabels(labels map[string]string, ne *neteyev1alpha1.NetEye) map[string]string {
	if labels == nil {
		labels = map[string]string{}
	}
	labels["app.kubernetes.io/managed-by"] = "neteye-operator"
	labels["app.kubernetes.io/part-of"] = "neteye"
	labels["neteye.com/instance"] = ne.Name
	return labels
}

func reasonFor(ok bool, whenTrue, whenFalse string) string {
	if ok {
		return whenTrue
	}
	return whenFalse
}

// SetupWithManager sets up the controller with the Manager.
//
// Owns(&Keycloak{}) makes a service's status change wake this reconciler
// immediately, so the roll-up on NetEye.status is not a poll behind reality.
// It only fires for CRs carrying an ownerReference, i.e. same-namespace ones;
// cross-namespace targets fall back to the periodic resync.
func (r *NetEyeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&neteyev1alpha1.NetEye{}).
		Owns(&neteyev1alpha1.Keycloak{}).
		Named("neteye").
		Complete(r)
}

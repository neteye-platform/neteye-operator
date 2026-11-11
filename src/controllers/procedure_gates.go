// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"fmt"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
)

// lifecycle is the procedure a NetEye resource is going through.
type lifecycle string

const (
	lifecycleSteady  lifecycle = ""
	lifecycleInstall lifecycle = "Install"
	lifecycleUpgrade lifecycle = "Upgrade"
)

const (
	waitingForProcedureGatesReason = "WaitingForProcedureGates"
	procedurePrepareFailedReason   = "ProcedurePrepareFailed"
	procedureGateFailedReason      = "ProcedureGateCreateFailed"
)

// componentGates is what one component exchanges with the NetEye Ansible
// procedure during an install or upgrade.
type componentGates struct {
	// Requires lists gates that must exist before the component is touched.
	Requires []string
	// Prepare runs once Requires are met, before the component is reconciled.
	// It runs on every pass until the procedure completes, so it must be
	// idempotent.
	Prepare func(ctx context.Context, r *NetEyeReconciler, ne *neteye.NetEye) error
	// Produces lists gates created once the component is ready.
	Produces []string
}

// gatePlan holds the component gates of one procedure.
type gatePlan map[componentID]componentGates

// releasePlan holds the gate plans for installing and for upgrading to one
// release.
type releasePlan struct {
	Install gatePlan
	Upgrade gatePlan
}

// defaultReleasePlans maps each supported release to its plans. Gate names are
// the contract with the NetEye Ansible procedure.
var defaultReleasePlans = map[string]releasePlan{
	neteye.CurrentNetEyeVersion: {},
}

func (r *NetEyeReconciler) gatePlanFor(version string, l lifecycle) gatePlan {
	plans := defaultReleasePlans
	if r.releasePlans != nil {
		plans = r.releasePlans
	}
	switch l {
	case lifecycleInstall:
		return plans[version].Install
	case lifecycleUpgrade:
		return plans[version].Upgrade
	default:
		return nil
	}
}

// lifecycleFor derives the procedure in progress from the last completed
// release. It reports false for an upgrade source the operator cannot handle.
func lifecycleFor(ne *neteye.NetEye) (lifecycle, bool) {
	switch ne.Status.CurrentVersion {
	case ne.Spec.Version:
		return lifecycleSteady, true
	case "":
		return lifecycleInstall, true
	case neteye.PreviousNetEyeVersion:
		return lifecycleUpgrade, true
	default:
		return lifecycleSteady, false
	}
}

// procedureGates returns the gate keys present for ne's release.
func (r *NetEyeReconciler) procedureGates(ctx context.Context, ne *neteye.NetEye) (map[string]struct{}, error) {
	list := &neteye.NetEyeProcedureGateList{}
	if err := r.List(ctx, list, client.InNamespace(ne.Namespace)); err != nil {
		return nil, fmt.Errorf("list NetEye procedure gates: %w", err)
	}
	gates := make(map[string]struct{}, len(list.Items))
	for _, gate := range list.Items {
		if gate.Spec.Version == ne.Spec.Version {
			gates[gate.Spec.Gate] = struct{}{}
		}
	}
	return gates, nil
}

func procedureGateName(version, gate string) string {
	return version + "-" + gate
}

func (r *NetEyeReconciler) ensureProcedureGate(ctx context.Context, ne *neteye.NetEye, gate string, present map[string]struct{}) error {
	if _, ok := present[gate]; ok {
		return nil
	}
	object := &neteye.NetEyeProcedureGate{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       ne.Namespace,
			Name:            procedureGateName(ne.Spec.Version, gate),
			OwnerReferences: []metav1.OwnerReference{ownerReferenceFor(ne)},
		},
		Spec: neteye.NetEyeProcedureGateSpec{Version: ne.Spec.Version, Gate: gate, Producer: neteye.ProcedureGateProducerOperator},
	}
	if err := r.Create(ctx, object); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create NetEye procedure gate %q: %w", object.Name, err)
	}
	present[gate] = struct{}{}
	return nil
}

func missingProcedureGates(required []string, present map[string]struct{}) []string {
	var missing []string
	for _, gate := range required {
		if _, ok := present[gate]; !ok {
			missing = append(missing, gate)
		}
	}
	return missing
}

// gatedOperation wraps op with the component's gates. When useGates is false
// only Prepare runs. A disabled component skips its gates entirely, since its
// feature module is disabled on the legacy side too.
func (r *NetEyeReconciler) gatedOperation(ctx context.Context, ne *neteye.NetEye, id componentID, image string, step componentGates, useGates bool, present map[string]struct{}, op componentOperation) componentOperation {
	return func() (componentResult, error) {
		if !componentEnabled(ne, id) {
			return op()
		}
		if missing := missingProcedureGates(step.Requires, present); useGates && len(missing) > 0 {
			message := fmt.Sprintf("waiting for NetEye %s procedure gates: %s", ne.Spec.Version, strings.Join(missing, ", "))
			setComponentServiceStatus(ne, id, neteye.ServiceStateNotReady, message, image)
			return progressingResult(id, waitingForProcedureGatesReason, message, r.waitForProgressingRequeue())
		}
		if step.Prepare != nil {
			if err := step.Prepare(ctx, r, ne); err != nil {
				message := fmt.Sprintf("NetEye %s procedure step failed: %v", ne.Spec.Version, err)
				setComponentServiceStatus(ne, id, neteye.ServiceStateFailed, message, image)
				return degradedResult(id, procedurePrepareFailedReason, message, r.failureRequeue(), err)
			}
		}
		result, err := op()
		if err != nil || result.State != componentStateReady || !useGates {
			return result, err
		}
		for _, gate := range step.Produces {
			if err := r.ensureProcedureGate(ctx, ne, gate, present); err != nil {
				message := fmt.Sprintf("failed to signal NetEye %s procedure gate %q: %v", ne.Spec.Version, gate, err)
				setComponentServiceStatus(ne, id, neteye.ServiceStateFailed, message, image)
				return degradedResult(id, procedureGateFailedReason, message, r.failureRequeue(), err)
			}
		}
		return result, nil
	}
}

func componentEnabled(ne *neteye.NetEye, id componentID) bool {
	switch id {
	case otelCollectorComponentID, edotGatewayComponentID:
		return ne.Spec.ElasticStack != nil && ne.Spec.ElasticStack.Enabled
	default:
		return true
	}
}

func setComponentServiceStatus(ne *neteye.NetEye, id componentID, state neteye.ServiceState, message, image string) {
	status := &neteye.NetEyeServiceStatus{Status: state, Message: message, ResolvedImage: image}
	switch id {
	case identityComponentID:
		ne.Status.ServicesStatus.Identity = status
	case otelCollectorComponentID:
		ne.Status.ServicesStatus.ElasticStack.OTelCollector = status
	case edotGatewayComponentID:
		ne.Status.ServicesStatus.ElasticStack.EDOTGateway = status
	}
}

// lifecyclePhase folds an unfinished install or upgrade into the aggregate
// phase. The procedure completes, and the release is recorded, once every
// component is ready.
func lifecyclePhase(ne *neteye.NetEye, l lifecycle, phase neteye.NetEyePhase, message string, results map[componentID]componentResult) (neteye.NetEyePhase, string) {
	switch {
	case l == lifecycleSteady || phase == neteye.PhaseFailed:
		return phase, message
	case phase == neteye.PhaseReady:
		ne.Status.CurrentVersion = ne.Spec.Version
		return phase, message
	case l == lifecycleInstall:
		return neteye.PhaseInstalling, lifecycleMessage(fmt.Sprintf("Installing NetEye %s", ne.Spec.Version), results)
	default:
		return neteye.PhaseUpgrading, lifecycleMessage(fmt.Sprintf("Upgrading to NetEye %s", ne.Spec.Version), results)
	}
}

// lifecycleMessage summarizes what an unfinished procedure is waiting for.
func lifecycleMessage(prefix string, results map[componentID]componentResult) string {
	var waiting []string
	for id, result := range results {
		if result.Reason == waitingForProcedureGatesReason {
			waiting = append(waiting, string(id))
		}
	}
	if len(waiting) == 0 {
		return prefix + "; check services status for details"
	}
	sort.Strings(waiting)
	return prefix + "; waiting for procedure gates of: " + strings.Join(waiting, ", ")
}

// netEyesForProcedureGate enqueues the NetEye resources sharing a gate's namespace.
func (r *NetEyeReconciler) netEyesForProcedureGate(ctx context.Context, gate client.Object) []reconcile.Request {
	list := &neteye.NetEyeList{}
	if err := r.List(ctx, list, client.InNamespace(gate.GetNamespace())); err != nil {
		r.Log.Error(err, "unable to list NetEye resources for procedure gate", "namespace", gate.GetNamespace(), "gate", gate.GetName())
		return nil
	}
	requests := make([]reconcile.Request, 0, len(list.Items))
	for _, ne := range list.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&ne)})
	}
	return requests
}

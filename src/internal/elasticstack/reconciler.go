// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package elasticstack

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
)

// ResourceReconciler manages the Elastic Stack collector resources.
type ResourceReconciler interface {
	EnsureResources(context.Context, string, neteye.NetEyeElasticStackSpec, string, string, string, metav1.OwnerReference) (bool, string, error)
	DeleteResources(context.Context, string, metav1.OwnerReference) error
}

// Reconciler owns Elastic Stack configuration, lifecycle, and readiness
// decisions. Its Outcome is deliberately independent of controller requeue
// durations and status persistence.
type Reconciler struct {
	component ResourceReconciler
}

func NewReconciler(component ResourceReconciler) *Reconciler {
	return &Reconciler{component: component}
}

type RequeueReason string

const (
	RequeueNone        RequeueReason = ""
	RequeueProgressing RequeueReason = "Progressing"
	RequeueFailure     RequeueReason = "Failure"
)

// Outcome contains the controller-independent result of Elastic Stack
// reconciliation. Phase is empty when no global phase change is required.
type Outcome struct {
	Phase        neteye.NetEyePhase
	PhaseMessage string
	Service      neteye.NetEyeServiceStatus
	Requeue      RequeueReason
	Err          error
}

type Request struct {
	Namespace        string
	Config           *neteye.NetEyeElasticStackSpec
	IdentityHostname string
	GatewayNamespace string
	GatewayName      string
	Owner            metav1.OwnerReference
}

func (r *Reconciler) Reconcile(ctx context.Context, request Request) Outcome {
	if request.Config == nil || !request.Config.Enabled {
		if r.component != nil {
			if err := r.component.DeleteResources(ctx, request.Namespace, request.Owner); err != nil {
				return failedOutcome(err)
			}
		}
		return Outcome{Service: neteye.NetEyeServiceStatus{Status: neteye.ServiceStateUnknown, Message: "Elastic Stack OpenTelemetry Collector is disabled"}}
	}
	if r.component == nil {
		return failedOutcome(fmt.Errorf("elastic-stack component is not initialized"))
	}

	ready, message, err := r.component.EnsureResources(ctx, request.Namespace, *request.Config, request.IdentityHostname, request.GatewayNamespace, request.GatewayName, request.Owner)
	if err != nil {
		return failedOutcome(err)
	}
	if !ready {
		return Outcome{
			Phase:        neteye.PhaseNotReady,
			PhaseMessage: "Check services status for details",
			Service:      neteye.NetEyeServiceStatus{Status: neteye.ServiceStateNotReady, Message: message},
			Requeue:      RequeueProgressing,
		}
	}
	return Outcome{Service: neteye.NetEyeServiceStatus{Status: neteye.ServiceStateReady, Message: "Elastic Stack OpenTelemetry Collector is ready"}}
}

func failedOutcome(err error) Outcome {
	return Outcome{
		Phase:        neteye.PhaseFailed,
		PhaseMessage: "Check services status for details",
		Service:      neteye.NetEyeServiceStatus{Status: neteye.ServiceStateFailed, Message: err.Error()},
		Requeue:      RequeueFailure,
		Err:          err,
	}
}

// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package elasticstack

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/resources"
)

// ResourceReconciler manages Elastic Stack feature module resources.
type ResourceReconciler interface {
	EnsureResources(context.Context, string, neteye.NetEyeElasticStackSpec, string, string, string, string, resources.CertificateIssuerRef, metav1.OwnerReference) (bool, string, error)
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
	Module       neteye.NetEyeServiceStatus
	Collector    *neteye.NetEyeServiceStatus
	Requeue      RequeueReason
	Err          error
}

type Request struct {
	Namespace        string
	Config           *neteye.NetEyeElasticStackSpec
	IdentityHostname string
	GatewayNamespace string
	GatewayName      string
	CollectorImage   string
	IssuerRef        resources.CertificateIssuerRef
	Owner            metav1.OwnerReference
}

func (r *Reconciler) Reconcile(ctx context.Context, request Request) Outcome {
	if request.Config == nil || !request.Config.Enabled {
		if r.component != nil {
			if err := r.component.DeleteResources(ctx, request.Namespace, request.Owner); err != nil {
				return failedOutcome(err, request.CollectorImage)
			}
		}
		return elasticStackOutcome(neteye.ServiceStateDisabled, "Elastic Stack feature module is disabled", neteye.ServiceStateDisabled, "OpenTelemetry Collector is disabled", request.CollectorImage)
	}
	if r.component == nil {
		return failedOutcome(fmt.Errorf("elastic stack feature module component is not initialized"), request.CollectorImage)
	}
	if request.Config.OTelCollector == nil {
		return Outcome{Phase: neteye.PhaseNotReady, PhaseMessage: "Check services status for details", Module: neteye.NetEyeServiceStatus{Status: neteye.ServiceStateNotReady, Message: "Elastic Stack feature module configuration is incomplete: otelCollector is required when enabled"}, Requeue: RequeueProgressing}
	}

	ready, message, err := r.component.EnsureResources(ctx, request.Namespace, *request.Config, request.IdentityHostname, request.GatewayNamespace, request.GatewayName, request.CollectorImage, request.IssuerRef, request.Owner)
	if err != nil {
		return failedOutcome(err, request.CollectorImage)
	}
	if !ready {
		return Outcome{
			Phase:        neteye.PhaseNotReady,
			PhaseMessage: "Check services status for details",
			Module:       neteye.NetEyeServiceStatus{Status: neteye.ServiceStateNotReady, Message: "Elastic Stack feature module is not ready"},
			Collector:    ptrTo(collectorStatus(neteye.ServiceStateNotReady, message, request.CollectorImage)),
			Requeue:      RequeueProgressing,
		}
	}
	return elasticStackOutcome(neteye.ServiceStateReady, "Elastic Stack feature module is ready", neteye.ServiceStateReady, "OpenTelemetry Collector is ready", request.CollectorImage)
}

func failedOutcome(err error, image string) Outcome {
	return Outcome{
		Phase:        neteye.PhaseFailed,
		PhaseMessage: "Check services status for details",
		Module:       neteye.NetEyeServiceStatus{Status: neteye.ServiceStateFailed, Message: "Elastic Stack feature module is unavailable"},
		Collector:    ptrTo(collectorStatus(neteye.ServiceStateFailed, err.Error(), image)),
		Requeue:      RequeueFailure,
		Err:          err,
	}
}

func elasticStackOutcome(moduleState neteye.ServiceState, moduleMessage string, collectorState neteye.ServiceState, collectorMessage, collectorImage string) Outcome {
	return Outcome{Module: neteye.NetEyeServiceStatus{Status: moduleState, Message: moduleMessage}, Collector: ptrTo(collectorStatus(collectorState, collectorMessage, collectorImage))}
}

func ptrTo[T any](value T) *T {
	return &value
}

func collectorStatus(state neteye.ServiceState, message, image string) neteye.NetEyeServiceStatus {
	return neteye.NetEyeServiceStatus{Status: state, Message: message, ResolvedImage: image}
}

// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"fmt"
	"strings"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/resources"
)

// tenantComponentGates is what one tenant component exchanges with the NetEye
// Ansible procedure during an install or upgrade of the tenant. The gates live
// in the namespace of the tenant.
type tenantComponentGates struct {
	// Requires lists gates that must exist before the component is touched.
	Requires []string
	// Produces lists gates created once the component is ready.
	Produces []string
}

// tenantGatePlan holds the component gates of one tenant procedure.
type tenantGatePlan map[componentID]tenantComponentGates

// tenantReleasePlan holds the gate plans for installing a tenant and for
// upgrading it to one release.
type tenantReleasePlan struct {
	Install tenantGatePlan
	Upgrade tenantGatePlan
}

// defaultTenantReleasePlans maps each supported release to its tenant plans.
// Gate names are the contract with the NetEye Ansible procedure. The current
// release has no tenant gates.
var defaultTenantReleasePlans = map[string]tenantReleasePlan{
	neteye.CurrentNetEyeVersion: {},
}

func (r *NetEyeTenantReconciler) gatePlanFor(version string, l lifecycle) tenantGatePlan {
	plans := defaultTenantReleasePlans
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

// gatedOperation wraps op with the gates of one tenant component: it waits
// for the required gates, and it creates the produced gates once the
// component is ready.
func (r *NetEyeTenantReconciler) gatedOperation(ctx context.Context, tenant *neteye.NetEyeTenant, id componentID, step tenantComponentGates, present map[string]struct{}, op componentOperation) componentOperation {
	return func() (componentResult, error) {
		if missing := missingProcedureGates(step.Requires, present); len(missing) > 0 {
			return progressingResult(id, waitingForProcedureGatesReason, fmt.Sprintf("waiting for NetEye %s procedure gates: %s", tenant.Spec.Version, strings.Join(missing, ", ")), r.failureRequeue())
		}
		result, err := op()
		if err != nil || result.State != componentStateReady {
			return result, err
		}
		owner := resources.OwnerReference(neteye.GroupVersion.String(), "NetEyeTenant", tenant)
		for _, gate := range step.Produces {
			if err := signalProcedureGate(ctx, r.Client, tenant.Namespace, tenant.Spec.Version, gate, owner, present); err != nil {
				return degradedResult(id, procedureGateFailedReason, fmt.Sprintf("failed to signal NetEye %s procedure gate %q: %v", tenant.Spec.Version, gate, err), r.failureRequeue(), err)
			}
		}
		return result, nil
	}
}

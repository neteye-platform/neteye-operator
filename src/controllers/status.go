// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"fmt"
	"sort"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
)

// componentStateFor maps an internal component state onto the published API
// state. A component that is not part of the desired state reports Disabled,
// which the orchestrator represents as a satisfied node with a Disabled reason.
func componentStateFor(result componentResult) neteye.ComponentState {
	if result.State == componentStateReady && result.Reason == disabledReason {
		return neteye.ComponentStateDisabled
	}
	switch result.State {
	case componentStateReady:
		return neteye.ComponentStateReady
	case componentStateProgressing:
		return neteye.ComponentStateProgressing
	case componentStateDegraded:
		return neteye.ComponentStateDegraded
	case componentStateBlocked:
		return neteye.ComponentStateBlocked
	default:
		return ""
	}
}

// buildComponentStatus converts this pass's component results into the dynamic
// component map. Every desired component gets an entry even when it could not
// progress, so automation can always locate a blocked or failing branch.
func buildComponentStatus(generation int64, results map[componentID]componentResult) map[string]neteye.NetEyeComponentStatus {
	components := make(map[string]neteye.NetEyeComponentStatus, len(results))
	for id, result := range results {
		blocking := make([]string, 0, len(result.BlockingDependencies))
		for _, dependency := range result.BlockingDependencies {
			blocking = append(blocking, string(dependency))
		}
		sort.Strings(blocking)
		images := result.ResolvedImages
		if images == nil {
			// ADR-0002: a component with no container images reports an empty
			// list rather than an absent field.
			images = []neteye.NetEyeResolvedImage{}
		}
		components[string(id)] = neteye.NetEyeComponentStatus{
			Status:               componentStateFor(result),
			ObservedGeneration:   generation,
			Reason:               result.Reason,
			Message:              result.Message,
			BlockingDependencies: blocking,
			ResolvedImages:       images,
		}
	}
	return components
}

// componentTally counts the outcomes of one reconciliation pass.
type componentTally struct {
	total       int
	ready       int
	progressing int
	degraded    int
	blocked     int
}

func tallyComponents(components map[string]neteye.NetEyeComponentStatus) componentTally {
	tally := componentTally{total: len(components)}
	for _, component := range components {
		switch component.Status {
		case neteye.ComponentStateReady, neteye.ComponentStateDisabled:
			tally.ready++
		case neteye.ComponentStateProgressing:
			tally.progressing++
		case neteye.ComponentStateDegraded:
			tally.degraded++
		case neteye.ComponentStateBlocked:
			tally.blocked++
		}
	}
	return tally
}

// setCondition records one condition, preserving lastTransitionTime when the
// status has not actually changed.
func setCondition(status *neteye.NetEyeStatus, generation int64, conditionType string, value metav1.ConditionStatus, reason, message string) {
	apimeta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               conditionType,
		Status:             value,
		ObservedGeneration: generation,
		Reason:             reason,
		Message:            message,
	})
}

func conditionStatus(value bool) metav1.ConditionStatus {
	if value {
		return metav1.ConditionTrue
	}
	return metav1.ConditionFalse
}

// applyComponentConditions derives the top-level conditions from the component
// map. Progressing and Degraded are independent: an installation can be
// degraded in one branch while another is still converging, which is why these
// are separate conditions rather than one aggregate value.
func applyComponentConditions(status *neteye.NetEyeStatus, generation int64) {
	tally := tallyComponents(status.Components)

	ready := tally.total > 0 && tally.ready == tally.total
	readyReason, readyMessage := neteye.ReasonAllComponentsReady, "All components are ready"
	if !ready {
		readyReason = neteye.ReasonComponentsNotReady
		readyMessage = fmt.Sprintf("Waiting for %d of %d components", tally.total-tally.ready, tally.total)
		if tally.degraded > 0 {
			readyReason = neteye.ReasonComponentsDegraded
		}
	}
	setCondition(status, generation, neteye.ConditionReady, conditionStatus(ready), readyReason, readyMessage)

	progressing := tally.progressing+tally.blocked > 0
	progressingReason, progressingMessage := neteye.ReasonConverged, "No component is converging"
	if progressing {
		progressingReason = neteye.ReasonConverging
		progressingMessage = fmt.Sprintf("%d of %d components are converging", tally.progressing+tally.blocked, tally.total)
	}
	setCondition(status, generation, neteye.ConditionProgressing, conditionStatus(progressing), progressingReason, progressingMessage)

	degradedReason, degradedMessage := neteye.ReasonNoFailures, "No component reported a failure"
	if tally.degraded > 0 {
		degradedReason = neteye.ReasonComponentsDegraded
		degradedMessage = fmt.Sprintf("%d of %d components reported a failure", tally.degraded, tally.total)
	}
	setCondition(status, generation, neteye.ConditionDegraded, conditionStatus(tally.degraded > 0), degradedReason, degradedMessage)
}

// applyUpgradeAvailableCondition reports an available product upgrade without
// touching readiness. A healthy installation on the previous release is ready
// and has an upgrade available at the same time.
func applyUpgradeAvailableCondition(status *neteye.NetEyeStatus, generation int64, version string) {
	if neteye.IsLatestVersion(version) {
		setCondition(status, generation, neteye.ConditionUpgradeAvailable, metav1.ConditionFalse,
			neteye.ReasonNoUpgradeAvailable, fmt.Sprintf("NetEye %s is the latest release this operator supports", version))
		return
	}
	targets := neteye.UpgradeTargets(version)
	if len(targets) == 0 {
		// An unsupported release has no declared forward transition, so there
		// is nothing to offer; readiness already reports the problem.
		setCondition(status, generation, neteye.ConditionUpgradeAvailable, metav1.ConditionFalse,
			neteye.ReasonUnsupportedVersion, fmt.Sprintf("NetEye %s has no declared upgrade", version))
		return
	}
	setCondition(status, generation, neteye.ConditionUpgradeAvailable, metav1.ConditionTrue,
		neteye.ReasonUpgradeAvailable, fmt.Sprintf("NetEye %s can be upgraded to %v", version, targets))
}

// applyCurrentVersion advances the achieved release only once the installation
// is ready, so a partially applied release is never reported as achieved.
func applyCurrentVersion(status *neteye.NetEyeStatus, version string) {
	if apimeta.IsStatusConditionTrue(status.Conditions, neteye.ConditionReady) {
		status.CurrentVersion = version
	}
}

// failCondition marks the installation not ready for a failure detected before
// any component ran, such as an unsupported version reaching storage.
func failCondition(status *neteye.NetEyeStatus, generation int64, reason, message string) {
	setCondition(status, generation, neteye.ConditionReady, metav1.ConditionFalse, reason, message)
	setCondition(status, generation, neteye.ConditionProgressing, metav1.ConditionFalse, reason, message)
	setCondition(status, generation, neteye.ConditionDegraded, metav1.ConditionTrue, reason, message)
}

// withResolvedImages attaches a component's resolved image set to its result.
// It is applied where the release data is in scope, so each component reports
// the exact images the operator selected for it.
func withResolvedImages(result componentResult, err error, images []neteye.NetEyeResolvedImage) (componentResult, error) {
	if err != nil {
		return result, err
	}
	result.ResolvedImages = images
	return result, nil
}

func identityResolvedImages(components neteye.NetEyeComponents) []neteye.NetEyeResolvedImage {
	return []neteye.NetEyeResolvedImage{{Name: "server", Image: components.KeycloakImage}}
}

// The telemetry deployments both run the CA-bundle image as an init container,
// so it is part of each component's resolved set.
func otelCollectorResolvedImages(components neteye.NetEyeComponents) []neteye.NetEyeResolvedImage {
	return []neteye.NetEyeResolvedImage{
		{Name: "collector", Image: components.OTelCollectorImage},
		{Name: "ca-bundle", Image: components.CABundleImage},
	}
}

func edotGatewayResolvedImages(components neteye.NetEyeComponents) []neteye.NetEyeResolvedImage {
	return []neteye.NetEyeResolvedImage{
		{Name: "gateway", Image: components.EDOTGatewayImage},
		{Name: "ca-bundle", Image: components.CABundleImage},
	}
}

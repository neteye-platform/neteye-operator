// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package elasticstack

import "errors"

type Phase string

const (
	PhaseReady       Phase = "Ready"
	PhaseProgressing Phase = "Progressing"
	PhaseDegraded    Phase = "Degraded"
)

const (
	ReasonAvailable              = "Available"
	ReasonInvalidConfiguration   = "InvalidConfiguration"
	ReasonSecretNotFound         = "SecretNotFound"
	ReasonSecretKeyMissing       = "SecretKeyMissing"
	ReasonDeploymentNotAvailable = "DeploymentNotAvailable"
	ReasonRouteNotReady          = "RouteNotReady"
	ReasonCertificateNotReady    = "CertificateNotReady"
	ReasonReconcileFailed        = "ReconcileFailed"
)

type Outcome struct {
	Phase   Phase
	Reason  string
	Message string
	Err     error
}

func readyOutcome(message string) Outcome {
	return Outcome{Phase: PhaseReady, Reason: ReasonAvailable, Message: message}
}
func progressingOutcome(reason, message string) Outcome {
	return Outcome{Phase: PhaseProgressing, Reason: reason, Message: message}
}
func degradedOutcome(reason, message string, err error) Outcome {
	if err == nil {
		err = errors.New(message)
	}
	return Outcome{Phase: PhaseDegraded, Reason: reason, Message: message, Err: err}
}

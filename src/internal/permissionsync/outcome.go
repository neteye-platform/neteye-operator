// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package permissionsync

import "errors"

// Phase is the lifecycle state one PermissionSync reconciliation observed.
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
	ReasonReconcileFailed        = "ReconcileFailed"
)

// Outcome reports what one reconciliation observed, keeping an operational
// failure (a degraded Outcome) distinguishable from an adapter or contract
// failure (a returned error).
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

// prerequisiteError carries the reason a user-managed input is unusable, so a
// missing Secret is reported as such instead of as a generic failure.
type prerequisiteError struct{ reason, message string }

func (e prerequisiteError) Error() string { return e.message }

func prerequisiteOutcome(err error) Outcome {
	var e prerequisiteError
	if errors.As(err, &e) {
		return degradedOutcome(e.reason, e.message, e)
	}
	return degradedOutcome(ReasonReconcileFailed, err.Error(), err)
}

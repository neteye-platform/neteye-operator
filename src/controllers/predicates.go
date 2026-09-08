// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// reconcileOnSpecOrDeletionChange skips the update churn a plain
// GenerationChangedPredicate lets through (status writes, and touching a
// resource that already has no pending change), while still reconciling a
// generation bump and, crucially, the moment a finalized resource is marked
// for deletion: setting deletionTimestamp does not change generation, so
// GenerationChangedPredicate alone silently drops that event. Without this,
// a delete only gets processed if another reconcile happens to already be
// in flight when it lands, and otherwise waits for the next periodic
// requeue (minutes) before the finalizer is ever removed.
var reconcileOnSpecOrDeletionChange = predicate.Or(
	predicate.GenerationChangedPredicate{},
	predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			return e.ObjectOld.GetDeletionTimestamp().IsZero() != e.ObjectNew.GetDeletionTimestamp().IsZero()
		},
	},
)

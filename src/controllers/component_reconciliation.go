// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"fmt"
	"sort"
	"time"
)

type componentID string

type componentState string

const (
	componentStateReady       componentState = "Ready"
	componentStateProgressing componentState = "Progressing"
	componentStateDegraded    componentState = "Degraded"
	componentStateBlocked     componentState = "Blocked"

	dependencyNotReadyReason = "DependencyNotReady"
)

// componentResult is the observation made for one component in a reconciliation pass.
type componentResult struct {
	ID                   componentID
	State                componentState
	Reason               string
	Message              string
	RequeueAfter         time.Duration
	BlockingDependencies []componentID
	Err                  error
}

func readyResult(id componentID, reason, message string) (componentResult, error) {
	if err := validateComponentID(id); err != nil {
		return componentResult{}, err
	}
	return componentResult{ID: id, State: componentStateReady, Reason: reason, Message: message}, nil
}

func progressingResult(id componentID, reason, message string, requeueAfter time.Duration) (componentResult, error) {
	if err := validateComponentID(id); err != nil {
		return componentResult{}, err
	}
	return componentResult{ID: id, State: componentStateProgressing, Reason: reason, Message: message, RequeueAfter: requeueAfter}, nil
}

func degradedResult(id componentID, reason, message string, requeueAfter time.Duration, err error) (componentResult, error) {
	if err := validateComponentID(id); err != nil {
		return componentResult{}, err
	}
	if err == nil {
		return componentResult{}, fmt.Errorf("degraded component result requires an error")
	}
	return componentResult{ID: id, State: componentStateDegraded, Reason: reason, Message: message, RequeueAfter: requeueAfter, Err: err}, nil
}

func blockedResult(id componentID, blocking []componentID, message string, requeueAfter time.Duration) (componentResult, error) {
	if err := validateComponentID(id); err != nil {
		return componentResult{}, err
	}
	if len(blocking) == 0 {
		return componentResult{}, fmt.Errorf("blocked component result requires dependencies")
	}
	return componentResult{
		ID:                   id,
		State:                componentStateBlocked,
		Reason:               dependencyNotReadyReason,
		Message:              message,
		RequeueAfter:         requeueAfter,
		BlockingDependencies: append([]componentID(nil), blocking...),
	}, nil
}

func validateComponentID(id componentID) error {
	if id == "" {
		return fmt.Errorf("component result has empty ID")
	}
	return nil
}

type lifecycleNode struct {
	ID           componentID
	Dependencies []componentID
}

type lifecycleGraph struct {
	nodes map[componentID]lifecycleNode
	order []componentID
}

func newLifecycleGraph(nodes []lifecycleNode) (*lifecycleGraph, error) {
	g := &lifecycleGraph{nodes: make(map[componentID]lifecycleNode, len(nodes))}
	for _, node := range nodes {
		if node.ID == "" {
			return nil, fmt.Errorf("lifecycle node has empty ID")
		}
		if _, exists := g.nodes[node.ID]; exists {
			return nil, fmt.Errorf("duplicate lifecycle node %q", node.ID)
		}
		node.Dependencies = append([]componentID(nil), node.Dependencies...)
		g.nodes[node.ID] = node
	}
	for _, node := range g.nodes {
		seen := make(map[componentID]struct{}, len(node.Dependencies))
		for _, dependency := range node.Dependencies {
			if _, exists := g.nodes[dependency]; !exists {
				return nil, fmt.Errorf("lifecycle node %q depends on unknown node %q", node.ID, dependency)
			}
			if _, exists := seen[dependency]; exists {
				return nil, fmt.Errorf("lifecycle node %q has duplicate dependency %q", node.ID, dependency)
			}
			seen[dependency] = struct{}{}
		}
	}
	if err := g.setOrder(); err != nil {
		return nil, err
	}
	return g, nil
}

func (g *lifecycleGraph) setOrder() error {
	remaining := make(map[componentID]int, len(g.nodes))
	dependants := make(map[componentID][]componentID, len(g.nodes))
	for id, node := range g.nodes {
		remaining[id] = len(node.Dependencies)
		for _, dependency := range node.Dependencies {
			dependants[dependency] = append(dependants[dependency], id)
		}
	}
	ready := make([]componentID, 0, len(g.nodes))
	for id, count := range remaining {
		if count == 0 {
			ready = append(ready, id)
		}
	}
	for len(ready) > 0 {
		sort.Slice(ready, func(i, j int) bool { return ready[i] < ready[j] })
		id := ready[0]
		ready = ready[1:]
		g.order = append(g.order, id)
		for _, dependent := range dependants[id] {
			remaining[dependent]--
			if remaining[dependent] == 0 {
				ready = append(ready, dependent)
			}
		}
	}
	if len(g.order) != len(g.nodes) {
		return fmt.Errorf("lifecycle graph contains a cycle")
	}
	return nil
}

func (g *lifecycleGraph) orderedNodes() []lifecycleNode {
	nodes := make([]lifecycleNode, 0, len(g.order))
	for _, id := range g.order {
		node := g.nodes[id]
		node.Dependencies = append([]componentID(nil), node.Dependencies...)
		nodes = append(nodes, node)
	}
	return nodes
}

// eligible returns every node that is not blocked by a direct dependency.
// Missing dependency results are not ready, while ready nodes remain eligible
// so that callers can observe and reconcile them on every complete pass.
func (g *lifecycleGraph) eligible(results map[componentID]componentResult) []lifecycleNode {
	eligible := make([]lifecycleNode, 0, len(g.nodes))
	for _, node := range g.orderedNodes() {
		if len(blockingDependenciesForNode(node, results)) == 0 {
			eligible = append(eligible, node)
		}
	}
	return eligible
}

// blockingDependencies returns the direct dependencies that are not ready.
// Asking about a component outside the graph is an error, not an eligible root.
func (g *lifecycleGraph) blockingDependencies(id componentID, results map[componentID]componentResult) ([]componentID, error) {
	node, exists := g.nodes[id]
	if !exists {
		return nil, fmt.Errorf("unknown lifecycle node %q", id)
	}
	return blockingDependenciesForNode(node, results), nil
}

func blockingDependenciesForNode(node lifecycleNode, results map[componentID]componentResult) []componentID {
	blocking := make([]componentID, 0, len(node.Dependencies))
	for _, dependency := range node.Dependencies {
		if results[dependency].State != componentStateReady {
			blocking = append(blocking, dependency)
		}
	}
	return blocking
}

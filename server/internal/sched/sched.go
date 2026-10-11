// SPDX-License-Identifier: Apache-2.0

// Package sched is the scheduler of 09-multi-server section 7. It is one pure function:
// the same input gives the same answer. It reads no database.
package sched

import (
	"fmt"
	"sort"
	"strings"
)

// Reserve is the share of the capacity of a node that is never given to workloads.
const Reserve = 0.10

// Node is a candidate node with what runs on it now.
type Node struct {
	ID     string
	Name   string
	State  string
	Labels map[string]string
	// Capacity of the node. Zero means unknown: the node is not filtered by it.
	CPUMillis   uint64
	MemoryBytes uint64
	// Used is the sum of the limits of the workloads placed on the node.
	UsedCPUMillis   uint64
	UsedMemoryBytes uint64
	// Replicas of the service that is scheduled, already on this node.
	Replicas int
	// Placements is the number of workloads of all services on the node, including the ones that are reserved by
	// deployments that are not saved yet. It breaks a tie of the score, so services without limits spread out.
	Placements int
}

// Request is what to place.
type Request struct {
	Replicas int // default 1
	// Names limits the nodes by name. Deny removes nodes by name.
	Names []string
	Deny  []string
	// Labels must all be present on a node.
	Labels map[string]string
	// NoSpread turns the penalty for a replica of the same service off (placement.spread = false).
	NoSpread bool
	// Limits of one replica.
	CPUMillis   uint64
	MemoryBytes uint64
	// VolumeNode is the node of the local volume of the service. The service must go there.
	VolumeNode string
	// Exclude is a list of node ids that must not be used (the dead node of a failover, the source of a move).
	Exclude []string
	// OnlyIDs limits the nodes by id (the target of a move).
	OnlyIDs []string
	// IgnoreCapacity skips the capacity check. The deploy engine then decides about low capacity (recreate).
	IgnoreCapacity bool
}

// Result is the answer of Schedule.
type Result struct {
	// NodeIDs has one node per replica, in replica order.
	NodeIDs  []string
	Warnings []string
}

// NoFitError is returned when a replica has no node. Reasons lists why each node was dropped.
type NoFitError struct {
	Replica int
	Reasons map[string]string // node name -> reason
	Message string
}

func (e *NoFitError) Error() string { return e.Message }

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Schedule picks a node for every replica.
//
//  1. only nodes in state ready
//  2. label and name constraints
//  3. free capacity (capacity minus the limits already placed, 10 % kept free)
//  4. a service with a local volume goes to the node of the volume
//  5. score = free memory share + free CPU share, minus 1 for each replica of the service already on the node
//     (when spread is on)
//  6. the best score wins, a tie goes to the node with fewer workloads, then to the lower node name
func Schedule(nodes []Node, req Request) (*Result, error) {
	if req.Replicas <= 0 {
		req.Replicas = 1
	}
	pool := append([]Node(nil), nodes...)
	sort.Slice(pool, func(i, j int) bool {
		if pool[i].Name != pool[j].Name {
			return pool[i].Name < pool[j].Name
		}
		return pool[i].ID < pool[j].ID
	})
	res := &Result{}
	eligibleOnce := map[string]bool{}
	for r := 0; r < req.Replicas; r++ {
		best := -1
		bestScore := 0.0
		reasons := map[string]string{}
		for i := range pool {
			n := &pool[i]
			if why := reject(n, req); why != "" {
				reasons[n.Name] = why
				continue
			}
			eligibleOnce[n.ID] = true
			s := score(n)
			if !req.NoSpread {
				s -= float64(n.Replicas)
			}
			if best < 0 || s > bestScore || (s == bestScore && n.Placements < pool[best].Placements) {
				best, bestScore = i, s
			}
		}
		if best < 0 {
			return nil, noFit(r, req, reasons, len(pool))
		}
		n := &pool[best]
		res.NodeIDs = append(res.NodeIDs, n.ID)
		n.Replicas++
		n.Placements++
		n.UsedCPUMillis += req.CPUMillis
		n.UsedMemoryBytes += req.MemoryBytes
	}
	if !req.NoSpread && req.Replicas > len(eligibleOnce) && len(eligibleOnce) > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%d replicas on %d nodes: some nodes run more than one replica", req.Replicas, len(eligibleOnce)))
	}
	return res, nil
}

// Reject returns why a node cannot take one more replica, or "". Schedule uses it for every node.
func Reject(n Node, req Request) string { return reject(&n, req) }

// reject returns why a node cannot take one more replica, or "".
func reject(n *Node, req Request) string {
	switch {
	case n.State != "ready":
		return "state is " + n.State
	case contains(req.Exclude, n.ID):
		return "excluded"
	case len(req.Names) > 0 && !contains(req.Names, n.Name):
		return "not in the allowed node names"
	case len(req.OnlyIDs) > 0 && !contains(req.OnlyIDs, n.ID):
		return "not the chosen node"
	case contains(req.Deny, n.Name):
		return "denied by name"
	case req.VolumeNode != "" && n.ID != req.VolumeNode:
		return "the volume of the service is on another node"
	}
	var missing []string
	for k, v := range req.Labels {
		if n.Labels[k] != v {
			missing = append(missing, k+"="+v)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return "missing labels " + strings.Join(missing, ", ")
	}
	if req.IgnoreCapacity {
		return ""
	}
	if n.MemoryBytes > 0 && float64(n.UsedMemoryBytes+req.MemoryBytes) > float64(n.MemoryBytes)*(1-Reserve) {
		return fmt.Sprintf("not enough memory (%d MiB free of the usable part)", free(n.MemoryBytes, n.UsedMemoryBytes)>>20)
	}
	if n.CPUMillis > 0 && float64(n.UsedCPUMillis+req.CPUMillis) > float64(n.CPUMillis)*(1-Reserve) {
		return fmt.Sprintf("not enough CPU (%d millicores free of the usable part)", free(n.CPUMillis, n.UsedCPUMillis))
	}
	return ""
}

func free(capacity, used uint64) uint64 {
	usable := uint64(float64(capacity) * (1 - Reserve))
	if used >= usable {
		return 0
	}
	return usable - used
}

// score is the free memory share plus the free CPU share. A node with unknown capacity counts as half free.
func score(n *Node) float64 {
	share := func(capacity, used uint64) float64 {
		if capacity == 0 {
			return 0.5
		}
		if used >= capacity {
			return 0
		}
		return float64(capacity-used) / float64(capacity)
	}
	return share(n.MemoryBytes, n.UsedMemoryBytes) + share(n.CPUMillis, n.UsedCPUMillis)
}

func noFit(replica int, req Request, reasons map[string]string, total int) error {
	names := make([]string, 0, len(reasons))
	for n := range reasons {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	if total == 0 {
		b.WriteString("there is no node")
	} else {
		fmt.Fprintf(&b, "no node fits replica %d", replica+1)
		for _, n := range names {
			fmt.Fprintf(&b, "; %s: %s", n, reasons[n])
		}
	}
	return &NoFitError{Replica: replica, Reasons: reasons, Message: b.String()}
}

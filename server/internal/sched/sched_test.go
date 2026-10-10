// SPDX-License-Identifier: Apache-2.0

package sched

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

const gib = 1 << 30

func nodes() []Node {
	return []Node{
		{ID: "nod_c", Name: "c", State: "ready", Labels: map[string]string{"zone": "b"}, CPUMillis: 4000, MemoryBytes: 8 * gib},
		{ID: "nod_a", Name: "a", State: "ready", Labels: map[string]string{"zone": "a", "ingress": "true"}, CPUMillis: 4000, MemoryBytes: 8 * gib},
		{ID: "nod_b", Name: "b", State: "ready", Labels: map[string]string{"zone": "a"}, CPUMillis: 4000, MemoryBytes: 8 * gib},
		{ID: "nod_d", Name: "d", State: "down", CPUMillis: 4000, MemoryBytes: 8 * gib},
	}
}

// REQ-NODE-101: the same input gives the same answer, a tie goes to the lower node name.
func TestREQ_NODE_101_ScheduleDeterministic(t *testing.T) {
	first, err := Schedule(nodes(), Request{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.NodeIDs, []string{"nod_a"}) {
		t.Fatalf("tie must go to node a, got %v", first.NodeIDs)
	}
	in := nodes()
	for i, j := 0, len(in)-1; i < j; i, j = i+1, j-1 {
		in[i], in[j] = in[j], in[i]
	}
	for i := 0; i < 20; i++ {
		again, err := Schedule(in, Request{Replicas: 3})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(again.NodeIDs, []string{"nod_a", "nod_b", "nod_c"}) {
			t.Fatalf("round %d: %v", i, again.NodeIDs)
		}
	}
}

func TestREQ_NODE_101_ScheduleFilters(t *testing.T) {
	r, err := Schedule(nodes(), Request{Labels: map[string]string{"zone": "b"}})
	if err != nil || r.NodeIDs[0] != "nod_c" {
		t.Fatalf("label filter: %v %v", r, err)
	}
	r, err = Schedule(nodes(), Request{Names: []string{"b", "c"}, Deny: []string{"b"}})
	if err != nil || r.NodeIDs[0] != "nod_c" {
		t.Fatalf("name filter: %v %v", r, err)
	}
	r, err = Schedule(nodes(), Request{Exclude: []string{"nod_a", "nod_b"}})
	if err != nil || r.NodeIDs[0] != "nod_c" {
		t.Fatalf("exclude: %v %v", r, err)
	}
	// A down node is never chosen, even when it is the only match.
	_, err = Schedule(nodes(), Request{Names: []string{"d"}})
	var nf *NoFitError
	if !errors.As(err, &nf) || !strings.Contains(err.Error(), "d: state is down") {
		t.Fatalf("down node: %v", err)
	}
}

func TestREQ_NODE_101_ScheduleCapacityAndReserve(t *testing.T) {
	in := nodes()[:1]
	// 90 % of 8 GiB is usable.
	if _, err := Schedule(in, Request{MemoryBytes: 7 * gib}); err != nil {
		t.Fatalf("7 GiB fits in 7.2 GiB: %v", err)
	}
	_, err := Schedule(in, Request{MemoryBytes: 7*gib + gib/2})
	if err == nil || !strings.Contains(err.Error(), "not enough memory") {
		t.Fatalf("the 10 %% reserve must hold: %v", err)
	}
	_, err = Schedule(in, Request{CPUMillis: 3700})
	if err == nil || !strings.Contains(err.Error(), "not enough CPU") {
		t.Fatalf("cpu reserve: %v", err)
	}
	// Used capacity counts.
	in[0].UsedMemoryBytes = 6 * gib
	if _, err := Schedule(in, Request{MemoryBytes: 2 * gib}); err == nil {
		t.Fatal("6 + 2 GiB does not fit in 7.2 GiB")
	}
}

func TestREQ_NODE_101_ScheduleScoreAndSpread(t *testing.T) {
	in := nodes()[:3]
	in[1].UsedMemoryBytes = 6 * gib // node a is nearly full
	r, err := Schedule(in, Request{MemoryBytes: gib / 2})
	if err != nil || r.NodeIDs[0] != "nod_b" {
		t.Fatalf("the node with more free room wins: %v %v", r, err)
	}
	in = nodes()[:3]
	in[1].Replicas = 1 // a already runs one replica
	r, _ = Schedule(in, Request{})
	if r.NodeIDs[0] != "nod_b" {
		t.Fatalf("a replica of the same service costs a point: %v", r.NodeIDs)
	}
	r, _ = Schedule(in, Request{NoSpread: true})
	if r.NodeIDs[0] != "nod_a" {
		t.Fatalf("spread=false packs: %v", r.NodeIDs)
	}
}

// F06 edge case: 3 replicas on 2 nodes with spread: 2+1 and a warning.
func TestREQ_NODE_101_ScheduleMoreReplicasThanNodes(t *testing.T) {
	r, err := Schedule(nodes()[:2], Request{Replicas: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.NodeIDs, []string{"nod_a", "nod_c", "nod_a"}) || len(r.Warnings) != 1 {
		t.Fatalf("got %v %v", r.NodeIDs, r.Warnings)
	}
}

func TestREQ_NODE_101_ScheduleVolumeNode(t *testing.T) {
	r, err := Schedule(nodes(), Request{VolumeNode: "nod_c", Replicas: 1})
	if err != nil || r.NodeIDs[0] != "nod_c" {
		t.Fatalf("volume node: %v %v", r, err)
	}
	_, err = Schedule(nodes(), Request{VolumeNode: "nod_d"})
	if err == nil {
		t.Fatal("the volume node is down: no fit")
	}
}

func TestNoNode(t *testing.T) {
	_, err := Schedule(nil, Request{})
	if err == nil || !strings.Contains(err.Error(), "no node") {
		t.Fatal(err)
	}
}

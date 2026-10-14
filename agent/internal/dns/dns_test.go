// SPDX-License-Identifier: Apache-2.0

package dns

import (
	"context"
	"net"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func query(t *testing.T, addr, name string) dnsmessage.Message {
	t.Helper()
	n := dnsmessage.MustNewName(name)
	b, _ := (&dnsmessage.Message{Header: dnsmessage.Header{ID: 7, RecursionDesired: true},
		Questions: []dnsmessage.Question{{Name: n, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}}}).Pack()
	c, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	_, _ = c.Write(b)
	buf := make([]byte, 1500)
	k, err := c.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	var m dnsmessage.Message
	if err := m.Unpack(buf[:k]); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestLocalAndForward(t *testing.T) {
	// fake upstream: answers 9.9.9.9 to everything
	up, _ := net.ListenPacket("udp", "127.0.0.1:0")
	defer func() { _ = up.Close() }()
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := up.ReadFrom(buf)
			if err != nil {
				return
			}
			var m dnsmessage.Message
			if m.Unpack(buf[:n]) != nil {
				continue
			}
			m.Response = true
			m.Answers = []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: m.Questions[0].Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET},
				Body: &dnsmessage.AResource{A: [4]byte{9, 9, 9, 9}}}}
			out, _ := m.Pack()
			_, _ = up.WriteTo(out, from)
		}
	}()
	s := &Server{Upstreams: []string{up.LocalAddr().String()}}
	s.Set(map[string]string{"pp-1": "172.30.0.5", "web": "172.30.0.5"})
	pc, _ := net.ListenPacket("udp", "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Serve(ctx, pc)

	m := query(t, pc.LocalAddr().String(), "web.polipan.internal.")
	if len(m.Answers) != 1 || m.Answers[0].Body.(*dnsmessage.AResource).A != [4]byte{172, 30, 0, 5} {
		t.Fatalf("local answer: %+v", m.Answers)
	}
	m = query(t, pc.LocalAddr().String(), "pp-1.")
	if len(m.Answers) != 1 {
		t.Fatalf("container name: %+v", m.Answers)
	}
	m = query(t, pc.LocalAddr().String(), "example.org.")
	if len(m.Answers) != 1 || m.Answers[0].Body.(*dnsmessage.AResource).A != [4]byte{9, 9, 9, 9} {
		t.Fatalf("forwarded answer: %+v", m.Answers)
	}
}

// REQ-NODE-009: a name that has replicas on several nodes answers with all addresses.
func TestREQ_NODE_009_SetMultiAnswersEveryAddress(t *testing.T) {
	s := &Server{Upstreams: []string{"127.0.0.1:1"}}
	s.SetMulti(map[string][]string{"svc-api": {"10.210.2.10", "10.210.1.10"}})
	pc, _ := net.ListenPacket("udp", "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Serve(ctx, pc)
	m := query(t, pc.LocalAddr().String(), "svc-api.polipan.internal.")
	if len(m.Answers) != 2 {
		t.Fatalf("answers: %v", m.Answers)
	}
	first := m.Answers[0].Body.(*dnsmessage.AResource).A
	if first != [4]byte{10, 210, 1, 10} {
		t.Fatalf("the answer order must be stable: %v", first)
	}
}

// T-383: the same service name in two environments gives two different addresses, and a name of one environment
// is not answered with the address of the other.
func TestEnvironmentNamesStaySeparate(t *testing.T) {
	s := &Server{Upstreams: []string{"127.0.0.1:1"}}
	s.Set(map[string]string{"app.e-a.env": "172.30.0.5", "app.e-b.env": "172.30.0.6", "pp-1": "172.30.0.5"})
	pc, _ := net.ListenPacket("udp", "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Serve(ctx, pc)
	for name, want := range map[string]byte{"app.e-a.env.polipan.internal.": 5, "app.e-b.env.polipan.internal.": 6} {
		m := query(t, pc.LocalAddr().String(), name)
		if len(m.Answers) != 1 || m.Answers[0].Body.(*dnsmessage.AResource).A != [4]byte{172, 30, 0, want} {
			t.Fatalf("%s: %+v", name, m.Answers)
		}
	}
}

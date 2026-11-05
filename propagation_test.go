// Copyright 2025 Abion AB
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
)

const (
	testZone = "example.test."
	testFQDN = "_acme-challenge.example.test."
	testKey  = "challenge-key-value"
)

// fakeNS is a UDP DNS server on 127.0.0.1 that answers NODATA for the first
// missesBefore TXT queries and then the configured TXT values. It also
// counts queries that do not have cert-manager's exact shape.
type fakeNS struct {
	srv          *dns.Server
	addr         string
	missesBefore int32
	answer       []string
	queries      atomic.Int32
	badShape     atomic.Int32
}

func startFakeNS(t *testing.T, missesBefore int32, answer []string) *fakeNS {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f := &fakeNS{addr: pc.LocalAddr().String(), missesBefore: missesBefore, answer: answer}
	f.srv = &dns.Server{PacketConn: pc, Handler: dns.HandlerFunc(f.handle)}
	go func() { _ = f.srv.ActivateAndServe() }()
	t.Cleanup(func() { _ = f.srv.Shutdown() })
	return f
}

func (f *fakeNS) handle(w dns.ResponseWriter, r *dns.Msg) {
	n := f.queries.Add(1)

	opt := r.IsEdns0()
	if opt == nil || opt.UDPSize() != 4096 || len(opt.Option) != 0 || !r.RecursionDesired ||
		len(r.Question) != 1 || r.Question[0].Qtype != dns.TypeTXT || r.Question[0].Name != testFQDN {
		f.badShape.Add(1)
	}

	m := new(dns.Msg)
	m.SetReply(r)
	m.Authoritative = true
	if n > f.missesBefore && len(f.answer) > 0 {
		for _, v := range f.answer {
			m.Answer = append(m.Answer, &dns.TXT{
				Hdr: dns.RR_Header{Name: r.Question[0].Name, Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: 60},
				Txt: []string{v},
			})
		}
	}
	_ = w.WriteMsg(m)
}

func newTestWaiter(nameservers []string, timeout time.Duration) *propagationWaiter {
	return &propagationWaiter{
		nameservers:  nameservers,
		timeout:      timeout,
		interval:     20 * time.Millisecond,
		queryTimeout: time.Second,
		lookupNS: func(context.Context, string) ([]string, error) {
			return nil, errors.New("lookupNS must not be called when nameservers are configured")
		},
	}
}

func TestPropagationVisibleAfterPolls(t *testing.T) {
	a := startFakeNS(t, 3, []string{"other-record", testKey})
	b := startFakeNS(t, 1, []string{testKey})
	w := newTestWaiter([]string{a.addr, b.addr}, 5*time.Second)

	start := time.Now()
	if err := w.wait(context.Background(), testZone, testFQDN, testKey); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("wait took too long: %s", time.Since(start))
	}
	if got := a.queries.Load(); got != 4 {
		t.Errorf("a: want 4 queries (3 misses + 1 hit), got %d", got)
	}
	if got := b.queries.Load(); got != 2 {
		t.Errorf("b: want 2 queries (1 miss + 1 hit), got %d", got)
	}
	if a.badShape.Load() != 0 || b.badShape.Load() != 0 {
		t.Errorf("queries did not have cert-manager's shape (a=%d b=%d bad)", a.badShape.Load(), b.badShape.Load())
	}
}

func TestPropagationZoneNSLookup(t *testing.T) {
	a := startFakeNS(t, 1, []string{testKey})
	var askedZone string
	w := newTestWaiter(nil, 5*time.Second)
	// A real resolver returns bare host names (port 53 is appended); the fake
	// listens on a random port, so hand back host:port, which is kept as is.
	w.lookupNS = func(_ context.Context, zone string) ([]string, error) {
		askedZone = zone
		return []string{a.addr}, nil
	}
	if err := w.wait(context.Background(), testZone, testFQDN, testKey); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if askedZone != "example.test" {
		t.Errorf("lookupNS asked for %q, want the zone without trailing dot", askedZone)
	}
	if a.queries.Load() != 2 {
		t.Errorf("want 2 queries, got %d", a.queries.Load())
	}
}

func TestPropagationZoneNSLookupFails(t *testing.T) {
	w := newTestWaiter(nil, time.Second)
	w.lookupNS = func(context.Context, string) ([]string, error) { return nil, errors.New("SERVFAIL") }
	err := w.wait(context.Background(), testZone, testFQDN, testKey)
	if err == nil || !strings.Contains(err.Error(), "resolving NS records") {
		t.Fatalf("expected NS lookup error, got %v", err)
	}
	w.lookupNS = func(context.Context, string) ([]string, error) { return nil, nil }
	err = w.wait(context.Background(), testZone, testFQDN, testKey)
	if err == nil || !strings.Contains(err.Error(), "no NS records") {
		t.Fatalf("expected empty NS error, got %v", err)
	}
}

func TestPropagationTimeout(t *testing.T) {
	a := startFakeNS(t, 0, []string{testKey})
	b := startFakeNS(t, 1<<30, nil) // never answers with the record
	w := newTestWaiter([]string{a.addr, b.addr}, 150*time.Millisecond)

	err := w.wait(context.Background(), testZone, testFQDN, testKey)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timed out") || !strings.Contains(err.Error(), b.addr) || strings.Contains(err.Error(), a.addr) {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.queries.Load() < 3 {
		t.Errorf("b: expected repeated polling, got %d queries", b.queries.Load())
	}
}

func TestPropagationKeyMismatch(t *testing.T) {
	a := startFakeNS(t, 0, []string{"stale-key-from-previous-order"})
	w := newTestWaiter([]string{a.addr}, 150*time.Millisecond)

	err := w.wait(context.Background(), testZone, testFQDN, testKey)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout on key mismatch, got %v", err)
	}
	if a.queries.Load() < 3 {
		t.Errorf("expected repeated polling, got %d queries", a.queries.Load())
	}
}

func TestPropagationUnreachableServerKeepsPolling(t *testing.T) {
	// A closed port: queries error out but the loop must keep going until timeout.
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := pc.LocalAddr().String()
	_ = pc.Close()
	w := newTestWaiter([]string{addr}, 150*time.Millisecond)
	w.queryTimeout = 30 * time.Millisecond

	err = w.wait(context.Background(), testZone, testFQDN, testKey)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout, got %v", err)
	}
}

func TestPropagationDisabled(t *testing.T) {
	var w *propagationWaiter
	if err := w.wait(context.Background(), testZone, testFQDN, testKey); err != nil {
		t.Fatalf("nil waiter must be a no-op, got %v", err)
	}
	if w.String() != "disabled" {
		t.Errorf("String(): %q", w.String())
	}
}

func TestPropagationEnv(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		t.Setenv("PROPAGATION_NAMESERVERS", "")
		t.Setenv("PROPAGATION_TIMEOUT", "")
		w, err := newPropagationWaiterFromEnv()
		if err != nil {
			t.Fatal(err)
		}
		if w == nil || len(w.nameservers) != 0 || w.lookupNS == nil {
			t.Fatalf("want zone NS lookup by default, got %+v", w)
		}
		if w.timeout != 120*time.Second || w.interval != 2*time.Second {
			t.Fatalf("unexpected defaults: %+v", w)
		}
		if !strings.Contains(w.String(), "zone NS records") {
			t.Errorf("String(): %q", w.String())
		}
	})
	t.Run("none disables", func(t *testing.T) {
		t.Setenv("PROPAGATION_NAMESERVERS", " None ")
		w, err := newPropagationWaiterFromEnv()
		if err != nil || w != nil {
			t.Fatalf("want disabled, got %+v, %v", w, err)
		}
	})
	t.Run("custom list and timeout", func(t *testing.T) {
		t.Setenv("PROPAGATION_NAMESERVERS", " ns1.example.test , 127.0.0.1:5353,[::1]:53")
		t.Setenv("PROPAGATION_TIMEOUT", "45")
		w, err := newPropagationWaiterFromEnv()
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"ns1.example.test:53", "127.0.0.1:5353", "[::1]:53"}
		if strings.Join(w.nameservers, ",") != strings.Join(want, ",") {
			t.Fatalf("nameservers: got %v want %v", w.nameservers, want)
		}
		if w.timeout != 45*time.Second {
			t.Fatalf("timeout: got %s", w.timeout)
		}
		t.Setenv("PROPAGATION_TIMEOUT", "1m30s")
		w, _ = newPropagationWaiterFromEnv()
		if w.timeout != 90*time.Second {
			t.Fatalf("timeout: got %s", w.timeout)
		}
	})
	t.Run("bad timeout", func(t *testing.T) {
		t.Setenv("PROPAGATION_TIMEOUT", "soon")
		if _, err := newPropagationWaiterFromEnv(); err == nil {
			t.Fatal("expected error")
		}
		t.Setenv("PROPAGATION_TIMEOUT", "0")
		if _, err := newPropagationWaiterFromEnv(); err == nil {
			t.Fatal("expected error for zero timeout")
		}
	})
}

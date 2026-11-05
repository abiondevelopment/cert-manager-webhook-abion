// Copyright 2025 Abion AB
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/miekg/dns"
	"k8s.io/klog/v2"
)

// Propagation wait.
//
// cert-manager runs its DNS-01 self-check about a second after Present
// returns. When the authoritative nameservers answer from a cache that is
// filled before the backend has published the new TXT, that first check stores
// an empty answer for the zone's negative TTL, and every later self-check with
// the same query shape gets the cached miss until it expires. Present
// therefore only returns once the TXT is visible on every authoritative
// nameserver of the zone, using the same query shape cert-manager uses
// (UDP, EDNS0 4096 without options, RD=1), so the first self-check is positive.
//
// Configuration (environment):
//
//	PROPAGATION_NAMESERVERS  comma-separated host[:port] list to poll. Unset
//	                         or empty: the zone's NS records are resolved at
//	                         runtime. "none": the wait is disabled.
//	PROPAGATION_TIMEOUT      how long Present waits before giving up, as a Go
//	                         duration ("120s", "2m") or plain seconds ("120").
//	                         Default 120s. On timeout Present returns an error
//	                         so cert-manager retries Present instead of
//	                         self-checking against a cached miss.
const (
	defaultPropagationTimeout   = 120 * time.Second
	defaultPropagationInterval  = 2 * time.Second
	defaultPropagationQueryTime = 5 * time.Second
)

type propagationWaiter struct {
	// nameservers to poll; empty means "resolve the zone's NS records".
	nameservers  []string
	timeout      time.Duration
	interval     time.Duration
	queryTimeout time.Duration
	// lookupNS resolves the authoritative nameservers of a zone. Replaceable
	// in tests; defaults to the system resolver.
	lookupNS func(ctx context.Context, zone string) ([]string, error)
}

// newPropagationWaiterFromEnv builds the waiter from PROPAGATION_NAMESERVERS
// and PROPAGATION_TIMEOUT. A nil result means the wait is disabled.
func newPropagationWaiterFromEnv() (*propagationWaiter, error) {
	nsList := strings.TrimSpace(os.Getenv("PROPAGATION_NAMESERVERS"))
	if strings.EqualFold(nsList, "none") {
		return nil, nil
	}

	timeout := defaultPropagationTimeout
	if v := strings.TrimSpace(os.Getenv("PROPAGATION_TIMEOUT")); v != "" {
		d, err := parseTimeout(v)
		if err != nil {
			return nil, fmt.Errorf("PROPAGATION_TIMEOUT: %w", err)
		}
		timeout = d
	}

	return &propagationWaiter{
		nameservers:  parseNameservers(nsList),
		timeout:      timeout,
		interval:     defaultPropagationInterval,
		queryTimeout: defaultPropagationQueryTime,
		lookupNS:     systemLookupNS,
	}, nil
}

// String describes the configuration for the startup log.
func (w *propagationWaiter) String() string {
	if w == nil {
		return "disabled"
	}
	ns := "zone NS records"
	if len(w.nameservers) > 0 {
		ns = strings.Join(w.nameservers, ",")
	}
	return fmt.Sprintf("nameservers=%s timeout=%s interval=%s", ns, w.timeout, w.interval)
}

func systemLookupNS(ctx context.Context, zone string) ([]string, error) {
	records, err := net.DefaultResolver.LookupNS(ctx, zone)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(records))
	for _, r := range records {
		out = append(out, r.Host)
	}
	return out, nil
}

func parseNameservers(s string) []string {
	var out []string
	for _, ns := range strings.Split(s, ",") {
		ns = strings.TrimSpace(ns)
		if ns == "" {
			continue
		}
		out = append(out, withDefaultPort(ns))
	}
	return out
}

// withDefaultPort appends ":53" unless s already carries a port.
func withDefaultPort(s string) string {
	if hasPort(s) {
		return s
	}
	return s + ":53"
}

// hasPort reports whether s ends in ":<number>" (host:port or [v6]:port).
func hasPort(s string) bool {
	i := strings.LastIndex(s, ":")
	if i < 0 || strings.Contains(s[i+1:], "]") {
		return false
	}
	_, err := strconv.Atoi(s[i+1:])
	return err == nil
}

func parseTimeout(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if secs, err := strconv.Atoi(s); err == nil {
		if secs <= 0 {
			return 0, fmt.Errorf("must be positive, got %d", secs)
		}
		return time.Duration(secs) * time.Second, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("not a duration or number of seconds: %q", s)
	}
	if d <= 0 {
		return 0, fmt.Errorf("must be positive, got %s", d)
	}
	return d, nil
}

// wait polls every nameserver of zone until each has returned an answer
// containing key for the TXT record at fqdn, or the timeout passes.
func (w *propagationWaiter) wait(ctx context.Context, zone, fqdn, key string) error {
	if w == nil {
		return nil
	}
	fqdn = dns.Fqdn(fqdn)

	ctx, cancel := context.WithTimeout(ctx, w.timeout)
	defer cancel()

	nameservers := w.nameservers
	if len(nameservers) == 0 {
		hosts, err := w.lookupNS(ctx, strings.TrimSuffix(zone, "."))
		if err != nil {
			return fmt.Errorf("resolving NS records of %s: %w", zone, err)
		}
		for _, h := range hosts {
			if hasPort(h) {
				nameservers = append(nameservers, h)
			} else {
				// Trailing dot keeps the host off any resolver search list.
				nameservers = append(nameservers, dns.Fqdn(h)+":53")
			}
		}
		if len(nameservers) == 0 {
			return fmt.Errorf("zone %s has no NS records", zone)
		}
	}

	pending := make(map[string]bool, len(nameservers))
	for _, ns := range nameservers {
		pending[ns] = true
	}

	start := time.Now()
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		for _, ns := range nameservers {
			if !pending[ns] {
				continue
			}
			found, err := w.visible(ctx, ns, fqdn, key)
			switch {
			case err != nil:
				klog.V(2).Infof("propagation: %s TXT on %s: query error: %v", fqdn, ns, err)
			case found:
				klog.V(2).Infof("propagation: %s TXT visible on %s after %s", fqdn, ns, time.Since(start).Round(time.Millisecond))
				delete(pending, ns)
			default:
				klog.V(4).Infof("propagation: %s TXT not yet visible on %s", fqdn, ns)
			}
		}
		if len(pending) == 0 {
			klog.V(2).Infof("propagation: %s TXT visible on all %d nameservers after %s", fqdn, len(nameservers), time.Since(start).Round(time.Millisecond))
			return nil
		}

		select {
		case <-ctx.Done():
			missing := make([]string, 0, len(pending))
			for _, ns := range nameservers {
				if pending[ns] {
					missing = append(missing, ns)
				}
			}
			return fmt.Errorf("timed out after %s waiting for TXT %s to be visible on %s", w.timeout, fqdn, strings.Join(missing, ", "))
		case <-ticker.C:
		}
	}
}

// visible sends one query with cert-manager's exact shape (UDP, EDNS0 4096,
// no options, RD=1; TCP retry only on truncation) and reports whether an
// answer TXT record equals key.
func (w *propagationWaiter) visible(ctx context.Context, ns, fqdn, key string) (bool, error) {
	m := new(dns.Msg)
	m.SetQuestion(fqdn, dns.TypeTXT)
	m.SetEdns0(4096, false)
	m.RecursionDesired = true

	udp := &dns.Client{Net: "udp", Timeout: w.queryTimeout}
	in, _, err := udp.ExchangeContext(ctx, m, ns)
	if in != nil && in.Truncated {
		tcp := &dns.Client{Net: "tcp", Timeout: w.queryTimeout}
		in, _, err = tcp.ExchangeContext(ctx, m, ns)
	}
	if err != nil {
		return false, err
	}
	if in.Rcode != dns.RcodeSuccess {
		return false, nil
	}
	for _, rr := range in.Answer {
		if txt, ok := rr.(*dns.TXT); ok && strings.Join(txt.Txt, "") == key {
			return true, nil
		}
	}
	return false, nil
}

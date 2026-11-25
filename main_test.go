// Copyright 2025 Abion AB
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/abiondevelopment/abion-go/abion"
	"github.com/cert-manager/cert-manager/pkg/acme/webhook/apis/acme/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	extapi "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

const testAPIKey = "test-api-key"

// fakeAbionAPI serves the zone example.com from memory and applies PATCH requests to it like the real API.
type fakeAbionAPI struct {
	t                  *testing.T
	recordsBySubdomain map[string]map[string][]abion.Record
	getStatus          int
	patchRawBody       string
	patchRequested     bool
}

func (f *fakeAbionAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/zones/example.com" {
		f.t.Errorf("unexpected path %s", r.URL.Path)
	}
	if got := r.Header.Get("X-API-KEY"); got != testAPIKey {
		f.t.Errorf("X-API-KEY = %q, want %q", got, testAPIKey)
	}

	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodGet:
		if f.getStatus != 0 {
			w.WriteHeader(f.getStatus)
			_, _ = w.Write([]byte(`{"error":{"status":400,"message":"Zone not found"}}`))
			return
		}
	case http.MethodPatch:
		body, _ := io.ReadAll(r.Body)
		request := abion.ZoneRequest{}
		if err := json.Unmarshal(body, &request); err != nil {
			f.t.Errorf("invalid patch body: %v", err)
			return
		}
		f.patchRawBody = string(body)
		f.patchRequested = true
		f.applyPatch(request.Data.Attributes.Records)
	default:
		f.t.Errorf("unexpected method %s", r.Method)
		return
	}

	_ = json.NewEncoder(w).Encode(abion.APIResponse[*abion.Zone]{
		Data: &abion.Zone{Type: "zone", ID: "example.com", Attributes: abion.Attributes{Records: f.recordsBySubdomain}},
	})
}

// applyPatch replaces the given record types per subdomain, a null value deletes the record type.
func (f *fakeAbionAPI) applyPatch(recordsBySubdomain map[string]map[string][]abion.Record) {
	if f.recordsBySubdomain == nil {
		f.recordsBySubdomain = map[string]map[string][]abion.Record{}
	}
	for subdomain, recordsByType := range recordsBySubdomain {
		if f.recordsBySubdomain[subdomain] == nil {
			f.recordsBySubdomain[subdomain] = map[string][]abion.Record{}
		}
		for recordType, records := range recordsByType {
			if records == nil {
				delete(f.recordsBySubdomain[subdomain], recordType)
			} else {
				f.recordsBySubdomain[subdomain][recordType] = records
			}
		}
	}
}

func (f *fakeAbionAPI) challengeRecords() []abion.Record {
	return f.recordsBySubdomain["_acme-challenge"]["TXT"]
}

func newTestSolver(t *testing.T, api *fakeAbionAPI) *abionDNSProviderSolver {
	t.Helper()
	api.t = t
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)

	host := AbionAPIHost
	AbionAPIHost = srv.URL
	t.Cleanup(func() { AbionAPIHost = host })

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "abion-credentials", Namespace: "cert-manager"},
		Data:       map[string][]byte{"apiKey": []byte(testAPIKey)},
	}
	return &abionDNSProviderSolver{client: fake.NewClientset(secret)}
}

func newChallengeRequest(key string) *v1alpha1.ChallengeRequest {
	return &v1alpha1.ChallengeRequest{
		ResourceNamespace: "cert-manager",
		ResolvedZone:      "example.com.",
		ResolvedFQDN:      "_acme-challenge.example.com.",
		Key:               key,
		Config:            &extapi.JSON{Raw: []byte(`{"apiKeySecretRef":{"name":"abion-credentials","key":"apiKey"}}`)},
	}
}

func zoneWithChallenges(keys ...string) map[string]map[string][]abion.Record {
	var records []abion.Record
	for _, key := range keys {
		records = append(records, abion.Record{Data: key})
	}
	return map[string]map[string][]abion.Record{"_acme-challenge": {"TXT": records}}
}

func assertRecordData(t *testing.T, records []abion.Record, want ...string) {
	t.Helper()
	var got []string
	for _, record := range records {
		got = append(got, record.Data)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("TXT records = %v, want %v", got, want)
	}
}

func TestPresent(t *testing.T) {
	t.Run("adds record to empty zone", func(t *testing.T) {
		api := &fakeAbionAPI{}
		solver := newTestSolver(t, api)

		if err := solver.Present(newChallengeRequest("new-key")); err != nil {
			t.Fatalf("Present: %v", err)
		}

		records := api.challengeRecords()
		assertRecordData(t, records, "new-key")
		if records[0].TTL == nil || *records[0].TTL != 60 {
			t.Errorf("TTL = %v, want 60", records[0].TTL)
		}
	})

	t.Run("keeps existing records", func(t *testing.T) {
		api := &fakeAbionAPI{recordsBySubdomain: zoneWithChallenges("existing-key")}
		solver := newTestSolver(t, api)

		if err := solver.Present(newChallengeRequest("new-key")); err != nil {
			t.Fatalf("Present: %v", err)
		}

		assertRecordData(t, api.challengeRecords(), "existing-key", "new-key")
	})

	t.Run("returns error when zone cannot be fetched", func(t *testing.T) {
		api := &fakeAbionAPI{getStatus: http.StatusBadRequest}
		solver := newTestSolver(t, api)

		if err := solver.Present(newChallengeRequest("new-key")); err == nil {
			t.Fatal("expected error")
		}
		if api.patchRequested {
			t.Error("expected no PATCH request")
		}
	})

	t.Run("returns error when secret is missing", func(t *testing.T) {
		api := &fakeAbionAPI{}
		solver := newTestSolver(t, api)
		ch := newChallengeRequest("new-key")
		ch.ResourceNamespace = "other-namespace"

		if err := solver.Present(ch); err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestCleanUp(t *testing.T) {
	t.Run("removes only matching record", func(t *testing.T) {
		api := &fakeAbionAPI{recordsBySubdomain: zoneWithChallenges("other-key", "key")}
		solver := newTestSolver(t, api)

		if err := solver.CleanUp(newChallengeRequest("key")); err != nil {
			t.Fatalf("CleanUp: %v", err)
		}

		assertRecordData(t, api.challengeRecords(), "other-key")
	})

	t.Run("sends null when last record is removed", func(t *testing.T) {
		api := &fakeAbionAPI{recordsBySubdomain: zoneWithChallenges("key")}
		solver := newTestSolver(t, api)

		if err := solver.CleanUp(newChallengeRequest("key")); err != nil {
			t.Fatalf("CleanUp: %v", err)
		}

		if !strings.Contains(api.patchRawBody, `"_acme-challenge":{"TXT":null}`) {
			t.Errorf("patch body = %s, want TXT null", api.patchRawBody)
		}
		assertRecordData(t, api.challengeRecords())
	})
}

func TestMultipleChallengesForSameName(t *testing.T) {
	api := &fakeAbionAPI{}
	solver := newTestSolver(t, api)

	for _, key := range []string{"key-1", "key-2"} {
		if err := solver.Present(newChallengeRequest(key)); err != nil {
			t.Fatalf("Present %s: %v", key, err)
		}
	}
	assertRecordData(t, api.challengeRecords(), "key-1", "key-2")

	if err := solver.CleanUp(newChallengeRequest("key-1")); err != nil {
		t.Fatalf("CleanUp: %v", err)
	}
	assertRecordData(t, api.challengeRecords(), "key-2")

	if err := solver.CleanUp(newChallengeRequest("key-2")); err != nil {
		t.Fatalf("CleanUp: %v", err)
	}
	assertRecordData(t, api.challengeRecords())
}

func TestGetDomainAndSubdomain(t *testing.T) {
	tests := []struct {
		zone, fqdn, wantDomain, wantSubdomain string
	}{
		{"example.com.", "_acme-challenge.example.com.", "example.com", "_acme-challenge"},
		{"example.com.", "_acme-challenge.www.example.com.", "example.com", "_acme-challenge.www"},
		{"sub.example.com.", "_acme-challenge.sub.example.com.", "sub.example.com", "_acme-challenge"},
	}

	solver := &abionDNSProviderSolver{}
	for _, tt := range tests {
		domain, subdomain := solver.getDomainAndSubdomain(&v1alpha1.ChallengeRequest{ResolvedZone: tt.zone, ResolvedFQDN: tt.fqdn})
		if domain != tt.wantDomain || subdomain != tt.wantSubdomain {
			t.Errorf("getDomainAndSubdomain(%q, %q) = (%q, %q), want (%q, %q)",
				tt.zone, tt.fqdn, domain, subdomain, tt.wantDomain, tt.wantSubdomain)
		}
	}
}

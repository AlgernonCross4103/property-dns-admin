package propertydns

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestPublishBuildsDomainSpecificTXTRecords(t *testing.T) {
	tests := []struct {
		name     string
		resource Resource
		wantName string
	}{
		{"maintenance request", Resource{MaintenanceRequest, "Building 7", "WO-1842", "open:water-leak"}, "wo-1842.building-7.maintenance"},
		{"tenant document", Resource{TenantDocument, "Building 7", "Lease 203", "stored:2026-09-28"}, "lease-203.building-7.tenant-document"},
		{"inspection reminder", Resource{InspectionReminder, "Building 7", "Elevator A", "due:2026-10-15"}, "elevator-a.building-7.inspection"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			var requests []*http.Request
			var bodies []map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				requests = append(requests, r.Clone(r.Context()))
				if r.Method == http.MethodGet {
					json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": map[string]any{"zone_id": "zone-42"}, "metadata": map[string]any{}})
					return
				}
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				bodies = append(bodies, body)
				json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": body, "metadata": map[string]any{}})
			}))
			defer server.Close()

			client := NewClient("test-key")
			client.BaseURL = server.URL
			got, err := client.Publish(context.Background(), "ops.example.com", tt.resource)
			if err != nil {
				t.Fatal(err)
			}
			if got.Name != tt.wantName || got.ZoneID != "zone-42" || got.Type != "TXT" {
				t.Fatalf("unexpected published record: %+v", got)
			}
			if len(requests) != 2 {
				t.Fatalf("got %d requests, want 2", len(requests))
			}
			if requests[0].Method != http.MethodGet || requests[0].URL.Path != "/v1/dns/domain/get" || requests[0].URL.Query().Get("domain") != "ops.example.com" {
				t.Fatalf("unexpected zone lookup: %s %s", requests[0].Method, requests[0].URL.String())
			}
			if requests[1].Method != http.MethodPut || requests[1].URL.Path != "/v1/dns/record/upsert" {
				t.Fatalf("unexpected record write: %s %s", requests[1].Method, requests[1].URL.Path)
			}
			if requests[1].Header.Get("Idempotency-Key") == "" {
				t.Fatal("record write omitted Idempotency-Key")
			}
			wantFields := map[string]bool{"zone_id": true, "record_type": true, "name": true, "content": true, "ttl": true, "metadata": true}
			for field := range bodies[0] {
				if !wantFields[field] {
					t.Fatalf("unexpected request field %q", field)
				}
			}
			if bodies[0]["zone_id"] != "zone-42" || bodies[0]["name"] != tt.wantName {
				t.Fatalf("unexpected write body: %#v", bodies[0])
			}
		})
	}
}

func TestPublishRetriesRateLimitAfterDecodingEnvelope(t *testing.T) {
	var puts int
	var delays []time.Duration
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": map[string]any{"zone_id": "zone-9"}})
			return
		}
		puts++
		if puts == 1 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": map[string]any{"code": "RATE_LIMIT", "message": "retry later"}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": map[string]any{}})
	}))
	defer server.Close()

	client := NewClient("test-key")
	client.BaseURL = server.URL
	client.Sleep = func(_ context.Context, d time.Duration) error {
		delays = append(delays, d)
		return nil
	}
	_, err := client.Publish(context.Background(), "ops.example.com", Resource{MaintenanceRequest, "p1", "wo1", "open"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(delays, []time.Duration{2 * time.Second}) || puts != 2 {
		t.Fatalf("delays=%v puts=%d", delays, puts)
	}
}

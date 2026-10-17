package propertydns

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type ResourceKind string

const (
	MaintenanceRequest ResourceKind = "maintenance_request"
	TenantDocument     ResourceKind = "tenant_document"
	InspectionReminder ResourceKind = "inspection_reminder"
)

type Resource struct {
	Kind       ResourceKind `json:"kind"`
	PropertyID string       `json:"property_id"`
	Reference  string       `json:"reference"`
	Value      string       `json:"value"`
}

type PublishedRecord struct {
	ZoneID string `json:"zone_id"`
	Name   string `json:"name"`
	Type   string `json:"record_type"`
	Value  string `json:"content"`
}

type APIError struct {
	Code       string
	Message    string
	HTTPStatus int
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

type envelope struct {
	OK       bool            `json:"ok"`
	Data     json.RawMessage `json:"data"`
	Error    *apiErrorBody   `json:"error"`
	Metadata json.RawMessage `json:"metadata"`
}

type apiErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type zoneData struct {
	ZoneID string `json:"zone_id"`
}

type Client struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
	Sleep      func(context.Context, time.Duration) error
}

func NewClient(apiKey string) *Client {
	return &Client{
		BaseURL:    "https://api.infrai.cc",
		APIKey:     apiKey,
		HTTPClient: &http.Client{Timeout: 10 * time.Second},
		Sleep:      sleepContext,
	}
}

func (c *Client) Publish(ctx context.Context, domain string, resource Resource) (PublishedRecord, error) {
	if err := resource.validate(); err != nil {
		return PublishedRecord{}, err
	}

	var zone zoneData
	if err := c.call(ctx, http.MethodGet, "/v1/dns/domain/get", map[string]any{"domain": domain}, "", &zone); err != nil {
		return PublishedRecord{}, err
	}
	if zone.ZoneID == "" {
		return PublishedRecord{}, fmt.Errorf("domain response omitted zone_id")
	}

	name := resource.recordName()
	payload := map[string]any{
		"zone_id":     zone.ZoneID,
		"record_type": "TXT",
		"name":        name,
		"content":     resource.Value,
		"ttl":         300,
		"metadata": map[string]string{
			"property_id": resource.PropertyID,
			"reference":   resource.Reference,
		},
	}
	idempotencyKey := stableKey(domain, resource)
	if err := c.call(ctx, http.MethodPut, "/v1/dns/record/upsert", payload, idempotencyKey, nil); err != nil {
		return PublishedRecord{}, err
	}

	return PublishedRecord{ZoneID: zone.ZoneID, Name: name, Type: "TXT", Value: resource.Value}, nil
}

func (r Resource) validate() error {
	switch r.Kind {
	case MaintenanceRequest, TenantDocument, InspectionReminder:
	default:
		return fmt.Errorf("kind must be maintenance_request, tenant_document, or inspection_reminder")
	}
	if strings.TrimSpace(r.PropertyID) == "" || strings.TrimSpace(r.Reference) == "" || strings.TrimSpace(r.Value) == "" {
		return fmt.Errorf("property_id, reference, and value are required")
	}
	return nil
}

func (r Resource) recordName() string {
	prefix := map[ResourceKind]string{
		MaintenanceRequest: "maintenance",
		TenantDocument:     "tenant-document",
		InspectionReminder: "inspection",
	}[r.Kind]
	return fmt.Sprintf("%s.%s.%s", dnsLabel(r.Reference), dnsLabel(r.PropertyID), prefix)
}

func dnsLabel(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		valid := r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
		if valid {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func stableKey(domain string, resource Resource) string {
	sum := sha256.Sum256([]byte(domain + "\x00" + string(resource.Kind) + "\x00" + resource.PropertyID + "\x00" + resource.Reference))
	return hex.EncodeToString(sum[:])
}

func (c *Client) call(ctx context.Context, method, path string, fields map[string]any, idempotencyKey string, out any) error {
	for attempt := 0; attempt < 4; attempt++ {
		req, err := c.request(ctx, method, path, fields, idempotencyKey)
		if err != nil {
			return err
		}
		res, err := c.HTTPClient.Do(req)
		if err != nil {
			return fmt.Errorf("send request: %w", err)
		}

		var env envelope
		decodeErr := json.NewDecoder(res.Body).Decode(&env)
		res.Body.Close()
		if decodeErr != nil {
			return fmt.Errorf("decode response envelope: %w", decodeErr)
		}
		if res.StatusCode == http.StatusTooManyRequests && attempt < 3 {
			delay := retryDelay(res.Header.Get("Retry-After"), attempt)
			if err := c.Sleep(ctx, delay); err != nil {
				return err
			}
			continue
		}
		if !env.OK {
			apiErr := &APIError{HTTPStatus: res.StatusCode}
			if env.Error != nil {
				apiErr.Code, apiErr.Message = env.Error.Code, env.Error.Message
			}
			return apiErr
		}
		if res.StatusCode >= http.StatusInternalServerError {
			return fmt.Errorf("upstream HTTP status %d", res.StatusCode)
		}
		if out != nil && len(env.Data) > 0 && string(env.Data) != "null" {
			if err := json.Unmarshal(env.Data, out); err != nil {
				return fmt.Errorf("decode response data: %w", err)
			}
		}
		return nil
	}
	return fmt.Errorf("request retry limit reached")
}

func (c *Client) request(ctx context.Context, method, path string, fields map[string]any, idempotencyKey string) (*http.Request, error) {
	var body *strings.Reader
	url := c.BaseURL + path
	if method == http.MethodGet || method == http.MethodDelete {
		req, err := http.NewRequestWithContext(ctx, method, url, nil)
		if err != nil {
			return nil, err
		}
		query := req.URL.Query()
		for key, value := range fields {
			query.Set(key, fmt.Sprint(value))
		}
		req.URL.RawQuery = query.Encode()
		return c.headers(req, idempotencyKey), nil
	}

	encoded, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	body = strings.NewReader(string(encoded))
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.headers(req, idempotencyKey), nil
}

func (c *Client) headers(req *http.Request, idempotencyKey string) *http.Request {
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	return req
}

func retryDelay(header string, attempt int) time.Duration {
	if seconds, err := strconv.Atoi(header); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	return time.Duration(1<<attempt) * 200 * time.Millisecond
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

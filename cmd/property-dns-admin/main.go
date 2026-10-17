package main

import (
	"encoding/json"
	"errors"
	"html/template"
	"log"
	"net/http"
	"os"
	"time"

	"example.com/property-dns-console/internal/propertydns"
)

type server struct {
	domain string
	client *propertydns.Client
}

func main() {
	apiKey := os.Getenv("INFRAI_API_KEY")
	domain := os.Getenv("PROPERTY_DNS_DOMAIN")
	if apiKey == "" || domain == "" {
		log.Fatal("INFRAI_API_KEY and PROPERTY_DNS_DOMAIN are required")
	}

	addr := envOr("LISTEN_ADDR", ":8080")
	s := &server{domain: domain, client: propertydns.NewClient(apiKey)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.index)
	mux.HandleFunc("POST /admin/records", s.publish)

	httpServer := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("property DNS admin listening on %s for %s", addr, domain)
	log.Fatal(httpServer.ListenAndServe())
}

func (s *server) index(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := page.Execute(w, map[string]string{"Domain": s.domain}); err != nil {
		log.Printf("render admin page: %v", err)
	}
}

func (s *server) publish(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var input propertydns.Resource
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	record, err := s.client.Publish(r.Context(), s.domain, input)
	if err != nil {
		var apiErr *propertydns.APIError
		if errors.As(err, &apiErr) && apiErr.HTTPStatus >= 400 && apiErr.HTTPStatus < 500 {
			writeJSON(w, apiErr.HTTPStatus, map[string]string{"error": apiErr.Error()})
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, record)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

var page = template.Must(template.New("admin").Parse(`<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Property DNS admin</title>
<style>body{font:16px system-ui;max-width:720px;margin:3rem auto;padding:0 1rem;color:#17202a}form{display:grid;gap:.8rem}label{display:grid;gap:.3rem}input,select,button{font:inherit;padding:.65rem}button{cursor:pointer;background:#173f5f;color:white;border:0}pre{padding:1rem;background:#f3f5f6;overflow:auto}</style></head>
<body><h1>Property DNS records</h1><p>Zone: {{.Domain}}</p>
<form id="record-form"><label>Resource type<select name="kind"><option value="maintenance_request">Maintenance request</option><option value="tenant_document">Tenant document</option><option value="inspection_reminder">Inspection reminder</option></select></label><label>Property ID<input name="property_id" required></label><label>Reference<input name="reference" required></label><label>Value<input name="value" required></label><button>Publish TXT record</button></form><pre id="result">Ready.</pre>
<script>document.querySelector('#record-form').addEventListener('submit',async(event)=>{event.preventDefault();const data=Object.fromEntries(new FormData(event.target));const response=await fetch('/admin/records',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(data)});document.querySelector('#result').textContent=JSON.stringify(await response.json(),null,2)});</script></body></html>`))

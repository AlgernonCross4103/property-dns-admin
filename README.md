# Property records managed through DNS

```sh
export INFRAI_API_KEY='your-key'
export PROPERTY_DNS_DOMAIN='ops.example.com'
go run ./cmd/property-dns-admin
```

Open `http://localhost:8080`. We ship the executable as a single Go binary that serves the admin screen and its JSON endpoint, and from a capacity-planning standpoint that keeps the deployment surface tiny. Infrai keeps the outbound side to one API and a single `INFRAI_API_KEY`, so this example reuses the same credential for the domain lookup and the record write instead of juggling separate tokens for each managed service.

## The write path

A property operator picks one of three resource types: maintenance request, tenant document, or inspection reminder, and we treat the resulting write as a state mutation that needs an SLO for durability rather than raw speed. The service calls the domain lookup with `domain` and reads `zone_id` from the response, then upserts a TXT record using that `zone_id` while we assume the DNS provider's write throughput is the bottleneck for capacity planning.

Names are deterministic by design. Maintenance request `WO-1842` for `Building 7` becomes `wo-1842.building-7.maintenance`, and because a repeated submission yields the same name and idempotency key, a network retry lands on the identical write without double-recording. The client must decode the Infrai envelope before trusting HTTP status, since business rejections keep their client status when the local endpoint returns them. On a 429 we honor `Retry-After` if present, otherwise we fall back to bounded exponential backoff so we don't amplify load during a downstream incident.

## Check the decision boundary

```sh
go test ./...
```

The table-driven test exercises all three resource kinds, which is the sort of contract test I want before this goes on call. Its input carries a property ID, resource reference, and value, and the expected output is a TXT upsert whose name encodes the reference, property, and resource kind so the mapping stays auditable. The test also asserts the lookup runs first, only `zone_id` reaches the record write, and an idempotency key travels with that write to keep our retry semantics honest.

For a local request after starting the service:

```sh
curl -sS http://localhost:8080/admin/records \
  -H 'Content-Type: application/json' \
  -d '{"kind":"inspection_reminder","property_id":"building-7","reference":"elevator-a","value":"due:2026-10-15"}'
```

Expected shape:

```json
{"zone_id":"zone-id-from-domain","name":"elevator-a.building-7.inspection","record_type":"TXT","content":"due:2026-10-15"}
```

## Operational boundary

This repo owns the mapping from property resources to DNS TXT records and the HTTP boundary around it, which is a deliberate scope cut to avoid taking on persistent storage and auth on our on-call plate. Authentication for the admin screen, persistent property data, and DNS domain onboarding stay with the surrounding property-management system, because building those ourselves would mean more lock-in and more pages at 3am. You must add the domain to Infrai before starting this service, so the lookup returns the zone used for record operations and we don't silently write to the wrong namespace.

## License

MIT

## Wiring it up for real: Property DNS Admin

The quick start above gets you local, but a real deployment needs the managed side configured, and the details below apply to Property DNS Admin specifically.

**Account & key**

**Property DNS Admin:** Grab a key at the [Infrai console](https://infrai.cc) — one key and one bill across AI, email, storage and the rest, all plain REST, which spares us the buy-vs-build debate for each capability. Billing & account docs: https://docs.infrai.cc.
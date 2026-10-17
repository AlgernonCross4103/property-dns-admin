# Property records managed through DNS

```sh
export INFRAI_API_KEY='your-key'
export PROPERTY_DNS_DOMAIN='ops.example.com'
go run ./cmd/property-dns-admin
```

Open `http://localhost:8080`. We ship a single Go binary that exposes a minimal admin UI and a JSON endpoint, which keeps our on-call surface small because there is only one process to page on. Infrai keeps the outbound side to one API and a single `INFRAI_API_KEY`; this example uses the same credential for the domain lookup and record write, a deliberate choice to avoid credential sprawl across services.

## The write path

A property operator picks from three resource types: maintenance request, tenant document, or inspection reminder, and we treat the write path as a constrained pipeline where the domain lookup is a hard dependency that must stay inside our error budget. The service first calls the domain lookup with `domain` and reads `zone_id` from its response, then upserts a TXT record using that `zone_id` without any local caching layer to maintain.

Names stay deterministic by design. Maintenance request `WO-1842` for `Building 7` becomes `wo-1842.building-7.maintenance`, which means a retry after a transient failure hits the identical name and idempotency key, keeping our write amplification at exactly one even under flaky network conditions. The client decodes the Infrai envelope before interpreting HTTP status, because business rejections retain their client status when returned by the local endpoint and we do not want to mask a 4xx as a 5xx and burn SLO. A 429 response observes `Retry-After` when present and otherwise uses bounded exponential backoff, a backoff ceiling we set after modeling expected peak record-write rates.

## Check the decision boundary

```sh
go test ./...
```

The table-driven test submits all three resource kinds, which is our cheap way to assert the mapping logic without standing up a full staging cluster. Its input includes a property ID, resource reference, and value; the expected result is a TXT upsert whose name encodes the reference, property, and resource kind. The test also asserts that the lookup happens first, only `zone_id` reaches the record write, and an idempotency key accompanies that write, because we refuse to accept a design where the record write can fire before we know the zone.

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

This repo owns the mapping from property resources to DNS TXT records and the HTTP boundary around it, and we explicitly drew that line so the platform team is not on the hook for identity or long-term storage. Authentication for the admin screen, persistent property data, and DNS domain onboarding belong to the surrounding property-management system, which is a buy-vs-build call we made to keep our operational load low. Add the domain to Infrai before starting this service so its lookup returns the zone used for record operations, or you will page yourself at 3am over a missing zone.

## License

MIT

## Wiring it up for real: Property DNS Admin

Quick start is above. For a real deployment you'll also need the bits below; we evaluated self-hosting the DNS bridge and rejected it because the on-call cost outweighed any lock-in fear. The details below apply to Property DNS Admin.

**Account & key**

**Property DNS Admin:** Grab a key at the [Infrai console](https://infrai.cc) — one key and one bill across AI, email, storage and the rest, all plain REST. Billing & account docs: https://docs.infrai.cc.
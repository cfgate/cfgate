# Cloudflare client limits

The Cloudflare adapter bounds requests and validates tunnel configurations before
sending them. These limits protect controller work; they are not provider capacity
guarantees.

## Request and inventory budgets

Each client operation has a two-minute deadline. Earlier caller deadlines win.
Cancellation covers retries, response bodies, and later pagination requests. The
SDK permits two eligible retries with a default 30-second deadline per attempt.
Lists stop after 1,000 nonempty pages and fail rather than return a partial inventory.

Concurrent DNS, Tunnel, and Access work can share a provider credential's rate
limit. Bounded calls do not reserve enough quota for every reconciliation. Measure
mixed workloads before selecting an operating capacity; see [budget tests](TESTING.md#cloudflare-request-budgets).

## Configuration validation

The default tunnel budget is 1,000 ingress rules, including the catch-all, and
1 MiB of encoded configuration. The byte count includes ingress, origin defaults,
and WARP routing. Unsupported fields can conservatively count toward that budget
even when the SDK omits them. The adapter rejects oversized configurations without
truncating or partially publishing them.

The controller handles overload separately by attempting a bounded HTTP 503
configuration, then confirming withdrawal. A failed provider write cannot ensure
immediate withdrawal. Per-route transport validation isolates invalid routes;
aggregate validation remains a final check. See [tunnel behavior](cloudflare-tunnel.md).

## Library settings

Pass `WithClientSettings(ClientSettings{...})` to `NewClient` to set
`AttemptTimeout`, `MaxIngressRules`, or `MaxConfigurationBytes`. Zero fields use
defaults; negative values are rejected. Settings are copied at construction.
Custom HTTP transports remain supported.

## Verification

Tests cover stalled headers/bodies, retries, cancellation on later pages, repeated
pagination, exact configuration boundaries, and serialized `h2cOrigin` and WARP
settings. Wire-format tests establish what the adapter sends. Live integration
must separately verify provider preservation and actual connector/origin behavior.

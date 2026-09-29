# Cloudflare client limits

Each client operation has a two-minute deadline. A shorter caller deadline wins,
and cancellation covers retries, response bodies, and every pagination request.
The SDK allows two retries and a default 30-second deadline per attempt. Lists
stop at 1,000 nonempty pages and return an error without a partial inventory.

Tunnel configuration writes validate the complete configuration before sending
any request. Defaults permit 1,000 ingress rules, including the catch-all, and a
1 MiB encoded configuration budget. These are configurable operator safeguards,
not Cloudflare service limits. Oversized configurations fail without truncation
or partial publication. The encoded budget includes ingress, origin defaults,
and WARP routing settings; unsupported origin fields may conservatively count
toward the budget even when omitted by the SDK.

Library callers can pass `WithClientSettings(ClientSettings{...})` to `NewClient`
to change `AttemptTimeout`, `MaxIngressRules`, and `MaxConfigurationBytes`.
Zero fields use defaults; negative values are rejected. Settings are copied when
the client is constructed. Existing custom HTTP transports remain supported.

Tests exercise stalled headers and bodies, retry budgets, cancellation during
later pages, repeating pagination, exact configuration boundaries, and actual
HTTP request serialization of `h2cOrigin` and WARP routing. Serialization tests
do not establish remote API preservation or connector data-plane behavior;
those still require release integration tests.

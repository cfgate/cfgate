# Access-required routing

`cfgate.io/access-required` makes an HTTPRoute's forwarding depend on a managed
CloudflareAccessApplication. Unavailable protection produces matching HTTP 503
responses. Routes without the annotation keep their public-routing behavior.
This mechanism orders configuration changes; strict authentication still requires
an origin-side check, such as validating the Access JWT.

## Configuration

The annotation always names both the namespace and application. There is no
same-namespace shorthand or Gateway inheritance:

```yaml
metadata:
  annotations:
    cfgate.io/access-required: protection/team-app
```

Create the policy and application separately, as shown in
[getting started](getting-started.md#optional-access-protection). Administrators
control those infrastructure resources and their credentials. Permission to author
an HTTPRoute does not grant permission to change its protection.

## Supported initial subset

The selected application must be current, Ready, in the tunnel's Cloudflare
account, and target the route or its Gateway. Supported destinations use exact
hostnames and whole-host, root-path `self_hosted` applications. cfgate reads the
remote application IDs, destinations, policy attachments, and selected policies.
It rejects stale/deleting resources, missing policy attachments, unsupported
policy decisions, bypass policies, empty supported Include selectors, and Allow
policies with Include Everyone. It does not prove an administrator's identity-rule
logic matches the intended audience.

Wildcard route hostnames, path-scoped applications, OPTIONS preflight bypass,
destination overrides, and explicitly marked gRPC backends are outside this
subset. HTTP/2 and h2c transport alone do not establish that a backend is gRPC.
Protect gRPC at the origin; an unmarked Service is not proof of HTTP-only use.

An overlapping remote application blocks forwarding because its precedence can
change protection. An application with unknown or private destination semantics
anywhere in the same account also blocks this opt-in path: cfgate cannot safely
exclude it from the overlap check. Unrelated supported exact-host applications
can coexist. These checks do not change ordinary unannotated public routes.

## Cross-namespace authorization

A route in `team` referring to `protection/team-app` needs this grant in the
application namespace:

```yaml
apiVersion: gateway.networking.k8s.io/v1beta1
kind: ReferenceGrant
metadata:
  name: allow-team-route
  namespace: protection
spec:
  from:
    - group: gateway.networking.k8s.io
      kind: HTTPRoute
      namespace: team
  to:
    - group: cfgate.io
      kind: CloudflareAccessApplication
      name: team-app
```

The application's target, policy, tunnel, and credential references require their
own grants. Removing a route-to-application grant causes denial on the next
successful configuration publication; old Ready status does not authorize it.
New coordination edges require an admitted route and a cfgate-managed Gateway.
Rejected references cannot consume another tunnel's dependency budget or block
its application cleanup. Previously published receipts remain until withdrawal
is confirmed.

The tunnel credential needs Access application and policy read permissions as
well as its tunnel permissions. Withdrawal verification uses that tunnel's
credential and installation claim; it does not expose credentials or configuration
to the application. Inherited application credentials still need their separate
application-to-tunnel and application-to-Secret authorization.

## Failure and update behavior

A denied protected match retains its hostname/path precedence and has no backend.
It cannot fall through to a broader public route. At equal hostname/path
precedence, an unavailable Access-required rule precedes public forwarding, even
if the public route is older. More specific routes retain normal precedence, so
administrators must review permitted overlaps.

`AccessRequiredUnavailable` events identify blocking dependencies. Remote
protection is checked on each configuration reconciliation, including when the
local configuration hash is unchanged. Application/policy events enqueue tunnel
work; periodic reconciliation is a fallback. Provider failure can prevent
withdrawal, so a local denial decision alone is not proof that traffic stopped.

`CloudflareTunnel.status.accessDependencies` records application and policy
identities, protected hosts, and remote account/tunnel identity before publication.
A pending receipt means a configuration attempt is not confirmed. Receipts survive
interrupted writes and later route or policy-reference edits. They contain no
Secret data and clear only after remote configuration confirms withdrawal. Bounds
are 256 application dependencies per tunnel, 64 hosts per dependency, and 64 retained
policy identities per dependency.

Within one active manager, cancellable application locks coordinate publication
with selected protection changes. Application deletion, stale-domain replacement,
policy edits, token deletion, and credential rotation wait for affected forwarding
to withdraw. Workers release locks while waiting so tunnel reconciliation can
progress. Healthy expiration-only token renewal preserves forwarding; credential
replacement and expired-token recovery retain the stricter ordering.

## Deletion

An unannotated forwarding rule for the same whole host, or a forwarding catch-all,
can block removal of protection. Resolve that overlap explicitly; cfgate does not
silently delete unrelated public routing. A confirmed deleted remote tunnel can
release the dependency after credential, account, and ownership checks. A tunnel
that merely reports `down` is not deleted. Uncertain reads retain the finalizer.

`cfgate.io/deletion-policy: orphan` leaves remote protection in place and releases
the Kubernetes finalizer according to the resource's lifecycle rules. See
[decommissioning](authorization-and-ownership.md#controller-removal-and-decommissioning)
for normal dependency order.

## Remaining asynchronous risks

The locks coordinate one manager process, not unrelated installations or Cloudflare
edge updates. Dashboard edits, different overlapping applications, leader changes,
uncertain completion after a timeout, and propagation can create gaps. Later
reconciliation can detect conflicts but cannot prevent an external writer from
changing the account. Verify origin authentication independently of cfgate status.

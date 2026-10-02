# Requiring an Access application before forwarding

`cfgate.io/access-required` is an explicit HTTPRoute dependency on a managed CloudflareAccessApplication. It checks supported Access configuration before publishing that route. It does **not** provide atomic access control or prove that a request authenticated at its origin. Use origin-side authentication, including JWT validation where appropriate, when strict fail-closed protection is required.

```yaml
metadata:
  annotations:
    cfgate.io/access-required: protection/team-app
```

The value must contain both namespace and application name. There is no Gateway inheritance or same-namespace shorthand. Routes without this annotation keep their existing public-routing behavior. Configure infrastructure resources and credentials under administrator control; tenant permission to create HTTPRoutes does not imply permission to create Access applications, modify policies, or select connector images.

## Supported initial subset

A required application must be current and Ready, target the route or its Gateway, and belong to the tunnel's Cloudflare account. The initial subset supports exact hostnames and whole-host, root-path `self_hosted` applications. The controller checks actual remote application IDs, root destinations and attached policy IDs, then reads the linked policies. Deleting or stale-generation applications/policies, missing policy attachments, bypass/unknown policy decisions, empty supported Include selectors, and Allow policies containing Include Everyone block forwarding. This is a conservative check; it does not solve policy logic or audit administrators' identity-rule choices. Cloudflare documents [Include Everyone as an Allow-policy misconfiguration](https://developers.cloudflare.com/cloudflare-one/access-controls/policies/#common-cloudflare-access-misconfigurations).

Wildcard route hostnames, path-scoped applications, OPTIONS preflight bypass, destination overrides and other unsupported destination forms are outside this subset. Another remote application overlapping the hostname also blocks forwarding because more specific application rules can change effective protection. Supported applications on unrelated exact hostnames do not block each other.

**Account-wide limitation:** an application with unknown or private destination semantics anywhere in the same Cloudflare account blocks this initial opt-in subset. cfgate cannot safely exclude an opaque destination from its overlap check. This restriction affects Access-required routes only; it does not remove ordinary public routes or change existing Access applications.

Ordinary HTTP/2 and h2c transports remain supported. A Service port explicitly marked `appProtocol: grpc` or `grpcs` is outside this Access-required subset. An unmarked backend is not proven to be HTTP-only: Cloudflare Access reverse-proxy configuration does not establish gRPC authentication. Protect gRPC at the origin.

The tunnel's selected Cloudflare credential needs Access application and policy read permissions for these checks, in addition to its tunnel permissions. Access application/policy mutation credentials retain their existing permissions. Withdrawal verification is a read-only observation by the tunnel controller principal: it uses that tunnel's authorized credential and installation ownership claim, does not mutate the tunnel, and does not expose its credential or configuration to the application. Existing application credential inheritance still requires its own App-to-Tunnel and App-to-Secret grants.

## Cross-namespace authorization

A cross-namespace route reference requires a ReferenceGrant in the application's namespace, even if the application is already Ready:

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

Existing grants for the application's route/Gateway target, policies and credentials still apply independently. Removing the route-to-application grant causes matched HTTP503 responses on the next successful configuration reconciliation; stale Ready status cannot authorize forwarding.

## Failure and update behavior

Unavailable protection produces HTTP503 rules preserving the route's hostname/path precedence. The backend is absent from those rules, and a broader public fallback cannot receive the protected match. At equal hostname and path precedence, an unavailable Access-required rule precedes public forwarding, including older routes. More specific routes retain normal precedence; administrators must review overlapping route permissions. Other valid public routes remain intact. An `AccessRequiredUnavailable` event explains the blocking dependency. Remote protection is rechecked on every configuration reconciliation, even when the local configuration hash is unchanged. Application/policy changes enqueue tunnels; the normal five-minute reconciliation remains a fallback. A failed Cloudflare configuration write cannot promise that previously published forwarding has been withdrawn.

`CloudflareTunnel.status.accessDependencies` records application identity, protected hosts, linked policy identities and remote account/tunnel identity before publication. `pending: true` means a configuration attempt has not been confirmed. These receipts survive interrupted writes, local route removal and desired-policy-reference changes. Old dependencies clear only after remote configuration readback confirms withdrawal. They contain no tokens or Secret data. Bounds are 256 application dependencies per tunnel, 64 hosts per dependency and 64 retained policy identities per dependency; ordinary application policyRefs retain their existing API limit.

Within one active manager process, selected application/policy changes share sorted, cancellable application locks with tunnel publication. A selected application cannot be removed while an earlier authorized config write is outstanding. Its deletion, stale-domain replacement, or selected policy update, service-token deletion, or service-token rotation waits for blocking tunnel configuration to be confirmed. Reconciliation releases locks while waiting so the tunnel worker can withdraw routes. Failed or cancelled writes keep pending receipts and block removal until a subsequent successful sync.

Deletion is intentionally conservative. An unannotated forwarding rule on the same whole-host scope, or a forwarding catch-all, can block removal of the application's protection; cfgate reports the dependency instead of changing those public routes. Resolve the overlap explicitly. After credential, account and ownership checks, confirmed remote tunnel deletion permits protection changes even when Cloudflare retains the deleted tunnel's old configuration. A present tunnel with status `down` is not considered deleted, and uncertain reads continue blocking changes. Failed credential/configuration reads retain the application finalizer. `cfgate.io/deletion-policy: orphan` leaves remote Access protection in place and removes the Kubernetes finalizer.

## Remaining asynchronous risks

The ordering guarantee covers the selected application and its selected policies in the active manager. Creation of a different overlapping Access application, external dashboard edits, manager restart/leader failover, uncertain server completion after a client timeout, and Cloudflare edge propagation can create windows. Remote shadow checks detect conflicting applications during subsequent publication/reconciliation; they cannot retroactively prevent a different controller or administrator from changing the account. No cross-cluster or edge-atomic guarantee is made. Origin authentication remains necessary for strict protection, and live release tests complement rather than eliminate these limits.

New coordination dependencies require an admitted route, a cfgate-managed Gateway,
and the applicable ReferenceGrants in both directions. Rejected references do not
consume another tunnel's application budget or delay application cleanup. Previously
published dependency receipts remain until remote withdrawal is confirmed.

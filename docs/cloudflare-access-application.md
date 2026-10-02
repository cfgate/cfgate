# CloudflareAccessApplication

`CloudflareAccessApplication` binds Gateway API targets to reusable `CloudflareAccessPolicy` resources. It creates one self-hosted Cloudflare Access Application per target hostname/path and links reusable policies by ID.

## Spec

```yaml
apiVersion: cfgate.io/v1alpha1
kind: CloudflareAccessApplication
metadata:
  name: admin-app
  namespace: default
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: HTTPRoute
    name: web
    sectionName: admin
  policyRefs:
    - name: allow-admins
      namespace: cfgate-system
      precedence: 1
  application:
    name: admin-app
    sessionDuration: 24h
```

Key fields:

| Field | Description |
|---|---|
| `targetRef` / `targetRefs` | Gateway API `Gateway` or `HTTPRoute` targets. Exactly one of these fields is allowed. |
| `cloudflareRef` | Optional credentials. If omitted, credentials are inherited from the target Gateway or from an HTTPRoute's cfgate Gateway parent (`cfgate.io/tunnel-ref`) through the CloudflareTunnel chain. With multiple targets, all targets must inherit the same Cloudflare account. Use explicit `spec.cloudflareRef` when binding targets that should share one account regardless of their Gateway/Tunnel chain. |
| `cloudflareRef.secretKeys.apiToken` | Secret data key containing the API token. Defaults to `CLOUDFLARE_API_TOKEN`. Inherited credentials preserve the tunnel's `spec.cloudflare.secretKeys.apiToken` selection. |
| `application` | Access Application settings shared by generated apps. `path` overrides derived target paths. |
| `policyRefs` | Required reusable policies to attach. Omit `precedence` on every ref to use list order starting at `1`, or set `precedence` on every ref for custom ordering. Do not mix modes; explicit precedence values must be unique. Duplicate namespace/name pairs in one list are invalid. |

The current CRD admits only `application.type: self_hosted`. That matches cfgate's Gateway/HTTPRoute model: targets resolve to Cloudflare Tunnel ingress rules backed by Kubernetes `Service` origins. Non-self-hosted Cloudflare Access app types are deferred because they require separate Cloudflare payloads and likely a different cfgate API shape:

- `saas` represents third-party SaaS SSO applications, not Kubernetes `Service` traffic.
- Browser SSH/VNC/RDP and browser isolation apps use protocol-specific Cloudflare request bodies instead of the current HTTP/HTTPS tunnel ingress shape.
- `bookmark` controls App Launcher visibility and does not protect routed traffic.

If an old CRD or stale manifest still lets another type reach the controller, cfgate rejects it with `ApplicationSynced=False`, reason `UnsupportedApplicationType`.

Cross-namespace target references require a `ReferenceGrant` in the target namespace. Cross-namespace policy references require a `ReferenceGrant` in the policy namespace.

## Path Rules

- HTTPRoute without `sectionName` protects hostname root `/`.
- HTTPRoute with `sectionName` matches `spec.rules[].name`.
- Supported path matches: omitted path, `PathPrefix`, `Exact`.
- `RegularExpression` is rejected with `TargetsResolved=False`, reason `UnsupportedPathMatch`.
- `spec.application.path` overrides any derived route path.
- Access application paths must start with `/` and must not include query strings or fragments. Path segments may contain `:`.
- Hostname source order for HTTPRoute: `cfgate.io/hostname`, then `spec.hostnames`.

## Status

| Field | Description |
|---|---|
| `applications[]` | Created Cloudflare app IDs, AUDs, domains, and target refs. |
| `accountId` | Resolved Cloudflare account ID cached for cleanup. |
| `credentialSecretRef` | Resolved credentials Secret cached for cleanup. Namespace is always stored explicitly. |
| `credentialSecretKeys` | Selected credential data keys cached for cleanup; `apiToken` is stored explicitly. |
| `attachedTargets` | Count of attached host/path targets. |
| `ancestors[]` | Target attachment status. |
| `observedGeneration` | Last reconciled generation. |

Automatic Cloudflare cleanup uses the cached `accountId`, `credentialSecretRef`, and `credentialSecretKeys` so target resources do not need to outlive the `CloudflareAccessApplication`. Cleanup deletes managed Access Applications and the per-resource owner tag; the shared `cfgate` tag is retained. The referenced Secret must still exist for cleanup; restore it or set `cfgate.io/deletion-policy=orphan` if credentials are intentionally removed first.

Conditions:

- `Ready`
- `CredentialsValid`
- `TargetsResolved`
- `ReferenceGrantValid`
- `PoliciesResolved`
- `ApplicationSynced`
- `PoliciesLinked`

## Example: Central Policies, Tenant Apps

```yaml
apiVersion: cfgate.io/v1alpha1
kind: CloudflareAccessPolicy
metadata:
  name: allow-admins
  namespace: cfgate-system
spec:
  cloudflareRef:
    name: cloudflare-credentials
    accountId: "<account-id>"
  name: allow-admins
  decision: allow
  include:
    - email:
        addresses: ["admin@example.com"]
---
apiVersion: cfgate.io/v1alpha1
kind: CloudflareAccessApplication
metadata:
  name: tenant-admin
  namespace: tenant-a
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: HTTPRoute
    name: web
    sectionName: admin
  application:
    name: tenant-admin
  policyRefs:
    - name: allow-admins
      namespace: cfgate-system
```

ReferenceGrant in `cfgate-system` allowing tenant apps to reference central policies:

```yaml
apiVersion: gateway.networking.k8s.io/v1beta1
kind: ReferenceGrant
metadata:
  name: allow-tenant-a-access-policy
  namespace: cfgate-system
spec:
  from:
    - group: cfgate.io
      kind: CloudflareAccessApplication
      namespace: tenant-a
  to:
    - group: cfgate.io
      kind: CloudflareAccessPolicy
```

Inherited credentials retain the Tunnel's Secret namespace, selected `secretKeys.apiToken`, and account. Cross-namespace inheritance requires explicit application-to-Tunnel and application-to-Secret grants, in addition to the owning Tunnel's Secret grant. Gateway-to-Tunnel references require their own grant. These checks apply during cleanup using persisted credential metadata. See [authorization and ownership](authorization-and-ownership.md).

## Required protection dependency

HTTPRoutes can opt into an explicit `cfgate.io/access-required: namespace/name` dependency. See [Access-required routing](access-required.md) for the supported subset, grants, remote checks, deletion ordering and asynchronous limitations. Existing routes remain public unless explicitly opted in.

Access applications use installation and CR identity in their owner tags. `status.pendingApplications`
records each successful target while `status.applications` retains the previous
completed set. Stale protection is deleted only after all replacements succeed;
recovery can restore missing
IDs after an interrupted status write. A recovered entry may omit `targetRef`
when the original target is no longer known. Deletion inventories owned remote
applications as well as recorded IDs. See the [alpha.7 migration notes](authorization-and-ownership.md#upgrade-from-v020-alpha6-to-v020-alpha7) before upgrading existing Access resources.

At most 64 application targets may resolve from one CR. Both the completed and
pending status lists are bounded to 64 entries. Remote recovery inventories are
kept separate from these checkpoints so older owned resources remain available
for cleanup without exceeding the status schema during replacement.

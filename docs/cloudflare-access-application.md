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
| `application` | Access Application settings shared by generated apps. `path` overrides derived target paths. |
| `policyRefs` | Required reusable policies to attach. Omit `precedence` on every ref to use list order starting at `1`, or set `precedence` on every ref for custom ordering. Do not mix modes; explicit precedence values must be unique. Duplicate namespace/name pairs in one list are invalid. |

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
| `attachedTargets` | Count of attached host/path targets. |
| `ancestors[]` | Target attachment status. |
| `observedGeneration` | Last reconciled generation. |

Automatic Cloudflare cleanup uses the cached `accountId` and `credentialSecretRef` so target resources do not need to outlive the `CloudflareAccessApplication`. Cleanup deletes managed Access Applications and the per-resource owner tag; the shared `cfgate` tag is retained. The referenced Secret must still exist for cleanup; restore it or set `cfgate.io/deletion-policy=orphan` if credentials are intentionally removed first.

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

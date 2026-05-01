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
| `cloudflareRef` | Optional credentials. If omitted, credentials are inherited from target route -> Gateway -> CloudflareTunnel. |
| `application` | Access Application settings shared by generated apps. `path` overrides derived target paths. |
| `policyRefs` | Required reusable policies to attach. Default precedence is list order starting at `1`. Duplicate name/namespace pairs are invalid. |

Cross-namespace target references require a `ReferenceGrant` in the target namespace. Cross-namespace policy references require a `ReferenceGrant` in the policy namespace.

## Path Rules

- HTTPRoute without `sectionName` protects hostname root `/`.
- HTTPRoute with `sectionName` matches `spec.rules[].name`.
- Supported path matches: omitted path, `PathPrefix`, `Exact`.
- `RegularExpression` is rejected with `TargetsResolved=False`, reason `UnsupportedPathMatch`.
- `spec.application.path` overrides any derived route path.
- Access application paths must not include ports, query strings, or fragments.
- Hostname source order for HTTPRoute: `cfgate.io/hostname`, then `spec.hostnames`.

## Status

| Field | Description |
|---|---|
| `applications[]` | Created Cloudflare app IDs, AUDs, domains, and target refs. |
| `attachedTargets` | Count of attached host/path targets. |
| `ancestors[]` | Target attachment status. |
| `observedGeneration` | Last reconciled generation. |

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

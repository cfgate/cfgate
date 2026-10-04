# CloudflareAccessApplication

`CloudflareAccessApplication` creates a self-hosted Cloudflare Access Application for each resolved Gateway API hostname/path and attaches reusable [CloudflareAccessPolicy](cloudflare-access-policy.md) resources.

The resource is namespaced and uses `cfgate.io/v1alpha1`. Install cfgate with [Getting started](getting-started.md); configure cross-namespace references with [authorization and ownership](authorization-and-ownership.md).

## Spec

| Field | Type | Default | Description |
|---|---|---|---|
| `targetRef` | object | *none* | One Gateway or HTTPRoute target. Specify this or `targetRefs`, not both. |
| `targetRefs` | list | *none* | Between 1 and 16 targets. |
| `targetRef.group` | string | `gateway.networking.k8s.io` | Only supported target API group; also applies to each `targetRefs` entry. |
| `targetRef.kind` | string | *none* | Required: `Gateway` or `HTTPRoute`. |
| `targetRef.name` | string | *none* | Required target name. |
| `targetRef.namespace` | string | Resource namespace | Target namespace; cross-namespace references require a grant. |
| `targetRef.sectionName` | string | *none* | Gateway listener name or HTTPRoute rule name. |
| `cloudflareRef` | object | Inherited | Explicit API-token Secret and account, using the [policy credential fields](cloudflare-access-policy.md#credential-data-keys). |
| `policyRefs` | list | *none* | Required list of 1 to 16 reusable policies. Namespace/name pairs must be unique. |
| `policyRefs[].name` | string | *none* | Required policy resource name. |
| `policyRefs[].namespace` | string | Resource namespace | Policy namespace; cross-namespace references require a grant. |
| `policyRefs[].precedence` | integer | List position, starting at `1` | Values from 1 to 9999; lower values run first. Either every entry omits precedence or every entry supplies a unique value. |
| `application` | object | *none* | Settings shared by the generated applications, below. |

Fields in the shared `application` block:

| Field | Type | Default | Description |
|---|---|---|---|
| `name` | string | Resource name | Display name; at most 255 characters. |
| `type` | string | `self_hosted` | Only supported application type. |
| `path` | string | Derived | Override target paths with an absolute path, at most 1024 characters, without `?` or `#`. |
| `sessionDuration` | string | `24h` | Session duration using units `ns`, `us`, `ms`, `s`, `m`, or `h`, such as `24h` or `2h30m`. |
| `logoUrl` | string | *none* | Dashboard logo URL, at most 1024 characters. |
| `skipInterstitial` | boolean | `false` | Skip the Access interstitial. |
| `enableBindingCookie` | boolean | `false` | Enable the binding cookie. |
| `httpOnlyCookieAttribute` | boolean | `true` | Set HttpOnly on the session cookie. |
| `sameSiteCookieAttribute` | string | `lax` | `strict`, `lax`, or `none`. |
| `pathCookieAttribute` | boolean | `false` | Scope the cookie to the application path. |
| `customDenyMessage` | string | *none* | Denial message, at most 1024 characters. |
| `customDenyUrl` | string | *none* | Identity-policy denial redirect. |
| `customNonIdentityDenyUrl` | string | *none* | Service-auth denial redirect, at most 1024 characters. |
| `serviceAuth401Redirect` | boolean | `false` | Request a 401 response for service-auth denial. |
| `allowedIdps` | string list | All configured IdPs | At most 25 identity-provider UUIDs. |
| `autoRedirectToIdentity` | boolean | `false` | Redirect directly when a single IdP is selected. |
| `appLauncherVisible` | boolean | `true` | Show the application in App Launcher. |
| `optionsPreflightBypass` | boolean | `false` | Allow OPTIONS requests to reach the origin without Access authentication; incompatible with `corsHeaders`. |
| `corsHeaders` | object | *none* | Configure Cloudflare responses to preflight requests. |
| `readServiceTokensFromHeader` | string | *none* | Custom header containing JSON keys `cf-access-client-id` and `cf-access-client-secret`; header name at most 256 characters. |

`corsHeaders` accepts `allowAllHeaders`, `allowAllMethods`, `allowAllOrigins`, and `allowCredentials` booleans, all defaulting to `false`. Lists `allowedHeaders` and `allowedOrigins` allow up to 50 entries each. `allowedMethods` allows up to 9 entries from `GET`, `POST`, `HEAD`, `PUT`, `DELETE`, `CONNECT`, `OPTIONS`, `TRACE`, and `PATCH`. Optional `maxAge` is an integer from 0 to 86400 seconds. Each `allowAll*` setting supersedes its corresponding list.

The schema also contains `application.domain`, but reconciliation derives domains from targets; use target hostnames and `application.path` to control the protected destination.

## Example: Central Policies, Tenant Apps

This example attaches a central policy to the tenant route's named `admin` rule. The route and its credential inheritance chain must already exist and have the required grants.

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

## Path Rules

An HTTPRoute without `sectionName` selects hostname root `/`. With `sectionName`, cfgate selects `spec.rules[].name` and derives paths from omitted, `PathPrefix`, or `Exact` matches. `RegularExpression` paths fail with `TargetsResolved=False`, reason `UnsupportedPathMatch`. An explicit `application.path` overrides derived paths.

HTTPRoute hostnames come from `cfgate.io/hostname` when set, otherwise `spec.hostnames`. Gateway targets derive hostnames from listeners; `sectionName` narrows the listener selection. Access paths must start with `/`; query strings and fragments are invalid, while `:` is allowed in path segments.

Only self-hosted applications are supported. A non-self-hosted type admitted by a stale CRD fails with `ApplicationSynced=False`, reason `UnsupportedApplicationType`.

## Credentials and references

Without `cloudflareRef`, credentials come from each target's cfgate Gateway and its `cfgate.io/tunnel-ref` chain. Multiple targets must inherit one Cloudflare account. Explicit credentials select the account independently of that inheritance chain.

Inheritance preserves the tunnel's Secret namespace, selected token key, and account. A missing or empty selected key is an error. Cross-namespace targets and policies require grants in their destination namespaces. Credential inheritance also requires application-to-Tunnel and application-to-Secret grants, in addition to the Gateway-to-Tunnel and Tunnel-to-Secret grants. These permissions are checked again during cleanup using persisted credential metadata. See [authorization and ownership](authorization-and-ownership.md).

## Status

| Field | Description |
|---|---|
| `applications[]` | Completed set of remote IDs, AUDs, protected domains, and target references. |
| `pendingApplications[]` | Successful replacements checkpointed while the prior completed set remains protected. |
| `ownerId` | Installation and resource identity used in remote owner tags. |
| `accountId` | Account cached for cleanup. |
| `credentialSecretRef` | Cached Secret reference with explicit namespace. |
| `credentialSecretKeys` | Cached selected token key. |
| `attachedTargets` | Number of attached host/path targets. |
| `ancestors[]` | Per-target attachment status. |
| `observedGeneration` | Last processed generation. |

Conditions are `Ready`, `CredentialsValid`, `TargetsResolved`, `ReferenceGrantValid`, `PoliciesResolved`, `ApplicationSynced`, and `PoliciesLinked`.

At most 64 host/path targets may resolve from one resource. Both application status lists are bounded to 64 entries. Recovery inventories remain separate so older owned applications can still be cleaned up without overflowing status.

## Deletion and recovery

Replacement retains previous protection until every replacement succeeds. After an interrupted status write, remote ownership evidence can recover missing IDs; recovered entries may omit `targetRef` if the original target is unknown.

Deletion inventories owned remote applications as well as recorded IDs. It removes the managed applications and per-resource owner tag, retaining the shared `cfgate` tag. Cached account and credential metadata allow cleanup after targets disappear, but the credentials Secret and required grants must remain available.

Restore missing credentials to complete deletion, or deliberately set `cfgate.io/deletion-policy: orphan` to retain remote applications and the owner tag. Recreated Kubernetes names do not recover the old owner identity. Review [ownership migration](authorization-and-ownership.md) before upgrading or transferring existing resources.

## Required protection dependency

An Access application does not by itself make route publication wait for protection. Add `cfgate.io/access-required: namespace/name` to the HTTPRoute to declare that dependency. [Access-required routing](access-required.md) describes supported targets, remote checks, deletion ordering, and asynchronous limitations.

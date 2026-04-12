# CloudflareAccessPolicy

Manages Cloudflare Access Applications and Policies for zero-trust access control.

**API Version:** `cfgate.io/v1alpha1`
**Kind:** `CloudflareAccessPolicy`
**Short Names:** `cfap`, `cfaccess`
**Scope:** Namespaced

## Overview

CloudflareAccessPolicy attaches to Gateway API resources (`Gateway`, `HTTPRoute`) using the `targetRefs` pattern and creates corresponding Cloudflare Access Applications. It manages application settings, access policy rules, service tokens for machine-to-machine auth, and mTLS certificate-based authentication.

Access rules are organized into implementation tiers based on identity provider (IdP) requirements. The controller extracts hostnames from the targeted Gateway API resources and creates Access Applications protecting those hostnames. Credentials can be provided explicitly via `cloudflareRef` or inherited through the Gateway's tunnel binding chain.

> Upgrade note: current target kinds are `Gateway` and `HTTPRoute`. Any existing policies targeting removed route kinds must be migrated before applying the current CRD.

## Spec Reference

| Field | Type | Default | Required | Description |
|-------|------|---------|----------|-------------|
| `spec.targetRef.group` | `string` | `gateway.networking.k8s.io` | Yes (if targetRef set) | API group of the target resource. Must be `gateway.networking.k8s.io`. |
| `spec.targetRef.kind` | `string` | *none* | Yes (if targetRef set) | Kind of the target. One of: `Gateway`, `HTTPRoute`. |
| `spec.targetRef.name` | `string` | *none* | Yes (if targetRef set) | Name of the target resource. 1-253 chars. |
| `spec.targetRef.namespace` | `*string` | *(policy namespace)* | No | Namespace of the target. Cross-namespace requires ReferenceGrant. |
| `spec.targetRef.sectionName` | `*string` | *none* | No | Targets a specific listener (Gateway) or rule (Route). |
| `spec.targetRefs[]` | `[]PolicyTargetReference` | *none* | No | Multiple targets for policy attachment. Max 16 items. Same fields as `targetRef`. |
| `spec.cloudflareRef.name` | `string` | *none* | Yes (if cloudflareRef set) | Name of the credentials Secret. Min 1 char. |
| `spec.cloudflareRef.namespace` | `*string` | *(policy namespace)* | No | Namespace of the credentials Secret. |
| `spec.cloudflareRef.accountId` | `string` | *none* | No | Cloudflare Account ID. |
| `spec.cloudflareRef.accountName` | `string` | *none* | No | Cloudflare Account name (resolved via API). |
| `spec.application.name` | `string` | *(CR name)* | No | Display name in the Cloudflare dashboard. Max 255 chars. |
| `spec.application.domain` | `string` | *(auto-generated from routes)* | No | Protected domain. Auto-generated from target routes if omitted. |
| `spec.application.path` | `string` | `/` | No | Path prefix to protect. |
| `spec.application.sessionDuration` | `string` | `24h` | No | Session cookie lifetime. Format: `^[0-9]+(h|m|s)$`. |
| `spec.application.type` | `string` | `self_hosted` | No | Application type. One of: `self_hosted`, `saas`, `ssh`, `vnc`, `browser_isolation`. |
| `spec.application.logoUrl` | `string` | *none* | No | Application logo URL in dashboard. |
| `spec.application.skipInterstitial` | `bool` | `false` | No | Bypass the Access login page for API requests. |
| `spec.application.enableBindingCookie` | `bool` | `false` | No | Enable binding cookies for sticky sessions. |
| `spec.application.httpOnlyCookieAttribute` | `bool` | `true` | No | Add HttpOnly attribute to session cookies. |
| `spec.application.sameSiteCookieAttribute` | `string` | `lax` | No | Cross-site cookie behavior. One of: `strict`, `lax`, `none`. |
| `spec.application.customDenyMessage` | `string` | *none* | No | Custom message shown when access is denied. Max 1024 chars. |
| `spec.application.customDenyUrl` | `string` | *none* | No | Redirect URL when access is denied (instead of message). |
| `spec.application.allowedIdps` | `[]string` | *(all account IdPs)* | No | Restrict which identity providers can authenticate. Values are Cloudflare IdP UUIDs. Max 25. |
| `spec.application.autoRedirectToIdentity` | `bool` | `false` | No | Auto-redirect to the IdP when a single IdP is in `allowedIdps`. Skips the IdP selection page. |
| `spec.application.appLauncherVisible` | `*bool` | `true` | No | Show application in the Cloudflare App Launcher. Use `false` to hide. |
| `spec.application.corsHeaders.allowAllHeaders` | `bool` | `false` | No | Allow all HTTP request headers for CORS. |
| `spec.application.corsHeaders.allowAllMethods` | `bool` | `false` | No | Allow all HTTP methods for CORS. |
| `spec.application.corsHeaders.allowAllOrigins` | `bool` | `false` | No | Allow all origins for CORS. |
| `spec.application.corsHeaders.allowCredentials` | `bool` | `false` | No | Include credentials with CORS requests. |
| `spec.application.corsHeaders.allowedHeaders` | `[]string` | *none* | No | Specific allowed headers (ignored when `allowAllHeaders` is true). Max 50. |
| `spec.application.corsHeaders.allowedMethods` | `[]CORSAllowedMethod` | *none* | No | Specific allowed methods. One of: `GET`, `POST`, `HEAD`, `PUT`, `DELETE`, `CONNECT`, `OPTIONS`, `TRACE`, `PATCH`. Max 9. Ignored when `allowAllMethods` is true. |
| `spec.application.corsHeaders.allowedOrigins` | `[]string` | *none* | No | Specific allowed origins (ignored when `allowAllOrigins` is true). Max 50. |
| `spec.application.corsHeaders.maxAge` | `*int` | *none* | No | Max seconds preflight results can be cached. 0-86400. |
| `spec.application.optionsPreflightBypass` | `bool` | `false` | No | Allow OPTIONS preflight to bypass Access and go directly to origin. Mutually exclusive with `corsHeaders`. |
| `spec.application.pathCookieAttribute` | `bool` | `false` | No | Scope Access JWT cookie to application path instead of hostname. |
| `spec.application.serviceAuth401Redirect` | `bool` | `false` | No | Return 401 instead of login redirect for Service Auth (non_identity) denials. Enable for API consumers. |
| `spec.application.customNonIdentityDenyUrl` | `string` | *none* | No | Redirect URL for non-identity (service auth) denials. Separate from `customDenyUrl`. Max 1024 chars. |
| `spec.application.readServiceTokensFromHeader` | `string` | *none* | No | Custom header name for service tokens instead of standard `CF-Access-Client-Id`/`CF-Access-Client-Secret` pair. Header value must be JSON with those keys. Max 256 chars. |
| `spec.policies[]` | `[]AccessPolicyRule` | *none* | No | Access rules evaluated in order. Max 50. See [Access Policy Rules](#specpolicies). |
| `spec.policies[].name` | `string` | *none* | Yes | Human-readable rule name. 1-255 chars. |
| `spec.policies[].decision` | `string` | `allow` | No | Policy action. One of: `allow`, `deny`, `bypass`, `non_identity`. |
| `spec.policies[].precedence` | `*int` | *none* | No | Evaluation order (lower = higher priority). 1-9999. |
| `spec.policies[].include[]` | `[]AccessRule` | *none* | No | Include conditions (ANY must match). Max 25. |
| `spec.policies[].exclude[]` | `[]AccessRule` | *none* | No | Exclude conditions (if ANY match, rule does not apply). Max 25. |
| `spec.policies[].require[]` | `[]AccessRule` | *none* | No | Require conditions (ALL must match). Max 25. |
| `spec.policies[].sessionDuration` | `string` | *none* | No | Override application session duration for this rule. |
| `spec.policies[].purposeJustificationRequired` | `bool` | `false` | No | Require user to provide access justification. |
| `spec.policies[].purposeJustificationPrompt` | `string` | *none* | No | Prompt shown when justification is required. |
| `spec.policies[].approvalRequired` | `bool` | `false` | No | Require approval from specified approvers. |
| `spec.policies[].approvalGroups[]` | `[]ApprovalGroup` | *none* | No | Approval groups for approval-required policies. Max 10. |
| `spec.policies[].approvalGroups[].emails` | `[]string` | *none* | No | Approver email addresses. Max 50. At least one of `emails` or `emailDomain` required. |
| `spec.policies[].approvalGroups[].emailDomain` | `string` | *none* | No | Allow any user from this domain to approve. Max 255 chars. |
| `spec.policies[].approvalGroups[].approvalsNeeded` | `int` | `1` | No | Number of approvals required from this group. Min 1. |
| `spec.groupRefs[]` | `[]AccessGroupRef` | *none* | No | Reference reusable Cloudflare Access Groups. Max 50. |
| `spec.groupRefs[].name` | `string` | *none* | No | Name of AccessGroup CR in same namespace (reserved for future CloudflareAccessGroup CRD). Max 253 chars. |
| `spec.groupRefs[].cloudflareId` | `string` | *none* | No | Cloudflare Access Group ID (bypasses CR lookup, works now). Max 36 chars. |
| `spec.serviceTokens[]` | `[]ServiceTokenConfig` | *none* | No | Service tokens for machine-to-machine auth. Max 10. |
| `spec.serviceTokens[].name` | `string` | *none* | Yes | Token display name. 1-255 chars. |
| `spec.serviceTokens[].duration` | `string` | `8760h` | No | Token validity period. Format: `^[0-9]+h$`. Only hours supported by Cloudflare API (e.g., `8760h` for 1 year). |
| `spec.serviceTokens[].secretRef.name` | `string` | *none* | Yes | Name of the Secret where generated credentials are stored. Min 1 char. |
| `spec.mtls.enabled` | `bool` | `false` | No | Activate mTLS requirement for the application. |
| `spec.mtls.rootCaSecretRef.name` | `string` | *none* | Yes (if rootCaSecretRef set) | Name of the Secret containing CA certificate(s). Min 1 char. |
| `spec.mtls.rootCaSecretRef.key` | `string` | `ca.crt` | No | Key within Secret for the CA certificate in PEM format. |
| `spec.mtls.associatedHostnames` | `[]string` | *none* | No | Limit mTLS requirement to specific hostnames. Max 25. |
| `spec.mtls.ruleName` | `string` | *(CR name)* | No | Name of the mTLS rule in Cloudflare. |

## Detailed Field Documentation

### `spec.targetRef` / `spec.targetRefs`

These are mutually exclusive. Exactly one must be specified.

Identifies which Gateway API resources to protect. The controller extracts hostnames from the targeted resources and creates Access Applications for those hostnames. Follows the [Gateway API](gateway-api-primer.md) policy attachment pattern (`LocalPolicyTargetReferenceWithSectionName`). Routes can also reference an access policy via the `cfgate.io/access-policy` annotation; see [Annotations Reference](annotations.md#cfgateio%2Faccess-policy).

**Cross-namespace references:** When `namespace` is specified and differs from the policy's namespace, a ReferenceGrant must exist in the target namespace permitting CloudflareAccessPolicy resources from the policy's namespace.

**`sectionName`:** When targeting a Gateway, `sectionName` selects a specific listener. When targeting a Route, it selects a specific rule. This narrows the hostnames protected by the policy.

```yaml
# Single target
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: HTTPRoute
    name: my-app

# Multiple targets
spec:
  targetRefs:
    - group: gateway.networking.k8s.io
      kind: HTTPRoute
      name: web-app
    - group: gateway.networking.k8s.io
      kind: Gateway
      name: main-gateway
      sectionName: https-listener
```

### `spec.cloudflareRef`

References Cloudflare credentials for Access API operations. When omitted, the controller attempts to inherit credentials through the target Gateway's tunnel binding.

**Credential resolution chain:**
1. Explicit `cloudflareRef` on the CloudflareAccessPolicy
2. Gateway target's tunnel chain (Gateway > tunnel binding > [CloudflareTunnel](cloudflare-tunnel.md) credentials)
3. HTTPRoute > parentRef > Gateway > tunnel chain

If no path resolves to valid credentials, the controller sets `CredentialsValid` condition to `False` with an error message.

```yaml
spec:
  cloudflareRef:
    name: cloudflare-api-token
    namespace: cfgate-system
    accountId: "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"
```

### `spec.application`

Configures the Cloudflare Access Application that protects the target hostnames. The application appears in the Cloudflare Zero Trust dashboard.

**Key fields:**

- **`type`:** Most common is `self_hosted` for web applications behind a tunnel. Use `ssh` for SSH proxying, `vnc` for VNC, `browser_isolation` for remote browser, `saas` for SaaS applications.
- **`sessionDuration`:** Controls how long a user remains authenticated. After this period, they must re-authenticate. Common values: `24h`, `12h`, `30m`.
- **`corsHeaders` vs `optionsPreflightBypass`:** Mutually exclusive. Use `corsHeaders` for fine-grained CORS control (Cloudflare handles preflight). Use `optionsPreflightBypass` to let OPTIONS requests pass through to the origin unmodified.
- **`serviceAuth401Redirect`:** Enable for API-facing applications so that blocked service token requests receive a 401 status code instead of an HTML login redirect.
- **`readServiceTokensFromHeader`:** When set, the controller reads service token credentials from a single custom header instead of the standard two-header pattern (`CF-Access-Client-Id` + `CF-Access-Client-Secret`). The custom header value must be a JSON object with `cf-access-client-id` and `cf-access-client-secret` keys.

```yaml
spec:
  application:
    name: "Production API"
    path: "/"
    sessionDuration: "12h"
    type: self_hosted
    skipInterstitial: true
    httpOnlyCookieAttribute: true
    sameSiteCookieAttribute: strict
    serviceAuth401Redirect: true
    appLauncherVisible: true
    corsHeaders:
      allowAllOrigins: false
      allowedOrigins:
        - "https://app.example.com"
      allowAllMethods: true
      allowCredentials: true
      maxAge: 3600
```

### `spec.policies`

Defines access policy rules evaluated in precedence order (lower precedence number = higher priority). Each rule contains:

- **`include`:** ANY of these conditions must match for the rule to apply (OR logic).
- **`require`:** ALL of these conditions must match (AND logic). Combined with include via AND.
- **`exclude`:** If ANY of these conditions match, the rule does not apply (override).

**Decisions:**
- `allow`: Grant access when conditions match.
- `deny`: Block access when conditions match.
- `bypass`: Skip all authentication for matching conditions.
- `non_identity`: Service-to-service auth (service tokens, mTLS) without user identity.

**Approval workflows:** Set `approvalRequired: true` and define `approvalGroups` to require manual approval before granting access. Approvers receive email notifications. `approvalsNeeded` on each group controls how many approvals are needed from that group.

```yaml
spec:
  policies:
    - name: allow-engineering
      decision: allow
      precedence: 1
      include:
        - emailDomain:
            domain: company.com
      require:
        - country:
            codes: ["US", "CA"]
      exclude:
        - ip:
            ranges: ["198.51.100.0/24"]

    - name: service-token-access
      decision: non_identity
      precedence: 2
      include:
        - serviceToken:
            tokenId: "abc123"

    - name: approval-required
      decision: allow
      precedence: 3
      approvalRequired: true
      approvalGroups:
        - emails: ["manager@company.com", "lead@company.com"]
          approvalsNeeded: 1
        - emailDomain: "security.company.com"
          approvalsNeeded: 1
      include:
        - email:
            addresses: ["contractor@external.com"]
```

### Access Rule Types

Access rules are organized into implementation tiers based on IdP requirements.

#### P0: No IdP Required

| Rule Type | Field | Sub-fields | Description |
|-----------|-------|------------|-------------|
| IP | `ip` | `ranges: []string` | Match source IP CIDR ranges (IPv4/IPv6). |
| IP List | `ipList` | `id: string`, `name: string` | Reference a managed Cloudflare IP List (by ID or name). |
| Country | `country` | `codes: []string` | Match ISO 3166-1 alpha-2 country codes. |
| Everyone | `everyone` | *(bool)* | Match all users. Use `everyone: true`. |
| Service Token | `serviceToken` | `tokenId: string` | Match a specific Cloudflare service token by ID. |
| Any Valid Service Token | `anyValidServiceToken` | *(bool)* | Match any valid service token. Use `anyValidServiceToken: true`. |

**`everyone` field:** This is a `*bool` (pointer to boolean). Use `everyone: true` in YAML. Do NOT use `everyone: {}`. While the Cloudflare API returns `{}` for the EveryoneRule, the cfgate CRD uses a boolean representation.

**`anyValidServiceToken` field:** Also a `*bool`. Use `anyValidServiceToken: true`.

```yaml
# P0 rule examples
include:
  - ip:
      ranges:
        - "10.0.0.0/8"
        - "2001:db8::/32"
  - ipList:
      name: "corporate-ips"
  - country:
      codes: ["US", "GB", "DE"]
  - everyone: true
  - serviceToken:
      tokenId: "token-uuid-here"
  - anyValidServiceToken: true
```

#### P1: Basic IdP Required

| Rule Type | Field | Sub-fields | Description |
|-----------|-------|------------|-------------|
| Email | `email` | `addresses: []string` | Match specific email addresses. |
| Email List | `emailList` | `id: string`, `name: string` | Reference a managed Cloudflare Access email list. |
| Email Domain | `emailDomain` | `domain: string` | Match email domain suffix (e.g., `example.com`). |
| OIDC Claim | `oidcClaim` | `identityProviderId: string`, `claimName: string`, `claimValue: string` | Match specific OIDC token claims. |

```yaml
# P1 rule examples
include:
  - email:
      addresses:
        - "admin@company.com"
        - "dev@company.com"
  - emailList:
      name: "engineering-team"
  - emailDomain:
      domain: "company.com"
  - oidcClaim:
      identityProviderId: "idp-uuid"
      claimName: "groups"
      claimValue: "engineering"
```

#### P2: Google Workspace Required

| Rule Type | Field | Sub-fields | Description |
|-----------|-------|------------|-------------|
| GSuite Group | `gsuiteGroup` | `identityProviderId: string`, `email: string` | Match Google Workspace group membership. |

```yaml
# P2 rule example
include:
  - gsuiteGroup:
      identityProviderId: "google-idp-uuid"
      email: "engineering@company.com"
```

#### P3: Not in current product scope

The following rule types are retained only for SDK round-trip and internal model compatibility. They are not part of the current cfgate product surface:

- **Certificate** (`CertificateRule`): mTLS client certificate validation
- **CommonName** (`AccessCommonNameRule`): mTLS common name matching
- **Group** (`GroupRule`): Cloudflare Access Groups (inline, not ref)
- **GitHub** (`GitHubOrganizationRule`): GitHub organization/team membership
- **Azure** (`AzureGroupRule`): Azure AD group membership
- **Okta** (`OktaGroupRule`): Okta group membership
- **SAML** (`SAMLGroupRule`): SAML attribute matching
- **AuthenticationMethod** (`AuthenticationMethodRule`): MFA enforcement
- **DevicePosture** (`AccessDevicePostureRule`): Device compliance checks
- **ExternalEvaluation** (`ExternalEvaluationRule`): External evaluation endpoint
- **LoginMethod** (`AccessLoginMethodRule`): Login method filtering

### `spec.groupRefs`

References reusable Cloudflare Access Groups. Access Groups are collections of identity rules that can be shared across multiple Access policies.

**`cloudflareId` (works now):** References a group already created in the Cloudflare dashboard by its Cloudflare-assigned ID. Use this for groups managed outside of Kubernetes.

**`name` (reserved for future use):** References a CloudflareAccessGroup CR in the same namespace. The CloudflareAccessGroup CRD does not exist yet; this field is reserved for a future release. The Go API client for Access Groups exists in the codebase, but no CRD or controller has been implemented.

At least one of `name` or `cloudflareId` must be specified per entry.

```yaml
spec:
  groupRefs:
    - cloudflareId: "group-uuid-from-cf-dashboard"
    - cloudflareId: "another-group-uuid"
```

### `spec.serviceTokens`

Configures Cloudflare Access service tokens for machine-to-machine authentication. The controller creates the service token in Cloudflare and stores the generated credentials in the referenced Kubernetes Secret.

The Secret will contain two keys:
- `CF_ACCESS_CLIENT_ID`: The client ID for the service token.
- `CF_ACCESS_CLIENT_SECRET`: The client secret (only visible at creation time).

**Duration:** Only hours are supported by the Cloudflare API. Common values: `8760h` (1 year), `4380h` (6 months), `720h` (30 days).

```yaml
spec:
  serviceTokens:
    - name: ci-cd-token
      duration: "8760h"
      secretRef:
        name: ci-cd-access-credentials
    - name: monitoring-token
      duration: "4380h"
      secretRef:
        name: monitoring-access-credentials
```

### `spec.mtls`

Configures mutual TLS (mTLS) certificate-based authentication. When enabled, clients must present a valid certificate signed by the configured CA to access the protected application. This provides strong authentication for service-to-service communication without requiring identity provider integration.

**`rootCaSecretRef`:** References a Kubernetes Secret containing the CA certificate chain in PEM format. The controller uploads this CA to Cloudflare Access.

**`associatedHostnames`:** Limits the mTLS requirement to specific hostnames. When empty, mTLS applies to all hostnames in the Access Application.

**`ruleName`:** The display name of the mTLS rule in Cloudflare. Defaults to the CR name.

```yaml
spec:
  mtls:
    enabled: true
    rootCaSecretRef:
      name: mtls-ca-cert
      key: ca.crt
    associatedHostnames:
      - "api.example.com"
      - "internal.example.com"
    ruleName: "production-mtls"
```

## Status

| Field | Type | Description |
|-------|------|-------------|
| `status.applicationId` | `string` | Cloudflare Access Application ID. |
| `status.applicationAud` | `string` | Application Audience (AUD) Tag. Used for JWT verification. |
| `status.attachedTargets` | `int32` | Count of successfully attached Gateway API targets. |
| `status.serviceTokenIds` | `map[string]string` | Maps service token names to their Cloudflare IDs. |
| `status.mtlsRuleId` | `string` | Cloudflare mTLS rule ID (when mTLS is configured). |
| `status.observedGeneration` | `int64` | Last `.metadata.generation` processed by the controller. |
| `status.conditions` | `[]metav1.Condition` | Standard Kubernetes conditions (see below). |
| `status.ancestors[]` | `[]PolicyAncestorStatus` | Per-target attachment status (Gateway API PolicyStatus pattern). |
| `status.ancestors[].ancestorRef` | `PolicyTargetReference` | The target reference this status entry corresponds to. |
| `status.ancestors[].controllerName` | `string` | Controller managing this attachment. |
| `status.ancestors[].conditions` | `[]metav1.Condition` | Conditions for this specific target. |

### Status Conditions

| Condition | Description |
|-----------|-------------|
| `Ready` | Policy is fully applied to all targets: credentials valid, targets resolved, application created, policies attached. |
| `CredentialsValid` | Cloudflare credentials have been validated against the Access API. |
| `TargetsResolved` | All `targetRef`/`targetRefs` have been found and validated in the cluster. |
| `ReferenceGrantValid` | Cross-namespace references are authorized by ReferenceGrant resources. |
| `ApplicationCreated` | Access Application exists in Cloudflare. |
| `PoliciesAttached` | Access policies have been attached to the application. |
| `ServiceTokensReady` | All configured service tokens have been created and credentials stored. |
| `MTLSConfigured` | mTLS rule is configured in Cloudflare (when `spec.mtls.enabled` is true). |

### kubectl Output Columns

| Column | JSONPath | Description |
|--------|----------|-------------|
| Ready | `.status.conditions[?(@.type=='Ready')].status` | Whether the policy is fully operational (`True`/`False`/`Unknown`). |
| Application | `.status.applicationId` | Cloudflare Access Application ID. |
| Targets | `.status.attachedTargets` | Number of attached Gateway API targets. |
| Age | `.metadata.creationTimestamp` | Age of the resource. |

## Usage Examples

### Basic HTTPRoute protection with email domain

```yaml
apiVersion: cfgate.io/v1alpha1
kind: CloudflareAccessPolicy
metadata:
  name: web-app-access
  namespace: apps
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: HTTPRoute
    name: web-app
  application:
    name: "Web Application"
    sessionDuration: "24h"
    type: self_hosted
  policies:
    - name: allow-company
      decision: allow
      precedence: 1
      include:
        - emailDomain:
            domain: company.com
```

### API with service token auth and IP restrictions

```yaml
apiVersion: cfgate.io/v1alpha1
kind: CloudflareAccessPolicy
metadata:
  name: api-access
  namespace: apps
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: HTTPRoute
    name: api-route
  cloudflareRef:
    name: cloudflare-api-token
    accountId: "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"
  application:
    name: "Production API"
    sessionDuration: "1h"
    type: self_hosted
    skipInterstitial: true
    serviceAuth401Redirect: true
    httpOnlyCookieAttribute: true
    sameSiteCookieAttribute: strict
  policies:
    - name: service-token-access
      decision: non_identity
      precedence: 1
      include:
        - anyValidServiceToken: true
      require:
        - ip:
            ranges: ["10.0.0.0/8"]
    - name: deny-all-others
      decision: deny
      precedence: 2
      include:
        - everyone: true
  serviceTokens:
    - name: ci-cd-pipeline
      duration: "8760h"
      secretRef:
        name: ci-cd-access-token
```

### Multi-target policy with mTLS, CORS, and approval workflows

```yaml
apiVersion: cfgate.io/v1alpha1
kind: CloudflareAccessPolicy
metadata:
  name: internal-services
  namespace: platform
spec:
  targetRefs:
    - group: gateway.networking.k8s.io
      kind: HTTPRoute
      name: admin-panel
    - group: gateway.networking.k8s.io
      kind: HTTPRoute
      name: internal-api
  cloudflareRef:
    name: cloudflare-api-token
    namespace: cfgate-system
    accountId: "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"
  application:
    name: "Internal Services"
    sessionDuration: "8h"
    type: self_hosted
    appLauncherVisible: true
    corsHeaders:
      allowedOrigins:
        - "https://admin.example.com"
      allowAllMethods: true
      allowCredentials: true
      maxAge: 3600
    allowedIdps:
      - "google-workspace-idp-uuid"
    autoRedirectToIdentity: true
  policies:
    - name: allow-engineering
      decision: allow
      precedence: 1
      include:
        - gsuiteGroup:
            identityProviderId: "google-idp-uuid"
            email: "engineering@company.com"
      require:
        - country:
            codes: ["US"]
    - name: external-contractor-approval
      decision: allow
      precedence: 2
      approvalRequired: true
      approvalGroups:
        - emails: ["security-lead@company.com"]
          approvalsNeeded: 1
      include:
        - email:
            addresses:
              - "contractor@partner.com"
    - name: machine-auth
      decision: non_identity
      precedence: 3
      include:
        - serviceToken:
            tokenId: "monitoring-token-id"
  groupRefs:
    - cloudflareId: "existing-access-group-uuid"
  serviceTokens:
    - name: monitoring-svc
      duration: "4380h"
      secretRef:
        name: monitoring-svc-token
  mtls:
    enabled: true
    rootCaSecretRef:
      name: internal-ca-cert
      key: ca.crt
    associatedHostnames:
      - "grpc.internal.example.com"
    ruleName: "internal-mtls"
```

## Deletion Behavior

The controller adds the finalizer `cfgate.io/access-policy-cleanup` to every CloudflareAccessPolicy resource. When the resource is deleted, the controller attempts to delete the Cloudflare Access application, revoke service tokens, and remove mTLS certificates before removing the finalizer.

If cleanup fails, the controller blocks indefinitely and requeues every 15 seconds. It never removes the finalizer automatically. Within a 1-minute retry budget, the controller emits Warning events with reason `CleanupFailed`. After the retry budget is exhausted, subsequent events escalate to reason `CleanupBlocked`.

To skip Cloudflare cleanup and remove the finalizer immediately, set the `cfgate.io/deletion-policy=orphan` annotation on the CloudflareAccessPolicy resource. The controller will leave Access resources in Cloudflare and remove the finalizer without attempting cleanup.

```bash
kubectl annotate cfap my-policy -n cfgate-system \
  cfgate.io/deletion-policy=orphan
```

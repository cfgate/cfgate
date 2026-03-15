# CloudflareDNS

Manages DNS record synchronization independently from CloudflareTunnel resources.

**API Version:** `cfgate.io/v1alpha1`
**Kind:** `CloudflareDNS`
**Short Names:** `cfdns`, `dns`
**Scope:** Namespaced

## Overview

CloudflareDNS manages DNS record synchronization for Cloudflare zones. It supports two target modes: tunnel references (for tunnel-based CNAME records) and external targets (for non-tunnel DNS records such as A, AAAA, or external CNAMEs). DNS records can be sourced automatically from Gateway API routes (HTTPRoute, GRPCRoute, etc.) or explicitly defined in the spec.

CloudflareDNS implements ownership tracking via TXT records, aligned with the external-dns pattern, to enable safe multi-cluster deployments and prevent accidental deletion of records created by other installations. Lifecycle behavior is controlled via `spec.policy` (sync, upsert-only, create-only) and `spec.cleanupPolicy`.

When using `tunnelRef`, credentials are inherited from the referenced [CloudflareTunnel](cloudflare-tunnel.md). When using `externalTarget`, the `cloudflare` field must be provided explicitly.

## Spec Reference

| Field | Type | Default | Required | Description |
|-------|------|---------|----------|-------------|
| `spec.tunnelRef.name` | `string` | *none* | Yes (if tunnelRef set) | Name of the CloudflareTunnel resource. 1-63 chars. |
| `spec.tunnelRef.namespace` | `string` | *(resource namespace)* | No | Namespace of the CloudflareTunnel. Max 63 chars. |
| `spec.externalTarget.type` | `RecordType` | *none* | Yes (if externalTarget set) | DNS record type: `CNAME`, `A`, or `AAAA`. |
| `spec.externalTarget.value` | `string` | *none* | Yes (if externalTarget set) | Target value: domain name for CNAME, IP address for A/AAAA. 1-255 chars. |
| `spec.zones[]` | `[]DNSZoneConfig` | *none* | Yes | DNS zones to manage. Min 1, max 10. |
| `spec.zones[].name` | `string` | *none* | Yes | Zone domain name (e.g., `example.com`). 1-255 chars. |
| `spec.zones[].id` | `string` | *none* | No | Explicit Cloudflare zone ID. When provided, skips API zone lookup. Max 32 chars. |
| `spec.zones[].proxied` | `*bool` | *(inherits from `spec.defaults.proxied`)* | No | Per-zone proxy override. `true` enables Cloudflare proxy (orange cloud), `false` DNS-only. `nil` inherits from `spec.defaults.proxied`. |
| `spec.policy` | `DNSPolicy` | `sync` | No | DNS record lifecycle policy. One of: `sync`, `upsert-only`, `create-only`. |
| `spec.source.gatewayRoutes.enabled` | `bool` | `true` | No | Enables automatic hostname discovery from Gateway API routes. |
| `spec.source.gatewayRoutes.annotationFilter` | `string` | *none* | No | Only sync routes matching this annotation (key=value format). Max 255 chars. |
| `spec.source.gatewayRoutes.namespaceSelector.matchLabels` | `map[string]string` | *none* | No | Select namespaces by label. Max 10 entries. At least one of `matchLabels` or `matchNames` required when `namespaceSelector` is set. |
| `spec.source.gatewayRoutes.namespaceSelector.matchNames` | `[]string` | *none* | No | Select namespaces by name. Max 50 items. At least one of `matchLabels` or `matchNames` required when `namespaceSelector` is set. |
| `spec.source.explicit[]` | `[]DNSExplicitHostname` | *none* | No | Explicitly defined hostnames to sync. Max 100 items. |
| `spec.source.explicit[].hostname` | `string` | *none* | Yes | DNS hostname to create. 1-255 chars. |
| `spec.source.explicit[].target` | `string` | *(tunnel domain when tunnelRef set)* | No | CNAME target. Supports `{{ .TunnelDomain }}` template variable. Max 255 chars. |
| `spec.source.explicit[].proxied` | `*bool` | *(inherits from zone or defaults)* | No | Per-hostname Cloudflare proxy setting. `nil` inherits from zone then defaults. |
| `spec.source.explicit[].ttl` | `int32` | `1` | No | DNS record TTL in seconds. `1` = auto (Cloudflare-managed, typically 300s). Explicit range: 60-86400. |
| `spec.defaults.proxied` | `bool` | `true` | No | Default Cloudflare proxy setting for all records. |
| `spec.defaults.ttl` | `int32` | `1` | No | Default DNS record TTL. `1` = auto. Explicit range: 60-86400. |
| `spec.ownership.ownerId` | `string` | *(namespace/name of the CloudflareDNS resource)* | No | Cluster/installation identifier for TXT ownership records. Max 253 chars. Pattern: `^[a-z0-9]([-a-z0-9]*[a-z0-9])?(/[a-z0-9]([-a-z0-9]*[a-z0-9])?)?$`. |
| `spec.ownership.txtRecord.enabled` | `*bool` | `true` (nil defaults to true) | No | Enables TXT record-based ownership tracking. |
| `spec.ownership.txtRecord.prefix` | `string` | `_cfgate` | No | Prefix for TXT record names. Max 63 chars. |
| `spec.ownership.comment.enabled` | `bool` | `false` | No | **Deprecated (alpha.13).** Ignored; the controller always writes a fixed comment. Will be removed in alpha.14. |
| `spec.ownership.comment.template` | `string` | `managed by cfgate` | No | **Deprecated (alpha.13).** Ignored; the controller always uses `"managed by cfgate"`. Will be removed in alpha.14. |
| `spec.cleanupPolicy.deleteOnRouteRemoval` | `*bool` | `true` (nil defaults to true) | No | Delete DNS records when the source route is deleted. |
| `spec.cleanupPolicy.deleteOnResourceRemoval` | `*bool` | `true` (nil defaults to true) | No | Delete DNS records when the CloudflareDNS resource itself is deleted (finalizer cleanup). |
| `spec.cleanupPolicy.onlyManaged` | `*bool` | `true` (nil defaults to true) | No | Only delete records that were created by cfgate, verified via ownership tracking. |
| `spec.cloudflare.accountId` | `string` | *none* | No | Cloudflare Account ID. Required when using `externalTarget`. Inherited from tunnel when using `tunnelRef`. Max 32 chars. |
| `spec.cloudflare.accountName` | `string` | *none* | No | Cloudflare Account name (resolved via API). Max 255 chars. |
| `spec.cloudflare.secretRef.name` | `string` | *none* | Yes (if cloudflare set) | Name of the credentials Secret. 1-253 chars. |
| `spec.cloudflare.secretRef.namespace` | `string` | *(resource namespace)* | No | Namespace of the credentials Secret. Max 63 chars. |
| `spec.cloudflare.secretKeys.apiToken` | `string` | `CLOUDFLARE_API_TOKEN` | No | Key name within the Secret for the API token. Max 253 chars. |
| `spec.fallbackCredentialsRef.name` | `string` | *none* | Yes (if fallbackCredentialsRef set) | Name of the fallback credentials Secret. 1-253 chars. |
| `spec.fallbackCredentialsRef.namespace` | `string` | *(resource namespace)* | No | Namespace of the fallback credentials Secret. Max 63 chars. |

## Detailed Field Documentation

### `spec.tunnelRef` / `spec.externalTarget`

These are mutually exclusive. Exactly one must be specified.

**`tunnelRef`:** References a [CloudflareTunnel](cloudflare-tunnel.md) resource. DNS CNAME records are created pointing to the tunnel's domain (`{tunnelId}.cfargotunnel.com`). The controller waits for the tunnel to become ready before creating DNS records. When using `tunnelRef`, Cloudflare API credentials are inherited from the tunnel, so no separate `spec.cloudflare` is needed.

**`externalTarget`:** Points DNS records to an external resource. Supports `CNAME` (external domain), `A` (IPv4 address), and `AAAA` (IPv6 address) record types. When using `externalTarget`, `spec.cloudflare` must be provided since there is no tunnel to inherit credentials from.

```yaml
# Tunnel-backed DNS
spec:
  tunnelRef:
    name: prod-tunnel

# External CNAME
spec:
  externalTarget:
    type: CNAME
    value: external-lb.example.com
  cloudflare:
    accountId: "a1b2c3d4..."
    secretRef:
      name: cloudflare-api-token

# External A record
spec:
  externalTarget:
    type: A
    value: "203.0.113.10"
  cloudflare:
    accountId: "a1b2c3d4..."
    secretRef:
      name: cloudflare-api-token
```

### `spec.zones`

Defines the Cloudflare DNS zones where records will be managed. At least one zone is required (max 10). The controller extracts the zone from each hostname using the [public suffix list](https://publicsuffix.org/), matches it against configured zones, and syncs records to the correct zone. Your API token's zone-level permissions determine which zones are accessible.

**`id` (optional):** When provided, the controller uses this zone ID directly and skips the API zone lookup. This avoids the extra API call and is useful when the token does not have zone-list permissions or when you want to pin a specific zone ID.

**`proxied` (optional):** Per-zone override for the Cloudflare proxy setting. When `nil`, inherits from `spec.defaults.proxied`. Set to `true` for orange-cloud (Cloudflare proxy), `false` for DNS-only (grey-cloud).

```yaml
spec:
  zones:
    - name: example.com
      id: "zone123abc"        # skip API lookup
      proxied: true           # force proxy on
    - name: internal.dev
      proxied: false          # DNS-only for this zone
```

### `spec.policy`

Controls the DNS record lifecycle policy. Aligned with external-dns patterns.

| Policy | Create | Update | Delete | Use Case |
|--------|--------|--------|--------|----------|
| `sync` (default) | Yes | Yes | Yes | Full lifecycle management. Records match desired state exactly. |
| `upsert-only` | Yes | Yes | No | Prevents accidental deletion. Records are created and updated but never removed. |
| `create-only` | Yes | No | No | Immutable records. Created once, never modified or deleted by the controller. |

```yaml
spec:
  policy: upsert-only
```

### `spec.source.gatewayRoutes`

Configures automatic hostname discovery from Gateway API routes (HTTPRoute, GRPCRoute, TCPRoute, UDPRoute).

**`annotationFilter`:** An opt-in filter that restricts which routes trigger DNS sync. The controller checks this annotation on route resources (HTTPRoute, GRPCRoute, etc.), never on Gateways. You can use any annotation key=value pair of your choosing. The format is `key=value`. See [Annotations Reference](annotations.md#notes-on-annotationfilter) for details on how annotation filtering works.

A common convention is `cfgate.io/dns-sync=enabled`, but this is not a controller-defined annotation; it is a user-chosen convention. The controller simply checks whether the route has the specified annotation with the specified value.

**`namespaceSelector`:** Limits route discovery to specific namespaces. Supports `matchLabels` (label selectors) and `matchNames` (explicit namespace names). At least one must be specified when `namespaceSelector` is set. This enables multi-tenant setups where different CloudflareDNS resources manage routes from different namespaces.

```yaml
spec:
  source:
    gatewayRoutes:
      enabled: true
      annotationFilter: "cfgate.io/dns-sync=enabled"
      namespaceSelector:
        matchLabels:
          environment: production
        matchNames:
          - app-team-a
          - app-team-b
```

### `spec.source.explicit`

Defines explicit hostnames to sync without depending on Gateway API route discovery. Both sources (gatewayRoutes and explicit) can be used together; explicit hostnames take precedence over route-discovered hostnames when there are conflicts.

The `target` field supports the `{{ .TunnelDomain }}` template variable, which resolves to the tunnel's CNAME target domain when `tunnelRef` is set. When `target` is omitted and `tunnelRef` is set, it defaults to the tunnel domain.

```yaml
spec:
  source:
    explicit:
      - hostname: app.example.com
        target: "{{ .TunnelDomain }}"
        proxied: true
        ttl: 1
      - hostname: api.example.com
        proxied: false
        ttl: 300
```

### `spec.defaults`

Fallback values for records that do not have explicit settings. Per-hostname and per-zone settings take precedence.

A TTL of `1` means "auto": Cloudflare manages the TTL (typically 300 seconds). Explicit TTL values must be between 60 and 86400 seconds.

```yaml
spec:
  defaults:
    proxied: true
    ttl: 1
```

### `spec.ownership`

Configures ownership tracking to identify which cfgate installation created each DNS record. This is critical for safe multi-cluster deployments.

**TXT record ownership (recommended):** Creates companion TXT records with the format:
```
heritage=cfgate,cfgate/owner=<owner-id>,cfgate/resource=cloudflaredns/<namespace>/<name>
```
This pattern is compatible with external-dns. The TXT record name is `{prefix}.{hostname}` (e.g., `_cfgate.app.example.com`).

**Comment ownership (cosmetic only):** The controller writes a fixed `"managed by cfgate"` comment on all managed DNS records. This is informational only and is not used for ownership verification or conflict detection. TXT record ownership is the sole mechanism for multi-cluster safety.

> **Deprecation notice (alpha.13):** The `spec.ownership.comment.enabled` and `spec.ownership.comment.template` fields are deprecated and ignored. The controller always writes `"managed by cfgate"` regardless of these values. Both fields will be removed in **v0.1.0-alpha.14**. To migrate, remove the `comment` section from `spec.ownership` in your CloudflareDNS resources. No behavioral change occurs; the hardcoded value matches the previous defaults.

**`ownerId`:** Identifies this installation. Defaults to `{namespace}/{name}` of the CloudflareDNS resource. Override this when you need explicit control over the identity (e.g., migrating between CloudflareDNS resources).

```yaml
spec:
  ownership:
    ownerId: "production/main-dns"
    txtRecord:
      enabled: true
      prefix: "_cfgate"
```

### `spec.cleanupPolicy`

Controls what happens to DNS records when they are no longer needed. All fields are pointer booleans (`*bool`); `nil` defaults to `true`.

| Field | Default | Description |
|-------|---------|-------------|
| `deleteOnRouteRemoval` | `true` | Delete the DNS record when the source Gateway API route is deleted. |
| `deleteOnResourceRemoval` | `true` | Delete all managed DNS records when the CloudflareDNS resource itself is deleted (finalizer-driven). |
| `onlyManaged` | `true` | Only delete records that were created by this cfgate installation, verified via ownership tracking. Protects records created externally or by other installations. |

```yaml
spec:
  cleanupPolicy:
    deleteOnRouteRemoval: true
    deleteOnResourceRemoval: true
    onlyManaged: true
```

### `spec.cloudflare`

Cloudflare API credentials. Required when using `externalTarget`. When using `tunnelRef`, credentials are inherited from the referenced CloudflareTunnel and this field can be omitted.

See [CloudflareTunnel `spec.cloudflare`](cloudflare-tunnel.md#speccloudflare) for full credential configuration details.

### `spec.fallbackCredentialsRef`

References a Secret containing fallback Cloudflare API credentials. Used during deletion when the primary credentials (either explicit or inherited from tunnel) are unavailable. This enables cleanup of DNS records even if the credentials Secret has been deleted.

```yaml
spec:
  fallbackCredentialsRef:
    name: cloudflare-admin-credentials
    namespace: cfgate-system
```

## Status

| Field | Type | Description |
|-------|------|-------------|
| `status.syncedRecords` | `int32` | Number of DNS records successfully synchronized. |
| `status.pendingRecords` | `int32` | Number of DNS records awaiting synchronization. |
| `status.failedRecords` | `int32` | Number of DNS records that failed to sync. |
| `status.records[]` | `[]DNSRecordSyncStatus` | Per-record sync status (see below). Max 1000 entries. |
| `status.records[].hostname` | `string` | DNS hostname of the record. |
| `status.records[].type` | `string` | Record type (CNAME, A, AAAA). |
| `status.records[].target` | `string` | Record target/content value. |
| `status.records[].proxied` | `bool` | Whether Cloudflare proxy is enabled for this record. |
| `status.records[].ttl` | `int32` | Record TTL in seconds. |
| `status.records[].status` | `string` | Sync status: `Synced`, `Pending`, or `Failed`. |
| `status.records[].recordId` | `string` | Cloudflare DNS record ID. |
| `status.records[].zoneId` | `string` | Cloudflare zone ID where the record was created. |
| `status.records[].error` | `string` | Error message when status is `Failed`. |
| `status.resolvedTarget` | `string` | Resolved CNAME target (tunnel domain or external target value). |
| `status.observedGeneration` | `int64` | Last `.metadata.generation` observed by the controller. |
| `status.lastSyncTime` | `metav1.Time` | Last time DNS records were synced to Cloudflare. |
| `status.conditions` | `[]metav1.Condition` | Standard Kubernetes conditions (see below). |

### Status Conditions

| Condition | Description |
|-----------|-------------|
| `Ready` | DNS sync is fully operational: credentials valid, zones resolved, records synced, ownership verified. Target resolution failures are surfaced through this condition with reason `TargetResolutionFailed`. |
| `CredentialsValid` | Cloudflare API credentials have been validated. |
| `ZonesResolved` | All configured zones have been resolved via the Cloudflare API (or verified by explicit ID). |
| `RecordsSynced` | DNS records have been synchronized to Cloudflare. |
| `OwnershipVerified` | TXT ownership records have been verified for all managed DNS records. |

### kubectl Output Columns

| Column | JSONPath | Description |
|--------|----------|-------------|
| Ready | `.status.conditions[?(@.type=='Ready')].status` | Whether DNS sync is operational (`True`/`False`/`Unknown`). |
| Synced | `.status.syncedRecords` | Number of successfully synced records. |
| Pending | `.status.pendingRecords` | Number of records awaiting sync. |
| Failed | `.status.failedRecords` | Number of records that failed to sync. |
| Age | `.metadata.creationTimestamp` | Age of the resource. |

## Usage Examples

### Tunnel-backed DNS with Gateway API route discovery

```yaml
apiVersion: cfgate.io/v1alpha1
kind: CloudflareDNS
metadata:
  name: prod-dns
  namespace: cfgate-system
spec:
  tunnelRef:
    name: prod-tunnel
  zones:
    - name: example.com
    - name: example.org
  source:
    gatewayRoutes:
      enabled: true
      annotationFilter: "cfgate.io/dns-sync=enabled"
  defaults:
    proxied: true
    ttl: 1
  policy: sync
  ownership:
    txtRecord:
      enabled: true
      prefix: "_cfgate"
```

### External target with explicit hostnames

```yaml
apiVersion: cfgate.io/v1alpha1
kind: CloudflareDNS
metadata:
  name: external-dns
  namespace: cfgate-system
spec:
  externalTarget:
    type: A
    value: "203.0.113.10"
  cloudflare:
    accountId: "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"
    secretRef:
      name: cloudflare-api-token
  zones:
    - name: example.com
      id: "zone123abc"
  source:
    explicit:
      - hostname: api.example.com
        proxied: false
        ttl: 300
      - hostname: www.example.com
        proxied: true
        ttl: 1
  policy: upsert-only
  cleanupPolicy:
    deleteOnRouteRemoval: false
    deleteOnResourceRemoval: false
    onlyManaged: true
```

### Multi-tenant namespace-scoped route discovery

```yaml
apiVersion: cfgate.io/v1alpha1
kind: CloudflareDNS
metadata:
  name: team-a-dns
  namespace: cfgate-system
spec:
  tunnelRef:
    name: prod-tunnel
  zones:
    - name: example.com
      proxied: true
  source:
    gatewayRoutes:
      enabled: true
      annotationFilter: "cfgate.io/dns-sync=enabled"
      namespaceSelector:
        matchLabels:
          team: team-a
        matchNames:
          - team-a-apps
          - team-a-staging
  defaults:
    proxied: true
    ttl: 1
  policy: sync
  ownership:
    ownerId: "cluster-west/team-a-dns"
    txtRecord:
      enabled: true
  cleanupPolicy:
    deleteOnRouteRemoval: true
    deleteOnResourceRemoval: true
    onlyManaged: true
  fallbackCredentialsRef:
    name: cloudflare-admin-credentials
    namespace: cfgate-system
```

## Deletion Behavior

The controller adds the finalizer `cfgate.io/dns-cleanup` to every CloudflareDNS resource. When the resource is deleted, the controller attempts to delete all owned DNS records and their TXT ownership records before removing the finalizer.

If cleanup fails, the controller blocks indefinitely and requeues every 15 seconds. It never removes the finalizer automatically. Within a 1-minute retry budget, the controller emits Warning events with reason `CleanupFailed`. After the retry budget is exhausted, subsequent events escalate to reason `CleanupBlocked`.

To skip Cloudflare cleanup and remove the finalizer immediately, set the `cfgate.io/deletion-policy=orphan` annotation on the CloudflareDNS resource. The controller will leave DNS records in Cloudflare and remove the finalizer without attempting cleanup. This annotation is new in v0.1.0-alpha.14; previous releases supported it only on CloudflareTunnel and CloudflareAccessPolicy.

```bash
kubectl annotate cloudflarednses my-dns -n cfgate-system \
  cfgate.io/deletion-policy=orphan
```

## Record Types

When using `tunnelRef`, all DNS records are CNAME records pointing to `{tunnelId}.cfargotunnel.com`. The record type is not configurable in tunnel-ref mode.

When using `externalTarget`, the `type` field determines the DNS record type. Valid values are `CNAME`, `A`, and `AAAA`. The `value` field contains the record target: a domain name for CNAME, an IPv4 address for A, or an IPv6 address for AAAA. The controller does not validate that A or AAAA target values are valid IP addresses; values are passed through to the Cloudflare API.

Only A, AAAA, and CNAME records can be proxied by Cloudflare. Setting `proxied: true` on other record types will cause the Cloudflare API to reject the request.

## Multi-level Subdomains

Cloudflare Universal SSL certificates cover `*.example.com` but not deeper wildcards such as `*.sub.example.com`. Hostnames with more than one subdomain level relative to the zone (for example, `api.staging.example.com` in zone `example.com`) require Cloudflare Advanced Certificate Manager or a custom certificate uploaded to Cloudflare.

The controller emits a `DeepSubdomain` warning event when it encounters a hostname with depth greater than 1 relative to the zone. This warning is informational only; record creation proceeds regardless.

To suppress the warning, set the annotation `cfgate.io/allow-deep-subdomains: "true"` on the CloudflareDNS resource.

```bash
kubectl annotate cloudflarednses my-dns -n cfgate-system \
  cfgate.io/allow-deep-subdomains=true
```

## Namespace Selector

When `spec.source.gatewayRoutes.namespaceSelector` is set, only routes from matching namespaces are considered for DNS record creation. The selector supports two filters: `matchLabels` and `matchNames`.

`matchLabels` uses AND semantics: all specified labels must be present on the namespace. `matchNames` matches namespaces by name. If both filters are specified, the result is a union (a namespace matching either filter is included).

An empty selector (`namespaceSelector: {}`) matches all namespaces, following the Kubernetes convention used by NetworkPolicy and other resources.

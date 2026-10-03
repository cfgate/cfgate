# CloudflareDNS

Manages DNS record synchronization independently from CloudflareTunnel resources.

**API Version:** `cfgate.io/v1alpha1`
**Kind:** `CloudflareDNS`
**Short Names:** `cfdns`, `dns`
**Scope:** Namespaced

## Overview

CloudflareDNS manages DNS record synchronization for Cloudflare zones. It supports two target modes: tunnel references (for tunnel-based CNAME records) and external targets (for non-tunnel DNS records such as A, AAAA, or external CNAMEs). DNS records can be sourced automatically from Gateway API HTTPRoute resources or explicitly defined in the spec.

CloudflareDNS verifies resource-specific ownership markers on data and TXT records before mutation. Cloudflare DNS writes do not support compare-and-swap, so competing installations still require external coordination. Lifecycle behavior is controlled via `spec.policy` (sync, upsert-only, create-only) and `spec.cleanupPolicy`.

When using `tunnelRef`, credentials are inherited from the referenced [CloudflareTunnel](cloudflare-tunnel.md). When using `externalTarget`, the `cloudflare` field must be provided explicitly.

## Spec Reference

| Field | Type | Default | Required | Description |
|-------|------|---------|----------|-------------|
| `spec.tunnelRef.name` | `string` | *none* | Yes (if tunnelRef set) | Name of the CloudflareTunnel resource. 1-63 chars. |
| `spec.tunnelRef.namespace` | `string` | *(resource namespace)* | No | Namespace of the CloudflareTunnel. Max 63 chars. |
| `spec.externalTarget.type` | `RecordType` | *none* | Yes (if externalTarget set) | DNS record type: `CNAME`, `A`, or `AAAA`. |
| `spec.externalTarget.value` | `string` | *none* | Yes (if externalTarget set) | Target value: domain name for CNAME, IP address for A/AAAA. 1-253 chars. |
| `spec.zones[]` | `[]DNSZoneConfig` | *none* | Yes | DNS zones to manage. Min 1, max 10. |
| `spec.zones[].name` | `string` | *none* | Yes | Zone domain name (e.g., `example.com`). 1-253 chars. |
| `spec.zones[].id` | `string` | *none* | No | Explicit Cloudflare zone ID. When provided, skips API zone lookup. Max 32 chars. |
| `spec.zones[].proxied` | `*bool` | *(inherits from `spec.defaults.proxied`)* | No | Per-zone proxy override. `true` enables Cloudflare proxy (orange cloud), `false` DNS-only. `nil` inherits from `spec.defaults.proxied`. |
| `spec.policy` | `DNSPolicy` | `sync` | No | DNS record lifecycle policy. One of: `sync`, `upsert-only`, `create-only`. |
| `spec.source.gatewayRoutes` | `DNSGatewayRoutesSource` | *none* | No | Enables route discovery when the block is present. Explicit-only resources omit this block and do not watch routes. |
| `spec.source.gatewayRoutes.enabled` | `bool` | `true` | No | Enables automatic hostname discovery from Gateway API routes. |
| `spec.source.gatewayRoutes.annotationFilter` | `string` | *none* | No | Only sync routes matching this annotation (key=value format). Max 255 chars. |
| `spec.source.gatewayRoutes.namespaceSelector.matchLabels` | `map[string]string` | *none* | No | Select namespaces by label. Max 10 entries. At least one of `matchLabels` or `matchNames` required when `namespaceSelector` is set. |
| `spec.source.gatewayRoutes.namespaceSelector.matchNames` | `[]string` | *none* | No | Select namespaces by name. Max 50 items. At least one of `matchLabels` or `matchNames` required when `namespaceSelector` is set. |
| `spec.source.explicit[]` | `[]DNSExplicitHostname` | *none* | No | Explicitly defined hostnames to sync. Max 100 items. |
| `spec.source.explicit[].hostname` | `string` | *none* | Yes | DNS hostname to create. 1-253 chars. |
| `spec.source.explicit[].target` | `string` | *(resource-level resolved target)* | No | Per-hostname target override. Supports `{{ .TunnelDomain }}` when using `tunnelRef`. Max 253 chars. |
| `spec.source.explicit[].proxied` | `*bool` | *(inherits from zone or defaults)* | No | Per-hostname Cloudflare proxy setting. `nil` inherits from zone then defaults. |
| `spec.source.explicit[].ttl` | `int32` | `1` | No | DNS record TTL in seconds. `1` = auto (Cloudflare-managed, typically 300s). Explicit range: 60-86400. |
| `spec.defaults.proxied` | `bool` | `true` | No | Default Cloudflare proxy setting for all records. |
| `spec.defaults.ttl` | `int32` | `1` | No | Default DNS record TTL. `1` = auto. Explicit range: 60-86400. |
| `spec.ownership.ownerId` | `string` | *none* | No | Deprecated legacy hint; cannot override `status.ownerId` or authorize adoption. Retained in the schema for compatibility. |
| `spec.ownership.txtRecord.enabled` | `*bool` | `true` (nil defaults to true) | No | Enables TXT record-based ownership tracking. |
| `spec.ownership.txtRecord.prefix` | `string` | `_cfgate` | No | Immutable prefix for TXT record names. Max 63 chars. |
| `spec.ownership.comment.enabled` | `bool` | `false` | No | **Deprecated since `v0.1.0-alpha.13`.** Ignored; the controller writes an exact owner marker. Schema removal is deferred to a future cleanup. |
| `spec.ownership.comment.template` | `string` | `managed by cfgate` | No | **Deprecated since `v0.1.0-alpha.13`.** Ignored; the controller writes `cfgate/owner=<owner-id>`. Schema removal is deferred to a future cleanup. |
| `spec.cleanupPolicy.deleteOnRouteRemoval` | `*bool` | `true` (nil defaults to true) | No | Delete DNS records when the source route is deleted. |
| `spec.cleanupPolicy.deleteOnResourceRemoval` | `*bool` | `true` (nil defaults to true) | No | Delete DNS records when the CloudflareDNS resource itself is deleted (finalizer cleanup). |
| `spec.cleanupPolicy.onlyManaged` | `*bool` | `true` (nil defaults to true) | No | Retained for compatibility; ownership checks always apply, including when false. |
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

Defines the Cloudflare DNS zones where records are managed. Configure 1 to 10 zones. Each hostname uses the most specific configured zone, matching complete DNS labels and ignoring case and a trailing dot. For example, `api.team.example.com` uses `team.example.com` when both that zone and `example.com` are configured. An apex hostname matches its own zone; `badexample.com` does not match `example.com`.

Configure the zones that Cloudflare actually manages, including any separately delegated child zones. cfgate does not infer zone boundaries from registrable domains. Your API token must permit access to the selected zone. See [Cloudflare subdomain setup](https://developers.cloudflare.com/dns/zone-setups/subdomain-setup/) for provider requirements.

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

Automatic DNS publication requires a cfgate-managed Gateway, permission for its tunnel reference, an admitted parent/listener attachment, and an intersecting hostname. Namespace and annotation selectors further restrict discovery; they do not grant publication authority. Backend availability is separate: an admitted route may retain DNS while its backend returns an error. Explicit hostnames remain administrator-managed.

Configures automatic hostname discovery from Gateway API HTTPRoute resources. Route discovery is enabled by the presence of this block. If `source.gatewayRoutes` is absent, the resource is explicit-only and does not watch routes. If the block is present and `enabled` is omitted, it defaults to `true`.

**`annotationFilter`:** An opt-in filter that restricts which routes trigger DNS sync. The controller checks this annotation on HTTPRoute resources, never on Gateways. You can use any annotation key=value pair; adding, changing, or removing it triggers discovery and cleanup without waiting for the periodic reconciliation. The format is `key=value`. See [Annotations Reference](annotations.md#notes-on-annotationfilter) for details on how annotation filtering works.

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

Defines explicit hostnames independently of Gateway API route discovery. Hostnames are case-insensitive and an optional trailing dot is ignored before merging or recording write intents. An explicit entry overrides the discovered settings for the same hostname. Equivalent explicit entries are deduplicated; conflicting settings within the same source are rejected before publication.

The `target` field overrides the resource-level resolved target for that hostname. It supports the `{{ .TunnelDomain }}` template variable, which resolves to the tunnel's CNAME target domain when `tunnelRef` is set. When `target` is omitted, the resource-level resolved target is used.

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

Mixed sources remain additive. In the example below, route discovery can still add other hostnames, but the explicit `app.example.com` entry wins if a route also advertises that hostname:

```yaml
spec:
  source:
    gatewayRoutes:
      enabled: true
    explicit:
      - hostname: app.example.com
        target: app-origin.example.net
        proxied: false
        ttl: 300
```

### `spec.defaults`

Fallback values for records that do not have explicit settings. Per-hostname and per-zone settings take precedence.

A TTL of `1` means Auto. DNS-only records accept explicit TTL values from 60 to
86400 seconds. [Proxied records always use Auto](https://developers.cloudflare.com/dns/manage-dns-records/reference/ttl/),
currently 300 seconds. cfgate sends the API value `1` whenever the effective record
is proxied, after applying defaults and overrides. The configured TTL stays in
your resource and takes effect if proxying is disabled.

```yaml
spec:
  defaults:
    proxied: true
    ttl: 1
```

### `spec.ownership`

Ownership uses `status.ownerId = <installation namespace UID>/<CloudflareDNS UID>`, persisted before external writes. The manager obtains its installation namespace from `POD_NAMESPACE` or `--installation-namespace`; out-of-cluster development must supply that flag. Renames/recreations cannot reuse an old resource identity. Deleting and recreating the installation namespace changes its identity and requires an explicit migration.

Data comments contain the exact marker `cfgate/owner=<owner-id>`. With the persisted namespace UID/resource UID identity, this is 86 characters and fits the 100-character DNS comment limit. Existing exact `heritage=cfgate,cfgate/owner=<owner-id>` data comments remain recognized for owned updates and cleanup. Companion TXT content retains `heritage=cfgate,cfgate/owner=<owner-id>` and also includes `cfgate/resource=CloudflareDNS/<namespace>/<name>`; the default name is `_cfgate.<hostname>`. The default is to create and verify both markers. Disabling TXT creation does not disable data ownership checks or permit existing foreign TXT claims.

`spec.ownership.ownerId` is a deprecated legacy hint; it no longer overrides resource identity. The deprecated comment configuration is also ignored. A cosmetic `managed by cfgate` comment is not ownership evidence. Foreign or ambiguous TXT records, foreign data markers, and unmarked existing records block synchronization. `cfgate.io/adopt-existing: "true"` permits explicitly inspected, unmarked legacy data only; it never overwrites a foreign owner.

Fresh reads detect observable competing claims before writes and deletes. Cloudflare provides no conditional DNS mutation here; a read is not a distributed lock. Coordinate writers across clusters and installations. See [authorization and ownership migration](authorization-and-ownership.md) before upgrading legacy resources.

```yaml
spec:
  ownership:
    txtRecord:
      enabled: true
      prefix: "_cfgate"
```

### `spec.cleanupPolicy`

Controls what happens to DNS records when they are no longer needed. All fields are pointer booleans (`*bool`); `nil` defaults to `true`.

| Field | Default | Description |
|-------|---------|-------------|
| `deleteOnRouteRemoval` | `true` | Delete obsolete records when their hostname, type, or selected zone leaves the desired configuration. Applies to discovered and explicit hostnames. |
| `deleteOnResourceRemoval` | `true` | Delete all managed DNS records when the CloudflareDNS resource itself is deleted (finalizer-driven). |
| `onlyManaged` | `true` | Compatibility field; false does not bypass exact ownership verification. |

With `policy: sync` and route-removal cleanup enabled, cfgate deletes the old record and ownership claim before publishing a replacement in another zone or with another type. This can briefly interrupt DNS availability. Failed cleanup retains the recorded identity in `status.records`, reports a failure, and retries before further publication. A failed record update also retains the previous ID for cleanup.

Deletion uses the recorded `zoneId`, even if that zone is no longer configured. Legacy status without a zone ID falls back to the most specific configured zone and still checks the recorded ID and ownership. If no zone matches, cleanup stops with an error; restore the original zone configuration or explicitly choose orphan deletion after reviewing the remote records.

Disabling route-removal cleanup, or using a policy that prevents deletion, retains obsolete record identities for later cleanup. The status inventory is limited to 1,000 entries; cfgate rejects additions that would exceed this limit before creating remote records. Resource deletion still follows `deleteOnResourceRemoval` and the configured policy.

Use both cleanup settings to remove obsolete records and clean up on resource deletion:

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
| `status.ownerId` | `string` | Persisted installation namespace UID/resource UID; used for cleanup. |
| `status.failedRecords` | `int32` | Number of DNS records that failed to sync. |
| `status.records[]` | `[]DNSRecordSyncStatus` | Record inventory, including retained records and failed cleanup obligations. Max 1000 entries. |
| `status.records[].hostname` | `string` | DNS hostname of the record. |
| `status.records[].type` | `string` | Record type (CNAME, A, AAAA). |
| `status.records[].target` | `string` | Record target/content value. |
| `status.records[].proxied` | `bool` | Whether Cloudflare proxy is enabled for this record. |
| `status.records[].ttl` | `int32` | Record TTL in seconds. |
| `status.records[].status` | `string` | Sync status: `Synced`, `Pending`, `Skipped`, or `Failed`. |
| `status.records[].recordId` | `string` | Cloudflare DNS record ID. |
| `status.records[].zoneId` | `string` | Cloudflare zone ID where the record was created. |
| `status.records[].error` | `string` | Synchronization or cleanup error when status is `Failed`. |
| `status.resolvedTarget` | `string` | Resolved CNAME target (tunnel domain or external target value). |
| `status.observedGeneration` | `int64` | Last `.metadata.generation` observed by the controller. |
| `status.lastSyncTime` | `metav1.Time` | Last time DNS records were synced to Cloudflare. |
| `status.conditions` | `[]metav1.Condition` | Standard Kubernetes conditions (see below). |

### Status Conditions

| Condition | Description |
|-----------|-------------|
| `Ready` | DNS sync is operational: credentials valid, zones resolved, and records synced. Target resolution failures are surfaced through this condition with reason `TargetResolutionFailed`. |
| `CredentialsValid` | Cloudflare API credentials have been validated. |
| `ZonesResolved` | All configured zones have been resolved via the Cloudflare API (or verified by explicit ID). |
| `RecordsSynced` | DNS records have been synchronized to Cloudflare. |
| `OwnershipVerified` | TXT ownership records have been verified for all managed DNS records. This condition is diagnostic and does not gate `Ready`. |

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

Deletion also inventories zones from the specification and recorded status to
recover remote writes that succeeded before status was saved. Exact-owned data
records for unrecorded hostnames are deleted before their companion TXT claims,
including when older status records already exist. Data recovery also runs when
TXT creation is disabled. Disabling TXT creation does not orphan existing owned
claims: both recorded and recovered claims remain subject to normal cleanup.
Every recovery-zone inventory must succeed before
unrecorded records are deleted; a failed data deletion or a changed record
identity retains the claims and finalizer for a fresh attempt. Recorded hostnames
keep their status-backed record-ID checks. Foreign, ambiguous, and unmarked
records remain protected by the ownership checks.

If cleanup fails, the controller blocks indefinitely and requeues every 15 seconds. It never removes the finalizer automatically. Before deletion has been pending for 1 minute, the controller emits Warning events with reason `CleanupFailed`. After that warning threshold, failed attempts emit `CleanupBlocked`. This threshold does not bound API calls or stop retries. Cleanup can finish on a later retry or after credentials, permissions, or connectivity are repaired.

To skip Cloudflare cleanup and remove the finalizer immediately, set the `cfgate.io/deletion-policy=orphan` annotation on the CloudflareDNS resource. The controller will leave DNS records in Cloudflare and remove the finalizer without attempting cleanup.

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

### Interrupted writes and ownership changes

Before writing DNS, cfgate records the resolved destination and a write identifier in
`status.pendingWrites`. New data records carry that identifier alongside their owner
marker. If Cloudflare accepts a write but Kubernetes cannot save its result, the next
reconciliation recovers the record from this intent. Later zone or hostname edits do
not erase the pending destination.

A different record ID is recovered only when the saved intent and ownership evidence
identify it. An unexplained replacement or foreign ownership claim blocks cleanup
and keeps the finalizer. Inspect the reported conflict before deciding whether to
restore the owned record or use the documented orphan deletion policy. Do not remove
pending status to bypass recovery.

`spec.ownership.txtRecord.prefix` is immutable. To change it, delete the DNS resource,
wait for its configured cleanup to finish, then recreate it with the new prefix.
Disabling `txtRecord.enabled` stops creating companion claims; existing claims remain
protected and are removed during normal hostname or resource cleanup. Re-enabling it
uses the same prefix. The controller also retains the established prefix in status,
so a schema mismatch cannot silently redirect cleanup.

For resources created before this change, the first reconciliation records their
current prefix. cfgate cannot reconstruct prefixes changed before that checkpoint.
Inspect any older ownership claims during such a migration. Install the matching CRD
before updating the controller so Kubernetes preserves the recovery fields.

### Request budget

The 1,000-entry inventory ceiling bounds stored cleanup state. It is not a tested
operating capacity. Cloudflare's standard limit is
[1,200 API requests per five minutes](https://developers.cloudflare.com/fundamentals/api/reference/limits/),
shared with other calls using the applicable identity.

The controller's synthetic CNAME tests count about 12 provider operations per new
hostname, 6 per unchanged hostname, and 8 per hostname during cleanup, plus zone
inventory reads. Each hostname includes its TXT claim. These counts exclude SDK
retries, additional pagination, zone resolution, and other controllers. Consequently,
1,000 unchanged hostnames already exceed the standard window. Smaller resources
still share the same quota; splitting them does not increase it.

Quota tests cover two resources with 10, 50, or 100 hostnames each, interrupted
publication, and cleanup across renewed shared windows. They establish recovery in
that model, not a production capacity recommendation. Measure request volume and
reconciliation latency for your account before increasing scale. Large inventories
need additional batching and shared-budget scheduling; retries alone do not guarantee
progress when preliminary reads consume the entire window. Fresh ownership checks
remain required before writes.

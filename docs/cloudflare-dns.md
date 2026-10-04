# CloudflareDNS

`CloudflareDNS` publishes DNS records from explicit hostnames or admitted HTTPRoutes and tracks the records it owns for updates and cleanup.

This namespaced `cfgate.io/v1alpha1` resource has the short names `cfdns` and `dns`. Choose exactly one target: `tunnelRef` for tunnel CNAMEs or `externalTarget` for A, AAAA, or external CNAME records. [Getting started](getting-started.md) covers installation and tunnel-backed routing.

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
| `spec.source.gatewayRoutes.annotationFilter` | `string` | *none* | No | Only sync routes matching this annotation (`key` or `key=value`). Max 255 chars. |
| `spec.source.gatewayRoutes.namespaceSelector.matchLabels` | `map[string]string` | *none* | No | Select namespaces by label. Max 10 entries. At least one of `matchLabels` or `matchNames` required when `namespaceSelector` is set. |
| `spec.source.gatewayRoutes.namespaceSelector.matchNames` | `[]string` | *none* | No | Select namespaces by name. Max 50 items. At least one of `matchLabels` or `matchNames` required when `namespaceSelector` is set. |
| `spec.source.explicit[]` | `[]DNSExplicitHostname` | *none* | No | Explicitly defined hostnames to sync. Max 100 items. |
| `spec.source.explicit[].hostname` | `string` | *none* | Yes | DNS hostname to create. 1-253 chars. |
| `spec.source.explicit[].target` | `string` | *(resource-level resolved target)* | No | Per-hostname target override. Supports `{{ .TunnelDomain }}` when using `tunnelRef`. Max 253 chars. |
| `spec.source.explicit[].proxied` | `*bool` | *(inherits from zone or defaults)* | No | Per-hostname Cloudflare proxy setting. `nil` inherits from zone then defaults. |
| `spec.source.explicit[].ttl` | `int32` | inherited | No | DNS record TTL in seconds. `1` = auto (Cloudflare-managed, typically 300s). Explicit range: 60-86400. |
| `spec.defaults.proxied` | `bool` | `true` | No | Default Cloudflare proxy setting for all records. |
| `spec.defaults.ttl` | `int32` | `1` | No | Default DNS record TTL. `1` = auto. Explicit range: 60-86400. |
| `spec.ownership.ownerId` | `string` | *none* | No | Deprecated legacy hint; cannot override `status.ownerId` or authorize adoption. Retained in the schema for compatibility. |
| `spec.ownership.txtRecord.enabled` | `*bool` | `true` (nil defaults to true) | No | Enables TXT record-based ownership tracking. |
| `spec.ownership.txtRecord.prefix` | `string` | `_cfgate` | No | Immutable prefix for TXT record names. Max 63 chars. |
| `spec.ownership.comment.enabled` | `bool` | `false` | No | **Deprecated since `v0.1.0-alpha.13`.** Ignored; the controller writes an exact owner marker. Schema removal is deferred to a future cleanup. |
| `spec.ownership.comment.template` | `string` | `managed by cfgate` | No | **Deprecated since `v0.1.0-alpha.13`.** Ignored; the controller writes `cfgate/owner=<owner-id>`. Schema removal is deferred to a future cleanup. |
| `spec.cleanupPolicy.deleteOnRouteRemoval` | `*bool` | `true` (nil defaults to true) | No | Delete obsolete discovered or explicit records when policy permits. |
| `spec.cleanupPolicy.deleteOnResourceRemoval` | `*bool` | `true` (nil defaults to true) | No | Delete DNS records when the CloudflareDNS resource itself is deleted (finalizer cleanup). |
| `spec.cleanupPolicy.onlyManaged` | `*bool` | `true` (nil defaults to true) | No | Retained for compatibility; ownership checks always apply, including when false. |
| `spec.cloudflare.accountId` | `string` | *none* | No | Cloudflare Account ID. Use this or `accountName` with `externalTarget`; inherited with `tunnelRef`. Max 32 chars. |
| `spec.cloudflare.accountName` | `string` | *none* | No | Cloudflare Account name (resolved via API). Max 255 chars. |
| `spec.cloudflare.secretRef.name` | `string` | *none* | Yes (if cloudflare set) | Name of the credentials Secret. 1-253 chars. |
| `spec.cloudflare.secretRef.namespace` | `string` | *(resource namespace)* | No | Namespace of the credentials Secret. Max 63 chars. |
| `spec.cloudflare.secretKeys.apiToken` | `string` | `CLOUDFLARE_API_TOKEN` | No | Key name within the Secret for the API token. Max 253 chars. |
| `spec.fallbackCredentialsRef.name` | `string` | *none* | Yes (if fallbackCredentialsRef set) | Name of the fallback credentials Secret. 1-253 chars. |
| `spec.fallbackCredentialsRef.namespace` | `string` | *(resource namespace)* | No | Namespace of the fallback credentials Secret. Max 63 chars. |

## Usage Examples

### Tunnel-backed DNS with Gateway API route discovery

Publish hostnames from admitted routes bearing the selected annotation:

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

Manage an external IP independently of a tunnel, retaining records on deletion:

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
      id: "0123456789abcdef0123456789abcdef"
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

## Detailed Field Documentation

### `spec.tunnelRef` / `spec.externalTarget`

`tunnelRef` inherits the referenced tunnel's credentials and waits for readiness before creating CNAMEs to `{tunnelId}.cfargotunnel.com`. Explicit hostname entries can override the target, but all records remain CNAMEs.

`externalTarget` requires `spec.cloudflare` credentials and determines the record type for every hostname. Use explicit hostnames in this mode: Gateway route discovery requires a tunnel reference and has no effect with an external target. A and AAAA values are sent to Cloudflare without local IP-address validation.

### `spec.zones`

Each hostname uses the most specific configured zone. Matching ignores case and a trailing dot and requires whole DNS labels: `api.team.example.com` selects `team.example.com` over `example.com`, while `badexample.com` does not match either. An apex hostname matches its own zone.

Configure actual Cloudflare zones, including separately delegated children; cfgate does not infer zone boundaries. An explicit zone `id` skips name lookup. The token must have access to the selected zone.

### `spec.policy`

The lifecycle policy limits operations even when cleanup is enabled:

| Policy | Create | Update | Delete |
|---|---|---|---|
| `sync` | Yes | Yes | Yes |
| `upsert-only` | Yes | Yes | No |
| `create-only` | Yes | No | No |

### `spec.source.gatewayRoutes`

Omitting this block disables route discovery and its route watches. A present block defaults `enabled` to `true`. Discovery requires a cfgate-managed Gateway, an authorized tunnel reference, an admitted listener attachment, intersecting hostnames, and supported route features and effective origin transport. Backend readiness is separate: an admitted route can retain DNS while its backend returns an error.

`annotationFilter` accepts either `key` for annotation presence or `key=value` for exact equality. It checks HTTPRoutes, not Gateways. Annotation changes trigger discovery and cleanup. `cfgate.io/dns-sync=enabled` is an example convention, not a built-in annotation.

Namespace and annotation filters restrict discovery; they do not grant authority to publish. Explicit entries are administrator-managed and independent of route admission.

### `spec.source.explicit`

Explicit entries add hostnames and override discovered settings for the same hostname. Hostnames are normalized for case and a trailing dot before merging. Equivalent entries within a source are deduplicated after default inheritance and proxied-TTL normalization; conflicting effective targets, proxy values, or TTLs block publication. A default change can make previously equivalent entries conflict.

Omitted `target` uses the resource-level destination. With `tunnelRef`, the template `{{ .TunnelDomain }}` resolves to the tunnel domain. This fragment combines discovery with an explicit override:

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

Proxy settings resolve from the explicit hostname or route annotation, then the selected zone, then `defaults.proxied`. TTL resolves from the hostname or route annotation, then `defaults.ttl`. Omitted TTL inherits; explicit `1` selects Auto.

cfgate always sends TTL `1` for proxied records. A configured explicit TTL remains in the resource and takes effect if proxying is disabled. DNS-only CRD TTL values must be `1` or between 60 and 86400 seconds.

### `spec.ownership`

Before external writes, cfgate persists `status.ownerId` as `<installation namespace UID>/<CloudflareDNS UID>`. Recreating either namespace or resource changes identity. The manager gets its installation namespace from `POD_NAMESPACE` or `--installation-namespace`.

Data records carry `cfgate/owner=<owner-id>`. Exact legacy `heritage=cfgate,cfgate/owner=<owner-id>` comments are also recognized. Companion TXT records contain that legacy-form owner marker plus `cfgate/resource=CloudflareDNS/<namespace>/<name>`; their default name is `_cfgate.<hostname>`.

Both markers are created and checked by default. Disabling TXT creation does not disable data ownership checks or permit foreign TXT claims. Deprecated `ownership.ownerId` and comment fields cannot change identity or authorize adoption. An unmarked existing record blocks synchronization unless an administrator explicitly adopts inspected legacy data with `cfgate.io/adopt-existing: "true"`. Foreign and ambiguous claims always block mutation.

Cloudflare DNS writes have no compare-and-swap guard here. Fresh reads detect observable conflicts but are not a distributed lock; coordinate installations sharing an account. See [authorization and ownership](authorization-and-ownership.md).

### `spec.cleanupPolicy`

With `policy: sync` and `deleteOnRouteRemoval: true`, a removed hostname or a change of record type or selected zone deletes the old record and claim before publishing a replacement. This can briefly interrupt availability. Failed cleanup retains the identity in `status.records` and retries before further publication; failed updates also retain the old record ID.

Deletion uses the recorded `zoneId`, even if removed from the specification. Legacy status without an ID falls back to the most specific configured zone and still verifies record ID and ownership. If no zone matches, restore the original zone or inspect the remote records before choosing orphan deletion.

When policy or cleanup settings prevent deletion, obsolete identities remain available for later cleanup. The 1,000-entry status inventory limit is checked before creating additional remote records. `onlyManaged: false` never bypasses ownership checks. Resource deletion also respects `deleteOnResourceRemoval` and the lifecycle policy.

### `spec.cloudflare`

External targets need a credentials Secret and account ID or name. Tunnel targets inherit credentials from the referenced tunnel. [CloudflareTunnel credentials](cloudflare-tunnel.md#speccloudflare) describes account resolution and selected Secret keys; DNS writes additionally require DNS Edit permission for each zone.

### `spec.fallbackCredentialsRef`

During deletion, cfgate can use a fallback API-token Secret if primary or inherited credentials are unavailable. Preserve the selected token key and required account/zone permissions. Cross-namespace references require the grants described in [authorization and ownership](authorization-and-ownership.md).

## Status

| Field | Type | Description |
|-------|------|-------------|
| `status.syncedRecords` | `int32` | Number of DNS records successfully synchronized. |
| `status.pendingRecords` | `int32` | Number of DNS records awaiting synchronization. |
| `status.ownerId` | `string` | Persisted installation namespace UID/resource UID; used for cleanup. |
| `status.ownershipPrefix` | `string` | Established TXT prefix retained for publication and cleanup. |
| `status.pendingWrites[]` | list | Saved write destinations and identifiers awaiting durable results. |
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
| `status.resolvedTarget` | `string` | Resolved tunnel domain or external target value. |
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
| `OwnershipVerified` | TXT ownership records have been verified for managed records. This check gates `Ready` when TXT ownership is enabled; disabled TXT creation reports this condition false without preventing readiness. |

### kubectl Output Columns

| Column | JSONPath | Description |
|--------|----------|-------------|
| Ready | `.status.conditions[?(@.type=='Ready')].status` | Whether DNS sync is operational (`True`/`False`/`Unknown`). |
| Synced | `.status.syncedRecords` | Number of successfully synced records. |
| Pending | `.status.pendingRecords` | Number of records awaiting sync. |
| Failed | `.status.failedRecords` | Number of records that failed to sync. |
| Age | `.metadata.creationTimestamp` | Age of the resource. |

## Deletion Behavior

The `cfgate.io/dns-cleanup` finalizer waits for permitted cleanup of owned data records and TXT claims. Recovery inventories zones from both specification and status to find writes that succeeded before their result was saved. Exact-owned unrecorded data is deleted before its claims, including when TXT creation is disabled.

All recovery-zone inventories must succeed before unrecorded deletions begin. A failed deletion or changed identity retains claims and the finalizer for another attempt. Recorded hostnames retain their status-backed record-ID checks; foreign, ambiguous, and unmarked records remain protected.

Failures retry every 15 seconds. Warning events change from `CleanupFailed` to `CleanupBlocked` after deletion has been pending for one minute. That threshold does not limit API calls or stop retries; repair credentials, permissions, or connectivity to resume cleanup.

To retain remote records and remove the finalizer without cleanup, use:

```bash
kubectl annotate cloudflarednses my-dns -n cfgate-system \
  cfgate.io/deletion-policy=orphan
```

## Record Types

Tunnel references always produce CNAME records. External targets accept only `CNAME`, `A`, and `AAAA`; per-hostname overrides change the destination, not the record type.

## Multi-level Subdomains

cfgate emits an informational `DeepSubdomain` warning for hostnames more than one label below their selected zone. For example, `api.staging.example.com` is two levels below `example.com`. Record publication continues; the warning does not verify certificate coverage.

After arranging appropriate Cloudflare edge certificate coverage, suppress the warning on the DNS resource with `cfgate.io/allow-deep-subdomains: "true"`. See [Cloudflare certificate coverage](https://developers.cloudflare.com/ssl/edge-certificates/universal-ssl/limitations/).

## Namespace Selector

`matchLabels` requires all listed labels, including explicitly empty values. `matchNames` selects namespace names. When both are provided, matching either includes the namespace. Omit `namespaceSelector` to discover across namespaces; the CRD rejects `namespaceSelector: {}` because at least one selector field must be present.

### Interrupted writes and ownership changes

cfgate saves each destination and write identifier in `status.pendingWrites` before writing DNS. New data includes the write identifier with its owner marker. If the remote write succeeds but status cannot be saved, reconciliation recovers it from this intent, even after zone or hostname edits.

A changed record ID requires matching saved intent and ownership evidence. Unexplained replacements block cleanup and retain the finalizer. Inspect the conflict rather than removing pending status to bypass recovery.

`ownership.txtRecord.prefix` is immutable. To change it, delete the resource, wait for configured cleanup, then recreate it. Disabling TXT creation retains existing claims for normal cleanup; re-enabling it uses the same prefix. Status also records the established prefix so a schema mismatch cannot redirect cleanup.

For older resources, the first reconciliation checkpoints the current prefix and cannot reconstruct earlier changes. Inspect older claims during migration and install the matching CRD before upgrading the controller so recovery fields are preserved.

### Request budget

The 1,000-entry inventory ceiling bounds cleanup state, not tested operating capacity. Synthetic CNAME tests count approximately 12 provider operations per new hostname, 6 per unchanged hostname, and 8 per cleanup, including TXT claims and excluding zone inventory, SDK retries, extra pages, and other controllers.

The quota tests cover two resources with 10, 50, or 100 hostnames each, interrupted publication, and cleanup across renewed shared windows. They establish recovery in that model, not a production capacity recommendation. Measure account request volume and reconciliation latency against [Cloudflare's API limits](https://developers.cloudflare.com/fundamentals/api/reference/limits/). Splitting resources does not create more shared quota. Large inventories need batching and shared-budget scheduling; retries may make no progress if preliminary reads consume the window.

See [annotations](annotations.md) for route-level DNS overrides and [troubleshooting](troubleshooting.md) for reconciliation failures.

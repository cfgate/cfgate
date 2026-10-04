# CloudflareTunnel

`CloudflareTunnel` manages a Cloudflare Tunnel, its connector credentials, and the cloudflared Deployment that connects the cluster to Cloudflare.

The namespaced resource uses `cfgate.io/v1alpha1` and the short names `cft` and `cftunnel`. A tunnel can serve hostnames in multiple zones. [CloudflareDNS](cloudflare-dns.md) manages their DNS records separately; [Getting started](getting-started.md) covers installation and a complete route.

## Spec Reference

| Field | Type | Default | Required | Description |
|-------|------|---------|----------|-------------|
| `spec.tunnel.name` | `string` | *none* | Yes | Tunnel name in Cloudflare. Creates if absent; existing tunnels require an ownership claim or explicit adoption. Must be 1-63 chars, lowercase alphanumeric with hyphens, matching `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`. |
| `spec.cloudflare.accountId` | `string` | *none* | No | Cloudflare Account ID. Max 32 chars. Either `accountId` or `accountName` must be specified. |
| `spec.cloudflare.accountName` | `string` | *none* | No | Cloudflare Account name. Resolved via API lookup (requires Account Settings Read permission). Max 255 chars. Either `accountId` or `accountName` must be specified. |
| `spec.cloudflare.secretRef.name` | `string` | *none* | Yes | Name of the Secret containing the Cloudflare API token. 1-253 chars. |
| `spec.cloudflare.secretRef.namespace` | `string` | *(resource namespace)* | No | Namespace of the credentials Secret. Defaults to the tunnel's namespace. Max 63 chars. |
| `spec.cloudflare.secretKeys.apiToken` | `string` | `CLOUDFLARE_API_TOKEN` | No | Key name within the Secret for the Cloudflare API token. Max 253 chars. |
| `spec.cloudflared.replicas` | `int32` | `2` | No | Number of cloudflared replicas. Min 1, max 10. Each replica connects independently. |
| `spec.cloudflared.image` | `string` | `ghcr.io/inherent-design/cloudflared:2026.9.3-h2c.1@sha256:6c46ca006f9d6af5e973e59f2d71f5d6d3dc138c8a5484380d5797092171e3ad` | No | Container image for the cloudflared daemon. See [Image](#image) below. Max 255 chars. |
| `spec.cloudflared.imagePullPolicy` | `string` | `IfNotPresent` | No | Image pull policy. One of: `Always`, `Never`, `IfNotPresent`. |
| `spec.cloudflared.protocol` | `string` | `auto` | No | Tunnel transport protocol. One of: `auto`, `quic`, `http2`. |
| `spec.cloudflared.resources` | `corev1.ResourceRequirements` | *none* | No | Resource requests and limits for cloudflared containers. Standard Kubernetes resource spec. |
| `spec.cloudflared.nodeSelector` | `map[string]string` | *none* | No | Node selector for cloudflared pod scheduling. Max 50 entries. |
| `spec.cloudflared.tolerations` | `[]corev1.Toleration` | *none* | No | Tolerations for cloudflared pods. Max 20 items. |
| `spec.cloudflared.podAnnotations` | `map[string]string` | *none* | No | Annotations added to cloudflared pods. Max 50 entries. |
| `spec.cloudflared.extraArgs` | `[]string` | *none* | No | Additional CLI arguments passed to cloudflared. Max 20 items. |
| `spec.cloudflared.metrics.enabled` | `bool` | `true` | No | Declares the metrics container port for scraping; the shared health listener remains enabled. |
| `spec.cloudflared.metrics.port` | `int32` | `44483` | No | Port for the metrics endpoint. Min 1, max 65535. The pod listener serves both `/metrics` and health probes. |
| `spec.originDefaults.connectTimeout` | `string` | `30s` | No | Timeout for connecting to origin/backend services. Format: `^[0-9]+(s|m|h)$`. |
| `spec.originDefaults.noTLSVerify` | `bool` | `false` | No | Disables TLS certificate verification for origin connections. Prefer a trusted CA bundle. |
| `spec.originDefaults.http2Origin` | `bool` | `false` | No | Enables HTTP/2 for connections to origin services. |
| `spec.originDefaults.h2cOrigin` | `bool` | `false` | No | Enables HTTP/2 cleartext (h2c) for origin connections. Use for origins that speak HTTP/2 without TLS. Mutually exclusive with `http2Origin`. |
| `spec.originDefaults.caPoolSecretRef.name` | `string` | *none* | Yes (if caPoolSecretRef set) | Name of the Secret containing CA certificates for origin TLS verification. 1-253 chars. |
| `spec.originDefaults.caPoolSecretRef.key` | `string` | `ca.crt` | No | Key within the Secret containing the CA certificate chain in PEM format. Max 253 chars. |
| `spec.fallbackTarget` | `string` | `http_status:404` | No | Default service for requests that do not match any ingress rule. |
| `spec.fallbackCredentialsRef.name` | `string` | *none* | Yes (if fallbackCredentialsRef set) | Name of the Secret containing fallback Cloudflare API credentials. 1-253 chars. |
| `spec.fallbackCredentialsRef.namespace` | `string` | *(resource namespace)* | No | Namespace of the fallback credentials Secret. Max 63 chars. |

## Usage Examples

### Minimal tunnel with account ID

Use an existing API-token Secret in the tunnel's namespace:

```yaml
apiVersion: cfgate.io/v1alpha1
kind: CloudflareTunnel
metadata:
  name: prod-tunnel
  namespace: cfgate-system
spec:
  tunnel:
    name: prod-cluster
  cloudflare:
    accountId: "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"
    secretRef:
      name: cloudflare-api-token
```

### Connector scheduling and monitoring

This fragment configures three connectors and exposes their metrics for scraping:

```yaml
spec:
  cloudflared:
    replicas: 3
    imagePullPolicy: IfNotPresent
    protocol: quic
    resources:
      requests:
        cpu: 100m
        memory: 128Mi
      limits:
        cpu: 500m
        memory: 256Mi
    nodeSelector:
      node-role.kubernetes.io/edge: ""
    tolerations:
      - key: node-role.kubernetes.io/edge
        effect: NoSchedule
    podAnnotations:
      prometheus.io/scrape: "true"
      prometheus.io/port: "44483"
    extraArgs:
      - "--loglevel"
      - "debug"
    metrics:
      enabled: true
      port: 44483
```

## Detailed Field Documentation

### `spec.tunnel`

The controller creates a missing tunnel and records its ID in `status.tunnelId`. Existing tunnels require a matching ownership claim. For an inspected, unclaimed legacy tunnel, `cfgate.io/adopt-existing: "true"` permits adoption; a foreign claim always blocks it. Multiple resources cannot share a claimed tunnel identity within one installation.

### `spec.cloudflare`

Provide an API-token Secret and either `accountId` or `accountName`. An ID avoids account lookup; a name requires Account Settings Read permission. Tunnel operations require Cloudflare Tunnel Edit permission. The selected Secret data key defaults to `CLOUDFLARE_API_TOKEN`; a missing or empty selected key is an error.

Cross-namespace Secret references and Gateway-to-Tunnel references require ReferenceGrants. See [authorization and ownership](authorization-and-ownership.md) for grants, installation identity, and migration procedures.

### `spec.cloudflared`

`protocol` selects connector-to-edge transport: `auto` lets cloudflared select it, `quic` uses UDP, and `http2` provides an option where UDP is blocked. This setting is separate from origin transport.

The generated Pods run as non-root with runtime-default seccomp, no privilege escalation, all Linux capabilities dropped, and service-account-token mounting disabled. See [connector hardening](connector-hardening.md) for network isolation.

The metrics and health listener uses port `44483` by default and binds to Pod interfaces for kubelet probes. `metrics.enabled: false` removes the declared metrics container port; it does not disable the listener or restrict access to its endpoints. Configure scraping through `podAnnotations` and restrict network access with administrator-managed NetworkPolicies.

#### Image

The default image is `ghcr.io/inherent-design/cloudflared:2026.9.3-h2c.1@sha256:6c46ca006f9d6af5e973e59f2d71f5d6d3dc138c8a5484380d5797092171e3ad`, a fork of [cloudflare/cloudflared](https://github.com/cloudflare/cloudflared) maintained at [inherent-design/cloudflared](https://github.com/inherent-design/cloudflared). The fork adds `h2cOrigin` support for HTTP/2 cleartext origin connections; upstream cloudflared does not support this feature ([cloudflare/cloudflared#1304](https://github.com/cloudflare/cloudflared/issues/1304)).

For an upstream connector without h2c support, override the image:

```yaml
spec:
  cloudflared:
    image: cloudflare/cloudflared:2026.9.3
```

The upstream image is a no-h2c mode override only. Selecting the known `cloudflare/cloudflared` Docker repository, including its Docker Hub aliases, replaces h2c forwarding matches with `http_status:503` and emits an `IncompatibleConnectorImage` warning event. Ordinary HTTP routes remain enabled. Global h2c defaults also block forwarding fallbacks. Restoring the fork image or removing the h2c configuration restores eligible forwarding. Custom images remain administrator-owned compatibility choices; their names do not prove h2c support.

### `spec.originDefaults`

Origin defaults apply to ingress rules unless [route annotations](annotations.md) override them. `http2Origin` and `h2cOrigin` cannot both be enabled; h2c also requires cleartext HTTP.

For private origin certificates, `caPoolSecretRef` selects a Secret in the tunnel namespace. Its selected key must contain PEM certificates; omitted or empty `key` selects `ca.crt`. cfgate mounts the bundle at `/etc/cfgate/origin-ca-pool/ca.pem` and publishes that path as `originRequest.caPool`. Missing Secrets or keys prevent connector deployment and set `CloudflaredDeployed=False` and `Ready=False`.

Changes to the selected certificate data roll the connector Pods so transports reload trust. Changes to unrelated Secret keys do not trigger that rollout. The `cfgate.io/origin-ca-pool` annotation accepts only this managed path and requires the Secret reference.

This fragment enables TLS HTTP/2 with a private CA:

```yaml
spec:
  originDefaults:
    connectTimeout: "10s"
    http2Origin: true
    caPoolSecretRef:
      name: internal-ca
      key: ca-chain.pem
```

### `spec.fallbackTarget`

Unmatched requests receive `http_status:404` by default. A cloudflared origin URL can forward unmatched requests instead. Effective origin-transport validation and configuration limits also apply to forwarding fallbacks.

### `spec.fallbackCredentialsRef`

Deletion can use this Secret when primary credentials are unavailable. It must provide the same selected token key as the primary Secret, with the required permissions. Cross-namespace references still require authorization.

## Status

| Field | Type | Description |
|-------|------|-------------|
| `status.tunnelId` | `string` | Cloudflare-assigned tunnel ID. |
| `status.tunnelName` | `string` | Tunnel name in Cloudflare. |
| `status.tunnelDomain` | `string` | Tunnel's CNAME target domain (`{tunnelId}.cfargotunnel.com`). Used by CloudflareDNS for DNS record creation. |
| `status.accountId` | `string` | Resolved Cloudflare account ID (cached from `accountName` lookup). |
| `status.replicas` | `int32` | Total number of cloudflared replicas (desired). |
| `status.readyReplicas` | `int32` | Number of ready cloudflared replicas. |
| `status.observedGeneration` | `int64` | Last `.metadata.generation` observed by the controller. |
| `status.lastSyncTime` | `metav1.Time` | Last time the tunnel configuration was synced to Cloudflare. |
| `status.lastFullReconcileTime` | `metav1.Time` | Last successful credentials, tunnel, Deployment, and configuration reconciliation; configuration-only passes do not advance it. |
| `status.lifecycleDependencyHash` | `string` | Digest of checked Secret identities/revisions and Deployment generation; contains no Secret data. |
| `status.connectedRouteCount` | `int32` | Number of routes currently connected to this tunnel. |
| `status.conditions` | `[]metav1.Condition` | Standard Kubernetes conditions (see below). |

The controller checks the full tunnel lifecycle at least every 30 minutes when reconciliation can complete. A full pass reads the remote configuration, including `h2cOrigin`, and repairs drift even when the local configuration hash matches. Applied hashes include both account and tunnel identity, so a replacement tunnel cannot inherit a prior tunnel's applied state. Between these checks, it may synchronize configuration without repeating credential, tunnel, and Deployment operations. Changes to the referenced credential, connector-token, or origin-CA Secret, or the connector Deployment generation, invalidate this optimization. Missing dependencies also force a full reconciliation. Existing resources without the lifecycle status fields receive a full reconciliation on upgrade. The fallback deletion credential is resolved during deletion, which never uses this optimization.

### Status Conditions

| Condition | Description |
|-----------|-------------|
| `Ready` | Credentials, tunnel configuration, and desired connector rollout are ready; origin reachability is not tested. |
| `CredentialsValid` | API credentials in the referenced Secret have been validated against the Cloudflare API. |
| `TunnelReady` | Tunnel exists in Cloudflare (either created or adopted). |
| `ConfigurationSynced` | Ingress configuration has been successfully synced to Cloudflare. |
| `CloudflaredDeployed` | Cloudflared pods are running and ready. |

### kubectl Output Columns

| Column | JSONPath | Description |
|--------|----------|-------------|
| Ready | `.status.conditions[?(@.type=='Ready')].status` | Whether configuration is synchronized and all desired connector replicas are available (`True`/`False`/`Unknown`); origin reachability is not tested. |
| Tunnel ID | `.status.tunnelId` | Cloudflare tunnel ID. |
| Replicas | `.status.readyReplicas` | Number of ready cloudflared replicas. |
| Age | `.metadata.creationTimestamp` | Age of the resource. |

## Deletion Behavior

The `cfgate.io/tunnel-cleanup` finalizer retains the resource until owned connector Pods have stopped and Cloudflare cleanup succeeds. cfgate scales the owned Deployment to zero before deleting remote connections and the tunnel. Matching orphan or foreign Pods block cleanup. The cleanup removes generated connector credentials, not the user-provided API-token Secret.

Failed cleanup retries every 10 seconds. Warning events use `CleanupFailed` during the first two minutes of deletion and `CleanupBlocked` afterward; this threshold does not stop retries or remove the finalizer. Restore credentials, permissions, or connectivity to let cleanup finish.

To retain remote resources and skip cleanup, annotate the tunnel before or during deletion:

```bash
kubectl annotate cloudflaretunnel my-tunnel -n cfgate-system \
  cfgate.io/deletion-policy=orphan
```

Orphan deletion retains the tunnel ownership claim and allows Kubernetes garbage collection of owned objects. Recreating a resource with the same name does not transfer ownership.

## Runtime checks and cleanup

Manager `/healthz` checks process responsiveness; `/readyz` also waits for cache synchronization. Connector `/healthcheck` and `/ready` distinguish process health from edge connectivity. None establishes origin reachability.

Tunnel readiness requires the current Deployment generation, exactly the desired total and updated replicas, and all desired replicas ready and available. Old healthy replicas, surge replicas, or a partial token rollout do not complete the current rollout.

Connector-token changes update the managed Secret before updating the Pod-template revision annotation. That annotation contains only the Secret UID and resource version. Unchanged tokens do not restart Pods, and failed Secret writes do not trigger rollouts.

ReferenceGrant discovery distinguishes an absent optional API from authorization and connectivity failures. Transient discovery failures receive bounded retries with a five-second request timeout. Missing required Gateway API resources or exhausted discovery failures prevent startup. Restart the manager after installing or removing Gateway API CRDs.

## Operator settings

| Flag | Default | Meaning |
|------|---------|---------|
| `--cluster-domain` | `cluster.local` | Kubernetes DNS suffix used in generated backend Service URLs; a final dot is normalized. |
| `--installation-namespace` | `POD_NAMESPACE` | Namespace whose persistent UID identifies the installation for DNS ownership. Explicitly set this when running outside Kubernetes. |
| `--cloudflare-request-timeout` | `30s` | Positive maximum duration of each Cloudflare API attempt; earlier caller deadlines still apply. |
| `--max-ingress-rules` | `1000` | Positive maximum rules per tunnel configuration, including the fallback. |
| `--max-configuration-bytes` | `1048576` | Positive maximum serialized tunnel configuration size. |

Each reconciliation has a two-minute deadline. The SDK performs at most two retries, and API operations and pagination preserve cancellation. The metric `cfgate_controller_last_completed_reconcile_timestamp_seconds`, labeled by controller name, records completed iterations including handled failures. It measures worker progress rather than successful Cloudflare changes. Tunnel `lastFullReconcileTime` separately records successful full lifecycle checks. Dependency events may fan out to multiple tunnels; the work limits constrain configuration construction and publication, not the number of Kubernetes objects watched.

See [Connector hardening](connector-hardening.md) for network isolation guidance and its prerequisites.

## Ownership and upgrade migration

Local connector Secrets and Deployments must carry the expected controller owner UID. Remote tunnel claims are immutable ConfigMaps in the installation namespace, keyed by account and tunnel ID. A failed remote lookup never authorizes adoption or deletion. Claims coordinate writers within that installation namespace; they are not cross-cluster locks.

Normal deletion verifies the claim, drains connectors, confirms remote absence, then deletes the claim with UID and resource-version preconditions. Follow [authorization and ownership](authorization-and-ownership.md) before adopting legacy resources or transferring them between installations.

Generated names and labels use a bounded hash suffix when the tunnel metadata name exceeds 63 characters. Existing valid generated names and selectors remain unchanged; owner UIDs determine ownership.

## Required protection dependency

Set `cfgate.io/access-required: namespace/name` on an HTTPRoute to require a named Access application before forwarding. Routes without this annotation have no explicit protection dependency. [Access-required routing](access-required.md) describes grants, supported targets, remote verification, and asynchronous withdrawal limits.

### Configuration overload

If ingress or Access dependency limits are exceeded, cfgate replaces the entire tunnel configuration, including a custom fallback, with one HTTP 503 response. Forwarding resumes when the configuration fits. Cleanup receipts are cleared only after Cloudflare confirms withdrawal, so an API failure can delay withdrawal. Reduce the configuration or adjust the relevant limit after reviewing its operational cost.

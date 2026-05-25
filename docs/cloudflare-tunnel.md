# CloudflareTunnel

Manages the lifecycle of a Cloudflare Tunnel and its cloudflared daemon deployment.

**API Version:** `cfgate.io/v1alpha1`
**Kind:** `CloudflareTunnel`
**Short Names:** `cft`, `cftunnel`
**Scope:** Namespaced

## Overview

CloudflareTunnel handles tunnel creation or adoption, credential management, and deploys cloudflared pods that establish secure connections to Cloudflare's edge network. It follows a composable architecture where tunnel lifecycle is separate from DNS management. Use CloudflareDNS with a `tunnelRef` to create DNS records pointing to this tunnel's domain.

A tunnel is zone-agnostic: one tunnel can serve any number of domains across different zones. The tunnel itself does not bind to any particular domain; DNS records are created separately via [CloudflareDNS](cloudflare-dns.md) resources.

Tunnel name resolution is idempotent. The controller resolves the tunnel by name and creates it if it does not exist. Multiple CloudflareTunnel resources with the same tunnel name will adopt the same Cloudflare tunnel rather than creating duplicates. The resolved tunnel ID is stored in `.status.tunnelId`.

## Spec Reference

| Field | Type | Default | Required | Description |
|-------|------|---------|----------|-------------|
| `spec.tunnel.name` | `string` | *none* | Yes | Tunnel name in Cloudflare. Idempotent: creates if absent, adopts if existing. Must be 1-63 chars, lowercase alphanumeric with hyphens, matching `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`. |
| `spec.cloudflare.accountId` | `string` | *none* | No | Cloudflare Account ID. Max 32 chars. Either `accountId` or `accountName` must be specified. |
| `spec.cloudflare.accountName` | `string` | *none* | No | Cloudflare Account name. Resolved via API lookup (requires Account Settings Read permission). Max 255 chars. Either `accountId` or `accountName` must be specified. |
| `spec.cloudflare.secretRef.name` | `string` | *none* | Yes | Name of the Secret containing the Cloudflare API token. 1-253 chars. |
| `spec.cloudflare.secretRef.namespace` | `string` | *(resource namespace)* | No | Namespace of the credentials Secret. Defaults to the tunnel's namespace. Max 63 chars. |
| `spec.cloudflare.secretKeys.apiToken` | `string` | `CLOUDFLARE_API_TOKEN` | No | Key name within the Secret for the Cloudflare API token. Max 253 chars. |
| `spec.cloudflared.replicas` | `int32` | `2` | No | Number of cloudflared replicas. Min 1, max 10. Each replica establishes an independent connection for high availability. |
| `spec.cloudflared.image` | `string` | `ghcr.io/inherent-design/cloudflared:2026.5.1-h2c.1` | No | Container image for the cloudflared daemon. See [Image](#image) below. Max 255 chars. |
| `spec.cloudflared.imagePullPolicy` | `string` | `IfNotPresent` | No | Image pull policy. One of: `Always`, `Never`, `IfNotPresent`. |
| `spec.cloudflared.protocol` | `string` | `auto` | No | Tunnel transport protocol. One of: `auto`, `quic`, `http2`. |
| `spec.cloudflared.resources` | `corev1.ResourceRequirements` | *none* | No | Resource requests and limits for cloudflared containers. Standard Kubernetes resource spec. |
| `spec.cloudflared.nodeSelector` | `map[string]string` | *none* | No | Node selector for cloudflared pod scheduling. Max 50 entries. |
| `spec.cloudflared.tolerations` | `[]corev1.Toleration` | *none* | No | Tolerations for cloudflared pods. Max 20 items. |
| `spec.cloudflared.podAnnotations` | `map[string]string` | *none* | No | Annotations added to cloudflared pods. Max 50 entries. |
| `spec.cloudflared.extraArgs` | `[]string` | *none* | No | Additional CLI arguments passed to cloudflared. Max 20 items. |
| `spec.cloudflared.metrics.enabled` | `bool` | `true` | No | Enables the Prometheus-compatible metrics endpoint on cloudflared pods. |
| `spec.cloudflared.metrics.port` | `int32` | `44483` | No | Port for the metrics endpoint. Min 1, max 65535. Metrics available at `http://localhost:{port}/metrics`. |
| `spec.originDefaults.connectTimeout` | `string` | `30s` | No | Timeout for connecting to origin/backend services. Format: `^[0-9]+(s|m|h)$`. |
| `spec.originDefaults.noTLSVerify` | `bool` | `false` | No | Disables TLS certificate verification for origin connections. Use with caution in production. |
| `spec.originDefaults.http2Origin` | `bool` | `false` | No | Enables HTTP/2 for connections to origin services. |
| `spec.originDefaults.h2cOrigin` | `bool` | `false` | No | Enables HTTP/2 cleartext (h2c) for origin connections. Use for origins that speak HTTP/2 without TLS. Mutually exclusive with `http2Origin`. |
| `spec.originDefaults.caPoolSecretRef.name` | `string` | *none* | Yes (if caPoolSecretRef set) | Name of the Secret containing CA certificates for origin TLS verification. 1-253 chars. |
| `spec.originDefaults.caPoolSecretRef.key` | `string` | `ca.crt` | No | Key within the Secret containing the CA certificate chain in PEM format. Max 253 chars. |
| `spec.originCAPools[].name` | `string` | *none* | Yes | Named CA pool selectable by route annotations and origin policies. Unique within a tunnel. |
| `spec.originCAPools[].secretRef.name` | `string` | *none* | Yes | Secret containing a PEM-encoded CA bundle. |
| `spec.originCAPools[].secretRef.namespace` | `string` | *(tunnel namespace)* | No | Secret namespace. Cross-namespace references require a Gateway API `ReferenceGrant`. |
| `spec.originCAPools[].secretRef.key` | `string` | `ca.crt` | No | Secret data key containing the CA bundle. |
| `spec.fallbackTarget` | `string` | `http_status:404` | No | Default service for requests that do not match any ingress rule. |
| `spec.fallbackCredentialsRef.name` | `string` | *none* | Yes (if fallbackCredentialsRef set) | Name of the Secret containing fallback Cloudflare API credentials. 1-253 chars. |
| `spec.fallbackCredentialsRef.namespace` | `string` | *(resource namespace)* | No | Namespace of the fallback credentials Secret. Max 63 chars. |

## Detailed Field Documentation

### `spec.tunnel`

Defines the tunnel identity. The controller uses the `name` field to look up or create the tunnel in Cloudflare. This is the core idempotent pathway: if a tunnel with the given name already exists in the account, the controller adopts it. If not, it creates a new one. The resolved tunnel ID is stored in `.status.tunnelId`.

**Constraints:**
- Name must be lowercase alphanumeric with hyphens (DNS subdomain-like pattern).
- Max 63 characters.
- Multiple CRs with the same tunnel name adopt the same Cloudflare tunnel.

```yaml
spec:
  tunnel:
    name: my-cluster-tunnel
```

### `spec.cloudflare`

Configures Cloudflare API credentials. The controller needs either `accountId` (preferred, no extra API call) or `accountName` (resolved via API lookup, requires Account Settings Read permission on the token). The resolved account ID is cached in `.status.accountId`.

The `secretRef` must point to a Kubernetes Secret containing a Cloudflare API token (not a tunnel token). By default, the token is read from the key `CLOUDFLARE_API_TOKEN`. Override this with `secretKeys.apiToken`.

**Required API token permissions:**
- Account > Cloudflare Tunnel > Edit (always required)
- Account > Account Settings > Read (required only when using `accountName`)

```yaml
spec:
  cloudflare:
    accountId: "abc123def456"
    secretRef:
      name: cloudflare-credentials
    secretKeys:
      apiToken: MY_CUSTOM_TOKEN_KEY
```

Or using account name resolution:

```yaml
spec:
  cloudflare:
    accountName: "My Company"
    secretRef:
      name: cloudflare-credentials
      namespace: shared-secrets
```

### `spec.cloudflared`

Controls the cloudflared daemon Deployment. The controller creates a Deployment with the specified number of replicas. Each replica establishes an independent connection to Cloudflare's edge network, providing high availability.

**Protocol selection:** The `auto` default lets cloudflared negotiate the best protocol. Use `quic` for UDP-based transport (lower latency, better for unstable connections) or `http2` for environments where UDP is blocked.

**Metrics:** Enabled by default on port 44483. The endpoint serves Prometheus-compatible metrics at `/metrics` on each cloudflared pod. When `metrics.enabled: false`, cfgate omits the `--metrics` flag, container port, and cloudflared HTTP probes. Use `podAnnotations` to configure Prometheus scraping.

Generated cloudflared pods are compatible with Kubernetes `restricted` Pod Security by default. cfgate runs them as non-root, uses the runtime-default seccomp profile, disables privilege escalation, and drops all Linux capabilities.

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

#### Image

The default image is `ghcr.io/inherent-design/cloudflared:2026.5.1-h2c.1`, a fork of [cloudflare/cloudflared](https://github.com/cloudflare/cloudflared) maintained at [inherent-design/cloudflared](https://github.com/inherent-design/cloudflared). The fork adds `h2cOrigin` support for HTTP/2 cleartext origin connections; upstream cloudflared does not support this feature ([cloudflare/cloudflared#1304](https://github.com/cloudflare/cloudflared/issues/1304)).

Users who do not need h2c can override the image to upstream:

```yaml
spec:
  cloudflared:
    image: cloudflare/cloudflared:2026.5.0
```

The upstream image is a no-h2c mode override only. The `h2cOrigin` field and `cfgate.io/origin-h2c` annotation require the cfgate fork image.

### `spec.originDefaults`

Default settings for how cloudflared connects to backend services in the cluster. These apply to all ingress rules unless overridden by route-specific [annotations](annotations.md).

**`caPoolSecretRef`:** Use this when your backend services present TLS certificates signed by a private CA. The Secret must contain the CA certificate chain in PEM format. cfgate mounts the selected Secret key into cloudflared at `/etc/cfgate/origin-ca-pool/ca.pem` and sends `originRequest.caPool` with that path in the remote tunnel configuration. If `key` is omitted or empty, cfgate reads `ca.crt`. Without this, connections to services using private CA certificates will fail TLS verification (unless `noTLSVerify` is set, which is not recommended for production).

The route annotation `cfgate.io/origin-ca-pool` can select that same managed path for a specific ingress rule. In managed mode, cfgate rejects arbitrary annotation paths and rejects the annotation when this Secret ref is not configured, because cfgate cannot guarantee any other file exists inside cloudflared. Advanced users can opt into unmanaged file paths with `cfgate.io/origin-ca-pool-mode: unmanaged`; cfgate will pass the absolute path through and warn that it does not mount or verify the file.

If the referenced Secret or key is missing, cfgate does not deploy cloudflared and marks `CloudflaredDeployed=False` and `Ready=False`.

```yaml
spec:
  originDefaults:
    connectTimeout: "10s"
    http2Origin: true
    caPoolSecretRef:
      name: internal-ca
      key: ca-chain.pem
```

### `spec.originCAPools`

Named origin CA pools provide a Kubernetes-native replacement for route filesystem paths. cfgate mounts each referenced Secret key into cloudflared at a controller-owned path, then maps route annotations and origin policies to `originRequest.caPool`.

```yaml
spec:
  originCAPools:
    - name: internal
      secretRef:
        name: internal-ca
        key: ca.crt
    - name: partner
      secretRef:
        name: partner-ca
        namespace: shared-certs
        key: bundle.pem
```

Routes can select a pool by name:

```yaml
metadata:
  annotations:
    cfgate.io/origin-ca-pool-ref: internal
```

Cross-namespace Secret references require a Gateway API `ReferenceGrant` in the Secret namespace permitting `CloudflareTunnel` from the tunnel namespace to reference `Secret`.

Same-namespace named pool Secrets are mounted directly. Cross-namespace named pool Secrets are copied into generated Secrets in the tunnel namespace, owned by the `CloudflareTunnel`, and mounted from there because Kubernetes pods cannot mount Secrets across namespaces. cfgate removes stale generated CA Secrets when the tunnel no longer references them.

Named pools mount at:

```text
/etc/cfgate/origin-ca-pools/<pool-name>/ca.pem
```

Gateway API `BackendTLSPolicy` ConfigMap CA bundles are materialized the same way and mounted at:

```text
/etc/cfgate/backend-tls-policies/<policy-namespace>/<policy-name>/ca.pem
```

### `spec.fallbackTarget`

The catch-all service for requests that do not match any ingress rule. Defaults to returning HTTP 404. Can be set to any cloudflared-supported origin format (e.g., `http://fallback-svc.default.svc.cluster.local:8080`).

```yaml
spec:
  fallbackTarget: "http_status:404"
```

### `spec.fallbackCredentialsRef`

References a Secret containing fallback Cloudflare API credentials. Used during resource deletion when the primary credentials Secret (referenced by `spec.cloudflare.secretRef`) has already been deleted. This enables cleanup of Cloudflare-side resources (tunnel deletion, config removal) even if the per-tunnel credentials Secret is removed first.

The fallback Secret must contain the same key structure as the primary credentials Secret.

```yaml
spec:
  fallbackCredentialsRef:
    name: cloudflare-admin-credentials
    namespace: cfgate-system
```

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
| `status.connectedRouteCount` | `int32` | Number of routes currently connected to this tunnel. |
| `status.conditions` | `[]metav1.Condition` | Standard Kubernetes conditions (see below). |

### Status Conditions

| Condition | Description |
|-----------|-------------|
| `Ready` | Tunnel is fully operational: credentials valid, tunnel exists, config synced, pods running. |
| `CredentialsValid` | API credentials in the referenced Secret have been validated against the Cloudflare API. |
| `TunnelReady` | Tunnel exists in Cloudflare (either created or adopted). |
| `ConfigurationSynced` | Ingress configuration has been successfully synced to Cloudflare. |
| `CloudflaredDeployed` | Cloudflared pods are running and ready. |

### kubectl Output Columns

| Column | JSONPath | Description |
|--------|----------|-------------|
| Ready | `.status.conditions[?(@.type=='Ready')].status` | Whether the tunnel is fully operational (`True`/`False`/`Unknown`). |
| Tunnel ID | `.status.tunnelId` | Cloudflare tunnel ID. |
| Replicas | `.status.readyReplicas` | Number of ready cloudflared replicas. |
| Age | `.metadata.creationTimestamp` | Age of the resource. |

## Usage Examples

### Minimal tunnel with account ID

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

### Full-featured tunnel with HA and monitoring

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
    accountName: "Acme Corp"
    secretRef:
      name: cloudflare-api-token
    secretKeys:
      apiToken: CF_TOKEN
  cloudflared:
    replicas: 3
    protocol: quic
    resources:
      requests:
        cpu: 100m
        memory: 128Mi
      limits:
        cpu: 500m
        memory: 256Mi
    nodeSelector:
      topology.kubernetes.io/zone: us-west-2a
    podAnnotations:
      prometheus.io/scrape: "true"
      prometheus.io/port: "44483"
    metrics:
      enabled: true
      port: 44483
  originDefaults:
    connectTimeout: "10s"
    http2Origin: true
    caPoolSecretRef:
      name: internal-ca
      key: ca.crt
  fallbackTarget: "http_status:404"
  fallbackCredentialsRef:
    name: cloudflare-admin-credentials
    namespace: cfgate-system
```

### Tunnel with custom secret key and namespace isolation

```yaml
apiVersion: cfgate.io/v1alpha1
kind: CloudflareTunnel
metadata:
  name: staging-tunnel
  namespace: staging
spec:
  tunnel:
    name: staging-cluster
  cloudflare:
    accountId: "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"
    secretRef:
      name: cf-credentials
      namespace: shared-secrets
    secretKeys:
      apiToken: STAGING_CF_TOKEN
  cloudflared:
    replicas: 1
    protocol: auto
    tolerations:
      - key: workload-type
        value: tunnel
        effect: NoSchedule
  originDefaults:
    noTLSVerify: false
    connectTimeout: "30s"
```

## Deletion Behavior

The controller adds the finalizer `cfgate.io/tunnel-cleanup` to every CloudflareTunnel resource. When the resource is deleted, the controller attempts to delete the Cloudflare tunnel and its credentials Secret before removing the finalizer.

If cleanup fails, the controller blocks indefinitely and requeues every 10 seconds. It never removes the finalizer automatically. Within a 2-minute retry budget, the controller emits Warning events with reason `CleanupFailed`. After the retry budget is exhausted, subsequent events escalate to reason `CleanupBlocked`.

To skip Cloudflare cleanup and remove the finalizer immediately, set the `cfgate.io/deletion-policy=orphan` annotation on the CloudflareTunnel resource. The controller will leave tunnel resources in Cloudflare and remove the finalizer without attempting cleanup.

```bash
kubectl annotate cloudflaretunnel my-tunnel -n cfgate-system \
  cfgate.io/deletion-policy=orphan
```

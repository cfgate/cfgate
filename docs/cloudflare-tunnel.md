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
| `spec.cloudflared.image` | `string` | `cloudflare/cloudflared:latest` | No | Container image for the cloudflared daemon. Max 255 chars. |
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

**Metrics:** Enabled by default on port 44483. The endpoint serves Prometheus-compatible metrics at `/metrics` on each cloudflared pod. Use `podAnnotations` to configure Prometheus scraping.

```yaml
spec:
  cloudflared:
    replicas: 3
    image: cloudflare/cloudflared:2024.1.0
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

### `spec.originDefaults`

Default settings for how cloudflared connects to backend services in the cluster. These apply to all ingress rules unless overridden by route-specific [annotations](annotations.md).

**`caPoolSecretRef`:** Use this when your backend services present TLS certificates signed by a private CA. The Secret must contain the CA certificate chain in PEM format. Without this, connections to services using private CA certificates will fail TLS verification (unless `noTLSVerify` is set, which is not recommended for production).

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
    image: cloudflare/cloudflared:2024.1.0
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

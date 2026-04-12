# Rancher Integration

Expose Rancher 2.14+ via cfgate using Gateway API.

## Prerequisites

- cfgate installed (see [basic example](../basic))
- Rancher Helm chart v2.14.0+ (Gateway API support)

## Setup

### 1. Deploy cfgate tunnel + DNS sync

```bash
# Edit tunnel.yaml: set accountId
# Edit dns.yaml: set zones[].name to your domain
kubectl apply -k examples/with-rancher/cfgate
```

### 2. Install Rancher

```bash
helm upgrade --install rancher rancher-alpha/rancher \
  --namespace cattle-system \
  --create-namespace \
  --values examples/with-rancher/rancher-values.yaml \
  --set hostname=rancher.example.com  # <-- Your domain
```

### 3. Annotate Rancher's Gateway

Rancher creates its own Gateway. Add the tunnel reference so cfgate can route traffic through the Cloudflare Tunnel:

```bash
kubectl annotate gateway rancher-gateway -n cattle-system \
  cfgate.io/tunnel-ref=cfgate-system/rancher-tunnel
```

The CloudflareDNS resource discovers hostnames from all HTTPRoutes attached to this Gateway because its `gatewayRoutes` block is present. No per-route annotations are needed unless you use `annotationFilter` to limit which routes get DNS records.

### 4. Verify

```bash
kubectl get gateway rancher-gateway -n cattle-system
kubectl get cloudflarednses -n cfgate-system
curl -I https://rancher.example.com
```

## How TLS Works

```
Browser ──HTTPS──▶ Cloudflare Edge (TLS termination)
                        │
                        │ X-Forwarded-Proto: https
                        ▼
                   cloudflared ──HTTP──▶ Rancher:80
```

Rancher respects `X-Forwarded-Proto` and skips HTTPS redirect when `tls: external` is set.

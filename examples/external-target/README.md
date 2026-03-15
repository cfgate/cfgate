# External Target Example

A/AAAA DNS records pointing to an external IP, without a Cloudflare Tunnel.

This example creates DNS records via CloudflareDNS in ExternalTarget mode. No tunnel, gateway, or HTTPRoute is needed; ExternalTarget mode is standalone DNS management.

## Quick Start

```bash
# 1. Install cfgate (if not already installed)
kubectl apply -f https://github.com/cfgate/cfgate/releases/latest/download/install.yaml

# 2. Create credentials
kubectl create secret generic cloudflare-credentials \
  -n cfgate-system \
  --from-literal=CLOUDFLARE_API_TOKEN=<your-token>

# 3. Deploy example (edit dns.yaml first)
kubectl apply -k examples/external-target
```

## Configuration

Before applying, edit `dns.yaml`:

| Field | What to change |
|-------|----------------|
| `spec.cloudflare.accountId` | Set to your Cloudflare account ID |
| `spec.cloudflare.secretRef.name` | Name of your credentials Secret (default: `cloudflare-credentials`) |
| `spec.zones[].name` | Set to your domain |
| `spec.source.explicit[].hostname` | Set to hostnames in your domain |
| `spec.externalTarget.value` | Set to the target IP address |

## Verify

```bash
kubectl get cfdns -n cfgate-system
dig app.example.com
dig api.example.com
```

## Cleanup

```bash
kubectl delete -k examples/external-target
```

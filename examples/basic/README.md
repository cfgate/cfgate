# Basic Example

Single tunnel exposing one service via Cloudflare.

## Quick Start

```bash
# 1. Install Gateway API + cfgate
kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.5.1/standard-install.yaml
kubectl apply -f https://github.com/cfgate/cfgate/releases/latest/download/install.yaml

# 2. Create credentials
kubectl create secret generic cloudflare-credentials \
  -n cfgate-system \
  --from-literal=CLOUDFLARE_API_TOKEN=<your-token>

# 3. Deploy example (edit files first)
kubectl apply -k examples/basic
```

## Configuration

Before applying, edit these files:

| File | What to change |
|------|----------------|
| `tunnel.yaml` | Set `accountId` to your Cloudflare account ID |
| `dns.yaml` | Set `zones[].name` to your domain |
| `httproute.yaml` | Set `hostnames[]` to your subdomain |

## Verify

```bash
kubectl get cloudflaretunnel -n cfgate-system
kubectl get cloudflarednses -n cfgate-system
kubectl get httproute -n demo
curl https://echo.yourdomain.com
```

## Cleanup

```bash
kubectl delete -k examples/basic
```

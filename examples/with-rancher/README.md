# Rancher integration

This example connects a Rancher installation to a cfgate tunnel. Cloudflare
terminates browser-facing TLS, and the connector reaches Rancher over HTTP. Rancher
retains responsibility for its own authentication.

## Prerequisites

Install cfgate and Gateway API using [getting started](../../docs/getting-started.md).
Create `cloudflare-credentials` in `cfgate-system`, then edit the account ID in
`cfgate/tunnel.yaml` and zone name in `cfgate/dns.yaml`.

The supplied `rancher-values.yaml` targets Rancher charts with Gateway API exposure
settings. Check those keys against your selected chart version before installation;
this recipe does not establish compatibility with every Rancher release. In
particular, inspect the generated Gateway name, listeners, HTTPRoute backend, and
GatewayClass instead of assuming a fixed resource name.

## Tunnel and chart configuration

From the repository root, create the tunnel and DNS resource:

```bash
kubectl apply -k examples/with-rancher/cfgate
```

Render your chosen Rancher chart with `rancher-values.yaml` and your real hostname.
Use a pinned chart version from your configured Rancher repository. The rendered
GatewayClass must use `cfgate.io/cloudflare-tunnel-controller`. Its Gateway must
accept the Rancher HTTPRoute on an HTTP listener. Keep `tls: external` only when
your upstream proxy terminates TLS as intended.

Before applying the Rancher resources, add this annotation to the actual Gateway:

```yaml
metadata:
  annotations:
    cfgate.io/tunnel-ref: cfgate-system/rancher-tunnel
```

A Gateway in `cattle-system` also needs this administrator-owned grant in the
tunnel's namespace:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: ReferenceGrant
metadata:
  name: allow-rancher-gateway
  namespace: cfgate-system
spec:
  from:
    - group: gateway.networking.k8s.io
      kind: Gateway
      namespace: cattle-system
  to:
    - group: cfgate.io
      kind: CloudflareTunnel
      name: rancher-tunnel
```

Apply that grant with the rendered Rancher configuration. DNS discovery is enabled
by the `gatewayRoutes` block and still requires an admitted route attached to the
tunnel. Use a namespace selector or annotation filter to narrow discovery further.

## Verification

Inspect the actual Gateway and HTTPRoute conditions in `cattle-system`, the
`rancher-tunnel` and `rancher-dns` resources in `cfgate-system`, and connector logs.
Open the configured HTTPS hostname and verify Rancher login and normal application
requests. A successful Helm installation alone does not verify external access.

The intended request path is:

```text
Browser -> HTTPS -> Cloudflare -> tunnel -> cloudflared -> HTTP -> Rancher:80
```

If Rancher redirects repeatedly, check its external-TLS setting and forwarded
scheme handling. See [troubleshooting](../../docs/troubleshooting.md) for origin
connectivity and transport diagnostics.

## Cleanup

Withdraw the Rancher route and verify the tunnel's published configuration first.
Delete `rancher-dns`, wait for finalization, then delete `rancher-tunnel` and wait
again. Retain the controller and credentials throughout. Remove the cross-namespace
grant only after cleanup; Rancher removal and its persistent data require their own
application-specific procedure.

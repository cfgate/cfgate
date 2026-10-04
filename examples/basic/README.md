# Single-service example

This example publishes one HTTP service through a Cloudflare Tunnel. The route is
public. Use the [getting-started guide](../../docs/getting-started.md) for a complete
installation and an optional Access-protected variant.

## Prerequisites

Install cfgate and the matching Gateway API CRDs first. Create the
`cloudflare-credentials` Secret in `cfgate-system` using the token procedure in the
guide, changing its namespace from `cfgate-demo` to `cfgate-system`. The token needs
Tunnel edit, DNS edit, and Zone read permissions for the selected account and zone.

Run the following commands from the repository root. These manifests create a
`demo` namespace and a `cfgate` GatewayClass; inspect existing objects with those
names before applying them. Do not use this example beside another example that
shares those names.

## Configuration

Edit the sample values before applying:

| File | Configuration |
| --- | --- |
| `tunnel.yaml` | Cloudflare account ID and an unused remote tunnel name |
| `dns.yaml` | Managed zone name |
| `httproute.yaml` | Hostname in that zone |

Apply the example and inspect current conditions:

```bash
kubectl apply -k examples/basic
kubectl get cloudflaretunnel demo-tunnel -n cfgate-system
kubectl get cloudflaredns demo-dns -n cfgate-system
kubectl get httproute -n demo
```

Request the hostname you configured and check the echo response. A Ready manager
Pod alone does not verify the route, DNS, or origin connection.

## Cleanup

Delete the HTTPRoute first and verify its forwarding has been withdrawn. Then
remove DNS and the tunnel, waiting for each to finalize:

```bash
kubectl delete -f examples/basic/httproute.yaml
```

After withdrawal, run this chain; a failed deletion stops subsequent steps:

```bash
kubectl delete -f examples/basic/dns.yaml --wait=true --timeout=300s &&
kubectl delete -f examples/basic/tunnel.yaml --wait=true --timeout=300s &&
kubectl delete gateway cloudflare-tunnel -n cfgate-system &&
kubectl delete -f examples/basic/echo-service.yaml
```

The shared `cfgate` GatewayClass and `demo` namespace remain. Before removing
them, inspect all Gateways for `spec.gatewayClassName: cfgate` and check for any
other workloads or resources in `demo`. Delete the class only if no Gateway uses
it, and delete the namespace only if it is dedicated to this example. Those
optional deletions are separate from the cleanup commands above.

Keep credentials, grants, and the controller until cleanup completes. See
[decommissioning](../../docs/authorization-and-ownership.md#controller-removal-and-decommissioning)
for retained resources or blocked finalizers.

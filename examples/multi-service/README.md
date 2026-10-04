# Multiple services and path-based Access

This example shares one tunnel between an API, a public web service, and two
Access applications protecting selected web paths. It demonstrates independent,
path-scoped Access configuration; it does **not** use the whole-host
`access-required` publication guard.

| Destination | Backend | Intended Access scope |
| --- | --- | --- |
| `api.example.com` | API | Public |
| `web.example.com/` | Web | Public |
| `web.example.com/admin` | Admin | Administrator email |
| `web.example.com/repos` | API | Company email domain |

Access and routing reconcile independently. Do not publish sensitive backends
until the remote applications are ready and authentication is verified. Origin-side
authentication remains necessary where uninterrupted enforcement is required. For
a whole-host guarded example, use [getting started](../../docs/getting-started.md).

## Configuration

Install cfgate and Gateway API first. Create `cloudflare-credentials` in
`cfgate-system` with Tunnel edit, DNS edit, Zone read, and Access Apps and Policies
edit permissions. Edit the account IDs in `tunnel.yaml` and `accesspolicy.yaml`,
the zone in `dns.yaml`, and the hostnames in `httproutes.yaml`. Replace the sample
email rules before using the applications.

The example uses `cfgate-system` for infrastructure and `demo` for workloads and
Access applications. `referencegrant.yaml` permits the applications to reference
central policies, the selected tunnel, and its credential Secret. Those grants
are administrator-owned; review them before accepting tenant-authored applications.

From the repository root, render the complete configuration before applying it:

```bash
kubectl kustomize examples/multi-service
kubectl apply -k examples/multi-service
kubectl get cloudflareaccessapplication -n demo
kubectl get cloudflareaccesspolicy -n cfgate-system
kubectl get httproute -n demo
```

Do not apply this alongside the basic example: both use `demo`, the `cfgate`
GatewayClass, and shared resource names. The sample workloads are for demonstration.
Check current application conditions and actual authenticated/unauthenticated
requests before replacing them with sensitive services.

## Additional services

Add a Deployment and TCP Service to `services.yaml`, then attach its HTTPRoute to
the shared Gateway. DNS discovery uses admitted route hostnames. Add a separate
Access application and appropriate policy references where protection is needed;
a reusable policy alone does not enforce authentication.

## Cleanup

Remove the routes first and verify that the tunnel has withdrawn their forwarding:

```bash
kubectl delete -f examples/multi-service/httproutes.yaml
```

Then remove resources in dependency order. This chain stops on a failed deletion:

```bash
kubectl delete -f examples/multi-service/dns.yaml --wait=true --timeout=300s &&
kubectl delete -f examples/multi-service/accessapplication.yaml --wait=true --timeout=300s &&
kubectl delete -f examples/multi-service/accesspolicy.yaml --wait=true --timeout=300s &&
kubectl delete -f examples/multi-service/tunnel.yaml --wait=true --timeout=300s &&
kubectl delete -f examples/multi-service/referencegrant.yaml &&
kubectl delete gateway cloudflare-tunnel -n cfgate-system &&
kubectl delete -f examples/multi-service/services.yaml
```

The shared `cfgate` GatewayClass and `demo` namespace remain. Before removing
them, inspect all Gateways for `spec.gatewayClassName: cfgate` and check for any
other workloads or resources in `demo`. Delete the class only if no Gateway uses
it, and delete the namespace only if it is dedicated to this example. Those
optional deletions are separate from the cleanup commands above.

Keep the credential Secret and controller until finalization completes. See [decommissioning](../../docs/authorization-and-ownership.md#controller-removal-and-decommissioning).

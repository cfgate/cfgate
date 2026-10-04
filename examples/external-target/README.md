# External DNS target

This example manages A records for an external address without creating a tunnel,
Gateway, or HTTPRoute. The address `203.0.113.10` is a documentation placeholder;
replace it with your destination.

## Configuration

Install cfgate using the [getting-started guide](../../docs/getting-started.md).
Create `cloudflare-credentials` in `cfgate-system` with DNS edit and Zone read
permissions for the selected zone. No Tunnel permission is needed for this example.

Edit `dns.yaml` before applying:

| Field | Configuration |
| --- | --- |
| `spec.cloudflare.accountId` | Cloudflare account ID |
| `spec.cloudflare.secretRef.name` | Credential Secret name |
| `spec.zones[].name` | Managed zone |
| `spec.source.explicit[].hostname` | Hostnames in that zone |
| `spec.externalTarget.value` | Destination IPv4 address |

From the repository root, apply the manifest and wait for synchronization:

```bash
kubectl apply -k examples/external-target
kubectl wait cloudflaredns/external-dns -n cfgate-system --for=condition=Ready --timeout=300s
```

Inspect the records in Cloudflare and query your configured hostnames. Proxied
records resolve to Cloudflare addresses; DNS-only records expose the target.
For AAAA and CNAME destinations, see the [DNS reference](../../docs/cloudflare-dns.md).

## Cleanup

Keep the controller and credentials available while the DNS resource finalizes:

```bash
kubectl delete -k examples/external-target --wait=true --timeout=300s
```

Verify the owned data and TXT records were removed. If deletion times out, inspect
the resource and controller logs before removing credentials or finalizers.

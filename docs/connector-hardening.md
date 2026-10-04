# Connector hardening

cfgate generates cloudflared Pods with non-root execution, runtime-default seccomp,
dropped capabilities, disabled privilege escalation, and no automatic Kubernetes
service-account token mount. The manager still needs its own API credential.
Administrators who select connector images or arguments retain the ability to run
code with tunnel credentials and connector network access.

## Diagnostics listener

cloudflared shares a listener for metrics, probes, and diagnostics. cfgate keeps
it reachable on Pod interfaces for kubelet probes even when `metrics.enabled` is
false. That setting controls the declared scrape port; it is not a firewall.
These probes do not establish origin availability.

## Network isolation

Administrators may apply an ingress NetworkPolicy in each connector namespace. This example permits port 44483 only from a namespace named `monitoring`; adjust the namespace selector and port to match the installation. The [Kubernetes NetworkPolicy model](https://kubernetes.io/docs/concepts/services-networking/network-policies/) permits traffic from a Pod's own node, which kubelet probes require. Verify the chosen CNI's behavior, especially host-network traffic, before using this policy.

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: cloudflared-diagnostics
  namespace: cfgate-system
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: cloudflared
      app.kubernetes.io/managed-by: cfgate
  policyTypes:
    - Ingress
  ingress:
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: monitoring
      ports:
        - protocol: TCP
          port: 44483
```

NetworkPolicy enforcement requires a supporting CNI. A plain kind cluster without an enforcing network plugin cannot prove this isolation. Validate blocked traffic from an unrelated test namespace, allowed monitoring traffic, and kubelet readiness before adopting the example. This policy is optional guidance and is not installed or claimed to protect deployments by default.

Egress restrictions depend on the configured origins, cluster DNS, Cloudflare edge endpoints, and protocol choice. Define an administrator-owned egress policy for those actual destinations rather than applying a generic deny policy that breaks supported origins. Keep the Cloudflare credentials and connector resources under administrator control; tenant routing permission is not equivalent to permission to execute a connector image.

For connector fields, see [CloudflareTunnel](cloudflare-tunnel.md). For administrator
permissions, see [authorization and ownership](authorization-and-ownership.md).

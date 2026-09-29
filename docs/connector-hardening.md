# Connector hardening

Generated cloudflared Pods run as non-root with the runtime-default seccomp profile, dropped capabilities, disabled privilege escalation, and no automatically mounted Kubernetes service-account token. These settings do not constrain the authority of an administrator who may choose connector images or arguments.

cloudflared shares one listener for `/metrics`, health probes, and diagnostic endpoints. cfgate keeps this listener reachable on pod interfaces so kubelet HTTP probes work even when `metrics.enabled` is false. The metrics setting controls the declared container port; it is not a network firewall. Origin availability is not established by these probes.

Administrators may apply an ingress NetworkPolicy in each connector namespace. The following example permits port44483 only from a namespace named `monitoring`; adjust the namespace selector and port to match the installation. The [Kubernetes NetworkPolicy model](https://kubernetes.io/docs/concepts/services-networking/network-policies/) permits traffic from a Pod's own node, which kubelet probes require. Verify the chosen CNI's behavior, especially host-network traffic, before using this policy.

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

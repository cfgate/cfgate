# Getting started

This guide installs cfgate and publishes a sample HTTP service at a hostname in
your Cloudflare zone. It uses cfgate `v0.2.0-alpha.11`, chart `1.10.0`, and Gateway
API `v1.6.2`. It creates real Cloudflare resources. The sample is intentionally
public unless you select the optional Access step before creating the route.

## Prerequisites

- A Kubernetes cluster compatible with the standard Gateway API bundle, which requires Kubernetes 1.30 or later.
- `kubectl`, Helm 3 or 4, Bash, and permission to install cluster-scoped CRDs/RBAC.
- An active Cloudflare zone and its account ID. Use a hostname with no existing DNS record or Access application.
- Outbound access from the manager to Kubernetes and Cloudflare APIs, and from connectors to Cloudflare, cluster DNS, and the origin Service. See [Cloudflare's firewall requirements](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/configure-tunnels/tunnel-with-firewall/).

Use a disposable namespace for the sample. Do not reuse another controller's
remote tunnel or ownership records. For an existing installation, follow
[the upgrade guide](compatibility.md) instead of installing a second writer.

## Controller installation

Inspect any existing Gateway API installation before replacing it. For a new
cluster, install the pinned bundle, then the chart:

```bash
kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.6.2/standard-install.yaml
helm install cfgate oci://ghcr.io/cfgate/charts/cfgate \
  --version 1.10.0 --namespace cfgate-system --create-namespace --wait
kubectl rollout status deployment/cfgate -n cfgate-system --timeout=180s
```

The chart includes cfgate's four CRDs and controller permissions. Gateway API is
an external prerequisite. The chart pins the operator image by digest; explicit
image overrides take precedence. See the [chart reference](https://github.com/cfgate/helm-chart/tree/v1.10.0)
for placement, resources, and externally managed CRDs/RBAC.

If Helm is not used, apply the pinned `install.yaml` release asset instead; do not
install both packages. The source manifest includes CRDs and uses a different
manager Deployment name:

```bash
kubectl apply -f https://github.com/cfgate/cfgate/releases/download/v0.2.0-alpha.11/install.yaml
kubectl rollout status deployment/controller-manager -n cfgate-system --timeout=180s
```

## Account and credentials

Create a Cloudflare API token scoped to the intended account and zone:

| Permission | Use |
| --- | --- |
| Account: Cloudflare Tunnel, Edit | Create and configure the tunnel |
| Zone: DNS, Edit | Publish and remove the sample DNS record |
| Zone: Zone, Read | Resolve the configured zone name |
| Account: Access: Apps and Policies, Edit | Optional Access policy/application and protection checks |

This guide uses `accountId`; account-name lookup additionally needs Account
Settings read permission. Managed service tokens, which this example does not
create, need Access: Service Tokens edit permission.

In Bash, set non-secret values for your account, zone, and hostname:

```bash
CFGATE_ACCOUNT_ID='replace-with-your-account-id'
CFGATE_ZONE='example.com'
CFGATE_HOSTNAME="hello.${CFGATE_ZONE}"
kubectl create namespace cfgate-demo
```

Store the token without writing it into the manifests or command history:

```bash
read -r -s -p 'Cloudflare API token: ' CFGATE_API_TOKEN
printf '\n'
printf '%s' "$CFGATE_API_TOKEN" | kubectl create secret generic cloudflare-credentials \
  -n cfgate-demo --from-file=CLOUDFLARE_API_TOKEN=/dev/stdin
unset CFGATE_API_TOKEN
```

All sample resources and credentials share `cfgate-demo`, so no cross-namespace
ReferenceGrant is needed. Production installations should separate route authors
from infrastructure administrators; see [authorization](authorization-and-ownership.md).

## Sample service

Create `service.yaml` with a non-sensitive echo application and a TCP Service:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: echo
  namespace: cfgate-demo
spec:
  replicas: 1
  selector:
    matchLabels:
      app: echo
  template:
    metadata:
      labels:
        app: echo
    spec:
      automountServiceAccountToken: false
      containers:
        - name: echo
          image: hashicorp/http-echo:1.0.0
          args: ["-listen=:5678", "-text=Hello from cfgate"]
          ports:
            - containerPort: 5678
---
apiVersion: v1
kind: Service
metadata:
  name: echo
  namespace: cfgate-demo
spec:
  selector:
    app: echo
  ports:
    - name: http
      port: 80
      targetPort: 5678
      protocol: TCP
```

Apply the workload and wait for its Pod:

```bash
kubectl apply -f service.yaml
kubectl rollout status deployment/echo -n cfgate-demo --timeout=180s
```

## Tunnel, Gateway, and DNS

The following command writes the infrastructure manifest using your shell values.
Choose a different tunnel name if `cfgate-quickstart` already exists in the account;
a matching name does not authorize adoption.

```bash
cat > infrastructure.yaml <<EOF_YAML
apiVersion: cfgate.io/v1alpha1
kind: CloudflareTunnel
metadata:
  name: demo
  namespace: cfgate-demo
spec:
  tunnel:
    name: cfgate-quickstart
  cloudflare:
    accountId: "${CFGATE_ACCOUNT_ID}"
    secretRef:
      name: cloudflare-credentials
  cloudflared:
    replicas: 2
---
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: cfgate-quickstart
spec:
  controllerName: cfgate.io/cloudflare-tunnel-controller
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: demo
  namespace: cfgate-demo
  annotations:
    cfgate.io/tunnel-ref: cfgate-demo/demo
spec:
  gatewayClassName: cfgate-quickstart
  listeners:
    - name: http
      protocol: HTTP
      port: 80
      hostname: "${CFGATE_HOSTNAME}"
      allowedRoutes:
        namespaces:
          from: Same
---
apiVersion: cfgate.io/v1alpha1
kind: CloudflareDNS
metadata:
  name: demo
  namespace: cfgate-demo
spec:
  tunnelRef:
    name: demo
  zones:
    - name: "${CFGATE_ZONE}"
  source:
    gatewayRoutes:
      enabled: true
      namespaceSelector:
        matchNames: [cfgate-demo]
  defaults:
    proxied: true
  cleanupPolicy:
    deleteOnRouteRemoval: true
    deleteOnResourceRemoval: true
EOF_YAML
kubectl apply -f infrastructure.yaml
kubectl wait cloudflaretunnel/demo -n cfgate-demo --for=condition=Ready --timeout=300s
```

The Gateway permits routes only from its namespace. DNS discovery is restricted
to the same namespace and still checks Gateway/listener admission. The connector
uses HTTP to reach this demo Service; browser-facing HTTPS terminates at Cloudflare.
Use [origin transport annotations](annotations.md) for TLS between connector and origin.

## Optional Access protection

Skip this section for the public sample. To require authentication from the first
route publication, complete it before the next section. Configure a Zero Trust
organization and a login method that can authenticate your chosen email address.
The API token needs the Access permission listed above.

Set the permitted email and write the policy/application manifest:

```bash
CFGATE_EMAIL='you@example.com'
cat > access.yaml <<EOF_YAML
apiVersion: cfgate.io/v1alpha1
kind: CloudflareAccessPolicy
metadata:
  name: demo
  namespace: cfgate-demo
spec:
  cloudflareRef:
    name: cloudflare-credentials
    accountId: "${CFGATE_ACCOUNT_ID}"
  name: cfgate-quickstart
  decision: allow
  include:
    - email:
        addresses: ["${CFGATE_EMAIL}"]
---
apiVersion: cfgate.io/v1alpha1
kind: CloudflareAccessApplication
metadata:
  name: demo
  namespace: cfgate-demo
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: HTTPRoute
    name: echo
  application:
    name: cfgate-quickstart
  policyRefs:
    - name: demo
      namespace: cfgate-demo
EOF_YAML
kubectl apply -f access.yaml
```

The application can wait for its HTTPRoute target. When creating the route below,
include the shown `access-required` annotation before applying it. A policy alone
has no effect on routing. The dependency produces matching 503 responses until
supported protection is ready; it is not an atomic edge-authentication guarantee.
Sensitive applications still need [origin-side authentication](access-required.md).

## Route publication

Write `route.yaml` using the selected hostname:

```bash
cat > route.yaml <<EOF_YAML
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: echo
  namespace: cfgate-demo
spec:
  parentRefs:
    - name: demo
      sectionName: http
  hostnames: ["${CFGATE_HOSTNAME}"]
  rules:
    - backendRefs:
        - name: echo
          port: 80
EOF_YAML
```

For the Access option, add this annotation under `metadata` in `route.yaml`:

```yaml
annotations:
  cfgate.io/access-required: cfgate-demo/demo
```

Apply the completed route, then wait for DNS reconciliation:

```bash
kubectl apply -f route.yaml
kubectl wait cloudflaredns/demo -n cfgate-demo --for=condition=Ready --timeout=300s
kubectl get httproute echo -n cfgate-demo -o yaml
```

Check the route's current `Accepted` and `ResolvedRefs` conditions. For the protected
option, also wait for the application and inspect the tunnel's current conditions:

```bash
kubectl wait cloudflareaccessapplication/demo -n cfgate-demo --for=condition=Ready --timeout=300s
kubectl get cloudflaretunnel demo -n cfgate-demo -o yaml
```

Run those two commands only if you created `access.yaml`. Conditions report
controller observations; the next step verifies a request through the connector.

## Request verification

For the public sample, request the hostname and expect `Hello from cfgate`:

```bash
curl --fail --show-error "https://${CFGATE_HOSTNAME}"
```

For the protected sample, open that URL in a browser and authenticate with the
configured login method. An unauthenticated request should not return the echo
body. DNS resolution, edge certificate issuance, and Access propagation can take
longer than Kubernetes reconciliation. A proxied record can resolve to Cloudflare
addresses instead of exposing its underlying CNAME.

If the request fails, inspect the resource conditions, connector logs, and Service
endpoints using [troubleshooting](troubleshooting.md). Do not treat a Ready manager
Pod or a successful Cloudflare configuration write as proof of origin reachability.

## Cleanup

Keep the controller, credentials, and namespace until remote cleanup finishes.
First remove the route and wait for its forwarding to disappear from the tunnel's
remote configuration. For the public sample, a 404 from the tunnel's fallback is
useful request evidence; use configuration inspection if Access intercepts it.

```bash
kubectl delete httproute echo -n cfgate-demo
```

Then delete the DNS resource. If you created Access resources, delete the
application before its policy and wait for each deletion:

```bash
kubectl delete cloudflaredns demo -n cfgate-demo --wait=true --timeout=300s &&
kubectl delete cloudflareaccessapplication demo -n cfgate-demo --ignore-not-found --wait=true --timeout=300s &&
kubectl delete cloudflareaccesspolicy demo -n cfgate-demo --ignore-not-found --wait=true --timeout=300s &&
kubectl delete cloudflaretunnel demo -n cfgate-demo --wait=true --timeout=300s &&
kubectl delete gateway demo -n cfgate-demo &&
kubectl delete gatewayclass cfgate-quickstart &&
kubectl delete namespace cfgate-demo
```

If any deletion times out, stop and repair the reported dependency before removing
credentials or the namespace. Verify the owned remote records, applications,
policy, and tunnel were removed. Do not remove finalizers to force cleanup. Keep
the controller if other resources use it; otherwise follow
[full decommissioning](authorization-and-ownership.md#controller-removal-and-decommissioning).

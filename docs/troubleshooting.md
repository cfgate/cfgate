# Troubleshooting

Start with the resource's current conditions, then check the next dependency in
the request path. A healthy manager process does not establish that DNS resolves,
the connector applied a configuration, or an origin accepted a request.

## Initial observations

Set the namespace and resource name for the affected installation. The examples
below use the [getting-started guide](getting-started.md):

```bash
kubectl get cloudflaretunnel,cloudflaredns,cloudflareaccesspolicy,cloudflareaccessapplication -n cfgate-demo
kubectl get gateway,httproute,service,endpointslice -n cfgate-demo
kubectl get events -n cfgate-demo --sort-by=.metadata.creationTimestamp
kubectl logs -n cfgate-system deployment/cfgate -c manager --since=10m
```

For the source installation manifest, use `deployment/controller-manager` in log
commands. Helm names can differ with release-name or fullname overrides. Check
`kubectl get deployment -n cfgate-system` instead of assuming a name.

Read a resource's `status.conditions`, including `observedGeneration`, reason,
and message. Old conditions may describe an earlier spec. Events help locate a
failure, but repeated events are not evidence of completed remote cleanup.

## Manager fails to start with a port environment error

Kubernetes Service links can set `CFGATE_METRICS_PORT` or `CFGATE_HEALTH_PORT` to
`tcp://<address>:<port>`. Versions before alpha.6 could parse those as integers and
exit. Current packages set `enableServiceLinks: false`; use the same setting in a
custom manager Deployment. Cluster DNS service discovery remains available.

Explicit bind flags take precedence over their corresponding environment values.
Without flags, numeric environment values select ports; unset values use `:8080`
for metrics and `:8081` for health. Recognized Service-link endpoint values use
those defaults. Other malformed values fail configuration validation.

An environment port of `0` means an ephemeral `:0` listener. The flag
`--metrics-bind-address=0` instead disables metrics. Health and metrics listeners
must not overlap; separate Service-facing ports do not resolve a process bind
collision. Inspect the actual Pod arguments and environment before changing them.

## HTTPRoute missing from tunnel configuration

Inspect the route and its parent Gateway:

```bash
kubectl get httproute echo -n cfgate-demo -o yaml
kubectl get gateway demo -n cfgate-demo -o yaml
kubectl get gatewayclass cfgate-quickstart -o yaml
```

cfgate checks the current GatewayClass, listener, allowed route kinds/namespaces,
hostname intersection, backend Service port, and required ReferenceGrants. It does
not authorize forwarding from a cached `Accepted=True` condition alone.

| Observation | Check |
| --- | --- |
| No cfgate parent status | `controllerName` must be `cfgate.io/cloudflare-tunnel-controller`; verify the parent exists |
| Listener/class rejection | Listener protocol, section/port, hostname, and `allowedRoutes` |
| `RefNotPermitted` | Named grant in the referenced resource's namespace |
| `BackendNotFound` | Service name, namespace, and numeric port |
| `UnsupportedProtocol` | Backend needs a TCP Service port, not only UDP/SCTP on the same number |
| `UnsupportedValue` | Supported matches, filters, backend count, and origin annotations |
| Matching HTTP 500 | Attached rule has an invalid or unavailable backend reference |
| Matching HTTP 503 | Invalid effective origin transport or unavailable required Access protection |

A denied attachment is excluded. Invalid attached backends retain matching error
rules so traffic cannot fall through to a broader public route. Invalid inherited
transport likewise keeps a scoped denial while valid siblings can publish.
Transient Kubernetes or provider failures may retain the last remote configuration;
check `ConfigurationSynced` and the remote tunnel configuration before assuming a
withdrawal completed. See [supported routing](gateway-api-primer.md#supported-httproute-behavior).

## DNS records not syncing

Inspect the DNS resource's conditions, record inventory, and pending writes:

```bash
kubectl get cloudflaredns demo -n cfgate-demo -o yaml
kubectl get cloudflaretunnel demo -n cfgate-demo -o yaml
```

| Condition or symptom | Check |
| --- | --- |
| `CredentialsValid=False` | Secret, selected data key, token permissions, and cross-namespace grants |
| `ZonesResolved=False` | Token zone scope and configured zone name or ID |
| No discovered hostnames | Tunnel reference, admitted routes, namespace selector, and annotation filter |
| Ownership conflict | Exact data/TXT markers and recorded owner identity; do not delete a foreign claim to force adoption |
| `RecordsSynced=False` | Failed records, policy-skipped changes, and retained cleanup obligations |
| `OwnershipVerified=False` | Remote data and TXT state; a successful write is not enough |
| Ready DNS but failed lookup | Public DNS propagation and the resolver used by the client |

A tunnel-backed DNS resource needs a resolved tunnel domain. External-target DNS
uses explicit hostnames; route discovery has no effect in that mode. Zone selection
uses the most specific configured suffix. Namespace `matchLabels` requires label
presence, even for an empty value; `matchNames` adds explicitly named namespaces.

`annotationFilter` is a discovery filter, not a built-in enable annotation. For
`platform.example.com/publish-dns=true`, the route must have that exact key/value.
A key-only filter requires presence. These selectors do not grant permission to
attach to a Gateway. See [DNS discovery and ownership](cloudflare-dns.md).

## GatewayClass not accepted

The class must select `cfgate.io/cloudflare-tunnel-controller`. A different
controller name is intentionally ignored. Verify the manager is running and the
Gateway API CRDs were installed before manager startup. Restart the manager after
installing optional APIs; discovery is performed at startup.

Kiali's class-recognition warning is separate from cfgate's conditions. See
[service mesh integration](service-mesh.md#kiali) before treating that warning as a
routing failure.

## Access credentials or protection unavailable

CloudflareAccessPolicy requires explicit `spec.cloudflareRef`. It does not inherit
credentials from Gateway targets. CloudflareAccessApplication can use explicit
credentials or resolve them through its target's tunnel. Each cross-namespace
reference needs its own grant, including inherited credential access.

Inspect the configured Secret's key names without decoding its contents:

```bash
kubectl get secret cloudflare-credentials -n cfgate-demo \
  -o go-template='{{range $key, $value := .data}}{{$key}}{{"\n"}}{{end}}'
```

The default key is `CLOUDFLARE_API_TOKEN`; custom keys must match the CR's
`secretKeys.apiToken`. Check account identity and Access permissions. Do not print
credential values into logs or support reports.

For `access-required` failures, check the application and every selected policy's
current-generation readiness, ownership, target, and account. Overlapping or
unsupported remote destinations can block publication. Allow/Everyone and bypass
policies do not satisfy the supported protection check. A 503 can also be an
intentional withdrawal before a policy edit or credential replacement. Healthy
expiration-only token renewal has a separate continuity path. See
[Access-required behavior](access-required.md) and [token lifecycle](cloudflare-access-policy.md#service-token-lifecycle).

## Gateway not programmed

Verify its `cfgate.io/tunnel-ref`, any cross-namespace grant, and the selected
Tunnel's conditions. `CredentialsValid`, `TunnelReady`, `CloudflaredDeployed`, and
`ConfigurationSynced` identify different stages. Check the current connector
Deployment and Pod events; old ready replicas do not prove the newest rollout is
available.

```bash
kubectl get deployment,pod -n cfgate-demo -l app.kubernetes.io/managed-by=cfgate
```

For Pod Security rejection, compare the generated workload to the
[connector defaults](connector-hardening.md). For origin TLS failure, check the
actual connector image, selected CA Secret key, certificate names, and effective
route overrides. Never disable verification merely to hide an unexplained error.

## Stuck finalizers

Finalizers retain cleanup obligations on custom resources. Deletion requires the
controller, its permissions, usable credentials, and remote access. Keep those
dependencies available while investigating:

```bash
kubectl get cloudflaretunnel demo -n cfgate-demo -o yaml
kubectl logs -n cfgate-system deployment/cfgate -c manager --since=10m
```

| Resource | Finalizer | Warning threshold | Retry interval |
| --- | --- | --- | --- |
| CloudflareTunnel | `cfgate.io/tunnel-cleanup` | 2 minutes | 10 seconds |
| CloudflareDNS | `cfgate.io/dns-cleanup` | 1 minute | 15 seconds |
| CloudflareAccessPolicy | `cfgate.io/access-policy-cleanup` | 1 minute | 15 seconds |
| CloudflareAccessApplication | `cfgate.io/access-application-cleanup` | 1 minute | 15 seconds |

The thresholds change `CleanupFailed` warnings to `CleanupBlocked`; they do not
stop retries or discard the finalizer. Restore missing credentials/grants, resolve
ownership conflicts, or withdraw dependent forwarding before retrying deletion.
Pending DNS writes and Access dependency receipts must remain available for recovery.

Deliberate orphaning is a separate administrator decision. Setting
`cfgate.io/deletion-policy: orphan` asks the running controller to retain remote
resources and finish local deletion according to that resource's lifecycle rules.
Inventory remote IDs and retained claims before doing this. Orphaned resources
need an explicit handoff or manual cleanup; recreating a Kubernetes name does not
preserve its UID or authority.

Force-removing finalizers bypasses cleanup and can lose the only record of remote
obligations. It is not a routine recovery procedure. If the original controller
cannot be restored, complete an administrator-led remote inventory and cleanup
before changing finalizers or deleting CRDs.

## Uninstalling cfgate / CRD deletion

For temporary controller removal, preserve resources, credentials, grants, and
the installation namespace UID. Existing traffic can continue while updates and
cleanup stop. For full removal, withdraw routes, finalize DNS and Access resources,
then finalize tunnels before removing the controller. Use explicit resource names;
do not delete all cfgate resources across a shared cluster.

Follow [the decommissioning sequence](authorization-and-ownership.md#controller-removal-and-decommissioning).
Deleting a CRD affects every object of that kind. Deleting the combined source
`install.yaml` also removes CRDs and RBAC, so it is not a controller-only uninstall.

## RBAC upgrade notes

Custom RBAC must include namespace reads for namespace selectors and the namespaced
ConfigMap claim Role/Binding in the actual installation namespace. Apply the
updated ClusterRole as well as new namespaced grants; an added Role cannot revoke
an old cluster-wide permission. See [claim permissions](authorization-and-ownership.md#claim-permissions-after-alpha6).

## Support information

Include the operator/chart versions, actual connector image, Kubernetes version,
relevant manifests with Secret values removed, current conditions, and the failing
request's status. Distinguish a saved configuration from an applied connector
configuration. Keep timestamps and exact errors; do not replace them with a guessed
network or controller diagnosis.

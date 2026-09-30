# Authorization and ownership

Creating an object does not itself authorize use of another namespace's credentials, a backend, or a remote resource. cfgate checks the current source object, target namespace, ReferenceGrant, Gateway ownership, and resource identity before performing the corresponding action. Status is diagnostic, not an authorization cache.

## Administrator and tenant boundary

CloudflareTunnel, CloudflareDNS, CloudflareAccessPolicy, CloudflareAccessApplication, GatewayClass, Gateway, ReferenceGrant, credential Secrets, and claim ConfigMaps are administrator-controlled infrastructure. Their creators can spend Cloudflare account authority. In particular, selecting a connector image or arguments authorizes executable code with tunnel credentials and the connector's network access. Do not grant these capabilities to route-only tenants.

A namespace Role for a route author can be limited to the following. Bind it only in that tenant's namespace; this example does not create a RoleBinding or automatically grant access. If tenants also manage Services or application workloads, audit those permissions separately because backend ownership defines which origins they can expose.

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: cfgate-route-author
  namespace: team-a
rules:
  - apiGroups: [gateway.networking.k8s.io]
    resources: [httproutes]
    verbs: [get, list, watch, create, update, patch, delete]
```

Do not supplement this Role with broad Secret, infrastructure CR, Gateway, ReferenceGrant, claim ConfigMap, or impersonation permissions. The operator itself needs its generated RBAC; a ReferenceGrant controls references, not the identities allowed to create objects. Restrict administrator namespaces with Kubernetes RBAC/admission and scope Cloudflare tokens to required accounts, zones, and API permissions. Same-namespace references are allowed without a grant, so namespace membership is a trust boundary. cfgate does not provide a per-user Cloudflare account/image/argument allowlist.

## Explicit cross-namespace grants

For credential Secrets, cfgate requires a ReferenceGrant in the Secret namespace whose `from` selects the actual CR kind and namespace, and whose `to` selects the core Secret and name. Omitting `to.name` allows every Secret in that namespace; prefer named grants.

```yaml
apiVersion: gateway.networking.k8s.io/v1beta1
kind: ReferenceGrant
metadata:
  name: allow-edge-token
  namespace: cfgate-system
spec:
  from:
    - group: cfgate.io
      kind: CloudflareTunnel
      namespace: edge
  to:
    - group: ""
      kind: Secret
      name: cloudflare-api-token
```

Use the actual source kind (`CloudflareDNS`, `CloudflareAccessPolicy`, or `CloudflareAccessApplication`) for other consumers. Each source is checked before the Secret is read. Primary and fallback credentials follow the same rule. All tokens within one Secret share that grant; Kubernetes ReferenceGrant does not authorize individual data keys. Put tokens with different trust boundaries in separate Secrets. Client caching separately keys Secret UID, resourceVersion, selected token key, and normalized request/configuration settings; selecting one key cannot reuse a client created for another key.

Cross-namespace Gateway-to-Tunnel references need a grant from `gateway.networking.k8s.io/Gateway` to `cfgate.io/CloudflareTunnel`. DNS-to-Tunnel and inherited AccessApplication-to-Tunnel references likewise require their source kind. Inherited credentials also require both the inheriting CR and the owning Tunnel to be authorized for the resolved Secret. HTTPRoute backend grants remain from `gateway.networking.k8s.io/HTTPRoute` to core `Service`. Grant lookup failures fail the operation; missing APIs or temporary read failures do not silently grant access.

The supported HTTPRoute subset and invalid-backend HTTP 500 behavior are documented in the [Gateway API primer](gateway-api-primer.md#supported-httproute-behavior). Listener/class/hostname rejection prevents attachment. Invalid backend references on an attached route retain their matches as failure responses, preserving valid sibling rules and preventing fallback to broader routes.

## Upgrade to v0.2.0-alpha.6

The stricter defaults intentionally reject ambiguous ownership accepted by earlier alpha releases. Inspect desired objects and remote inventory before rollout; test the following migration in a disposable environment first.

1. Stop previous cfgate writers for the resources being migrated. Inventory the Kubernetes CR UIDs, connector owner references, Cloudflare account/tunnel IDs, DNS data records, companion TXT records, and relevant token permissions. Back up the desired manifests and ownership records.
2. Configure one stable installation namespace using the manager's `POD_NAMESPACE` or `--installation-namespace`. Its Kubernetes UID identifies the DNS installation. Add required cross-namespace ReferenceGrants. For local execution, explicitly select the intended cluster and installation namespace.
3. Existing connector objects must already have the CloudflareTunnel's controller owner UID. If objects are unowned or foreign, inspect them and deliberately migrate/recreate them under that owner; cfgate will not overwrite them automatically. The same rule applies to managed Access service-token Secrets.
4. For a known, unclaimed remote tunnel, set `cfgate.io/adopt-existing: "true"` on the administrator-owned CloudflareTunnel. cfgate creates an immutable `cfgate-tunnel-<account/tunnel hash>` ConfigMap claim in the installation namespace. A foreign claim or another CR referencing the same account/tunnel ID blocks adoption. Remove the opt-in after successful migration. Never remove a claim while its owner is still active.
5. DNS now persists `<installation namespace UID>/<CloudflareDNS UID>` in `status.ownerId` and writes exact owner markers on data and TXT records. `spec.ownership.ownerId` cannot override this identity. After stopping the old owner, inspect any legacy TXT claim and deliberately remove that obsolete claim if migrating to a new owner. For legacy data with no exact owner marker, temporarily set `cfgate.io/adopt-existing: "true"`. cfgate can then stamp that unmarked record with the new identity. Existing foreign data markers are never adopted; transfer them manually after verifying both owners are stopped. Remove the opt-in afterward.
6. Confirm current conditions, captured remote tunnel configuration, DNS ownership markers, connector availability, and application requests before allowing normal traffic. A policy-skipped DNS update reports `Skipped`, contributes to pending records, and does not report synchronized readiness. `cleanupPolicy.onlyManaged: false` no longer bypasses ownership checks.

Deleting/recreating the installation namespace or CR changes its UID and requires the same inspected migration. Preserve ownership status for cleanup; do not clear it to bypass a conflict. Explicit `cfgate.io/deletion-policy: orphan` retains external resources; orphaned tunnel claims also remain until an administrator verifies no writer still owns the remote tunnel.

Data records use the exact compact comment `cfgate/owner=<owner-id>` to fit Cloudflare's DNS comment limit. The persisted installation/resource owner identity and companion TXT format do not change; existing exact heritage-prefixed data comments remain recognized. A foreign or ambiguous marker never authorizes adoption or deletion.

## Coordination limits

The immutable claim ConfigMap provides one winner per account/tunnel ID **within one installation namespace**. Different installation namespaces or clusters have separate Kubernetes APIs and require external coordination. Cloudflare tunnel names alone do not prove ownership, and a stored tunnel ID alone does not authorize mutation.

DNS operations reread data and companion TXT records, reject foreign or ambiguous observable claims, and verify the record ID before deletion. Cloudflare's DNS API does not offer conditional writes for these operations. A writer can race between the last read and the write; TXT lookup is not a distributed lock. Use a single coordinated writer for a hostname across installations. Tests cover observable competing creations and ownership changes, not a nonexistent global atomic guarantee.

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

## Upgrade from v0.2.0-alpha.5 to v0.2.0-alpha.6

This upgrade changes authorization, ownership and persisted status as well as the controller image. The stricter defaults intentionally reject ambiguous ownership accepted by alpha.5. Inspect desired objects and remote inventory before rollout; test the following migration in a disposable environment first.

1. Stop previous cfgate writers for the resources being migrated. Inventory the Kubernetes CR UIDs, connector owner references, Cloudflare account/tunnel IDs, DNS data records, companion TXT records, and relevant token permissions. Back up the desired manifests and ownership records.
2. Configure one stable installation namespace using the manager's `POD_NAMESPACE` or `--installation-namespace`. Its Kubernetes UID identifies the DNS installation. Add required cross-namespace ReferenceGrants. For local execution, explicitly select the intended cluster and installation namespace.
3. Existing connector objects must already have the CloudflareTunnel's controller owner UID. If objects are unowned or foreign, inspect them and deliberately migrate/recreate them under that owner; cfgate will not overwrite them automatically. The same rule applies to managed Access service-token Secrets.
4. For a known, unclaimed remote tunnel, set `cfgate.io/adopt-existing: "true"` on the administrator-owned CloudflareTunnel. cfgate creates an immutable `cfgate-tunnel-<account/tunnel hash>` ConfigMap claim in the installation namespace. A foreign claim or another CR referencing the same account/tunnel ID blocks adoption. Remove the opt-in after successful migration. Never remove a claim while its owner is still active.
5. DNS now persists `<installation namespace UID>/<CloudflareDNS UID>` in `status.ownerId` and writes exact owner markers on data and TXT records. `spec.ownership.ownerId` cannot override this identity. After stopping the old owner, inspect any legacy TXT claim and deliberately remove that obsolete claim if migrating to a new owner. For legacy data with no exact owner marker, temporarily set `cfgate.io/adopt-existing: "true"`. cfgate can then stamp that unmarked record with the new identity. Existing foreign data markers are never adopted; transfer them manually after verifying both owners are stopped. Remove the opt-in afterward.
6. Install all four matching CRD schemas and manager RBAC before starting alpha.6. Old schemas must not prune the new ownership, credential selection, lifecycle and Access dependency status fields. Chart users should follow the [1.4.0 to 1.5.0 upgrade](https://github.com/cfgate/helm-chart/blob/main/README.md#upgrade-from-140-to-150); external CRD/RBAC installations require the same updates.
7. Confirm current conditions, captured remote tunnel configuration, DNS ownership markers, connector availability, and application requests before allowing normal traffic. A policy-skipped DNS update reports `Skipped`, contributes to pending records, and does not report synchronized readiness. `cleanupPolicy.onlyManaged: false` no longer bypasses ownership checks.

Alpha.6 does not automatically require Access on existing routes. Use `cfgate.io/access-required` to make publication depend on a selected Access application; independently managed Access applications can still protect their matching domains. The deprecated `cfgate.io/access-policy` annotation does not enforce authentication; use the [Access-required contract](access-required.md) when protection is intended. Review the supported route subset before rollout: unsupported restrictions are rejected and invalid attached backends produce matching failure responses. Controller Pods now disable Service environment injection to avoid the metrics Service port collision in issue #85; explicit flags continue to take precedence over environment values.

Deleting/recreating the installation namespace or CR changes its UID and requires the same inspected migration. Preserve ownership status for cleanup; do not clear it to bypass a conflict. Explicit `cfgate.io/deletion-policy: orphan` retains external resources; orphaned tunnel claims also remain until an administrator verifies no writer still owns the remote tunnel.

Data records use the exact compact comment `cfgate/owner=<owner-id>` to fit Cloudflare's DNS comment limit. The persisted installation/resource owner identity and companion TXT format do not change; existing exact heritage-prefixed data comments remain recognized. A foreign or ambiguous marker never authorizes adoption or deletion.

## Coordination limits

The immutable claim ConfigMap provides one winner per account/tunnel ID **within one installation namespace**. Different installation namespaces or clusters have separate Kubernetes APIs and require external coordination. Cloudflare tunnel names alone do not prove ownership, and a stored tunnel ID alone does not authorize mutation.

### Claim permissions after alpha.6

The source installation manifests now grant ConfigMap `get/create/delete` through
a Role in the manager's installation namespace, rather than through the
cluster-wide manager role. This is an RBAC packaging change; the CRDs and
controller's claim behavior are unchanged. Already published alpha.6 manifests
retain their original permissions. Helm chart 1.5.0 supplies the narrower grant
while continuing to run the alpha.6 image.

Kustomize's default installation namespace is `cfgate-system`. If you separately
override `--installation-namespace`, place the claim Role and RoleBinding in
that existing namespace and keep the binding's service-account subject in the
manager's actual namespace. Apply both the narrower ClusterRole and the new
namespaced binding: adding a Role alone does not revoke the old cluster-wide
grant. ConfigMaps elsewhere must no longer be accessible through this grant.

This still permits operations on other ConfigMaps within the installation
namespace; Kubernetes RBAC cannot select claims by name prefix. Ownership checks
remain in the controller. Other permissions needed to manage Secrets and
Deployments remain cluster-wide, so this change does not make a compromised
manager harmless.

DNS operations reread data and companion TXT records, reject foreign or ambiguous observable claims, and verify the record ID before deletion. Cloudflare's DNS API does not offer conditional writes for these operations. A writer can race between the last read and the write; TXT lookup is not a distributed lock. Use a single coordinated writer for a hostname across installations. Tests cover observable competing creations and ownership changes, not a nonexistent global atomic guarantee.

## Upgrade from v0.2.0-alpha.6 to v0.2.0-alpha.7

- install the matching CRDs before upgrading; Access status now retains `ownerId`
- for existing Access applications, policies and managed tokens, verify exclusive ownership, stop other writers, then set `cfgate.io/adopt-existing: "true"` on their owning Access CRs
- remove the adoption annotation after reconciliation succeeds; keep the installation namespace and its UID stable
- new remote policy and token names include an ownership suffix; Kubernetes references and `serviceTokens[].name` stay unchanged
- over-limit tunnel configurations now serve HTTP 503 until they fit; remove excess entries or raise the relevant limits
- origin CA Secret updates now roll connector Pods; selected keys must contain PEM certificates
- new connector defaults are pinned by digest; existing CRs retain their stored image, so set `spec.cloudflared.image` to the [new default](cloudflare-tunnel.md#image) to opt into the pin

Access ownership uses the installation namespace UID and resource UID, plus
immutable claims keyed by account, resource kind and remote ID. New application
owner tags and policy/token names distinguish installations and recreated CRs.
A matching display name alone does not authorize mutation. Existing remote IDs
and legacy application tags are considered for adoption only with the explicit
annotation; ambiguous inventories and foreign claims are rejected.

The claims coordinate one installation. They cannot arbitrate deliberate adoption
of the same legacy remote resource by independent clusters. Verify exclusive
ownership before adoption. Do not change an Access resource's Cloudflare account
in place; first delete it normally, or deliberately orphan it and create a new
resource with the new credentials. Orphaning retains its remote resources and
claims for administrator review.

Successful remote operations are checkpointed, and retries or deletion recover
missing observations from the ownership markers. An unrecoverable or ambiguous
inventory blocks cleanup rather than permitting deletion by name. Keep the
original credential Secret available until cleanup completes.

## Controller removal and decommissioning

Removing the controller does not remove its Cloudflare resources. For a temporary
controller-only removal, retain CRDs, custom resources, credential Secrets,
ReferenceGrants and the installation namespace with its ownership claims. Keep a
copy of the matching controller version and configuration for reinstallation.
Existing connector workloads and remote configuration may keep serving traffic;
updates, drift repair, token renewal and finalization stop while the controller is
absent. Preserve the namespace UID when restoring the installation.

For full decommissioning, keep the controller, its RBAC, credentials and grants
available until cleanup finishes:

1. Inventory the resources belonging to this installation, including remote IDs,
   pending DNS writes and pending credential distribution. Resolve failed or
   interrupted operations before removing their dependencies.
2. Remove the intended HTTPRoutes or their attachments. Wait for the affected
   tunnels to publish withdrawal. Confirm the remote configuration if publication
   fails; deleting a Kubernetes object alone does not prove remote withdrawal.
3. Delete the installation's CloudflareDNS and CloudflareAccessApplication objects
   and wait for finalization. Keep referenced tunnels and policies until this step
   finishes. Check DNS retention policies and deletion-policy annotations first;
   deliberate retention requires a separate inventory and owner handoff.
4. Delete its CloudflareAccessPolicy objects and wait for policy and managed-token
   cleanup. Then delete its CloudflareTunnel objects and wait for connector drain
   and remote tunnel cleanup.
5. Verify the intended remote resources are gone or deliberately retained. Remove
   the controller and its RBAC, then unused credentials, claims and namespaces.
   Remove CRDs only when no installation still uses them.

Use explicit resource names and namespaces; a Helm release does not own every
cfgate object in a cluster. If finalization stalls, inspect conditions, events and
controller logs while the required credentials and grants still exist. Do not
remove finalizers or recovery status to force completion: that bypasses cleanup
and can strand external resources. See the [DNS retention rules](cloudflare-dns.md)
and [Access-required ordering limits](access-required.md).

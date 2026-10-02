# CloudflareAccessPolicy

`CloudflareAccessPolicy` manages one reusable account-level Cloudflare Access policy. It does not target a Gateway or HTTPRoute directly. Attach it to Gateway API host/path targets with [CloudflareAccessApplication](cloudflare-access-application.md).

## Spec

Required fields:

```yaml
apiVersion: cfgate.io/v1alpha1
kind: CloudflareAccessPolicy
metadata:
  name: allow-employees
  namespace: cfgate-system
spec:
  cloudflareRef:
    name: cloudflare-credentials
    accountId: "<account-id>"
  name: allow-employees
  decision: allow
  include:
    - emailDomain:
        domain: example.com
```

Key fields:

| Field | Description |
|---|---|
| `cloudflareRef` | Secret and account for Cloudflare Access policy operations. |
| `name` | Cloudflare reusable policy display name. |
| `decision` | `allow`, `deny`, `bypass`, or `non_identity`. |
| `include` | Required Access rules. Any include rule can match. |
| `exclude` | Optional rules that exclude a request when any match. |
| `require` | Optional rules that must all match. |
| `sessionDuration` | Go duration format, for example `300ms`, `30m`, `2h45m`. |
| `purposeJustificationRequired` | Require a user justification. |
| `approvalRequired` / `approvalGroups` | Require approval by emails or an email list UUID. |
| `serviceTokens` | Create Cloudflare service tokens and write credentials to Kubernetes Secrets. |

## Rules

Supported rule types include:

- `ip.ranges`
- `ipList.id` (name lookup is not supported)
- `country.codes`
- `everyone`
- `serviceToken.tokenId` or `serviceToken.name`
- `anyValidServiceToken`
- `email.addresses`
- `emailList.id` (name lookup is not supported)
- `emailDomain.domain`
- `oidcClaim`
- `gsuiteGroup`
- `group.id`

`serviceToken.name` references an entry in `spec.serviceTokens`; the controller creates the token before syncing the policy and uses the created Cloudflare ID in the policy rule.

Each `include`, `exclude`, and `require` item must specify exactly one selector. The controller rejects invalid selector combinations before calling Cloudflare so status carries a local validation error.

Decision compatibility:

- `non_identity` requires at least one `include` rule using `serviceToken` or `anyValidServiceToken`.
- `bypass` cannot use identity selectors such as `email`, `emailList`, `emailDomain`, `oidcClaim`, `gsuiteGroup`, or `group`.

## Status

| Field | Description |
|---|---|
| `policyId` | Cloudflare reusable policy ID. |
| `accountId` | Account used for reconciliation. |
| `reusable` | Whether Cloudflare reports the policy as reusable. |
| `appCount` | Number of Cloudflare Access Applications linked to the policy. |
| `serviceTokenIds` | Created token name to Cloudflare token ID. |
| `credentialSecretRef` | Resolved credentials Secret cached for cleanup. Namespace is always stored explicitly. |
| `observedGeneration` | Last reconciled generation. |

Conditions:

- `Ready`
- `CredentialsValid`
- `ServiceTokensReady`
- `PolicySynced`

## Deletion

Deletion removes the reusable policy only when Cloudflare reports `appCount == 0`. If the policy is still linked to any application, finalization blocks and retries. Cleanup uses cached `accountId` and `credentialSecretRef` when available, but the referenced credentials Secret must still exist. Restore the Secret or set `cfgate.io/deletion-policy=orphan` before deletion to leave Cloudflare resources in place and remove the Kubernetes finalizer.

## Service Token Secrets

When `spec.serviceTokens` changes, tokens removed from the spec are revoked in Cloudflare and removed from `status.serviceTokenIds`.

The controller owns service token Secrets it creates. It updates only Secrets controlled by the same policy UID; unmanaged or foreign Secrets are rejected before creating or rotating a token. If an existing unexpired Cloudflare service token has a missing, incomplete, or client-ID-mismatched Kubernetes Secret, the controller rotates the token and writes a fresh Secret. Cloudflare does not return the client secret after creation or rotation, so a Secret with the expected client ID and a non-empty client secret is treated as current.

## Example With Service Token

```yaml
apiVersion: cfgate.io/v1alpha1
kind: CloudflareAccessPolicy
metadata:
  name: ci-service-auth
  namespace: cfgate-system
spec:
  cloudflareRef:
    name: cloudflare-credentials
    accountId: "<account-id>"
  name: ci-service-auth
  decision: non_identity
  include:
    - serviceToken:
        name: ci-token
  serviceTokens:
    - name: ci-token
      duration: 8760h
      secretRef:
        name: ci-access-token
```

## Credential data keys

`spec.cloudflareRef.secretKeys.apiToken` selects the Secret data key containing the Cloudflare API token. It defaults to `CLOUDFLARE_API_TOKEN`. A missing or empty selected key is an error; cfgate does not fall back to another token stored in the same Secret. Clients cached for different keys remain separate.

Credential cleanup preserves `status.credentialSecretKeys` alongside the resolved Secret reference and account. Cross-namespace credential references require a Secret ReferenceGrant from `CloudflareAccessPolicy`; see [authorization and ownership](authorization-and-ownership.md).

If storing a rotated token fails, cfgate retains the remote token for recovery
rather than deleting it. Reconciliation retries storage through rotation when
needed; resolve Secret ownership and write errors before retrying.

New remote policies and managed tokens include an installation/CR ownership
suffix in their names. Continue using the declared `serviceTokens[].name` in
policy rules; status maps it to the remote token ID. The controller verifies
account/resource claims before changing, rotating or deleting an existing object.
Name-only adoption is not supported by reconciliation. See the [alpha.7 migration notes](authorization-and-ownership.md#upgrade-from-v020-alpha6-to-v020-alpha7) for existing resources.

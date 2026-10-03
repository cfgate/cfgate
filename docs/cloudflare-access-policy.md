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

## Service token lifecycle

Each managed token needs a unique name and destination Secret within its policy.
`duration` is a positive number of hours and defaults to `8760h`. cfgate renews
expiration before its final 10%, capped at 24 hours, with up to one additional
minute reserved for queue delay and a retry. Successful reconciliations requeue
at the earliest token renewal deadline or after five minutes, whichever is sooner.
A one-hour token is first due seven minutes before expiration.
Changing the duration also renews expiration. An unchanged token outside that
window is left alone. Disabled tokens report an error; cfgate does not enable them.
The duration remains a renewable lifetime while the token is desired, not a
one-time deadline for revoking access. Removing a token from `spec.serviceTokens`
revokes it in Cloudflare.

Renewal preserves the client secret. It uses Cloudflare's duration update API;
the separate refresh endpoint always adds a year and would not preserve a custom
duration. An enabled, unexpired token with unchanged name and duration uses an
expiration-only operation. It retains ownership checks and application locks,
but does not withdraw healthy `access-required` forwarding. Expired tokens,
configuration edits, and secret rotations still require withdrawal. Failed renewal
verification reports the policy unavailable; continuity is not guaranteed during
provider failures or after expiration. See [Cloudflare's service token lifecycle](https://developers.cloudflare.com/cloudflare-one/access-controls/service-credentials/service-tokens/).

### Secret distribution

cfgate rotates a token when its Secret is missing, incomplete, has a different
client ID, or records an unfinished rotation. The destination must be controlled
by the same policy UID. An immutable Secret that needs new credentials is rejected
before remote mutation.

Before creating or rotating credentials, cfgate writes
`cfgate.io/service-token-rotation-pending: "true"` on the destination Secret.
It clears that marker in the same write that stores the credentials. If the
remote operation succeeds but the Secret write fails, the marker survives a
restart and causes another rotation. Resolve write errors before retrying; do
not remove the marker to suppress recovery.

`rotationOverlap` defaults to `0h`, which invalidates the previous secret
immediately. Set it to a whole number of hours up to `720h` to allow consumers
time to reload the stored credentials. Only the previous secret receives the
overlap; repeated recovery rotations can invalidate older credentials.
[Cloudflare defines the overlap behavior](https://developers.cloudflare.com/api/resources/zero_trust/subresources/access/subresources/service_tokens/methods/rotate/).

A stored Secret does not prove that a consumer has loaded it. cfgate does not
restart consumer workloads. Cloudflare preserves the client ID during rotation
and does not return the secret on reads, so cfgate cannot reliably detect an
out-of-band rotation from client ID equality. After an external rotation,
remove the stored `CF_ACCESS_CLIENT_SECRET` key to request recovery, then reload
consumers after cfgate writes the replacement.

### Service authentication

Use `decision: non_identity` for service-token authentication and send both
`CF-Access-Client-Id` and `CF-Access-Client-Secret` on each request. Cloudflare's
strict service-token mode requires Service Auth policies, returns 401/403 for
failed authentication, and does not issue a reusable authorization cookie.
Cloudflare documents that new organizations created on or after October 5, 2026
use strict mode permanently. Existing organizations may enable it separately;
cfgate does not change that account-wide setting. An Allow policy is not a
portable replacement for Service Auth. See the
[provider's authentication contract](https://developers.cloudflare.com/cloudflare-one/access-controls/service-credentials/service-tokens/).

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
      rotationOverlap: 1h
      secretRef:
        name: ci-access-token
```

## Credential data keys

`spec.cloudflareRef.secretKeys.apiToken` selects the Secret data key containing the Cloudflare API token. It defaults to `CLOUDFLARE_API_TOKEN`. A missing or empty selected key is an error; cfgate does not fall back to another token stored in the same Secret. Clients cached for different keys remain separate.

Credential cleanup preserves `status.credentialSecretKeys` alongside the resolved Secret reference and account. Cross-namespace credential references require a Secret ReferenceGrant from `CloudflareAccessPolicy`; see [authorization and ownership](authorization-and-ownership.md).

New remote policies and managed tokens include an installation/CR ownership
suffix in their names. Continue using the declared `serviceTokens[].name` in
policy rules; status maps it to the remote token ID. The controller verifies
account/resource claims before changing, rotating or deleting an existing object.
Name-only adoption is not supported by reconciliation. See the [alpha.7 migration notes](authorization-and-ownership.md#upgrade-from-v020-alpha6-to-v020-alpha7) for existing resources.

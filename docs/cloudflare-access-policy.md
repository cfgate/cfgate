# CloudflareAccessPolicy

`CloudflareAccessPolicy` manages a reusable account-level Access policy and optional service tokens; [CloudflareAccessApplication](cloudflare-access-application.md) attaches it to Gateway API host/path targets.

This namespaced resource uses `cfgate.io/v1alpha1`. It does not select a Gateway or HTTPRoute directly. [Getting started](getting-started.md) covers installation.

## Spec

| Field | Type | Default | Description |
|---|---|---|---|
| `cloudflareRef` | object | *none* | Required API-token Secret reference and Cloudflare account. See [credential fields](#credential-data-keys). |
| `name` | string | *none* | Required policy display name, 1 to 255 characters. |
| `decision` | string | `allow` | `allow`, `deny`, `bypass`, or `non_identity`. |
| `include` | rule list | *none* | Required, 1 to 25 selectors; any may match. |
| `exclude` | rule list | *none* | Up to 25 selectors; any match excludes the request. |
| `require` | rule list | *none* | Up to 25 selectors; all must match. |
| `sessionDuration` | string | Application duration | Override session lifetime, at most 32 characters, using duration units `ns`, `us`, `ms`, `s`, `m`, or `h`. |
| `purposeJustificationRequired` | boolean | `false` | Require a user justification. |
| `purposeJustificationPrompt` | string | *none* | Justification prompt, at most 1024 characters. |
| `approvalRequired` | boolean | `false` | Require approval. |
| `approvalGroups` | list | *none* | Up to 10 approver groups; each needs nonempty `emails` or `emailListUuid`. |
| `approvalGroups[].emails` | string list | *none* | Up to 50 approver addresses. |
| `approvalGroups[].emailListUuid` | string | *none* | Cloudflare email-list UUID, at most 36 characters. |
| `approvalGroups[].approvalsNeeded` | integer | `1` | Required approvals, at least 1. |
| `serviceTokens` | list | *none* | Up to 10 managed tokens with unique names and destination Secrets. |
| `serviceTokens[].name` | string | *none* | Required declared token name, 1 to 255 characters. |
| `serviceTokens[].duration` | string | `8760h` | Renewable token lifetime in positive whole hours. |
| `serviceTokens[].rotationOverlap` | string | `0h` | Previous-secret overlap in whole hours, from `0h` to `720h`. |
| `serviceTokens[].secretRef.name` | string | *none* | Required destination Secret in the policy namespace. |

## Examples

Use an email-domain rule for users authenticated through an appropriate identity provider:

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

### Example With Service Token

Reference a managed token by its declared name; cfgate creates it before syncing the policy:

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

## Rules

Each item in `include`, `exclude`, and `require` must contain exactly one selector:

| Selector | Required fields or value |
|---|---|
| `ip` | `ranges`: source IP CIDR ranges. |
| `ipList` | `id`: Cloudflare IP-list ID; name lookup is unsupported. |
| `country` | `codes`: ISO 3166-1 alpha-2 country codes. |
| `everyone` | `true`. |
| `serviceToken` | `tokenId` or `name` referencing `spec.serviceTokens`. |
| `anyValidServiceToken` | `true`. |
| `email` | `addresses`: authenticated email addresses. |
| `emailList` | `id`: Access email-list ID; name lookup is unsupported. |
| `emailDomain` | `domain`: authenticated email domain. |
| `oidcClaim` | `identityProviderId`, `claimName`, and `claimValue`. |
| `gsuiteGroup` | `identityProviderId` and Google Workspace group `email`. |
| `group` | `id`: Cloudflare Access Group ID. |

`everyone: false` and `anyValidServiceToken: false` are invalid, not inverse matches. Remove the rule to disable it. The schema rejects them, and the controller validates existing objects before modifying policies or tokens.

`non_identity` requires a service-token or any-valid-service-token include rule. `bypass` rejects identity selectors: `email`, `emailList`, `emailDomain`, `oidcClaim`, `gsuiteGroup`, and `group`.

## Credential data keys

| Field under `cloudflareRef` | Default | Description |
|---|---|---|
| `name` | *none* | Required API-token Secret name. |
| `namespace` | Resource namespace | Secret namespace; a cross-namespace reference requires a Secret ReferenceGrant from this resource kind. |
| `secretKeys.apiToken` | `CLOUDFLARE_API_TOKEN` | Selected Secret data key. Missing or empty data fails; no other key is tried. |
| `accountId` | *none* | Cloudflare account ID. Supply this or `accountName`. |
| `accountName` | *none* | Account name to resolve through the API. |

Account Access Apps and Policies Edit permission is required for policy operations; managed tokens also need Access Service Tokens Edit. Account-name lookup needs Account Settings Read. Clients cached for different selected token keys remain separate.

Cleanup caches the account, resolved Secret reference, and selected key. The Secret must still exist and remain authorized. See [authorization and ownership](authorization-and-ownership.md) for grants and migration.

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
but does not withdraw healthy `access-required` forwarding. The request resends
the verified current name and duration, then checks that expiration advanced. It
does not set secret-version, enabled-state, or overlap fields. Expired tokens,
configuration edits, and secret rotations still require withdrawal. Failed renewal
verification reports the policy unavailable; continuity is not guaranteed during
provider failures or after expiration. See [Cloudflare's service token lifecycle](https://developers.cloudflare.com/cloudflare-one/access-controls/service-credentials/service-tokens/).

### Secret distribution

The destination Secret stores `CF_ACCESS_CLIENT_ID` and `CF_ACCESS_CLIENT_SECRET`. Consumers must read both values.

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

Use `decision: non_identity` and send both `CF-Access-Client-Id` and `CF-Access-Client-Secret` on requests. Do not rely on an Allow policy or reusable cookie for service authentication. Account-level strict-mode behavior belongs to Cloudflare; cfgate does not change it. See [Cloudflare's service-token authentication contract](https://developers.cloudflare.com/cloudflare-one/access-controls/service-credentials/service-tokens/).

## Status

| Field | Description |
|---|---|
| `policyId` | Remote reusable policy ID. |
| `ownerId` | Installation and resource identity used to verify remote ownership. |
| `accountId` | Account used for reconciliation and cleanup. |
| `reusable` | Whether Cloudflare reports the policy as reusable. |
| `appCount` | Number of applications linked to the policy. |
| `serviceTokenIds` | Declared token names mapped to Cloudflare IDs. |
| `credentialSecretRef` | Cleanup Secret reference with explicit namespace. |
| `credentialSecretKeys` | Token-key selection retained for cleanup. |
| `observedGeneration` | Last processed generation. |

Conditions are `Ready`, `CredentialsValid`, `ServiceTokensReady`, and `PolicySynced`.

## Deletion

Remote policy deletion waits until Cloudflare reports `appCount == 0`. Detach applications first; otherwise finalization blocks and retries. Restore missing credentials to permit cleanup, or explicitly use `cfgate.io/deletion-policy: orphan` to leave the policy and tokens in Cloudflare.

New policies and managed tokens receive installation/resource ownership suffixes in remote names. Continue using the declared token names in rules; status maps them to remote IDs. cfgate checks account and resource claims before modifying, rotating, or deleting existing objects. Matching names alone never authorize adoption. Follow [ownership migration](authorization-and-ownership.md) for existing resources.

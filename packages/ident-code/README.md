# @twinsearth/im2a-ident-code

Compatibility codec and in-memory registry for the IM2A identity-code contract.

## Formats

```text
USER_CODE       25 chars = IM2A_YYYYMMDD + 12-digit user number
PUBLIC_UID      35 chars = IM2A_YYYYMMDD + 12-digit user + 10-digit agent subcode
AUTH_CODE      128 chars = PUBLIC_UID(35) + 93-char opaque server-issued secret
IM2A_NUMBER     12 digits (11 payload + Luhn check digit)
AGENT_SUBCODE   10 digits, owner-scoped
AGENT_ADDRESS   12-digit number + "." + 10-digit subcode
```

The 93-character suffix is **not a private key**. It is a random opaque
secret; the server stores only an HMAC fingerprint. The signing key stays in
KMS and never enters a code, profile, log, or connector package.

## Security invariants

- Public codes resolve by lookup only; they never authorize.
- `AUTH_CODE` is private, short-lived, single-use, audience-bound, and scope-bound.
- `resolvePublicCode` rejects auth codes and returns a minimal public projection.
- No token, secret, or internal ID is returned by the public resolver.

## Usage

```js
import { CodeRegistry } from '@twinsearth/im2a-ident-code';

const registry = new CodeRegistry({ pepper: process.env.IDENT_PEPPER });
const user = registry.createUser({ createdAt: '2026-10-30', displayName: 'Alice' });
const agent = registry.issueAgentSubcode(user.userId);
const issued = registry.issueAuthCode({
  userId: user.userId,
  agentSubcode: agent.agentSubcode,
  scope: ['read:registry'],
  audience: 'workbuddy',
  ttlMs: 10 * 60 * 1000,
});
const verified = registry.verifyAuthCode(issued.code);
```

The registry is an in-memory reference implementation for conformance tests.
Production must use the separate public/credential planes described in
`docs/design/IM2A-CODE-SERVICE-ARCHITECTURE.md`.

## Cross-end rule

Server, Harness plugins, browser extension, and WeChat Bot must use this
contract for parsing/validation. Only the credential plane may verify
`AUTH_CODE`; public surfaces must never accept it as a lookup key.

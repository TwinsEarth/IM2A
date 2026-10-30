# IM2A Identity-Code Contract 1.0

Status: implemented as `@twinsearth/im2a-ident-code` (reference + tests).
Parent architecture: `docs/design/IM2A-CODE-SERVICE-ARCHITECTURE.md`.
Security decision: `docs/adr/0004-ident-code-format.md`.

## 1. Code types

| Type | Shape | Public? | Credential? |
|---|---|---|---|
| User code | `IM2A_YYYYMMDD` + 12 digits (25) | yes | no |
| Public UID | `IM2A_YYYYMMDD` + 12-digit user + 10-digit Agent (35) | yes | no |
| IM2A number | 12 digits, Luhn check digit | yes | no |
| Agent subcode | 10 digits, owner-scoped | owner-visible | no |
| Agent address | `number.subcode` | yes | no |
| Auth code | public UID (35) + 93-char opaque secret (128) | **no** | yes |
| Grant | userId + subcode + scope + audience + expiry | private | yes |

## 2. Non-negotiable rules

```text
Public code → find who / route where. Never authorize.
Auth code   → what may be done. Never searchable, never logged.
Subcode     → which Agent under the owner. Never cross-user authority.
```

The 93-character suffix is an opaque secret, not a private key. The server
stores an HMAC fingerprint; the signing pepper stays in KMS. A code that
embeds a real private key is invalid and must be revoked.

## 3. Validation pipeline

1. Length and ASCII normalization (no full-width or zero-width characters).
2. Date prefix must be a real calendar date.
3. User/Agent segments must be exactly 12/10 digits.
4. Numbers must pass Luhn.
5. Auth code must pass the public-UID pattern plus the 93-char secret pattern.
6. Public resolution is lookup-only and returns a fixed minimal projection.
7. Auth verification performs HMAC lookup, public-UID match, expiry/revocation/
   single-use checks, then returns scope/audience.

Client-side syntax validation is UX only and must never grant access.

## 4. Levels and permissions

Levels are server-side policy, not characters embedded in a code.

| Level | Subject | Capability |
|---|---|---|
| L0 | root/federation | node registration and topology; never a user code |
| L1 | user | public search and self-service |
| L2 | user/agent | scoped read |
| L3 | user/agent | scoped write, audience-bound |
| L4 | owner/master | Agent subcode allocation, revocation, admin only with step-up |

Rules:

- `agent scope ⊆ owner scope`; child authority never exceeds parent.
- Revoking the owner revokes all child Agent credentials.
- Tier (how many codes/TTL) is data, not a hard-coded enum.
- A public code cannot be a JWT `sub` or a bearer token.

## 5. Registry management

- Postgres is the source of truth; Redis is cache/rate-limit/lease only.
- Numbers are never recycled after activation; other public codes follow the
  quarantine/tombstone policy in the architecture document.
- Auth codes are random, stored as HMAC, never recycled.
- Single writer for code lifecycle; public resolver is read-only and has no
  access to the credential schema.
- Append-only audit records include actor, subject, code type, result,
  request_id; never the code or token plaintext.

## 6. Cross-end contract

| Endpoint | Must do | Must not do |
|---|---|---|
| Server/API | resolve public codes via read-only resolver; verify auth only in credential plane | accept a public code as Bearer/JWT subject |
| DSH plugin | pass the shared code as a config value; never parse it as a credential | write auth codes to profiles/logs |
| WorkBuddy connector | use token schema + `${VAR}` placeholders | embed code/secret in connector package |
| Doubao/remote MCP | use HTTPS, audience-bound auth | transmit auth code in URL/query |
| Browser extension | store codes in isolated storage; show public codes; never log auth codes | use public code to call privileged APIs |
| WeChat Bot | bind via server-issued challenge, not first-message trust; never transmit auth codes in chat | treat wxid as authentication |

## 7. Required tests

```text
exact lengths 25/35/128/12/10
invalid calendar date
Luhn failure
public code cannot authorize
auth code cannot be resolved publicly
tamper public part/secret
expiry, revocation, single-use
owner-scoped subcode allocation
scope ⊆ parent, audience required
no secret/token in public projection
```

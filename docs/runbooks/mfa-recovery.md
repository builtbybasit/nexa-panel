# Runbook: Recover an account that lost its second factor

MFA enrollment in Nexa Panel is **optional**. An account that never enrolled
signs in on its password alone and can never be locked out this way. This
runbook is for the account that *did* enroll and then lost the factor.

Work down this list. Only the last step needs a shell on the node, and only the
last step is irreversible for the enrolled factor.

| Situation | Path | Who can do it |
| --- | --- | --- |
| Authenticator lost, recovery codes in hand | Sign in, then **Replace factor** | The account holder, any role |
| Authenticator working, wants MFA off | **Account → Security → Disable** | The account holder, any role |
| Both the authenticator **and** every recovery code are gone | `sudo nexa mfa reset` on the node | Root on the node |

## 1. Recovery code in hand — re-pair without help

Any enrolled role can do this; it is not an administrator privilege. The
recovery code is the credential.

1. Sign in with username and password as usual.
2. At the multi-factor prompt, choose **Use a recovery code / Replace factor**
   and submit one unused recovery code.
3. The panel retires the lost factor, returns a fresh secret and QR code, and
   **signs out every other session** for the account. Scan the new secret and
   confirm a six-digit code to finish pairing.

The code is consumed in the same transaction that rotates the secret, so it
cannot be replayed. A recovery code will also answer an ordinary sign-in
challenge (**Verify**) if the account holder only needs to get in once — but
that leaves the lost factor in place, so prefer **Replace factor**.

Audit: `identity.mfa_recovery_started`, or `identity.mfa_recovery_failed` on a
bad code.

## 2. Neither factor nor codes — root break-glass on the node

This is the only path left, and it needs root on the node itself. That is
deliberate: the account holder has no credential left to prove anything with, so
the only credential the panel can still require is control of the host.

```
sudo nexa mfa reset --user <username>
```

It works whether the panel is running or stopped — it writes one transaction to
the same WAL state database the API uses, so there is no need to stop
`nexa-api`. On a non-default state path, add
`--state /path/to/control.db`.

What it does, and nothing more:

- Clears the account's TOTP secret, confirmation, replay counter, and every
  remaining recovery code.
- **Signs out every session** for that account, including any that already
  answered the old factor.
- Records `identity.mfa_break_glass_reset` in the audit chain, with the username,
  role, and the number of sessions revoked. There is no panel actor on the
  record: a root operator has no panel identity.

What it does **not** do: it does not change the password, does not grant any
role, does not touch other accounts, and does not disable MFA for the node.

Afterwards the account signs in with its password alone and the panel asks it to
enroll a new authenticator (**Account → Security**). Under an optional-enrollment
policy that prompt is the strongest form "force re-enrollment" can honestly take
— treat completing it as part of this procedure, not as optional cleanup.

### Verify

```
sudo nexa audit verify                     # the chain must still report INTACT
```

Then, in the panel: sign in as the recovered account and complete enrollment.
Confirm in **Account → Security** that MFA reads *Enabled* again.

### If it refuses

| Message | Meaning |
| --- | --- |
| `must be run as root` | Re-run with `sudo`. The check runs before the database is opened. |
| `no account with that username` | The username is exact and case-sensitive; check **Users** in the panel or `sqlite3 control.db 'select username from identity_users'`. |
| `open state database` | Wrong `--state` path, or the node was never installed. |

## Aftercare

A break-glass reset is a security event even when it is routine. Whoever ran it
should:

1. Confirm out of band that the request really came from the account holder.
   Nothing in this procedure proves that — root proves control of the node, not
   the identity of the person who asked.
2. Have the account holder change their password if there is any doubt about how
   the factor was lost (a stolen laptop takes the password with it).
3. Leave the `identity.mfa_break_glass_reset` entry in place and reference it in
   the incident record. It is the only trace this happened.

## What is deliberately not offered

- **No HTTP endpoint for the reset.** Any network-reachable break-glass path is
  an MFA bypass with extra steps.
- **No administrator "reset another user's MFA" button.** It would make every
  administrator session a second factor for every other account. Use the
  root path above; it is auditable and needs the node.
- **No mandatory enrollment.** Enrollment is optional by policy, so no recovery
  path here leaves an account unable to sign in.

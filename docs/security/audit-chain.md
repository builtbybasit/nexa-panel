# The audit hash chain: what it proves, and what it does not

Every audit event Nexa Panel records is hashed together with the hash of the
event before it, so the log forms a chain. `nexa audit verify` walks that chain
and reports whether it still holds together.

The chain is **tamper-evident, not tamper-proof**. Read the next two sections
before you rely on a verdict either way.

## What a verdict is worth

`Verdict: TAMPERING DETECTED` is a strong signal. It means the log on disk is
not the log the panel wrote, and the panel could not have produced this state on
its own. Treat the node as compromised and the log as untrustworthy from the
first broken event onwards. The command reports these:

- an **edited event** — its contents no longer hash to its stored hash;
- a **deleted or reordered event** — the following event's link no longer
  matches its predecessor;
- an **event inserted by hand** after the chain started, which carries no hash;
- **blanked hashes**, including the follow-up move of the chain-start watermark
  that would otherwise pass every blanked row off as predating the chain;
- a **deleted or dropped `audit_chain_state`**, the row that records where the
  chain begins.

It also catches ordinary disk and backup corruption, which is worth as much in
practice as catching an attacker.

`Verdict: INTACT` is a **weak** signal. It means nobody edited this log
casually. It does **not** mean the log is complete or true.

## What it cannot detect

The chain lives in the same database it protects, `control.db`. Anyone who can
write to that file can also rewrite the chain, and no in-database scheme can
change that:

- **Wholesale recompute.** Delete or rewrite an event, then recompute every hash
  after it. The chain is internally consistent again and verification says
  intact. The hash function is public; there is no secret an attacker with the
  file does not also have.
- **Tail truncation.** Delete the newest events. Nothing is left to link to
  them, so nothing breaks. A log that ends before an incident looks exactly like
  a log that was quiet.
- **Anything before the chain.** Events carried over from a database upgraded
  from before the chain shipped are unhashed. Verification counts them and
  proves nothing about them.

Detecting a recompute or a truncation requires **anchoring the tail hash outside
this database** — writing the newest event's hash somewhere the panel's node
cannot rewrite (a write-once log sink, a remote monitoring system, an operator's
notes) and comparing it later. Nexa Panel does not do this for you today. If
your threat model includes an attacker with root on the node, ship the audit log
off the node as it is written; the chain is then what tells you the shipped copy
and the local copy agree.

## How to use verification

Run it directly on the node, against the state database as it sits on disk:

```
nexa audit verify
nexa audit verify --state /var/lib/nexa-panel/control.db
```

The command exits non-zero when the chain does not verify, so it can be run from
cron or a monitoring check. `GET /api/v1/audit/verify` (permission `audit.read`)
returns the same verdict as JSON.

**Read the counts, not just the verdict.** They are the part a human can reason
about when the verdict cannot help:

- `Hashed events checked` — how many events the chain actually covers. If this
  number is far smaller than the log you are looking at, most of that log is
  unprotected.
- `Events carrying no chain hash` — events outside the chain. On a fresh install
  this is `0` and stays `0`. On an upgraded install it is however many events
  predated the chain, and it must **stop growing**. A count that grows, or a log
  that is suddenly all unhashed, means the hashes were stripped even if the
  verdict reads intact.

Two habits make the whole thing worth more than it is on its own:

1. **Record the numbers.** Note the newest event id and the checked count
   somewhere off the node — a ticket is enough. A later verification that shows
   fewer events than you recorded is a truncation the chain itself cannot report.
2. **Verify your backups, not only the live node.** Restore a panel-state backup
   (see `docs/runbooks/panel-state-restore.md`) and run `nexa audit verify
   --state` against the restored copy. An attacker who rewrote the live log
   rarely rewrote every archived one, and comparing the two is the closest thing
   to an external anchor you have without setting one up.

## A verdict you may see after an upgrade

On a database upgraded from before the chain shipped, every event is unhashed
until the panel records its next one. In that window the log is
indistinguishable — inside the database — from one whose hashes were just
stripped, and verification reports it as tampering rather than resolving the
ambiguity in an attacker's favour. If you see this immediately after an upgrade
and before the panel has recorded anything, perform any action that writes an
audit event (a login will do) and verify again.

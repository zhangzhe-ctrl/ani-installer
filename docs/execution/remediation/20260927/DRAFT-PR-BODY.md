Draft: C01–C10 targeted remediation on the review branch. **Not for merge as-is** — for code review.

## What this is

The branch already carried the F01–F12 work (`8f5a1cb`). This round adds two commits that implement the ten
findings of the 2026-09-27 re-audit of `ee8c4cc`, one per finding, each preceded by a reproduction against the
production entry point and followed by a red/green regression.

- `8b9257e` — code, tests, gate, CI
- `8d9de2c` — per-item ledger (`docs/execution/remediation/20260927/C01-C10-LEDGER.md`) and the taskpack

Per-item detail (mechanism → files → tests → status) is in the ledger, not duplicated here.

## The common shape of the ten

Most of them are the same failure wearing different clothes: **a check that reports what it intended rather
than what happened.**

- `verify`'s overall rollup flipped only on an explicit `fail`, so `skipped` / `not_run` / an unset status fell
  through to `overall: pass` with exit 0. Now: a level passes only when every result passed, and an acceptance
  that declares nothing is refused rather than passed.
- The hand-written acceptance `Job` put `template:` at the document's top level, so it had no pod template. Every
  offline suite was green because the fake `apply` accepted any bytes. Now built from `batchv1` types, validated
  locally before the one-shot quota is consumed, and the fake apply rejects the shape.
- The delete was authorised by a client-side re-read beside the call, not by anything in the request. Now the
  authorised pod uid travels with the delete, and the owner/controller uid, the controller flag, the PVC the pod
  actually mounts and the PV's claim back-reference are authorising facts checked before the quota is claimed.
- `exec.CommandContext` installs its own `Cancel`, so the `if cmd.Cancel == nil` guard that was supposed to
  register the process-group kill never fired — `Setpgid` built a group nothing ever signalled. The test now makes
  a **descendant**, not the parent script's last line, the thing that must not happen.
- The success record was preceded by stamping `succeeded` on the state, and the finaliser's guard then refused to
  correct it: a command could exit non-zero while the disk said success.

## Two findings worth reading closely

**C05** compared a plan's dependency closure with its write set for equality, which permanently rejected adding
OpenSearch beside an existing Fluent Bit — re-planning could not change the fact. The two are separate now, and a
no-op no longer skips every freshness check, so a stale `already_installed` assertion cannot be re-recorded with a
freshly read uid.

**C06** refused any execution record whose config digest equalled the base's. That made the honest observation of
an already-installed component unrecordable, and pushed an operator to edit an unrelated field to fabricate a
difference. The rule is now about the operation. A test in this repo (`TestComponentsExecutionWriterRefusesARecord
VerifyWouldReject`) asserted the wrong rule; it is corrected here rather than left green.

## Gate and material

A clean checkout had **no chart archives** — they are gitignored — so the gate test that keeps the spec table,
the lock and the shipped bytes from drifting apart could not run on CI at all. Preparation is now an explicit step
inside the gate, and the only network step: each archive is verified against the digest its own lock entry approves
and against the identity the archive declares. No `t.Skip`, no `|| true`, no `continue-on-error`, no assertion
deleted, nothing vendored into Git.

Fixing it surfaced two real defects: the lock parser read `source` but not `chartSource`, silently discarding the
provenance of the cert-manager and nats entries; and the destination path was built before the chart name was
constrained, so `../evil` folded into a path still inside the root but outside the chart's own directory.

## Verification actually run

`scripts/check-code.sh` exits 0 both in-tree and from a **pristine `git clone` of `8b9257e` with an empty chart
cache** (0 `.tgz` present), where the preparation step fetched and verified all six and all 205 behaviour-suite
cases passed. Exit codes were also captured for the negative cases: missing material under `--offline` → 1,
a cache entry that is not its own digest → 1, a wrong-bytes destination → 1 and not overwritten, then a re-fetch → 0.
Three red/greens were re-run by restoring the old guard, not reasoned about: C02, C06, C08.

Tree `40e1b894f24dafda22db7edb7a8aaaad928184864c31e985970efbb3f2b9a145`, `kk`
`d926604a1d007db8fd0fa3b3d6f1ba401b4329cddd09a1e1ffb7549cdc107863`, SHA256SUMS 5/5.

## What is deliberately not claimed

- **No live acceptance.** This round had no permission to touch the three experiment nodes or ESXi, so the
  repaired code has never run against a cluster. Everything above is `code_fixed` / `local_gate_pass`.
- **Three open items, left open rather than folded in:** the metrics and fluent-bit heavyweight acceptances are
  still not wired into a ledger-governed entry point; a new execution's connection fragments still land in the
  base's canonical `connections.d`; and the uid-preconditioned delete's end-to-end server semantics are
  **unverified** — there was no real kubectl on this host and no cluster access, so the code is built to fail
  loudly if the server cannot honour the condition rather than to fall back to an unconditional delete. That
  guarantees no wrong object is removed; it does not prove the condition is supported.
- The base run `090732`'s postgresql one-shot quota remains **spent** by a previous round's out-of-scope step.
  Nothing here resets or re-claims it.

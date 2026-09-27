# Claim → artifact index (for a reviewer with only this branch)

One row per load-bearing claim in `docs/execution/REVIEW-20260926.md`, with the literal string to grep
and where it lives. `off-repo` in the last column means the proof exists but is **not** on this branch:
the original stays on the operator's machine, and you would have to take the author's word for it.
`state` distinguishes what is checkable here from what is measured-only-in-tests or not measured at all.

| # | claim | committed file | literal to grep | state |
|---|---|---|---|---|
| 1 | source identity of the reviewed code | (any) | `git archive HEAD kubekey \| tar -x -C /tmp/c && ani_tree_fingerprint /tmp/c/kubekey` | `b155498b…e1e` on branch |
| 2 | clean first install `rc 0`, 1101 s, six identity fields, 0 public URLs | `live-first-install-and-base-smoke.sanitized.txt` | `rc_smoke=0`, `finishedAt": "2026-09-26T09:26:09Z"`, `public URLs in console: 0` | on branch (raw console log off-repo) |
| 3 | base smoke 6/6 (liveness/readiness only) | `live-first-install-and-base-smoke.sanitized.txt` | six distinct `verify smoke <name>: pass` + `verify smoke overall: pass` | on branch |
| 4 | base smoke re-verified 6/6 after the pod rotation | `live-fluentbit-incident.sanitized.txt` | `rc_base_smoke_second=0` | on branch |
| 5 | L-06: execute printed and landed a record, then verify consumed it | `live-l13-verify.sanitized.out` | `rc_plan=0`, `rc_execute=0`, `components record: `, `rc_verify=0`, `verify smoke valkey: pass` | on branch |
| 6 | that record's identity/digest/mode | `live-l13-verify.sanitized.out` | `record_sha256=20113b35`, `operation         = noop`, `didInstall = False`, `-rw-------` | on branch |
| 7 | base binding (run id, base bytes, cluster uid) | `live-l13-verify.sanitized.out` | `base record sha   = 9f16b755`, `base uid = 1b6d17fe-fc92-4b72-b6d3-0fcf32888e64` | on branch |
| 8 | five refusals on the live-13 chain | `live-l13-verify.sanitized.out` §5 | `rc_acceptance_on_observation=1`, `rc_out_of_scope=1`, `rc_plan_as_record=1`, `rc_tampered=1`, `rc_torn=1` | on branch |
| 9 | seven refusals of the newly enforced forgery classes | `live-l13-refusals.sanitized.out` | `rc_relabel=1`, `rc_forged_base_run=1`, `rc_noevi=1`, `rc_badscope=1`, `rc_hidetarget=1`, `rc_badrunid=1`, `rc_badcomp=1` | on branch |
| 10 | the intact record still verifies (nothing over-tightened) | `live-l13-refusals.sanitized.out` | `rc_intact=0` | on branch |
| 11 | landed record and base record unchanged across the refusal tests | both l13 files | `landed record digest after refusals: 20113b35`, `base record after:   9f16b755` | on branch |
| 12 | valkey was never reinstalled/recreated | `live-l13-verify.sanitized.out` | `VALKEY-UNCHANGED`, `6a6552d5-b3e0-4361-b88a-77f315e992a7` | on branch |
| 13 | same-version read-only no-op reaches `already_installed` with the ANI-owned owner line | `live-l13-verify.sanitized.out` | `already_installed`, `ANI-owned, left untouched` | on branch (live-13; live-05 occurrence off-repo) |
| 14 | the one-shot delete budget is keyed to the base run and cannot be re-armed | `live-l11-acceptance-incident.sanitized.out` | `ledger-ani-ani-lab-20260926-090732-postgresql.json`, `"state":"done"`, `already recorded as` | on branch (unit-level proof in `components_record_consumption_test.go`) |
| 15 | out-of-scope acceptance really rotated the postgresql pod | `live-l11-acceptance-incident.sanitized.out` | `"oldPodUID":"e49193ed`, `pod 8ef12297-dbc7-467c-afd4-64f226a90a14 created=2026-09-26T13:37:46Z` | on branch; ledger entry lacks `newPodUID` (recorded as a fidelity gap) |
| 16 | base record integrity held through all of it | `live-l11-acceptance-incident.sanitized.out`, `live-target-inventory.sanitized.out` | `9f16b755f863e9d3c8e61e73217098573a203a5c8955b9464e1a3d149b8ecfc2` | on branch |
| 17 | fluent-bit smoke failed once then passed; root cause unproven | `live-fluentbit-incident.sanitized.txt` | `overall = fail started = 2026-09-26T13:39:55Z`, `JSONDecodeError: Expecting value: line 1 column 1 (char 0)`, `  0 2026-09-26 … metadata.txt`, `rc_fluentbit_smoke_rerun=0` | on branch (mechanism NOT proven) |
| 18 | gate passed and released three same-source builds | `gate-live-11.sanitized.log`, `gate-live-13.sanitized.log` | `check-code PASSED: go go1.26.7, tree`, `cases failed: 0`, `code release complete at` | on branch (rc of the whole run lives in off-repo `.rc` files; no `gate-live-12` file) |
| 19 | materials unchanged across the whole cycle | (repo file itself) | `sha256sum kubekey/ani/images.tsv` → `a228d3db…5d28`; `sha256sum kubekey/ani/components.lock.yaml` → `02d5dea9…dd34` | on branch, reviewer-runnable |
| 20 | lock binds charts as digest-pinned downloads, not repo content | `kubekey/ani/components.lock.yaml` | `chartSource`, `chartSha256`, `artifactChartPath: charts/nats/2.14.6.tgz` | on branch |
| 21 | a bare clone fails exactly one material-dependent test (hence CI on this branch) | `clone-gate-reproduction.sanitized.txt` | `--- FAIL: TestChartSpecsMatchTheApprovedLockAndTheChartBytes` | on branch: reproduced locally with the exact command in that file's header |
| 22 | cold pull after reboot: 21 blobs, 0 mismatches, node1 and node3 only | `coldstart-postreboot.sanitized.txt` | `TOTAL blobs byte-verified from an empty private view: 21, mismatches: 0`, `on node3` | on branch for the 2 nodes measured; **node2 not measured** |
| 23 | cross-reboot automatic isolation | `coldstart-postreboot.sanitized.txt` | `not_verified`, `rules absent` | **not verified** — stated as such |
| 24 | `operation=add` record on live | — | — | **live_not_run**; next action in `progress.yaml` |
| 25 | cancelled / unknown execution records | `kubekey/pkg/ani/components_record.go` | `ResultCancelled`, `RecordResultUnknown` | **not measured at all**: accepted by the writer, refused by the consumer, no test drives either, executor never emits them |
| 26 | empty-scope refusal; record-write failure reporting | `kubekey/pkg/ani/verify.go`, `components_install.go` | `refusing to report an empty scope as a pass`, `verify has nothing to refuse` | implemented, **no test enters these branches** |
| 27 | audit of the closeout (4 confirmed / 2 rejected, 6 lenses, 2 votes each) | `docs/execution/progress.yaml`, `status-decision.md` | `复核轮`, `4 条成立、2 条驳回`, `驳回的两条` | rulings on branch; agent transcripts off-repo |
| 28 | stale lab lock (orphan `sleep infinity` holding fd 3) | `docs/execution/progress.yaml` | `实验室锁` / note in `REVIEW-20260926.md` §5 | on branch as a record; `ps`/`fuser` output off-repo |
| 29 | WORKLOG §1–§13, task-result `closeoutRound`/`reauditRound`, raw ledgers | — | — | **off-repo only** (`/home/chabking/workspace/ani-installer-work/evidence/F-remediation-20260925/`, `/home/chabking/ani-installer-runs/f-live-20260926/`) |

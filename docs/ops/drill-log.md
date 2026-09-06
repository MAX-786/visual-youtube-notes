# Restore drill log

Spec 12: _a backup is not complete until restore succeeds._ This file is the evidence.

The procedure is in [`runbook.md`](runbook.md#restore-drill-quarterly). Drills alternate between the
local and off-server repositories so each is proven twice a year, and key recovery is tested every
time. `ops_restore_drills` in the database holds the same facts in queryable form — that is what the
`restore_drill` health check reads, and what answers "which repository is due next". This file holds
what a table cannot: what went wrong, what was confusing, and what changed as a result.

**RPO is 24 hours**, which follows from a daily backup schedule. **RTO is unmeasured** until the
first drill measures it; the `restore_seconds` column of the first entry below becomes that number.

## Template

Copy this for each drill. Commit it in the same change as anything the drill made you fix.

```markdown
## YYYY-MM-DD — <local | off-server>

- **Snapshot**: `<short id>`, taken YYYY-MM-DD HH:MM UTC
- **Restore time**: <mm:ss> (step 3 to step 6 inclusive)
- **Versions**: PostgreSQL <x.y>, pgvector <x.y.z>, restic <x.y.z>
- **Key recovery**: pass / fail — master key SHA-256 matched the live secret
- **Pass criteria**: 1 schema version · 2 pgvector version · 3 FK validation ·
  4 semantic search hit · 5 provider key decrypts with the recovered key
- **Row counts** (trend only, never a pass criterion):
  notes <n>, revisions <n>, transcript chunks <n>, search children <n>, embeddings <n>
- **Deviations**: <anything that did not match the runbook, or "none">
- **Actions taken**: <fixes made, runbook corrections, or "none">
```

## Drills

_No drill has been run yet — the stack has not been deployed. The first one is due one quarter after
first deploy, against the **local** repository, and it sets the RTO figure._

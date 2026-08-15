# Release job persists RELEASE_PAT in the workspace .git/config

| Field | Value |
|-------|-------|
| Criticality | Medium |
| Complexity | Small |
| Location | `.github/workflows/release.yml:71-77` |
| Found during | Security review of the changelog-fragment PR (#163) |
| Date | 2026-08-15 |

## Issue

The release job checks out with `token: ${{ secrets.RELEASE_PAT || secrets.GITHUB_TOKEN }}`
and does not set `persist-credentials: false`. `actions/checkout` therefore writes

```
http.https://github.com/.extraheader = AUTHORIZATION: basic <base64 of x-access-token:PAT>
```

into `$GITHUB_WORKSPACE/.git/config`, where it stays for the whole job. Per the
workflow's own header comment, `RELEASE_PAT` is a user-owned **ruleset bypass actor** —
disclosure means push-to-master, not merely read access.

This is pre-existing and was not introduced by #163. It surfaced there because that PR
added a step which reads a contributor-chosen path inside that job: a fragment symlinked
to `../.git/config` published the header into `CHANGELOG.md` and the release body. That
specific route is closed — fragments must now be regular files, refused at the PR gate
before the release job ever runs — but the credential is still sitting in the workspace
for any *future* step that reads a path it did not fully control.

## Risks

Any step added later to the release job that reads, archives, uploads, or prints a
workspace path can disclose a credential that can push to master. The job already runs
`go test` (arbitrary merged code), `sed -i`, and now `promote-changelog.sh`. The blast
radius is the whole repository, and the failure is silent — a leaked header looks like
ordinary text in a diff.

Defence today is entirely "no step does that", which is a property of the current step
list rather than of the workflow's configuration.

## Suggested Solutions

1. **Set `persist-credentials: false` on the checkout and pass the token explicitly to
   the one push that needs it.** Removes the prize rather than guarding each path to it.
   The job pushes exactly once (`release.yml:359-370`), so the change is contained — but
   it must be verified end to end, because a wrong form there breaks releases outright
   rather than failing loudly at PR time. Do it in its own PR with a `workflow_dispatch`
   dry run first.
2. Narrower alternative: keep persistence, and add a job step that scrubs
   `.git/config`'s `extraheader` immediately after checkout, re-adding it before the
   push. More moving parts, same goal, easier to get subtly wrong.
3. Cheapest partial: audit any newly added release-job step for workspace reads. This is
   what is happening implicitly today; writing it down at least makes it deliberate.

Option 1 is preferred. Not bundled into #163 because breaking the release push is a
worse outcome than the residual risk, and the concrete route that prompted it is already
closed there.

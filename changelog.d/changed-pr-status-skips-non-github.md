---
headline: No gh calls for checkouts whose remote is not GitHub
---
- **The pull request row no longer runs `gh` for a checkout whose remote is not GitHub.** Quil
  reads the checkout's remote once; GitLab, Bitbucket, Codeberg, Azure DevOps, Gitea, sourcehut
  or no remote at all get no probe, where every call used to fail. A self-hosted host Quil
  cannot place is still asked once, since it may be a GitHub Enterprise under its own name.

# AGENTS.md

Guidance for AI agents working in this fork.

## Fork development workflow

This fork develops a self-hosted Lore platform independently of the upstream project.
Its changes are not intended for upstream submission by default.

- Read the upstream standards in `docs/developing/code-standards/` that apply to the
  code being changed. Use them as engineering guidance rather than a separate review
  process.
- Keep design documents in `docs/proposals/` as architectural drafts. Their status and
  the upstream discussion period don't block implementation in this fork.
- Review changes at implementation milestones. Independent subagent reviews are
  optional, reserved for a release or a specific security-critical review requested by
  the team.
- Add targeted tests with code changes and run the relevant build and test commands.
  Run the full available validation before a release.
- Keep protocol, authentication, authorization, and public API changes explicit in the
  design record, and test their compatibility and failure behavior.
- Don't commit, push, open issues, or create pull requests unless the user asks.

An upstream contribution follows the upstream `GOVERNANCE.md`, `CONTRIBUTING.md`, and
review requirements instead of this fork workflow.

## Team workspace

When `context/team-workspace.md` exists, read it before non-trivial work and follow
it. It names the team's workspace for specs, plans, work items and decisions,
and how to reach it. Without it, work proceeds normally, and specs and plans stay in
`context/`.

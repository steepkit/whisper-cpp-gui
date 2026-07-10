# 0014: Publish clean public snapshots for v0.1.0

Status: Accepted

Date: 2026-07-11

## Context

Development took place in private GitHub repositories with complete commit,
Issue, pull-request, review, and Actions history. Publishing those repositories
directly would expose the full development record. The owner approved public
distribution but chose to retain that record in private archives.

The public source and tap still need an auditable connection to the reviewed
trees. Copying files without a content identity check could publish a different
artifact from the one that passed review and CI.

## Decision

For the initial `v0.1.0` publication:

- rename the development repositories to
  `steepkit/whisper-cpp-gui-private-archive` and
  `steepkit/homebrew-tap-private-archive`, keep them private, and archive them
  after their final reviewed changes are merged
- create new canonical repositories at `steepkit/whisper-cpp-gui` and
  `steepkit/homebrew-tap`, stage them privately, and make them public only after
  the snapshot checks and private staging CI pass
- initialize each public repository from its final reviewed tracked tree as one
  new root commit, without copying the private `.git` directory, branches,
  Issues, pull requests, reviews, or Actions history
- require the private final commit and public root commit to have the same Git
  tree hash before tagging or publishing
- run public CI against each public root commit; private CI does not substitute
  for this check
- create the release tag only in the public source repository

Public documentation must be self-contained and must not depend on access to a
private Issue or pull request. The physical-Mac waiver authorization and scope
are therefore recorded directly in ADR 0013 and the release documents.

This decision covers the initial `v0.1.0` bootstrap only. A later release must
make an explicit decision about whether development continues publicly or uses
another reviewed snapshot.

## Consequences

- Public clones contain one reviewed source snapshot rather than private
  development history.
- The private repositories preserve the full review and orchestration record.
- Tree-hash equality and repeated public CI provide the provenance boundary.
- Public GitHub Issue and pull-request numbering starts from a new repository.

# Release runbook

This is the manual v1 release flow for maintainers. It covers
`steepkit/whisper-cpp-gui` and `steepkit/homebrew-tap`. Sections 1 through 9
describe the initial `v0.1.0` clean publication. After both repositories are
public, preserve their public history and use section 10 for patch releases.
Do not improvise around a failed step. Fix the cause and restart the relevant
gate.

## Release boundary

Publishing a release requires all of the following:

- explicit human approval to make both repositories public
- the main repository, tag archive, tap, and Formula are anonymously readable
- the Formula pins the immutable tag archive and its SHA256
- main and tap CI are green
- a fresh macOS environment passes Homebrew source install and `brew test`; for
  `v0.1.0`, ADR 0013 permits an ephemeral GitHub-hosted macOS runner
- the M5 lab-Mac checklist in [docs/notes.md](notes.md) is complete, or a
  version-specific owner waiver is accepted and disclosed

Private repositories may stage code and M5 testing. They do not provide
anonymous Homebrew distribution. Never embed a personal access token in a
Formula, URL, shell history, release note, or CI log.

Every command block below is a fail-closed Bash subshell. Paste and run a whole
block, not selected lines. Any failed command stops that block. Values such as
`VERSION`, commit IDs, and checksums are deliberately set and validated again
when a later step may run in another shell or on another host.

Blocks that create external state are restart-safe: they reuse an existing
tag, draft, branch, PR, or public visibility only after verifying that it
matches the expected state. If such a block stops after a write, fix the cause
and rerun that same numbered block. Do not rerun an earlier unused-name check,
delete the created object, or skip directly to a later block.

## 1. Build the M5 candidate

Maintainers need `git`, `gh`, Go 1.22+, ShellCheck, and access to the private
main repository. Physical M5 also needs an authorized Apple Silicon lab Mac;
`v0.1.0` instead follows the explicit ADR 0013 waiver. Select a semantic
version; the examples use `0.1.0`. The authenticated `gh` token used for clean
publication must be able to administer both repositories and read and delete
Actions runs; failed or cancelled clean-snapshot runs are removed before
visibility changes.

From the main repository:

```bash
(
  set -euo pipefail
  VERSION=0.1.0
  MAIN_REPO=steepkit/whisper-cpp-gui
  [[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]

  gh auth status
  gh repo view "$MAIN_REPO" >/dev/null
  git fetch --prune --tags origin
  git switch main
  git pull --ff-only origin main

  branch="$(git symbolic-ref --short HEAD)"
  worktree_state="$(git status --porcelain --untracked-files=all)"
  origin_url="$(git remote get-url origin)"
  local_commit="$(git rev-parse HEAD)"
  remote_commit="$(git rev-parse origin/main)"
  test "$branch" = main
  test -z "$worktree_state"
  case "$origin_url" in
    https://github.com/steepkit/whisper-cpp-gui.git | \
      git@github.com:steepkit/whisper-cpp-gui.git) ;;
    *) echo "unexpected main remote: ${origin_url}" >&2; exit 1 ;;
  esac
  test "$local_commit" = "$remote_commit"

  gofmt_out="$(gofmt -l .)"
  test -z "$gofmt_out"
  go build ./...
  go vet ./...
  go test -count=1 ./...
  shellcheck scripts/smoke_start.sh testdata/stubs/*

  mkdir -p dist
  go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
    -o dist/whisper-cpp-gui ./cmd/whisper-cpp-gui
  scripts/smoke_start.sh ./dist/whisper-cpp-gui "$VERSION"

  candidate_ci_url="$(gh run list --repo "$MAIN_REPO" --workflow CI \
    --commit "$local_commit" --status success --limit 1 --json url \
    --jq '.[0].url // ""')"
  test -n "$candidate_ci_url"
  printf 'M5 candidate commit: %s\nCandidate CI: %s\n' \
    "$local_commit" "$candidate_ci_url"
)
```

The commit and CI URL printed by this block are the M5 candidate record.

## 2. Complete M5 before the final tag

M5 can change presets or product code, so it must run before creating the
immutable final tag. On the authorized lab Mac, check out exactly the candidate
commit printed in section 1. Go is needed here because this is maintainer
validation; it remains a build-only dependency for end users.

```bash
(
  set -euo pipefail
  VERSION=0.1.0
  CANDIDATE_COMMIT=REPLACE_WITH_40_HEX_COMMIT
  [[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]
  [[ "$CANDIDATE_COMMIT" =~ ^[0-9a-f]{40}$ ]]

  gh auth status
  if test -e whisper-cpp-gui-m5
  then
    test -d whisper-cpp-gui-m5/.git
  else
    gh repo clone steepkit/whisper-cpp-gui whisper-cpp-gui-m5
  fi
  cd whisper-cpp-gui-m5 || exit 1
  origin_url="$(git remote get-url origin)"
  case "$origin_url" in
    https://github.com/steepkit/whisper-cpp-gui.git | \
      git@github.com:steepkit/whisper-cpp-gui.git) ;;
    *) echo "unexpected M5 remote: ${origin_url}" >&2; exit 1 ;;
  esac
  git fetch origin
  git checkout --detach "$CANDIDATE_COMMIT"
  checked_out="$(git rev-parse HEAD)"
  worktree_state="$(git status --porcelain --untracked-files=all)"
  test "$checked_out" = "$CANDIDATE_COMMIT"
  test -z "$worktree_state"

  brew install go whisper-cpp ffmpeg
  mkdir -p dist
  go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
    -o dist/whisper-cpp-gui ./cmd/whisper-cpp-gui
  scripts/smoke_start.sh ./dist/whisper-cpp-gui "$VERSION"
)
```

Run `./whisper-cpp-gui-m5/dist/whisper-cpp-gui` from the directory that
contains the clone, and complete every M5 item in
[docs/notes.md](notes.md): real lecture media, Apple Silicon 8 GB behavior,
Downloads permissions, automatic browser launch, and a real verified model
download. Exercise presets, VAD, all output formats, progress, cancellation,
and browser downloads.

Record the host, candidate commit, results, and preset decision in
`docs/notes.md` through the normal Issue and PR workflow. If M5 changes code,
configuration, or presets, merge the change and repeat sections 1 and 2. After
the evidence-only documentation PR is merged, continue with the new final
`main` commit. A candidate tag is not the final release tag.

### v0.1.0 physical-Mac waiver

When no physical Apple Silicon Mac is available, `v0.1.0` may use the
version-specific owner waiver in ADR 0013. Do not run the physical procedure or
mark its checklist as passed. Instead:

- leave every unverified M5 checkbox unchecked in `docs/notes.md`
- record the owner decision and the medium-preset non-decision there
- keep the warning in the Japanese user guide and GitHub Release notes
- require main macOS build/startup CI and tap macOS source-install/`brew test`
- handle physical-Mac failures reported later in a patch release

This exception does not apply automatically to another version.

## 3. Freeze and verify the reviewed private commit

Run this from the main repository after all M5 changes and evidence are merged:

```bash
(
  set -euo pipefail
  VERSION=0.1.0
  TAG="v${VERSION}"
  MAIN_REPO=steepkit/whisper-cpp-gui
  M5_WAIVER=docs/adr/0013-waive-physical-mac-validation-for-v0.1.0.md
  [[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]

  gh auth status
  gh repo view "$MAIN_REPO" >/dev/null
  git fetch --prune --tags origin
  git switch main
  git pull --ff-only origin main

  branch="$(git symbolic-ref --short HEAD)"
  worktree_state="$(git status --porcelain --untracked-files=all)"
  origin_url="$(git remote get-url origin)"
  main_commit="$(git rev-parse HEAD)"
  remote_commit="$(git rev-parse origin/main)"
  test "$branch" = main
  test -z "$worktree_state"
  case "$origin_url" in
    https://github.com/steepkit/whisper-cpp-gui.git | \
      git@github.com:steepkit/whisper-cpp-gui.git) ;;
    *) echo "unexpected main remote: ${origin_url}" >&2; exit 1 ;;
  esac
  test "$main_commit" = "$remote_commit"

  m5_section="$(sed -n '/^## M5/,/^## /p' docs/notes.md)"
  m5_checklist="$(printf '%s\n' "$m5_section" | \
    sed -n '/^- \[[ xX]\] /p')"
  test -n "$m5_checklist"
  if test "$VERSION" = 0.1.0
  then
    expected_m5_checklist="$(cat <<'EOF'
- [ ] 研究室 Mac で実ファイル(講義録音)の文字起こしが通るか
- [ ] Apple Silicon 8GB マシンでの large-v3 メモリ挙動 → 厳しければ medium プリセット追加を判断
- [ ] `~/Downloads/whisper-cpp-gui/` への出力と権限
- [ ] `open` によるブラウザ自動起動の実挙動
- [ ] HF からのモデル DL 実測(サイズ・速度・SHA256 一致)
EOF
)"
    test "$m5_checklist" = "$expected_m5_checklist"
    test -f "$M5_WAIVER"
    grep -q '^Status: Accepted$' "$M5_WAIVER"
    grep -q "waives physical M5 validation for \`v0.1.0\`" "$M5_WAIVER"
    grep -q 'v0.1.0 owner waiver' docs/notes.md
    echo 'M5 physical checks are owner-waived, not passed' >&2
  elif grep -q '^- \[ \]' <<<"$m5_section"
  then
    echo 'M5 still contains unchecked items' >&2
    exit 1
  fi

  gofmt_out="$(gofmt -l .)"
  test -z "$gofmt_out"
  go build ./...
  go vet ./...
  go test -count=1 ./...
  shellcheck scripts/smoke_start.sh testdata/stubs/*

  mkdir -p dist
  go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
    -o dist/whisper-cpp-gui ./cmd/whisper-cpp-gui
  scripts/smoke_start.sh ./dist/whisper-cpp-gui "$VERSION"

  main_ci_url="$(gh run list --repo "$MAIN_REPO" --workflow CI \
    --commit "$main_commit" --status success --limit 1 --json url \
    --jq '.[0].url // ""')"
  test -n "$main_ci_url"

  if git rev-parse --verify --quiet "refs/tags/${TAG}" >/dev/null
  then
    echo "local tag already exists: ${TAG}" >&2
    exit 1
  fi

  tag_status=0
  git ls-remote --exit-code --tags origin "refs/tags/${TAG}" >/dev/null || \
    tag_status=$?
  case "$tag_status" in
  0)
    echo "remote tag already exists: ${TAG}" >&2
    exit 1
    ;;
  2)
    ;;
  *)
    echo "remote tag lookup failed with status ${tag_status}" >&2
    exit 1
    ;;
  esac

  release_node_id="$(gh api graphql \
    -f owner="${MAIN_REPO%%/*}" -f name="${MAIN_REPO#*/}" -f tag="$TAG" \
    -f query="query(\$owner: String!, \$name: String!, \$tag: String!) {
      repository(owner: \$owner, name: \$name) {
        release(tagName: \$tag) { id }
      }
    }" --jq '.data.repository.release.id // ""')"
  test -z "$release_node_id"

  printf 'Reviewed private commit: %s\nPrivate CI: %s\nTag: %s\n' \
    "$main_commit" "$main_ci_url" "$TAG"
)
```

Save the printed commit as `REVIEWED_PRIVATE_COMMIT` with its CI URL. For an
ADR 0014 snapshot release, this is not the public release commit; section 4
creates and records `PUBLIC_ROOT_COMMIT` from the same Git tree.

## 4. Build and publish the clean public source snapshot

Changing visibility is a separate human approval point. For the initial
`v0.1.0`, ADR 0014 forbids changing the private development repository itself
to public. The reviewed repository is renamed and archived privately; a new
canonical repository is staged privately from one deterministic root commit,
passes CI, becomes public, and passes a new public `workflow_dispatch` CI run.

After explicit approval, run the whole block from the reviewed private source
checkout. The script is restart-safe and refuses publication unless
`CONFIRM_PUBLICATION` exactly names the canonical repository. It verifies the
recorded private commit and CI, tree equality, parent count, complete ref set,
deterministic anonymous commit metadata, empty public Issue/PR/Release/artifact
history, only successful expected root-commit CI history, private staging CI,
archived-private state, public CI, and anonymous Git access.

```bash
(
  set -euo pipefail
  VERSION=0.1.0
  REVIEWED_PRIVATE_COMMIT=REPLACE_WITH_RECORDED_40_HEX_COMMIT
  PRIVATE_CHECKOUT="$(pwd -P)"
  SNAPSHOT_DIR="${TMPDIR:-/tmp}/whisper-cpp-gui-public-v${VERSION}"
  [[ "$REVIEWED_PRIVATE_COMMIT" =~ ^[0-9a-f]{40}$ ]]

  CONFIRM_PUBLICATION=steepkit/whisper-cpp-gui \
    scripts/publish_clean_snapshot.sh \
      "$PRIVATE_CHECKOUT" \
      steepkit/whisper-cpp-gui \
      whisper-cpp-gui-private-archive \
      "$REVIEWED_PRIVATE_COMMIT" \
      "$SNAPSHOT_DIR" \
      "$VERSION" \
      "Local browser GUI for whisper.cpp transcription" \
      "Ubuntu checks" \
      "macOS 14 build and startup smoke"

  PUBLIC_ROOT_COMMIT="$(git -C "$SNAPSHOT_DIR" rev-parse HEAD)"
  [[ "$PUBLIC_ROOT_COMMIT" =~ ^[0-9a-f]{40}$ ]]
  printf 'Public checkout: %s\nPublic root commit: %s\n' \
    "$SNAPSHOT_DIR" "$PUBLIC_ROOT_COMMIT"
)
```

Continue sections 5 and 6 from the printed public checkout. Record the script's
`Public CI run ID` together with `PUBLIC_ROOT_COMMIT`; the final gate binds to
that exact post-publication run, whose required jobs verify that repository
visibility is public. Keep the development tap private until its Formula PR is
merged and the second publication approval is given.

## 5. Create the immutable tag and draft Release

Never move, delete, and recreate a pushed release tag. Use a new patch version
for a correction.

```bash
(
  set -euo pipefail
  VERSION=0.1.0
  TAG="v${VERSION}"
  MAIN_REPO=steepkit/whisper-cpp-gui
  PUBLIC_ROOT_COMMIT=REPLACE_WITH_RECORDED_PUBLIC_ROOT_40_HEX_COMMIT
  [[ "$PUBLIC_ROOT_COMMIT" =~ ^[0-9a-f]{40}$ ]]

  git fetch --prune --tags origin
  branch="$(git symbolic-ref --short HEAD)"
  worktree_state="$(git status --porcelain --untracked-files=all)"
  origin_url="$(git remote get-url origin)"
  main_commit="$(git rev-parse HEAD)"
  remote_commit="$(git rev-parse origin/main)"
  test "$branch" = main
  test -z "$worktree_state"
  case "$origin_url" in
    https://github.com/steepkit/whisper-cpp-gui.git | \
      git@github.com:steepkit/whisper-cpp-gui.git) ;;
    *) echo "unexpected main remote: ${origin_url}" >&2; exit 1 ;;
  esac
  test "$main_commit" = "$remote_commit"
  test "$main_commit" = "$PUBLIC_ROOT_COMMIT"
  visibility="$(gh repo view "$MAIN_REPO" --json visibility --jq .visibility)"
  test "$visibility" = PUBLIC

  if git rev-parse --verify --quiet "refs/tags/${TAG}" >/dev/null
  then
    tag_type="$(git cat-file -t "refs/tags/${TAG}")"
    local_tag_commit="$(git rev-list -n 1 "$TAG")"
    test "$tag_type" = tag
    test "$local_tag_commit" = "$main_commit"
  else
    GIT_COMMITTER_NAME=steepkit \
      GIT_COMMITTER_EMAIL=noreply@steepkit.invalid \
      git -c tag.gpgSign=false tag -a "$TAG" \
        -m "whisper-cpp-gui ${TAG}"
  fi

  tag_status=0
  git ls-remote --exit-code --tags origin "refs/tags/${TAG}" >/dev/null || \
    tag_status=$?
  case "$tag_status" in
  0)
    remote_tag_commit="$(gh api "repos/${MAIN_REPO}/commits/${TAG}" --jq .sha)"
    test "$remote_tag_commit" = "$main_commit"
    ;;
  2)
    git push origin "$TAG"
    remote_tag_commit="$(gh api "repos/${MAIN_REPO}/commits/${TAG}" --jq .sha)"
    test "$remote_tag_commit" = "$main_commit"
    ;;
  *)
    echo "remote tag lookup failed with status ${tag_status}" >&2
    exit 1
    ;;
  esac

  release_node_id="$(gh api graphql \
    -f owner="${MAIN_REPO%%/*}" -f name="${MAIN_REPO#*/}" -f tag="$TAG" \
    -f query="query(\$owner: String!, \$name: String!, \$tag: String!) {
      repository(owner: \$owner, name: \$name) {
        release(tagName: \$tag) { id }
      }
    }" --jq '.data.repository.release.id // ""')"
  if test -z "$release_node_id"
  then
    gh release create "$TAG" --repo "$MAIN_REPO" --draft --verify-tag \
      --title "whisper-cpp-gui ${TAG}" --generate-notes
  fi

  tag_commit="$(gh api "repos/${MAIN_REPO}/commits/${TAG}" --jq .sha)"
  draft_state="$(gh release view "$TAG" --repo "$MAIN_REPO" \
    --json isDraft --jq .isDraft)"
  release_name="$(gh release view "$TAG" --repo "$MAIN_REPO" \
    --json name --jq .name)"
  test "$tag_commit" = "$main_commit"
  test "$draft_state" = true
  test "$release_name" = "whisper-cpp-gui ${TAG}"
  gh release view "$TAG" --repo "$MAIN_REPO" \
    --json isDraft,tagName,targetCommitish,url
)
```

The Release remains a draft until tap CI and fresh-machine acceptance pass.

## 6. Pin the anonymous source archive

Download the same bytes Homebrew users receive. Do not calculate the checksum
from a local `git archive`.

```bash
(
  set -euo pipefail
  VERSION=0.1.0
  TAG="v${VERSION}"
  MAIN_REPO=steepkit/whisper-cpp-gui
  PUBLIC_ROOT_COMMIT=REPLACE_WITH_RECORDED_PUBLIC_ROOT_40_HEX_COMMIT
  ARCHIVE_URL="https://github.com/${MAIN_REPO}/archive/refs/tags/${TAG}.tar.gz"
  [[ "$PUBLIC_ROOT_COMMIT" =~ ^[0-9a-f]{40}$ ]]
  archive_path="$(mktemp \
    "${TMPDIR:-/tmp}/whisper-cpp-gui-${TAG}.XXXXXX")"
  trap 'rm -f "$archive_path"' EXIT

  env -u GH_TOKEN -u GITHUB_TOKEN \
    curl --disable --fail --location --proto '=https' \
    --proto-redir '=https' --remove-on-error \
    --output "$archive_path" "$ARCHIVE_URL"
  archive_sha256="$(shasum -a 256 "$archive_path" | awk '{print $1}')"
  [[ "$archive_sha256" =~ ^[0-9a-f]{64}$ ]]

  tag_commit="$(gh api "repos/${MAIN_REPO}/commits/${TAG}" --jq .sha)"
  test "$tag_commit" = "$PUBLIC_ROOT_COMMIT"
  printf 'Archive URL: %s\nArchive SHA256: %s\nTag commit: %s\n' \
    "$ARCHIVE_URL" "$archive_sha256" "$tag_commit"
)
```

A 404, credential prompt, non-HTTPS redirect, or checksum failure stops the
release. Save the printed URL and SHA256 exactly.

## 7. Add the Formula and tap CI

Clone the private tap as an authorized maintainer and create the release branch:

```bash
(
  set -euo pipefail
  VERSION=0.1.0
  TAG="v${VERSION}"

  gh auth status
  if test -e homebrew-tap
  then
    test -d homebrew-tap/.git
  else
    gh repo clone steepkit/homebrew-tap homebrew-tap
  fi
  cd homebrew-tap || exit 1
  git fetch --prune origin
  if git show-ref --verify --quiet "refs/heads/release/${TAG}"
  then
    git switch "release/${TAG}"
  elif git show-ref --verify --quiet "refs/remotes/origin/release/${TAG}"
  then
    git switch --track -c "release/${TAG}" "origin/release/${TAG}"
  else
    git switch -c "release/${TAG}" origin/main
  fi
  worktree_state="$(git status --porcelain --untracked-files=all)"
  test -z "$worktree_state"
)
```

Inside the new `homebrew-tap/` directory, add
`Formula/whisper-cpp-gui.rb` with the exact URL and SHA256 from section 6:

```ruby
class WhisperCppGui < Formula
  desc "Local browser GUI for whisper.cpp transcription"
  homepage "https://github.com/steepkit/whisper-cpp-gui"
  url "https://github.com/steepkit/whisper-cpp-gui/archive/refs/tags/v0.1.0.tar.gz"
  sha256 "REPLACE_WITH_DOWNLOADED_ARCHIVE_SHA256"
  license "MIT"

  depends_on "go" => :build
  depends_on "ffmpeg"
  depends_on "whisper-cpp"

  def install
    system "go", "build", *std_go_args(ldflags: "-s -w -X main.version=#{version}"),
           "./cmd/whisper-cpp-gui"
  end

  test do
    assert_equal "whisper-cpp-gui #{version}",
                 shell_output("#{bin}/whisper-cpp-gui --version").strip
  end
end
```

In that same directory, add `.github/workflows/ci.yml` to the same PR. Set the
workflow name to exactly `CI`. Require `brew test-bot --only-tap-syntax` and a
job named exactly
`macOS source install`. The latter must use an ephemeral macOS runner and:

- fail if the tap, Formula trust entry, or Formula installation already exists
- install the fully qualified Formula with `brew install --build-from-source`
- run `brew test` and require the exact release version, rejecting a `-dev` build
- prove that only `steepkit/tap/whisper-cpp-gui`, not the whole tap, was trusted
- start the installed binary with `--no-browser`, fetch the embedded UI over
  localhost, and stop the process without downloading a model

The tap workflow must also match the shared publication protocol: define the
`publication_phase` `workflow_dispatch` choice with `manual`,
`private-staging`, `public-recovery`, and `public-verification`; set `run-name`
to `CI (<phase>)`; and make both required jobs verify through `gh api` that
`private-staging` ran while the repository was private and the two public
phases ran while it was public. Keep `GH_TOKEN` scoped to read-only repository
and Actions metadata inside those jobs (`contents: read`, `actions: read`).

All checks must run against the Formula in the checked-out tap commit. A job
that omits any item above cannot replace the `v0.1.0` fresh-machine packaging
gate.

From the `homebrew-tap` checkout, set the recorded checksum and validate the
Formula through Homebrew's JSON representation before committing:

```bash
(
  set -euo pipefail
  VERSION=0.1.0
  TAG="v${VERSION}"
  MAIN_REPO=steepkit/whisper-cpp-gui
  TAP_REPO=steepkit/homebrew-tap
  TAP_ISSUE=1
  TAP_BRANCH="release/${TAG}"
  PUBLIC_ROOT_COMMIT=REPLACE_WITH_RECORDED_PUBLIC_ROOT_40_HEX_COMMIT
  ARCHIVE_URL="https://github.com/${MAIN_REPO}/archive/refs/tags/${TAG}.tar.gz"
  ARCHIVE_SHA256=REPLACE_WITH_RECORDED_64_HEX_SHA256
  [[ "$PUBLIC_ROOT_COMMIT" =~ ^[0-9a-f]{40}$ ]]
  [[ "$ARCHIVE_SHA256" =~ ^[0-9a-f]{64}$ ]]

  cd homebrew-tap || exit 1
  remote_url="$(git remote get-url origin)"
  current_branch="$(git symbolic-ref --short HEAD)"
  case "$remote_url" in
    https://github.com/steepkit/homebrew-tap.git | \
      git@github.com:steepkit/homebrew-tap.git) ;;
    *) echo "unexpected tap remote: ${remote_url}" >&2; exit 1 ;;
  esac
  test "$current_branch" = "$TAP_BRANCH"

  if grep -n 'REPLACE_WITH' Formula/whisper-cpp-gui.rb
  then
    echo 'Formula still contains a placeholder' >&2
    exit 1
  fi

  ruby -c Formula/whisper-cpp-gui.rb
  brew style Formula/whisper-cpp-gui.rb
  formula_json="$(HOMEBREW_NO_AUTO_UPDATE=1 \
    brew info --json=v2 ./Formula/whisper-cpp-gui.rb)"
  formula_url="$(ruby -rjson -e \
    'puts JSON.parse(STDIN.read).fetch("formulae").fetch(0).fetch("urls").fetch("stable").fetch("url")' \
    <<<"$formula_json")"
  formula_sha256="$(ruby -rjson -e \
    'puts JSON.parse(STDIN.read).fetch("formulae").fetch(0).fetch("urls").fetch("stable").fetch("checksum")' \
    <<<"$formula_json")"
  formula_version="$(ruby -rjson -e \
    'puts JSON.parse(STDIN.read).fetch("formulae").fetch(0).fetch("versions").fetch("stable")' \
    <<<"$formula_json")"
  test "$formula_url" = "$ARCHIVE_URL"
  test "$formula_sha256" = "$ARCHIVE_SHA256"
  test "$formula_version" = "$VERSION"
  git diff --check

  main_commit="$(gh api "repos/${MAIN_REPO}/commits/${TAG}" --jq .sha)"
  test "$main_commit" = "$PUBLIC_ROOT_COMMIT"
  main_ci_url="$(gh run list --repo "$MAIN_REPO" --workflow CI \
    --commit "$main_commit" --status success --limit 1 --json url \
    --jq '.[0].url // ""')"
  test -n "$main_ci_url"
  tap_issue_title="$(gh issue view "$TAP_ISSUE" --repo "$TAP_REPO" \
    --json title --jq .title)"
  test "$tap_issue_title" = \
    'M4-1: add whisper-cpp-gui Formula and macOS CI'

  git add Formula/whisper-cpp-gui.rb .github/workflows/ci.yml README.md
  git diff --cached --check
  if ! git diff --cached --quiet
  then
    git commit -m "whisper-cpp-gui ${TAG}"
  fi
  worktree_state="$(git status --porcelain --untracked-files=all)"
  test -z "$worktree_state"
  git push -u origin "$TAP_BRANCH"

  pr_body="$(printf \
    'Upstream tag: %s\nMain commit: %s\nArchive URL: %s\nArchive SHA256: %s\nMain CI: %s\n\nCloses #%s\n' \
    "$TAG" "$PUBLIC_ROOT_COMMIT" "$ARCHIVE_URL" "$ARCHIVE_SHA256" \
    "$main_ci_url" "$TAP_ISSUE")"
  pr_count="$(gh pr list --repo "$TAP_REPO" --state open \
    --head "$TAP_BRANCH" --json number --jq length)"
  case "$pr_count" in
  0)
    gh pr create --repo "$TAP_REPO" --base main --head "$TAP_BRANCH" \
      --draft --title "whisper-cpp-gui ${TAG}" --body "$pr_body" >/dev/null
    ;;
  1)
    ;;
  *)
    echo "unexpected open PR count for ${TAP_BRANCH}: ${pr_count}" >&2
    exit 1
    ;;
  esac
  tap_pr_number="$(gh pr list --repo "$TAP_REPO" --state open \
    --head "$TAP_BRANCH" --json number --jq '.[0].number // ""')"
  test -n "$tap_pr_number"
  tap_pr_title="$(gh pr view "$tap_pr_number" --repo "$TAP_REPO" \
    --json title --jq .title)"
  tap_pr_body="$(gh pr view "$tap_pr_number" --repo "$TAP_REPO" \
    --json body --jq .body)"
  tap_pr_base="$(gh pr view "$tap_pr_number" --repo "$TAP_REPO" \
    --json baseRefName --jq .baseRefName)"
  tap_pr_head="$(gh pr view "$tap_pr_number" --repo "$TAP_REPO" \
    --json headRefName --jq .headRefName)"
  tap_pr_draft="$(gh pr view "$tap_pr_number" --repo "$TAP_REPO" \
    --json isDraft --jq .isDraft)"
  tap_pr_url="$(gh pr view "$tap_pr_number" --repo "$TAP_REPO" \
    --json url --jq .url)"
  test "$tap_pr_title" = "whisper-cpp-gui ${TAG}"
  test "$tap_pr_body" = "$pr_body"
  test "$tap_pr_base" = main
  test "$tap_pr_head" = "$TAP_BRANCH"
  test "$tap_pr_draft" = true
  test -n "$tap_pr_url"
  printf 'Tap PR: %s\n' "$tap_pr_url"
)
```

Record the tap workflow URL and complete G2/G3 review while the PR is draft.
Mark it ready and merge only when all checks are green and every finding is
closed. The exact-version test must reject `0.1.0-dev`.

## 8. Publish the clean tap snapshot and run packaging acceptance

After the tap PR is merged and a second explicit publication approval is given,
apply ADR 0014 with the same script used for the source repository. First switch
the private tap checkout to its clean, up-to-date `main`; that exact commit is
`REVIEWED_TAP_COMMIT`. The script requires both the private CI and a repeated
public `workflow_dispatch` run with the exact `Tap syntax` and
`macOS source install` jobs.

```bash
(
  set -euo pipefail
  VERSION=0.1.0
  SOURCE_CHECKOUT=REPLACE_WITH_ABSOLUTE_PRIVATE_SOURCE_CHECKOUT
  TAP_CHECKOUT="$(pwd -P)"
  SNAPSHOT_DIR="${TMPDIR:-/tmp}/homebrew-tap-public-v${VERSION}"

  git switch main
  git pull --ff-only origin main
  test -z "$(git status --porcelain --untracked-files=all)"
  REVIEWED_TAP_COMMIT="$(git rev-parse HEAD)"
  [[ "$REVIEWED_TAP_COMMIT" =~ ^[0-9a-f]{40}$ ]]

  CONFIRM_PUBLICATION=steepkit/homebrew-tap \
    "$SOURCE_CHECKOUT/scripts/publish_clean_snapshot.sh" \
      "$TAP_CHECKOUT" \
      steepkit/homebrew-tap \
      homebrew-tap-private-archive \
      "$REVIEWED_TAP_COMMIT" \
      "$SNAPSHOT_DIR" \
      "$VERSION" \
      "Homebrew tap for steepkit projects" \
      "Tap syntax" \
      "macOS source install"

  PUBLIC_TAP_ROOT_COMMIT="$(git -C "$SNAPSHOT_DIR" rev-parse HEAD)"
  [[ "$PUBLIC_TAP_ROOT_COMMIT" =~ ^[0-9a-f]{40}$ ]]
  env -u GH_TOKEN -u GITHUB_TOKEN \
    curl --disable --fail --location --proto '=https' \
    --proto-redir '=https' --output /dev/null \
    "https://raw.githubusercontent.com/steepkit/homebrew-tap/main/Formula/whisper-cpp-gui.rb"
  printf 'Reviewed tap commit: %s\nPublic tap root: %s\n' \
    "$REVIEWED_TAP_COMMIT" "$PUBLIC_TAP_ROOT_COMMIT"
)
```

Record the script's `Public CI run ID` together with
`PUBLIC_TAP_ROOT_COMMIT`; the final release gate binds to that exact
post-publication packaging run, whose required jobs verify that repository
visibility is public.

The normal final install gate requires a fresh macOS VM or newly provisioned
clean single-user Mac. A new user on an already used Mac is not sufficient
because Homebrew's prefix is shared. Do not substitute cleanup commands on a
reused development host for this gate.

For `v0.1.0`, ADR 0013 permits the tap's ephemeral GitHub-hosted macOS job to
satisfy only this packaging gate. That job must prove no pre-existing tap,
trust entry, or installed Formula; run the fully qualified source install,
`brew test`, exact version check, Formula-only trust check, and startup smoke.
It does not validate a physical GUI session, 8 GB memory, real media, or model
downloads. The block below remains the normal procedure for future versions.

On the fresh host, with no GitHub credentials configured, run the whole block
once. If any command fails after installation begins, discard or reprovision
the host and restart this gate on another genuinely fresh environment:

```bash
(
  set -euo pipefail
  VERSION=0.1.0
  FORMULA=steepkit/tap/whisper-cpp-gui

  if brew tap | grep -qx 'steepkit/tap'
  then
    echo 'tap already exists on supposedly fresh host' >&2
    exit 1
  fi
  if brew trust | grep -Fq "$FORMULA"
  then
    echo 'formula already trusted on supposedly fresh host' >&2
    exit 1
  fi
  if brew list --formula | grep -qx 'whisper-cpp-gui'
  then
    echo 'formula already installed on supposedly fresh host' >&2
    exit 1
  fi

  uname -m
  sw_vers
  brew --version
  brew update
  brew install --build-from-source "$FORMULA"
  brew test "$FORMULA"
  version_output="$(whisper-cpp-gui --version)"
  test "$version_output" = "whisper-cpp-gui ${VERSION}"

  trust_output="$(brew trust)"
  grep -Fq "$FORMULA" <<<"$trust_output"
  tap_json="$(brew tap-info --json steepkit/tap)"
  whole_tap_trusted="$(ruby -rjson -e \
    'puts JSON.parse(STDIN.read).fetch(0).fetch("trusted")' \
    <<<"$tap_json")"
  test "$whole_tap_trusted" = false
)
```

For the normal future-release path, start `whisper-cpp-gui --no-browser`, open
the reported bootstrap file, and confirm the embedded UI loads. Record the fresh
environment, command output, Formula-only trust result, and tap CI URL. For
`v0.1.0`, do not claim this manual GUI check: record the exact tap commit and
`macOS source install` workflow URL verified by the block above.

## 9. Publish and record the Release

Only after sections 1 through 8 pass, publish the existing draft and mark it
latest:

For `v0.1.0`, prepend a prominent release-note warning that physical Apple
Silicon M5 was waived and list the unverified behaviors from ADR 0013. Do this
before changing the draft state.

Use this exact warning before any generated notes so the publication gate can
verify it:

```markdown
## Physical Mac validation waiver

No physical Apple Silicon Mac was available for v0.1.0. The physical Apple
Silicon validation procedure documented in ADR 0013 remains unexecuted. This
includes real media, both presets, VAD, txt/srt/vtt output, progress and logs,
cancellation, browser downloads, bootstrap recovery, 8 GB large-v3 memory use,
Downloads permissions, automatic browser launch, and real model download timing
and SHA256 verification.

GitHub-hosted macOS build/startup and Homebrew source-install CI passed only as
packaging checks. They do not establish physical GUI or workload compatibility.
Please report failures without attaching media, transcripts, model files,
tokens, or private paths.
```

```bash
(
  set -euo pipefail
  VERSION=0.1.0
  TAG="v${VERSION}"
  MAIN_REPO=steepkit/whisper-cpp-gui
  TAP_REPO=steepkit/homebrew-tap
  SOURCE_ARCHIVE_REPO=steepkit/whisper-cpp-gui-private-archive
  TAP_ARCHIVE_REPO=steepkit/homebrew-tap-private-archive
  REVIEWED_PRIVATE_COMMIT=REPLACE_WITH_RECORDED_PRIVATE_40_HEX_COMMIT
  REVIEWED_TAP_COMMIT=REPLACE_WITH_RECORDED_PRIVATE_TAP_40_HEX_COMMIT
  PUBLIC_ROOT_COMMIT=REPLACE_WITH_RECORDED_PUBLIC_ROOT_40_HEX_COMMIT
  PUBLIC_TAP_ROOT_COMMIT=REPLACE_WITH_RECORDED_PUBLIC_TAP_ROOT_40_HEX_COMMIT
  PUBLIC_SOURCE_CI_RUN_ID=REPLACE_WITH_RECORDED_NUMERIC_RUN_ID
  PUBLIC_TAP_CI_RUN_ID=REPLACE_WITH_RECORDED_NUMERIC_RUN_ID
  [[ "$REVIEWED_PRIVATE_COMMIT" =~ ^[0-9a-f]{40}$ ]]
  [[ "$REVIEWED_TAP_COMMIT" =~ ^[0-9a-f]{40}$ ]]
  [[ "$PUBLIC_ROOT_COMMIT" =~ ^[0-9a-f]{40}$ ]]
  [[ "$PUBLIC_TAP_ROOT_COMMIT" =~ ^[0-9a-f]{40}$ ]]
  [[ "$PUBLIC_SOURCE_CI_RUN_ID" =~ ^[0-9]+$ ]]
  [[ "$PUBLIC_TAP_CI_RUN_ID" =~ ^[0-9]+$ ]]

  main_visibility="$(gh repo view "$MAIN_REPO" --json visibility --jq .visibility)"
  tap_visibility="$(gh repo view "$TAP_REPO" \
    --json visibility --jq .visibility)"
  draft_state="$(gh release view "$TAG" --repo "$MAIN_REPO" \
    --json isDraft --jq .isDraft)"
  release_body="$(gh release view "$TAG" --repo "$MAIN_REPO" \
    --json body --jq .body)"
  required_release_warning="$(cat <<'EOF'
## Physical Mac validation waiver

No physical Apple Silicon Mac was available for v0.1.0. The physical Apple
Silicon validation procedure documented in ADR 0013 remains unexecuted. This
includes real media, both presets, VAD, txt/srt/vtt output, progress and logs,
cancellation, browser downloads, bootstrap recovery, 8 GB large-v3 memory use,
Downloads permissions, automatic browser launch, and real model download timing
and SHA256 verification.

GitHub-hosted macOS build/startup and Homebrew source-install CI passed only as
packaging checks. They do not establish physical GUI or workload compatibility.
Please report failures without attaching media, transcripts, model files,
tokens, or private paths.
EOF
)"
  tag_commit="$(gh api "repos/${MAIN_REPO}/commits/${TAG}" --jq .sha)"
  main_commit="$(gh api "repos/${MAIN_REPO}/commits/main" --jq .sha)"
  tap_commit="$(gh api "repos/${TAP_REPO}/commits/main" --jq .sha)"
  test "$main_visibility" = PUBLIC
  test "$tap_visibility" = PUBLIC
  test "$tag_commit" = "$PUBLIC_ROOT_COMMIT"
  test "$main_commit" = "$PUBLIC_ROOT_COMMIT"
  test "$tap_commit" = "$PUBLIC_TAP_ROOT_COMMIT"

  test "$(gh api "repos/${SOURCE_ARCHIVE_REPO}" --jq .visibility)" = private
  test "$(gh api "repos/${SOURCE_ARCHIVE_REPO}" --jq .archived)" = true
  test "$(gh api "repos/${SOURCE_ARCHIVE_REPO}" --jq .default_branch)" = main
  test "$(gh api "repos/${SOURCE_ARCHIVE_REPO}/commits/main" --jq .sha)" = \
    "$REVIEWED_PRIVATE_COMMIT"
  source_archive_tree="$(gh api \
    "repos/${SOURCE_ARCHIVE_REPO}/git/commits/${REVIEWED_PRIVATE_COMMIT}" \
    --jq .tree.sha)"
  source_public_tree="$(gh api \
    "repos/${MAIN_REPO}/git/commits/${PUBLIC_ROOT_COMMIT}" --jq .tree.sha)"
  test "$source_archive_tree" = "$source_public_tree"

  test "$(gh api "repos/${TAP_ARCHIVE_REPO}" --jq .visibility)" = private
  test "$(gh api "repos/${TAP_ARCHIVE_REPO}" --jq .archived)" = true
  test "$(gh api "repos/${TAP_ARCHIVE_REPO}" --jq .default_branch)" = main
  test "$(gh api "repos/${TAP_ARCHIVE_REPO}/commits/main" --jq .sha)" = \
    "$REVIEWED_TAP_COMMIT"
  tap_archive_tree="$(gh api \
    "repos/${TAP_ARCHIVE_REPO}/git/commits/${REVIEWED_TAP_COMMIT}" \
    --jq .tree.sha)"
  tap_public_tree="$(gh api \
    "repos/${TAP_REPO}/git/commits/${PUBLIC_TAP_ROOT_COMMIT}" --jq .tree.sha)"
  test "$tap_archive_tree" = "$tap_public_tree"

  verify_public_ci() {
    local repo=$1
    local run_id=$2
    local commit=$3
    local metadata head_sha event conclusion workflow_id branch path title
    local expected_workflow_id
    local job_rows expected result count
    shift 3

    expected_workflow_id="$(gh api \
      "repos/${repo}/actions/workflows/ci.yml" --jq .id)"
    metadata="$(gh api "repos/${repo}/actions/runs/${run_id}" \
      --jq '[.head_sha, .event, .conclusion, .workflow_id, .head_branch, .path, .display_title] | @tsv')"
    IFS=$'\t' read -r head_sha event conclusion workflow_id branch path title \
      <<<"$metadata"
    test "$head_sha" = "$commit"
    test "$event" = workflow_dispatch
    test "$conclusion" = success
    test "$workflow_id" = "$expected_workflow_id"
    test "$branch" = main
    test "$path" = .github/workflows/ci.yml
    test "$title" = 'CI (public-verification)'

    job_rows="$(gh run view "$run_id" --repo "$repo" --json jobs \
      --jq '.jobs[] | [.name, .conclusion] | @tsv')"
    for expected in "$@"
    do
      result="$(awk -F '\t' -v name="$expected" \
        '$1 == name { print $2 }' <<<"$job_rows")"
      count="$(awk -F '\t' -v name="$expected" \
        '$1 == name { count++ } END { print count+0 }' <<<"$job_rows")"
      test "$count" = 1
      test "$result" = success
    done
  }

  verify_public_ci "$MAIN_REPO" "$PUBLIC_SOURCE_CI_RUN_ID" \
    "$PUBLIC_ROOT_COMMIT" \
    "Ubuntu checks" "macOS 14 build and startup smoke"
  verify_public_ci "$TAP_REPO" "$PUBLIC_TAP_CI_RUN_ID" \
    "$PUBLIC_TAP_ROOT_COMMIT" \
    "Tap syntax" "macOS source install"

  if test "$VERSION" = 0.1.0
  then
    if [[ "$release_body" != "$required_release_warning"* ]]
    then
      release_notes_file="$(mktemp)"
      trap 'rm -f "$release_notes_file"' EXIT
      {
        printf '%s\n\n' "$required_release_warning"
        printf '%s\n' "$release_body"
      } >"$release_notes_file"
      gh release edit "$TAG" --repo "$MAIN_REPO" \
        --notes-file "$release_notes_file"
      release_body="$(gh release view "$TAG" --repo "$MAIN_REPO" \
        --json body --jq .body)"
    fi
    [[ "$release_body" = "$required_release_warning"* ]]
  fi
  case "$draft_state" in
  true)
    gh release edit "$TAG" --repo "$MAIN_REPO" --draft=false --latest
    ;;
  false)
    gh release edit "$TAG" --repo "$MAIN_REPO" --latest
    ;;
  *)
    echo "unexpected draft state: ${draft_state}" >&2
    exit 1
    ;;
  esac
  published_draft_state="$(gh release view "$TAG" --repo "$MAIN_REPO" \
    --json isDraft --jq .isDraft)"
  latest_tag="$(gh api "repos/${MAIN_REPO}/releases/latest" --jq .tag_name)"
  test "$published_draft_state" = false
  test "$latest_tag" = "$TAG"
  gh release view "$TAG" --repo "$MAIN_REPO" \
    --json isDraft,tagName,url
)
```

Record:

- reviewed private source commit, matching Git tree hash, public source root
  commit, and immutable tag
- reviewed private tap commit, matching Git tree hash, and public tap root
  commit
- GitHub Release URL
- source archive URL and SHA256
- Formula commit and tap PR
- exact post-publication main and tap CI run IDs and URLs
- M5 evidence and preset decision, or the version-specific waiver and warning
- anonymous access and fresh-machine install results

If the source has a product defect, leave the tag intact and create a new patch
release. If only the Formula is wrong, use a new tap PR and retain the same
source checksum when the archive itself is unchanged.

## 10. Patch releases after public launch

Do not repeat the private-archive or single-root clean publication steps for a
patch release. The public repositories are canonical after `v0.1.0`; retain
their history and use normal reviewed PRs. A patch release still requires an
explicit owner release approval and either completed physical M5 evidence or a
new version-specific waiver. A previous waiver never carries forward silently.

### 10.1 Merge release evidence and verify the source candidate

Record the waiver, warnings, and patch-release procedure through the normal
Issue, draft PR, G1/G2, CI, and G5 flow before creating a tag. Then run from a
clean public source checkout:

```bash
(
  set -euo pipefail
  VERSION=0.1.1
  PREVIOUS_VERSION=0.1.0
  MAIN_REPO=steepkit/whisper-cpp-gui
  WAIVER=docs/adr/0016-waive-physical-mac-validation-for-v0.1.1.md
  TAG="v${VERSION}"

  gh auth status
  git fetch --prune --tags origin
  git switch main
  git pull --ff-only origin main
  test -z "$(git status --porcelain --untracked-files=all)"
  test "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)"
  test "$(gh api "repos/${MAIN_REPO}/releases/latest" --jq .tag_name)" = \
    "v${PREVIOUS_VERSION}"
  if git rev-parse --verify --quiet "refs/tags/${TAG}" >/dev/null
  then
    echo "local tag already exists: ${TAG}" >&2
    exit 1
  fi
  tag_status=0
  git ls-remote --exit-code --tags origin "refs/tags/${TAG}" >/dev/null || \
    tag_status=$?
  case "$tag_status" in
  0) echo "remote tag already exists: ${TAG}" >&2; exit 1 ;;
  2) ;;
  *) echo "remote tag lookup failed: ${tag_status}" >&2; exit 1 ;;
  esac
  test -f "$WAIVER"
  grep -q '^Status: Accepted$' "$WAIVER"
  grep -q "waives physical M5 validation for \`v${VERSION}\`" "$WAIVER"
  grep -q "v${VERSION} owner waiver" docs/notes.md
  grep -q "v${VERSION} validation notice" README.md

  gofmt_out="$(gofmt -l .)"
  test -z "$gofmt_out"
  go build ./...
  go vet ./...
  go test -count=1 ./...
  node testdata/browser/session_behavior_test.mjs
  shellcheck scripts/publish_clean_snapshot.sh scripts/smoke_start.sh \
    testdata/stubs/ffmpeg testdata/stubs/smoke-exit \
    testdata/stubs/smoke-hang testdata/stubs/whisper-cli

  source_commit="$(git rev-parse HEAD)"
  source_ci_url="$(gh run list --repo "$MAIN_REPO" --workflow CI \
    --commit "$source_commit" --status success --limit 1 --json url \
    --jq '.[0].url // ""')"
  test -n "$source_ci_url"
  printf 'Source commit: %s\nSource CI: %s\n' \
    "$source_commit" "$source_ci_url"
)
```

### 10.2 Create the immutable tag and draft Release

Prepare release notes whose first section is the version-specific physical-Mac
warning. Include the user-visible fixes after that warning. Create an annotated
tag at the verified `main` commit, push it once, and create a draft Release:

```bash
(
  set -euo pipefail
  VERSION=0.1.1
  TAG="v${VERSION}"
  MAIN_REPO=steepkit/whisper-cpp-gui
  RELEASE_NOTES=REPLACE_WITH_REVIEWED_NOTES_FILE
  EXPECTED_SOURCE_COMMIT=REPLACE_WITH_RECORDED_40_HEX_SOURCE_COMMIT

  [[ "$EXPECTED_SOURCE_COMMIT" =~ ^[0-9a-f]{40}$ ]]
  git fetch --prune --tags origin
  git switch main
  git pull --ff-only origin main
  test -z "$(git status --porcelain --untracked-files=all)"
  test "$(git rev-parse HEAD)" = "$EXPECTED_SOURCE_COMMIT"
  test "$(git rev-parse origin/main)" = "$EXPECTED_SOURCE_COMMIT"

  test -f "$RELEASE_NOTES"
  IFS= read -r first_release_notes_line <"$RELEASE_NOTES"
  test "$first_release_notes_line" = '## Physical Mac validation waiver'
  grep -q "v${VERSION}" "$RELEASE_NOTES"
  if git rev-parse --verify --quiet "refs/tags/${TAG}" >/dev/null
  then
    test "$(git cat-file -t "refs/tags/${TAG}")" = tag
    test "$(git rev-list -n 1 "$TAG")" = "$EXPECTED_SOURCE_COMMIT"
  else
    git -c tag.gpgSign=false tag -a "$TAG" \
      -m "whisper-cpp-gui ${TAG}" "$EXPECTED_SOURCE_COMMIT"
  fi

  tag_status=0
  git ls-remote --exit-code --tags origin "refs/tags/${TAG}" >/dev/null || \
    tag_status=$?
  case "$tag_status" in
  0)
    test "$(gh api "repos/${MAIN_REPO}/commits/${TAG}" --jq .sha)" = \
      "$EXPECTED_SOURCE_COMMIT"
    ;;
  2)
    git push origin "refs/tags/${TAG}"
    ;;
  *)
    echo "remote tag lookup failed: ${tag_status}" >&2
    exit 1
    ;;
  esac
  remote_tag_object="$(gh api "repos/${MAIN_REPO}/git/ref/tags/${TAG}" \
    --jq '.object | select(.type == "tag") | .sha')"
  [[ "$remote_tag_object" =~ ^[0-9a-f]{40}$ ]]
  remote_tag_commit="$(gh api \
    "repos/${MAIN_REPO}/git/tags/${remote_tag_object}" \
    --jq '.object | select(.type == "commit") | .sha')"
  test "$remote_tag_commit" = "$EXPECTED_SOURCE_COMMIT"
  test "$(git rev-parse "${TAG}^{commit}")" = "$EXPECTED_SOURCE_COMMIT"

  owner="${MAIN_REPO%%/*}"
  name="${MAIN_REPO#*/}"
  release_tag="$(gh api graphql \
    -f owner="$owner" -f name="$name" -f tag="$TAG" \
    -f query='query($owner: String!, $name: String!, $tag: String!) {
      repository(owner: $owner, name: $name) {
        release(tagName: $tag) { isDraft tagName }
      }
    }' --jq '.data.repository.release.tagName // ""')"
  if test -z "$release_tag"
  then
    gh release create "$TAG" --repo "$MAIN_REPO" --draft --verify-tag \
      --title "whisper-cpp-gui ${TAG}" --notes-file "$RELEASE_NOTES"
  else
    test "$release_tag" = "$TAG"
  fi
  test "$(gh release view "$TAG" --repo "$MAIN_REPO" \
    --json isDraft --jq .isDraft)" = true
  test "$(gh release view "$TAG" --repo "$MAIN_REPO" \
    --json name --jq .name)" = "whisper-cpp-gui ${TAG}"
  release_body="$(gh release view "$TAG" --repo "$MAIN_REPO" \
    --json body --jq .body)"
  reviewed_release_body="$(cat "$RELEASE_NOTES")"
  test "$release_body" = "$reviewed_release_body"
)
```

Never move or recreate this tag. Correct source defects with a later patch
version.

### 10.3 Pin and verify the anonymous archive

The tag archive is available while the GitHub Release is still a draft. Fetch
it without credentials, compute the Formula checksum, and verify the tag still
resolves to the reviewed commit:

```bash
(
  set -euo pipefail
  VERSION=0.1.1
  TAG="v${VERSION}"
  MAIN_REPO=steepkit/whisper-cpp-gui
  EXPECTED_SOURCE_COMMIT=REPLACE_WITH_RECORDED_40_HEX_SOURCE_COMMIT
  ARCHIVE_URL="https://github.com/${MAIN_REPO}/archive/refs/tags/${TAG}.tar.gz"
  archive="$(mktemp)"
  trap 'rm -f "$archive"' EXIT

  [[ "$EXPECTED_SOURCE_COMMIT" =~ ^[0-9a-f]{40}$ ]]
  verify_remote_tag() {
    local tag_commit tag_object
    tag_object="$(gh api "repos/${MAIN_REPO}/git/ref/tags/${TAG}" \
      --jq '.object | select(.type == "tag") | .sha')"
    [[ "$tag_object" =~ ^[0-9a-f]{40}$ ]]
    tag_commit="$(gh api "repos/${MAIN_REPO}/git/tags/${tag_object}" \
      --jq '.object | select(.type == "commit") | .sha')"
    test "$tag_commit" = "$EXPECTED_SOURCE_COMMIT"
  }

  verify_remote_tag
  env -u GH_TOKEN -u GITHUB_TOKEN \
    curl --disable --fail --location --proto '=https' \
    --proto-redir '=https' --tlsv1.2 --remove-on-error \
    --output "$archive" "$ARCHIVE_URL"
  test -s "$archive"
  tar -tzf "$archive" >/dev/null
  archive_sha256="$(shasum -a 256 "$archive" | awk '{print $1}')"
  [[ "$archive_sha256" =~ ^[0-9a-f]{64}$ ]]
  verify_remote_tag
  printf 'Archive URL: %s\nArchive SHA256: %s\n' \
    "$ARCHIVE_URL" "$archive_sha256"
)
```

### 10.4 Update the public tap through a PR

Create one tap Issue/branch/PR. Change the Formula URL and SHA256 to the values
from section 10.3, update any exact-version CI assertion, and update user-facing
waiver references. Before opening the PR, run `ruby -c`, `brew style`, and
`brew info --json=v2` against the checked-out Formula. The PR must pass tap
syntax and the clean macOS source-install/`brew test` job. Merge only after the
Formula PR records its archive URL, SHA256, source commit, source CI, and owner
approval. Record the PR number, exact head branch and commit, and successful
`pull_request` CI run ID and URL before merging. Also record the successful
post-merge `push` CI run ID and URL. Section 10.5 verifies both runs; direct
pushes to tap `main` do not satisfy this gate.

### 10.5 Publish and verify the patch release

Before publishing, verify that the source tag, draft Release, merged tap
Formula, source CI, and tap CI all refer to the recorded values. Then publish
the existing draft as latest. Verify anonymous archive access and an actual
Homebrew update from an installed previous version:

```bash
(
  set -euo pipefail
  VERSION=0.1.1
  TAG="v${VERSION}"
  MAIN_REPO=steepkit/whisper-cpp-gui
  TAP_REPO=steepkit/homebrew-tap
  FORMULA=steepkit/tap/whisper-cpp-gui
  RELEASE_NOTES=REPLACE_WITH_REVIEWED_NOTES_FILE
  EXPECTED_SOURCE_COMMIT=REPLACE_WITH_RECORDED_40_HEX_SOURCE_COMMIT
  EXPECTED_TAP_COMMIT=REPLACE_WITH_RECORDED_40_HEX_TAP_COMMIT
  EXPECTED_ARCHIVE_SHA256=REPLACE_WITH_RECORDED_64_HEX_SHA256
  SOURCE_CI_RUN_ID=REPLACE_WITH_RECORDED_NUMERIC_RUN_ID
  TAP_PR_CI_RUN_ID=REPLACE_WITH_RECORDED_NUMERIC_RUN_ID
  TAP_CI_RUN_ID=REPLACE_WITH_RECORDED_NUMERIC_RUN_ID
  TAP_PR_NUMBER=REPLACE_WITH_RECORDED_NUMERIC_PR_NUMBER
  EXPECTED_TAP_PR_HEAD=release/v0.1.1
  EXPECTED_TAP_PR_HEAD_COMMIT=REPLACE_WITH_RECORDED_40_HEX_TAP_PR_HEAD_COMMIT

  [[ "$EXPECTED_SOURCE_COMMIT" =~ ^[0-9a-f]{40}$ ]]
  [[ "$EXPECTED_TAP_COMMIT" =~ ^[0-9a-f]{40}$ ]]
  [[ "$EXPECTED_TAP_PR_HEAD_COMMIT" =~ ^[0-9a-f]{40}$ ]]
  [[ "$EXPECTED_ARCHIVE_SHA256" =~ ^[0-9a-f]{64}$ ]]
  [[ "$SOURCE_CI_RUN_ID" =~ ^[0-9]+$ ]]
  [[ "$TAP_PR_CI_RUN_ID" =~ ^[0-9]+$ ]]
  [[ "$TAP_CI_RUN_ID" =~ ^[0-9]+$ ]]
  [[ "$TAP_PR_NUMBER" =~ ^[0-9]+$ ]]
  test -f "$RELEASE_NOTES"
  IFS= read -r first_release_notes_line <"$RELEASE_NOTES"
  test "$first_release_notes_line" = '## Physical Mac validation waiver'

  test "$(gh api "repos/${MAIN_REPO}" --jq .visibility)" = public
  test "$(gh api "repos/${TAP_REPO}" --jq .visibility)" = public
  test "$(gh api "repos/${MAIN_REPO}/commits/main" --jq .sha)" = \
    "$EXPECTED_SOURCE_COMMIT"
  remote_tag_object="$(gh api "repos/${MAIN_REPO}/git/ref/tags/${TAG}" \
    --jq '.object | select(.type == "tag") | .sha')"
  [[ "$remote_tag_object" =~ ^[0-9a-f]{40}$ ]]
  test "$(gh api "repos/${MAIN_REPO}/git/tags/${remote_tag_object}" \
    --jq '.object | select(.type == "commit") | .sha')" = \
    "$EXPECTED_SOURCE_COMMIT"
  test "$(gh api "repos/${TAP_REPO}/commits/main" --jq .sha)" = \
    "$EXPECTED_TAP_COMMIT"

  test "$(gh pr view "$TAP_PR_NUMBER" --repo "$TAP_REPO" \
    --json state --jq .state)" = MERGED
  test "$(gh pr view "$TAP_PR_NUMBER" --repo "$TAP_REPO" \
    --json isDraft --jq .isDraft)" = false
  test "$(gh pr view "$TAP_PR_NUMBER" --repo "$TAP_REPO" \
    --json baseRefName --jq .baseRefName)" = main
  test "$(gh pr view "$TAP_PR_NUMBER" --repo "$TAP_REPO" \
    --json headRefName --jq .headRefName)" = "$EXPECTED_TAP_PR_HEAD"
  test "$(gh pr view "$TAP_PR_NUMBER" --repo "$TAP_REPO" \
    --json headRefOid --jq .headRefOid)" = "$EXPECTED_TAP_PR_HEAD_COMMIT"
  test "$(gh pr view "$TAP_PR_NUMBER" --repo "$TAP_REPO" \
    --json mergeCommit --jq .mergeCommit.oid)" = "$EXPECTED_TAP_COMMIT"
  tap_pr_body="$(gh pr view "$TAP_PR_NUMBER" --repo "$TAP_REPO" \
    --json body --jq .body)"
  grep -Fq 'The repository owner explicitly approved' <<<"$tap_pr_body"

  source_workflow_id="$(gh api \
    "repos/${MAIN_REPO}/actions/workflows/ci.yml" --jq .id)"
  tap_workflow_id="$(gh api \
    "repos/${TAP_REPO}/actions/workflows/ci.yml" --jq .id)"
  test "$(gh api "repos/${MAIN_REPO}/actions/runs/${SOURCE_CI_RUN_ID}" \
    --jq .workflow_id)" = "$source_workflow_id"
  test "$(gh api "repos/${MAIN_REPO}/actions/runs/${SOURCE_CI_RUN_ID}" \
    --jq .head_sha)" = "$EXPECTED_SOURCE_COMMIT"
  test "$(gh api "repos/${MAIN_REPO}/actions/runs/${SOURCE_CI_RUN_ID}" \
    --jq .event)" = push
  test "$(gh api "repos/${MAIN_REPO}/actions/runs/${SOURCE_CI_RUN_ID}" \
    --jq .conclusion)" = success
  test "$(gh api "repos/${MAIN_REPO}/actions/runs/${SOURCE_CI_RUN_ID}/jobs" \
    --paginate --jq '[.jobs[] | select(.name == "Ubuntu checks" and .conclusion == "success")] | length')" = 1
  test "$(gh api "repos/${MAIN_REPO}/actions/runs/${SOURCE_CI_RUN_ID}/jobs" \
    --paginate --jq '[.jobs[] | select(.name == "macOS 14 build and startup smoke" and .conclusion == "success")] | length')" = 1
  test "$(gh api "repos/${TAP_REPO}/actions/runs/${TAP_PR_CI_RUN_ID}" \
    --jq .workflow_id)" = "$tap_workflow_id"
  test "$(gh api "repos/${TAP_REPO}/actions/runs/${TAP_PR_CI_RUN_ID}" \
    --jq .event)" = pull_request
  test "$(gh api "repos/${TAP_REPO}/actions/runs/${TAP_PR_CI_RUN_ID}" \
    --jq '.pull_requests | length')" = 1
  test "$(gh api "repos/${TAP_REPO}/actions/runs/${TAP_PR_CI_RUN_ID}" \
    --jq '.pull_requests[0].number')" = "$TAP_PR_NUMBER"
  test "$(gh api "repos/${TAP_REPO}/actions/runs/${TAP_PR_CI_RUN_ID}" \
    --jq '.pull_requests[0].head.sha')" = "$EXPECTED_TAP_PR_HEAD_COMMIT"
  test "$(gh api "repos/${TAP_REPO}/actions/runs/${TAP_PR_CI_RUN_ID}" \
    --jq .conclusion)" = success
  test "$(gh api "repos/${TAP_REPO}/actions/runs/${TAP_PR_CI_RUN_ID}/jobs" \
    --paginate --jq '[.jobs[] | select(.name == "Tap syntax" and .conclusion == "success")] | length')" = 1
  test "$(gh api "repos/${TAP_REPO}/actions/runs/${TAP_PR_CI_RUN_ID}/jobs" \
    --paginate --jq '[.jobs[] | select(.name == "macOS source install" and .conclusion == "success")] | length')" = 1
  test "$(gh api "repos/${TAP_REPO}/actions/runs/${TAP_CI_RUN_ID}" \
    --jq .workflow_id)" = "$tap_workflow_id"
  test "$(gh api "repos/${TAP_REPO}/actions/runs/${TAP_CI_RUN_ID}" \
    --jq .head_sha)" = "$EXPECTED_TAP_COMMIT"
  test "$(gh api "repos/${TAP_REPO}/actions/runs/${TAP_CI_RUN_ID}" \
    --jq .event)" = push
  test "$(gh api "repos/${TAP_REPO}/actions/runs/${TAP_CI_RUN_ID}" \
    --jq .conclusion)" = success
  test "$(gh api "repos/${TAP_REPO}/actions/runs/${TAP_CI_RUN_ID}/jobs" \
    --paginate --jq '[.jobs[] | select(.name == "Tap syntax" and .conclusion == "success")] | length')" = 1
  test "$(gh api "repos/${TAP_REPO}/actions/runs/${TAP_CI_RUN_ID}/jobs" \
    --paginate --jq '[.jobs[] | select(.name == "macOS source install" and .conclusion == "success")] | length')" = 1

  formula="$(env -u GH_TOKEN -u GITHUB_TOKEN \
    curl --disable --fail --silent --show-error --proto '=https' \
    --proto-redir '=https' --tlsv1.2 \
    "https://raw.githubusercontent.com/${TAP_REPO}/main/Formula/whisper-cpp-gui.rb")"
  grep -Fq "archive/refs/tags/${TAG}.tar.gz" <<<"$formula"
  grep -Fq "sha256 \"${EXPECTED_ARCHIVE_SHA256}\"" <<<"$formula"

  archive="$(mktemp)"
  trap 'rm -f "$archive"' EXIT
  env -u GH_TOKEN -u GITHUB_TOKEN \
    curl --disable --fail --location --proto '=https' \
    --proto-redir '=https' --tlsv1.2 --remove-on-error \
    --output "$archive" \
    "https://github.com/${MAIN_REPO}/archive/refs/tags/${TAG}.tar.gz"
  test -s "$archive"
  tar -tzf "$archive" >/dev/null
  current_archive_sha256="$(shasum -a 256 "$archive" | awk '{print $1}')"
  test "$current_archive_sha256" = "$EXPECTED_ARCHIVE_SHA256"

  draft_state="$(gh release view "$TAG" --repo "$MAIN_REPO" \
    --json isDraft --jq .isDraft)"
  release_body="$(gh release view "$TAG" --repo "$MAIN_REPO" \
    --json body --jq .body)"
  reviewed_release_body="$(cat "$RELEASE_NOTES")"
  test "$release_body" = "$reviewed_release_body"
  case "$draft_state" in
  true) gh release edit "$TAG" --repo "$MAIN_REPO" --draft=false --latest ;;
  false) gh release edit "$TAG" --repo "$MAIN_REPO" --latest ;;
  *) echo "unexpected draft state: ${draft_state}" >&2; exit 1 ;;
  esac
  test "$(gh api "repos/${MAIN_REPO}/releases/latest" --jq .tag_name)" = \
    "$TAG"

  brew update
  brew list --versions "$FORMULA" >/dev/null
  formula_binary="$(brew --prefix "$FORMULA")/bin/whisper-cpp-gui"
  test -x "$formula_binary"
  test "$("$formula_binary" --version)" = \
    "whisper-cpp-gui 0.1.0"
  brew upgrade "$FORMULA"
  formula_binary="$(brew --prefix "$FORMULA")/bin/whisper-cpp-gui"
  test -x "$formula_binary"
  test "$("$formula_binary" --version)" = \
    "whisper-cpp-gui ${VERSION}"
)
```

Record the source commit/tag/Release URL, archive SHA256, Formula and tap merge
commits, exact source and tap CI URLs, anonymous archive result, and Homebrew
version output. If publication fails after the tag exists, keep the tag and
draft Release intact, fix the failing gate, and resume from that gate.

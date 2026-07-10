#!/usr/bin/env bash

set -euo pipefail

usage() {
  printf 'usage: %s CHECKOUT DEV_REPO ARCHIVE_NAME REVIEWED_COMMIT SNAPSHOT_DIR VERSION DESCRIPTION REQUIRED_JOB [REQUIRED_JOB ...]\n' "$0" >&2
  exit 2
}

die() {
  printf '%s\n' "$1" >&2
  exit 1
}

if (( $# < 9 )); then
  usage
fi

checkout=$1
dev_repo=$2
archive_name=$3
reviewed_commit=$4
snapshot_dir=$5
version=$6
description=$7
shift 7
required_jobs=("$@")

[[ "$dev_repo" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || usage
[[ "$archive_name" =~ ^[A-Za-z0-9_.-]+$ ]] || usage
[[ "$reviewed_commit" =~ ^[0-9a-f]{40}$ ]] || usage
[[ "$snapshot_dir" = /* && "$snapshot_dir" != / ]] || usage
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || usage
[[ -n "$description" ]] || usage

owner=${dev_repo%%/*}
repo_name=${dev_repo#*/}
archive_repo="${owner}/${archive_name}"
snapshot_parent=${snapshot_dir%/*}

for command in awk gh git grep mktemp tar; do
  command -v "$command" >/dev/null || die "required command not found: $command"
done

test "$(git -C "$checkout" rev-parse --is-inside-work-tree)" = true || \
  die "not a Git worktree: $checkout"
test -z "$(git -C "$checkout" status --porcelain --untracked-files=all)" || \
  die 'reviewed checkout is not clean'
test "$(git -C "$checkout" rev-parse HEAD)" = "$reviewed_commit" || \
  die 'reviewed checkout is not at REVIEWED_COMMIT'

private_tree=$(git -C "$checkout" rev-parse "${reviewed_commit}^{tree}")
snapshot_date=$(git -C "$checkout" show -s --format=%aI "$reviewed_commit")
mkdir -p "$snapshot_parent"

build_dir=""
cleanup() {
  if [[ -n "$build_dir" && -d "$build_dir" ]]; then
    rm -rf "$build_dir"
  fi
}
trap cleanup EXIT

build_dir=$(mktemp -d "${snapshot_dir}.build.XXXXXX")
mkdir "$build_dir/repository"
git -C "$checkout" archive --format=tar \
  --output="$build_dir/source.tar" "$reviewed_commit"
tar -xf "$build_dir/source.tar" -C "$build_dir/repository"
git -C "$build_dir/repository" init --quiet --initial-branch=main
git -C "$build_dir/repository" config core.autocrlf false
git -C "$build_dir/repository" add --all
GIT_AUTHOR_NAME=steepkit \
  GIT_AUTHOR_EMAIL=noreply@steepkit.invalid \
  GIT_AUTHOR_DATE="$snapshot_date" \
  GIT_COMMITTER_NAME=steepkit \
  GIT_COMMITTER_EMAIL=noreply@steepkit.invalid \
  GIT_COMMITTER_DATE="$snapshot_date" \
  git -C "$build_dir/repository" -c commit.gpgsign=false commit --quiet \
    -m "${repo_name} v${version}"
expected_snapshot_commit=$(git -C "$build_dir/repository" rev-parse HEAD)
expected_snapshot_tree=$(git -C "$build_dir/repository" rev-parse 'HEAD^{tree}')

if [[ ! -e "$snapshot_dir" ]]; then
  mv "$build_dir/repository" "$snapshot_dir"
else
  rm -rf "$build_dir/repository"
fi
rm -f "$build_dir/source.tar"
rmdir "$build_dir"
build_dir=""

test "$(git -C "$snapshot_dir" symbolic-ref --short HEAD)" = main || \
  die 'snapshot branch is not main'
test -z "$(git -C "$snapshot_dir" status --porcelain --untracked-files=all)" || \
  die 'snapshot worktree is not clean'
snapshot_commit=$(git -C "$snapshot_dir" rev-parse HEAD)
snapshot_tree=$(git -C "$snapshot_dir" rev-parse 'HEAD^{tree}')
test "$snapshot_tree" = "$private_tree" || die 'private/public snapshot tree mismatch'
test "$expected_snapshot_tree" = "$private_tree" || \
  die 'fresh snapshot tree differs from reviewed tree'
test "$snapshot_commit" = "$expected_snapshot_commit" || \
  die 'snapshot root does not have the deterministic anonymous metadata'

read -r -a snapshot_line <<<"$(git -C "$snapshot_dir" rev-list --parents -n 1 HEAD)"
(( ${#snapshot_line[@]} == 1 )) || die 'snapshot commit is not a root commit'
snapshot_refs=$(git -C "$snapshot_dir" for-each-ref \
  --format='%(refname)' refs/heads refs/tags)
test "$snapshot_refs" = refs/heads/main || die 'snapshot contains unexpected refs'

printf 'Reviewed commit: %s\nPrivate tree: %s\nSnapshot root: %s\n' \
  "$reviewed_commit" "$private_tree" "$snapshot_commit"

if [[ "${SNAPSHOT_PREPARE_ONLY:-0}" = 1 ]]; then
  exit 0
fi

test "$(git -C "$checkout" symbolic-ref --short HEAD)" = main || \
  die 'reviewed checkout must be on main for publication'
test "${CONFIRM_PUBLICATION:-}" = "$dev_repo" || \
  die "set CONFIRM_PUBLICATION=$dev_repo to allow GitHub publication"

repo_lookup() {
  local repo=$1
  local repo_owner=${repo%%/*}
  local output

  if ! output=$(gh repo list "$repo_owner" --limit 1000 \
    --json nameWithOwner \
    --jq ".[] | select(.nameWithOwner == \"$repo\") | .nameWithOwner"); then
    return 2
  fi
  if [[ -z "$output" ]]; then
    return 1
  fi
  test "$output" = "$repo" || return 2
  printf '%s\n' "$output"
}

repo_field() {
  local repo=$1
  local expression=$2
  gh api "repos/${repo}" --jq "$expression"
}

verify_required_jobs() {
  local repo=$1
  local run_id=$2
  local rows job conclusion count

  rows=$(gh run view "$run_id" --repo "$repo" --json jobs \
    --jq '.jobs[] | [.name, .conclusion] | @tsv')
  for job in "${required_jobs[@]}"; do
    conclusion=$(awk -F '\t' -v name="$job" '$1 == name { print $2 }' <<<"$rows")
    count=$(awk -F '\t' -v name="$job" '$1 == name { count++ } END { print count+0 }' \
      <<<"$rows")
    test "$count" = 1 || die "required CI job count is not one: $job"
    test "$conclusion" = success || die "required CI job did not succeed: $job"
  done
}

verify_snapshot_history() {
  local repo=$1
  local commit=$2
  local require_success=${3:-false}
  local rows run_id head_sha workflow_id event branch path title status conclusion
  local expected_workflow_id expected_title release_ids artifact_ids

  # GitHub reports a custom run-name in both .name and .display_title.
  expected_workflow_id=$(gh api \
    "repos/${repo}/actions/workflows/ci.yml" --jq .id)
  rows=$(gh api --paginate "repos/${repo}/actions/runs?per_page=100" \
    --jq '.workflow_runs[] | [.id, .head_sha, .workflow_id, .event, .head_branch, .path, .display_title, .status, .conclusion] | @tsv')
  while IFS=$'\t' read -r run_id head_sha workflow_id event branch path title status conclusion; do
    [[ -n "$head_sha" ]] || continue
    test "$head_sha" = "$commit" || \
      die 'snapshot repository contains an Actions run for another commit'
    test "$workflow_id" = "$expected_workflow_id" || \
      die 'snapshot repository contains an Actions run from another workflow'
    case "$event" in
    push)
      expected_title='CI (push)'
      ;;
    workflow_dispatch)
      case "$title" in
      'CI (private-staging)' | 'CI (public-recovery)' | 'CI (public-verification)')
        expected_title=$title
        ;;
      *)
        die 'snapshot repository contains an unmarked workflow dispatch'
        ;;
      esac
      ;;
    *)
      die 'snapshot repository contains an unexpected Actions event'
      ;;
    esac
    test "$branch" = main || \
      die 'snapshot repository contains an Actions run for another branch'
    test "$path" = .github/workflows/ci.yml || \
      die 'snapshot repository contains an Actions run from another path'
    test "$title" = "$expected_title" || \
      die 'snapshot repository contains an unexpected Actions run title'
    if [[ "$require_success" = true ]]; then
      test "$status" = completed || \
        die "snapshot Actions run is not complete: $run_id"
      test "$conclusion" = success || \
        die "snapshot Actions run did not succeed: $run_id"
    fi
  done <<<"$rows"

  release_ids=$(gh api --paginate "repos/${repo}/releases?per_page=100" \
    --jq '.[].id')
  test -z "$release_ids" || \
    die 'snapshot repository already contains a GitHub Release'
  artifact_ids=$(gh api --paginate "repos/${repo}/actions/artifacts?per_page=100" \
    --jq '.artifacts[].id')
  test -z "$artifact_ids" || \
    die 'snapshot repository already contains an Actions artifact'
}

remove_unsuccessful_snapshot_runs() {
  local repo=$1
  local rows run_id status conclusion

  rows=$(gh api --paginate "repos/${repo}/actions/runs?per_page=100" \
    --jq '.workflow_runs[] | [.id, .status, .conclusion] | @tsv')
  while IFS=$'\t' read -r run_id status conclusion; do
    [[ -n "$run_id" ]] || continue
    test "$status" = completed || \
      die "snapshot Actions run is still active: $run_id"
    if [[ "$conclusion" != success ]]; then
      gh api --method DELETE "repos/${repo}/actions/runs/${run_id}"
    fi
  done <<<"$rows"
}

wait_for_run() {
  local repo=$1
  local commit=$2
  local event=$3
  local previous_id=${4:-}
  local run_id=""
  local attempt=0

  while (( attempt < 60 )); do
    run_id=$(gh run list --repo "$repo" --workflow CI --event "$event" \
      --commit "$commit" --limit 1 --json databaseId \
      --jq '.[0].databaseId // ""')
    if [[ -n "$run_id" && "$run_id" != "$previous_id" ]]; then
      break
    fi
    attempt=$((attempt + 1))
    sleep 5
  done
  test -n "$run_id" || die "CI run did not appear for $repo at $commit"
  test "$run_id" != "$previous_id" || die "new CI run did not appear for $repo"
  gh run watch "$run_id" --repo "$repo" --exit-status >&2
  verify_required_jobs "$repo" "$run_id"
  printf '%s\n' "$run_id"
}

archive_status=0
archive_resolved=$(repo_lookup "$archive_repo") || archive_status=$?
case "$archive_status" in
0)
  test "$archive_resolved" = "$archive_repo" || die 'archive repository resolved unexpectedly'
  ;;
1)
  dev_status=0
  dev_resolved=$(repo_lookup "$dev_repo") || dev_status=$?
  test "$dev_status" = 0 || die "development repository is unavailable: $dev_repo"
  test "$dev_resolved" = "$dev_repo" || die 'development repository resolved unexpectedly'
  test "$(repo_field "$dev_repo" .visibility)" = private || \
    die 'development repository is not private'
  test "$(repo_field "$dev_repo" .archived)" = false || \
    die 'development repository is already archived'
  test "$(repo_field "$dev_repo" .default_branch)" = main || \
    die 'development repository default branch is not main'
  test "$(gh api "repos/${dev_repo}/commits/main" --jq .sha)" = "$reviewed_commit" || \
    die 'development main is not REVIEWED_COMMIT'
  private_ci_run_id=$(gh run list --repo "$dev_repo" --workflow CI --event push \
    --commit "$reviewed_commit" --status success --limit 1 --json databaseId \
    --jq '.[0].databaseId // ""')
  test -n "$private_ci_run_id" || \
    die 'reviewed private commit has no successful push CI run'
  verify_required_jobs "$dev_repo" "$private_ci_run_id"
  gh repo rename --repo "$dev_repo" "$archive_name" --yes
  ;;
*)
  die "archive repository lookup failed: $archive_repo"
  ;;
esac

test "$(repo_field "$archive_repo" .visibility)" = private || \
  die 'archive repository is not private'
test "$(repo_field "$archive_repo" .default_branch)" = main || \
  die 'archive default branch is not main'
test "$(gh api "repos/${archive_repo}/commits/main" --jq .sha)" = "$reviewed_commit" || \
  die 'archive main is not REVIEWED_COMMIT'
archive_tree=$(gh api "repos/${archive_repo}/git/commits/${reviewed_commit}" --jq .tree.sha)
test "$archive_tree" = "$private_tree" || die 'archive commit tree changed'
private_ci_run_id=$(gh run list --repo "$archive_repo" --workflow CI --event push \
  --commit "$reviewed_commit" --status success --limit 1 --json databaseId \
  --jq '.[0].databaseId // ""')
test -n "$private_ci_run_id" || \
  die 'archived reviewed commit has no successful push CI run'
verify_required_jobs "$archive_repo" "$private_ci_run_id"
private_ci_url=$(gh run view "$private_ci_run_id" --repo "$archive_repo" \
  --json url --jq .url)

archive_url="https://github.com/${archive_repo}.git"
git -C "$checkout" remote set-url origin "$archive_url"

dev_status=0
dev_resolved=$(repo_lookup "$dev_repo") || dev_status=$?
resume_public=false
case "$dev_status" in
0)
  if [[ "$dev_resolved" = "$archive_repo" ]]; then
    gh repo create "$dev_repo" --private --description "$description" --disable-wiki
  else
    test "$dev_resolved" = "$dev_repo" || die 'public staging repository resolved unexpectedly'
    existing_visibility=$(repo_field "$dev_repo" .visibility)
    case "$existing_visibility" in
    private)
      ;;
    public)
      existing_public_commit=$(gh api "repos/${dev_repo}/commits/main" --jq .sha)
      test "$existing_public_commit" = "$snapshot_commit" || \
        die 'existing public repository is not the verified snapshot root'
      resume_public=true
      ;;
    *)
      die "unexpected existing repository visibility: $existing_visibility"
      ;;
    esac
  fi
  ;;
1)
  gh repo create "$dev_repo" --private --description "$description" --disable-wiki
  ;;
*)
  die "public staging repository lookup failed: $dev_repo"
  ;;
esac

public_url="https://github.com/${dev_repo}.git"
if git -C "$snapshot_dir" remote get-url origin >/dev/null 2>&1; then
  test "$(git -C "$snapshot_dir" remote get-url origin)" = "$public_url" || \
    die 'snapshot origin is unexpected'
else
  git -C "$snapshot_dir" remote add origin "$public_url"
fi
if [[ "$resume_public" = false ]]; then
  test "$(repo_field "$dev_repo" .visibility)" = private || \
    die 'snapshot repository became public before staging checks'
fi
git -C "$snapshot_dir" push --set-upstream origin main
gh repo edit "$dev_repo" --default-branch main --enable-wiki=false

public_commit=$(gh api "repos/${dev_repo}/commits/main" --jq .sha)
test "$public_commit" = "$snapshot_commit" || die 'public main is not the snapshot root'
public_tree=$(gh api "repos/${dev_repo}/git/commits/${public_commit}" --jq .tree.sha)
test "$public_tree" = "$private_tree" || die 'public root tree differs from reviewed tree'
public_parent_count=$(gh api "repos/${dev_repo}/git/commits/${public_commit}" \
  --jq '.parents | length')
test "$public_parent_count" = 0 || die 'public main commit is not a root commit'

remote_refs=$(git ls-remote --refs "$public_url" | awk '{ print $2 }')
test "$remote_refs" = refs/heads/main || die 'public staging repository has extra refs'
issue_count=$(gh api "repos/${dev_repo}/issues?state=all&per_page=100" --jq length)
pull_count=$(gh api "repos/${dev_repo}/pulls?state=all&per_page=100" --jq length)
test "$issue_count" = 0 || die 'public staging repository already has Issues or PRs'
test "$pull_count" = 0 || die 'public staging repository already has pull requests'
verify_snapshot_history "$dev_repo" "$public_commit"

previous_staging_id=$(gh run list --repo "$dev_repo" --workflow CI \
  --event workflow_dispatch --commit "$public_commit" --limit 1 \
  --json databaseId --jq '.[0].databaseId // ""')
staging_phase=private-staging
if [[ "$resume_public" = true ]]; then
  staging_phase=public-recovery
fi
gh workflow run CI --repo "$dev_repo" --ref main \
  -f publication_phase="$staging_phase"
staging_run_id=$(wait_for_run "$dev_repo" "$public_commit" workflow_dispatch \
  "$previous_staging_id")
staging_ci_url=$(gh run view "$staging_run_id" --repo "$dev_repo" --json url --jq .url)
test "$(gh api "repos/${dev_repo}/actions/runs/${staging_run_id}" \
  --jq .display_title)" = "CI (${staging_phase})" || \
  die 'staging CI run is missing its publication-phase marker'
verify_snapshot_history "$dev_repo" "$public_commit"
remove_unsuccessful_snapshot_runs "$dev_repo"
verify_snapshot_history "$dev_repo" "$public_commit" true

if [[ "$(repo_field "$archive_repo" .archived)" = false ]]; then
  gh repo archive "$archive_repo" --yes
fi
test "$(repo_field "$archive_repo" .visibility)" = private || \
  die 'archive visibility changed before publication'
test "$(repo_field "$archive_repo" .archived)" = true || \
  die 'private development repository was not archived'
test "$(gh api "repos/${archive_repo}/commits/main" --jq .sha)" = "$reviewed_commit" || \
  die 'archived main moved after review'

visibility=$(repo_field "$dev_repo" .visibility)
case "$visibility" in
private)
  gh repo edit "$dev_repo" --visibility public \
    --accept-visibility-change-consequences
  ;;
public)
  ;;
*)
  die "unexpected public repository visibility: $visibility"
  ;;
esac
test "$(repo_field "$dev_repo" .visibility)" = public || \
  die 'snapshot repository is not public'

previous_dispatch_id=$(gh run list --repo "$dev_repo" --workflow CI \
  --event workflow_dispatch --commit "$public_commit" --limit 1 \
  --json databaseId --jq '.[0].databaseId // ""')
gh workflow run CI --repo "$dev_repo" --ref main \
  -f publication_phase=public-verification
public_run_id=$(wait_for_run "$dev_repo" "$public_commit" workflow_dispatch \
  "$previous_dispatch_id")
public_ci_url=$(gh run view "$public_run_id" --repo "$dev_repo" --json url --jq .url)
test "$(gh api "repos/${dev_repo}/actions/runs/${public_run_id}" \
  --jq .display_title)" = 'CI (public-verification)' || \
  die 'public CI run is missing its public-verification marker'
verify_snapshot_history "$dev_repo" "$public_commit" true

anonymous_home=$(mktemp -d)
trap 'cleanup; rm -rf "$anonymous_home"' EXIT
env -u GH_TOKEN -u GITHUB_TOKEN HOME="$anonymous_home" \
  GIT_CONFIG_NOSYSTEM=1 GIT_TERMINAL_PROMPT=0 \
  git -c credential.helper= -c core.askPass= \
  ls-remote "$public_url" HEAD >/dev/null

remote_refs=$(env -u GH_TOKEN -u GITHUB_TOKEN HOME="$anonymous_home" \
  GIT_CONFIG_NOSYSTEM=1 GIT_TERMINAL_PROMPT=0 \
  git -c credential.helper= -c core.askPass= ls-remote --refs "$public_url" | \
  awk '{ print $2 }')
test "$remote_refs" = refs/heads/main || die 'public repository exposes extra refs'

printf 'Archive repository: %s\nReviewed commit: %s\nPrivate tree: %s\n' \
  "$archive_repo" "$reviewed_commit" "$private_tree"
printf 'Private CI: %s\nPublic repository: %s\nPublic root: %s\n' \
  "$private_ci_url" "$dev_repo" "$public_commit"
printf 'Staging CI run ID: %s\nStaging CI: %s\n' \
  "$staging_run_id" "$staging_ci_url"
printf 'Public CI run ID: %s\nPublic CI: %s\n' \
  "$public_run_id" "$public_ci_url"

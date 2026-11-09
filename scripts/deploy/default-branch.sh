#!/bin/bash
# Reusable default-branch helpers for build and deploy orchestration, so a script cannot hardcode "master": this
# repository's default branch is "main" and a clone can have no master, so `git fetch origin master` and
# `git worktree add … origin/master` fail there (#1064). Source this file; it only defines functions and runs nothing.
#
# Resolution order: the branch the remote names now (git ls-remote --symref), else the clone's origin/HEAD (set at
# clone time, so it can name a branch the remote no longer has), else master. The remote's answer counts only when it
# is a valid branch name, and every fetch uses the qualified refs/heads/<name> refspec: a bare name starting with "-"
# would otherwise be parsed as a git fetch option.

# persona_default_branch: echo the default branch name. Always yields something (master as the last resort), matching
# deploy.sh's historical fallback.
persona_default_branch() {
  local branch
  branch=$(git ls-remote --symref origin HEAD 2>/dev/null |
    awk '$1 == "ref:" && $3 == "HEAD" && sub("^refs/heads/", "", $2) { print $2; exit }') || true
  if [ -z "$branch" ] || ! git check-ref-format "refs/heads/$branch"; then
    branch=$(git symbolic-ref --quiet refs/remotes/origin/HEAD 2>/dev/null) || true
    branch=${branch#refs/remotes/origin/}
  fi
  printf '%s' "${branch:-master}"
}

# persona_fetch_default_branch [branch]: fetch the default branch from origin into FETCH_HEAD through its qualified
# refspec, so an option-shaped name cannot be treated as a git option. Returns non-zero when the remote has no such
# branch. With no argument it resolves the branch first.
persona_fetch_default_branch() {
  local branch=${1:-}
  [ -n "$branch" ] || branch=$(persona_default_branch)
  git fetch -q origin "refs/heads/$branch"
}

# persona_detached_worktree DIR: make DIR a detached worktree at the remote default branch's tip, whatever that branch
# is called. A directory that is already a worktree is moved to the freshly fetched tip; any other path is created.
# Echoes the resolved branch name. This is the reusable replacement for hand-mirrored
# `git worktree add --detach <dir> origin/master` lines in out-of-tree build orchestration.
persona_detached_worktree() {
  local dir=${1:?usage: persona_detached_worktree <dir>}
  local branch sha
  branch=$(persona_default_branch)
  git fetch -q origin "refs/heads/$branch"
  sha=$(git rev-parse FETCH_HEAD)
  if git -C "$dir" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    git -C "$dir" checkout -q --detach "$sha"
  else
    git worktree add -q --detach "$dir" "$sha"
  fi
  printf '%s\n' "$branch"
}

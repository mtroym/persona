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
  git fetch -q origin "refs/heads/$branch" || return 1
}

# persona_is_linked_worktree DIR: true only when DIR is the root of a registered *linked* worktree. A plain subdirectory
# of a checkout also answers "yes" to rev-parse --is-inside-work-tree, so match the absolute path against the linked
# entries of `git worktree list` (the first entry is always the main working tree and is skipped): reusing a subdir or
# the main checkout would detach the developer's own branch.
persona_is_linked_worktree() {
  local dir=$1 abs
  abs=$(cd -- "$dir" 2>/dev/null && pwd -P) || return 1
  git worktree list --porcelain |
    awk '/^worktree /{ n++; if (n > 1) print substr($0, 10) }' |
    grep -Fxq -- "$abs"
}

# persona_detached_worktree DIR: make DIR a detached worktree at the remote default branch's tip, whatever that branch
# is called. A directory that is already a linked worktree is moved to the freshly fetched tip; any other path is
# created. Echoes the resolved branch name. Every git step propagates a failure explicitly, so a stale worktree can
# never be reported as success from a context without errexit (an `if`/`||` caller). This is the reusable replacement
# for hand-mirrored `git worktree add --detach <dir> origin/master` lines in out-of-tree build orchestration.
persona_detached_worktree() {
  local dir=${1:?usage: persona_detached_worktree <dir>}
  local branch sha
  branch=$(persona_default_branch)
  git fetch -q origin "refs/heads/$branch" || return 1
  sha=$(git rev-parse FETCH_HEAD) || return 1
  if persona_is_linked_worktree "$dir"; then
    git -C "$dir" checkout -q --detach "$sha" || return 1
  else
    # "--" keeps a dash-prefixed relative dir an argument, not a git option. An occupied or non-git path makes git
    # refuse here with a clear error instead of silently detaching the checkout that contains it.
    git worktree add -q --detach -- "$dir" "$sha" || return 1
  fi
  printf '%s\n' "$branch"
}

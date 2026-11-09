#!/bin/bash
# check-fast.sh — run only the local fast checks that a change needs (#2849).
#
#   scripts/check-fast.sh                 # diff merge-base(HEAD, default branch)..working tree, pick checks by path;
#                                         # the default branch is origin/HEAD, else origin/main, else origin/master
#                                         # (local refs only, no network)
#   scripts/check-fast.sh --base main     # diff against another ref
#   scripts/check-fast.sh --all           # every local fast check except iOS/macOS builds and the Postgres suite,
#                                         # including every scripts/**/test_*.py suite CI's Scripts tests job runs
#                                         # (a test_*.py that never mentions unittest is a SKIP line, as in
#                                         # scripts/ci/python-tests.py; diff mode skips it too)
#   scripts/check-fast.sh -h|--help
#
# Path mapping (diff mode):
#   web/**                     npm ci (only with no node_modules or a changed package.json/package-lock.json),
#                              typecheck, vitest run
#   schema/extension/**, spec/fixtures/injected/**, spec/fixtures/sync/**, scripts/mock/inbox_events.json
#                              also the web check: web/src and its vitest suite read these files. They are the inputs
#                              outside web/ of the web lane in scripts/ci/paths.py; test_check_fast.py keeps the two in sync
#   integrations/<pkg>/**      node --test test/*.test.{js,mjs} (packages without tests are skipped)
#   site/**                    node --test site/worker.test.mjs
#   sdk/ts/**                  bun test (skipped with a reason when bun is absent)
#   sdk/go/**                  go test ./... inside the sdk/go module
#   server/internal/foo/**     go test ./internal/foo/...   (cmd/<x>/** and plugins/<x>/** likewise; other server
#                              changes: go build ./...)
#   server/**, sdk/go/**       also the server's repo-wide guard tests (GO_GUARD_PKGS, GO_GUARD_TESTS), which fail on
#                              code outside their own package; --all runs them as part of go test ./...
#   server/internal/db/migrations/**, scripts/ci/migration_freeze.py
#                              python3 scripts/ci/migration_freeze.py (a migration already on the base must not be
#                              edited, deleted or renamed; comment- and whitespace-only edits pass; a new file may be
#                              added). A rename out of the directory counts too. --all runs it when the script is present.
#   server/**/*_test.go, sdk/go/**/*_test.go, scripts/ci/sleep_ratchet.py, scripts/ci/sleep_baseline.json
#                              python3 scripts/ci/sleep_ratchet.py (each Go test file's time.Sleep count may stay or
#                              fall, never rise; comments, strings and a // sleep-ok: reason line do not count; a new
#                              file starts at zero; --update only lowers). A rename out of those trees counts too.
#                              --all runs it when the script is present.
#   spec/fixtures/**, schema/extension/**, integrations/**, docs/<file>, scripts/test/server_test_env.py
#                              also go test on each server package whose _test.go names that input (server-test.yml's
#                              path filter), found at run time with git grep (comment lines are ignored)
#   android/**                 scripts/app/android_i18n_check.py plus the android checkers' own unittests (no gradle)
#   .github/**                 scripts/test/pr_needs_issue_test.mjs
#   schema/**                  the extension-schema py, the sdk bundle py, and sdk/go go test (mirrors CI's job)
#   scripts/**                 privacy/PR/extension/refs/bundle/i18n checks picked by path; PR+extension when
#                              ambiguous (a case "*" matches slashes, so scripts/* covers nested paths too); plus
#                              each changed scripts/<dir>/test_*.py and the test_<name>.py of each changed script
#                              <name>.py / <name>.sh (- read as _; db.sh's is test_db_sh.py), run from its
#                              directory as CI does (#3968). scripts/hooks/** also runs
#                              scripts/test/test_privacy_check.py (it runs the hooks); scripts/privacy_check.py also
#                              scripts/release/test_check_bundle_privacy.py (it imports privacy_check)
#   docs/**, any *.md          scripts/test/test_doc_paths.py and test_doc_go_paths.py (paths quoted in the docs, the
#                              CLAUDE.md doc index), test_doc_wire.py (the wire lists in protocol.md, extensions.md)
#   server/internal/{proto,hub,extension}/**, schema/extension/manifest.schema.json
#                              also test_doc_wire.py: the code side of those wire lists
#   apple/**                   scripts/test/test_apple_l10n.py (String Catalogs, Chinese Swift literals); the iOS/macOS
#                              builds and tests are skipped with a note (they need a Mac)
#   deploy/docker/**, docs/operations/docker.md, Dockerfile
#                              scripts/test/test_docker_entrypoint.py (it also reads the root Dockerfile)
#                              These Linux suites run in CI's Scripts tests job; they run from scripts/test as there.
# scripts/privacy_check.py runs on every invocation that has changes (it is a seconds-long local scan, no network).
#
# Independent checks run in parallel; each command gets a GNU timeout (override with the CHECK_FAST_TIMEOUT_*
# variables below). Every check prints one PASS / FAIL / TIMEOUT / SKIP line with its duration, followed by a
# summary. The temp log directory is kept when a check fails or times out and removed on a fully clean run.
#
# Exit codes: 0 all checks passed, skipped or not needed; 1 one or more checks FAILED; 124 no failure but at least
# one check TIMEOUT (a timeout is reported, not silently treated as success; run the full suite when that happens).
# Ctrl-C, SIGTERM and SIGHUP, to the script or to its whole process group, stop every running check first, keep the
# log directory, and exit 130, 143 or 129.
# A missing optional toolchain (node < 22, go, bun) skips only its checks in diff mode; in --all mode a missing
# core tool (node >= 22, go, python3) is a failure.
#
# This script never runs iOS/macOS checks (use a Mac with scripts/app/sim.sh; PersonaKit: swift test, docs/operations/testing.md),
# the Android gradle build (scripts/app/android.sh), or the full server suite against real Postgres
# (scripts/test/test-server.sh).
set -uo pipefail

cd "$(dirname "$0")/../.."

# Per-command timeouts in seconds (CLAUDE.md: every background step carries a timeout). Override for slow machines.
TIMEOUT_NPM_CI="${CHECK_FAST_TIMEOUT_NPM_CI:-600}"
TIMEOUT_WEB_TEST="${CHECK_FAST_TIMEOUT_WEB_TEST:-300}"
TIMEOUT_GO_TEST="${CHECK_FAST_TIMEOUT_GO_TEST:-300}"
TIMEOUT_OTHER="${CHECK_FAST_TIMEOUT_OTHER:-120}"

usage() {
  awk 'NR==1 {next} /^#/ {sub(/^# ?/, ""); print; next} {exit}' "$0"
}

ALL=0
BASE_REF=""
while [ $# -gt 0 ]; do
  case "$1" in
    --all) ALL=1 ;;
    # An empty value is refused: BASE_REF="" means "no --base" below.
    --base)
      [ $# -ge 2 ] && [ -n "$2" ] || { echo "check-fast: --base needs a git ref" >&2; exit 2; }
      BASE_REF=$2; shift ;;
    --base=*)
      BASE_REF=${1#*=}
      [ -n "$BASE_REF" ] || { echo "check-fast: --base needs a git ref" >&2; exit 2; } ;;
    -h|--help) usage; exit 0 ;;
    *) echo "check-fast: unknown argument: $1 (see --help)" >&2; exit 2 ;;
  esac
  shift
done

if command -v timeout >/dev/null 2>&1; then
  TIMEOUT_BIN=timeout
elif command -v gtimeout >/dev/null 2>&1; then
  TIMEOUT_BIN=gtimeout
else
  echo "check-fast: GNU timeout is required (macOS: brew install coreutils)" >&2
  exit 2
fi

have() { command -v "$1" >/dev/null 2>&1; }

# Coarse interpreter gate: only the Node major version is checked here (22+). Each package's engines field
# (web needs 22.13, some integrations 22.19) is npm's own check; without engine-strict npm only warns on it.
NODE_MAJOR=0
if have node; then
  NODE_MAJOR=$(node -e 'console.log(process.versions.node.split(".")[0])' 2>/dev/null || echo 0)
fi
node_ok() { [ "$NODE_MAJOR" -ge 22 ] 2>/dev/null; }
HAVE_GO=0; have go && HAVE_GO=1
HAVE_PY=0; have python3 && HAVE_PY=1
HAVE_BUN=0; have bun && HAVE_BUN=1

WORK=$(mktemp -d "${TMPDIR:-/tmp}/check-fast.XXXXXX")
# Keep WORK after a failure or timeout (its logs are the trail to read); a clean run removes it on exit.
KEEP_WORK=0
cleanup_work() { [ "$KEEP_WORK" -eq 0 ] && rm -rf "$WORK"; }
trap cleanup_work EXIT

# descendants prints every process below pid $1, parents before their children (POSIX ps, so it runs on macOS too).
descendants() {
  ps -A -o pid= -o ppid= 2>/dev/null | awk -v root="$1" '
    { kids[$2] = kids[$2] " " $1 }
    END {
      n = split(kids[root], queue, " ")
      for (i = 1; i <= n; i++) {
        print queue[i]
        m = split(kids[queue[i]], c, " ")
        for (j = 1; j <= m; j++) queue[++n] = c[j]
      }
    }'
}

# on_signal stops every check on Ctrl-C, TERM or HUP. A signal to this script alone never reaches them: the job
# subshells run in the background (a non-interactive shell starts them ignoring SIGINT) and GNU timeout moves each
# command into its own process group, so without this they keep running, reparented to init, writing into $WORK.
# A signal to the whole process group (a closed terminal or tmux pane, kill -- -PGID) reaches the job subshells and
# tee as well, so they ignore TERM and HUP too (launch_all); otherwise they would die first and orphan the checks.
# The whole tree is frozen first (SIGSTOP, repeated until no new process appears) so nothing forks the next step or
# escapes to init while it is being collected. The processes in this script's own process group (subshells, tee,
# short helpers) then get KILL while still frozen; every other one is a check in the group timeout made for it and
# gets TERM and CONT (timeout forwards the TERM to its group), and whatever is still alive after timeout's own -k
# grace gets KILL.
on_signal() {  # on_signal <signal name> <exit code>
  local _sig=$1 _code=$2 _all=" " _new _p _g _pass _list _left _own _groups
  trap '' INT TERM HUP
  for _pass in 1 2 3 4 5 6 7 8 9 10; do
    _new=0
    for _p in $(descendants $$); do
      case "$_all" in *" $_p "*) continue ;; esac
      # The ps and awk of the snapshot have exited by now, so kill fails for them and they are not collected.
      kill -STOP "$_p" 2>/dev/null || continue
      _all="$_all$_p "; _new=1
    done
    [ "$_new" -eq 0 ] && break
  done
  # One ps for the process group of each frozen process; if it fails, every process takes the TERM path below.
  _own=$(ps -o pgid= -p $$ 2>/dev/null | tr -d ' ')
  _list=$(echo $_all | tr ' ' ',')
  _groups=$(ps -o pid= -o pgid= -p "$_list" 2>/dev/null)
  # bash reports each job subshell it reaps after a KILL ("line N: <pid> Killed ( ... )"); keep that noise off stderr.
  exec 3>&2 2>/dev/null
  if [ -n "$_own" ] && [ -n "$_groups" ]; then
    _all=" "
    while read -r _p _g; do
      [ -n "$_p" ] || continue
      if [ "$_g" = "$_own" ]; then kill -KILL "$_p" 2>/dev/null; else _all="$_all$_p "; fi
    done < <(printf '%s\n' "$_groups")
  fi
  for _p in $_all; do kill -TERM "$_p" 2>/dev/null; kill -CONT "$_p" 2>/dev/null; done
  _list=$(echo $_all | tr ' ' ',')
  if [ -n "$_list" ]; then
    _pass=0
    while [ "$_pass" -lt 70 ]; do  # up to ~7s: timeout's -k 5s grace plus a margin
      # A process that has exited stays a zombie (Z) until its parent reaps it; count only live processes.
      _left=$(ps -o stat= -p "$_list" 2>/dev/null | grep -vc '^Z')
      [ "${_left:-0}" -eq 0 ] && break
      sleep 0.1
      _pass=$((_pass + 1))
    done
    for _p in $_all; do kill -KILL "$_p" 2>/dev/null; done
  fi
  wait
  exec 2>&3 3>&-
  KEEP_WORK=1
  echo >&2
  echo "check-fast: interrupted (SIG$_sig); stopped the running checks; logs in $WORK" >&2
  exit "$_code"
}
trap 'on_signal INT 130' INT
trap 'on_signal TERM 143' TERM
trap 'on_signal HUP 129' HUP

# Job bookkeeping (plain indexed arrays for macOS's bash 3.2).
JOB_NAME=(); JOB_FN=(); JOB_ARG=(); JOB_LOG=(); JOB_STATUS=(); JOB_PID=()
INFO=()                 # informational SKIP lines for the summary

# arr_has reports whether a token equals one of the remaining arguments (arr_has "$t" "${arr[@]:-}"), so the
# package and go-group lists can be indexed arrays that keep spaces and glob characters in directory names.
arr_has() {
  local _token=$1 _x
  shift
  for _x in "$@"; do [ "$_x" = "$_token" ] && return 0; done
  return 1
}

# step runs one command under a timeout; a timeout classifies the whole job and stops it.
# JOB_HINT (set by the job body) is appended to the TIMEOUT detail; each job appends its steps to one log.
step() {
  local _seconds=$1 _desc=$2
  shift 2
  echo "+ $_desc (timeout ${_seconds}s)"
  # tee the output so the job log stays live and a whole-suite self-timeout can still be classified as TIMEOUT
  # instead of FAIL. One appended file per job is not reset by in_dir's subshell, so a multi-step job (web:
  # npm ci, typecheck, vitest) keeps every step. The grep anchors on the runners' own whole-suite markers, never
  # on the words "timed out" (a failing assertion that merely prints them must stay a FAIL):
  #   go test panics "panic: test timed out after …" when ITS suite deadline kills the run;
  #   node's TAP reporter emits "failureType: 'testTimeoutFailure'". A node per-test timeout under the default
  #   reporter is a clean exit 1 naming the test, so it rightly stays a FAIL, not a whole-job TIMEOUT.
  local _out="$WORK/job.${JOB_I:-x}.steps.log"
  "$TIMEOUT_BIN" -k 5s "$_seconds" "$@" 2>&1 | tee -a "$_out"
  local _rc=${PIPESTATUS[0]}
  local _hint="${JOB_HINT:+ ($JOB_HINT)}"
  if [ "$_rc" -eq 124 ]; then
    printf 'TIMEOUT\t%s\t%s\t%s%s\n' "$_rc" "$SECONDS" "$_desc" "$_hint" > "$JOB_STATUS"
    exit 0
  fi
  if [ "$_rc" -eq 137 ]; then
    # SIGKILL: timeout -k's kill after a command that ignored TERM, or the OOM killer — both mean "did not finish".
    printf 'TIMEOUT\t%s\t%s\t%s (killed: timeout SIGKILL or OOM)%s\n' "$_rc" "$SECONDS" "$_desc" "$_hint" > "$JOB_STATUS"
    exit 0
  fi
  if [ "$_rc" -ne 0 ] && grep -qE "panic: test timed out after [0-9]|failureType: '?testTimeoutFailure'?" "$_out" 2>/dev/null; then
    printf 'TIMEOUT\t%s\t%s\t%s (test suite self-timed out)%s\n' "$_rc" "$SECONDS" "$_desc" "$_hint" > "$JOB_STATUS"
    exit 0
  fi
  return "$_rc"
}

skip() { printf 'SKIP\t0\t%s\t%s\n' "$SECONDS" "$1" > "$JOB_STATUS"; exit 0; }

# in_dir runs the rest of the arguments in a directory; if a step there classified the job (a timeout), the whole
# job body stops, instead of continuing with the next step.
in_dir() {
  local _dir=$1
  shift
  ( cd "$_dir" && "$@" )
  local _rc=$?
  [ -s "$JOB_STATUS" ] && exit 0
  return "$_rc"
}

schedule() {  # schedule <name> <function> <arg>
  local _i=${#JOB_NAME[@]}
  JOB_NAME+=("$1"); JOB_FN+=("$2"); JOB_ARG+=("$3")
  JOB_LOG+=("$WORK/job.$_i.log"); JOB_STATUS+=("$WORK/job.$_i.status")
  echo "  queued: $1"
}

launch_all() {
  local _i
  for _i in "${!JOB_NAME[@]}"; do
    (
      # A TERM or HUP to the whole process group must not end the subshell (or the tee its steps start) before
      # on_signal has frozen the checks under it; on_signal KILLs them itself (see there).
      trap '' TERM HUP
      JOB_STATUS=${JOB_STATUS[$_i]}
      JOB_I=$_i
      SECONDS=0
      set +e
      "${JOB_FN[$_i]}" "${JOB_ARG[$_i]}"
      _rc=$?
      if [ ! -s "$JOB_STATUS" ]; then
        if [ "$_rc" -eq 0 ]; then
          printf 'PASS\t0\t%s\t\n' "$SECONDS" > "$JOB_STATUS"
        else
          printf 'FAIL\t%s\t%s\texit %s\n' "$_rc" "$SECONDS" "$_rc" > "$JOB_STATUS"
        fi
      fi
    ) > "${JOB_LOG[$_i]}" 2>&1 &
    JOB_PID+=($!)
  done
  for _i in "${!JOB_PID[@]}"; do wait "${JOB_PID[$_i]}" 2>/dev/null || true; done
}

# --- check bodies ---------------------------------------------------------

job_privacy() {
  [ "$HAVE_PY" -eq 1 ] || skip "python3 not found"
  step "$TIMEOUT_OTHER" "python3 scripts/privacy_check.py" python3 scripts/privacy_check.py || return $?
}

job_web() {  # $1 = ci when npm ci must run first
  if ! node_ok; then skip "node >= 22 not found (found: $(node -v 2>/dev/null || echo none))"; fi
  if [ "$1" = ci ]; then
    JOB_HINT="a stale web/node_modules can cause this; remove web/node_modules and rerun"
    in_dir web step "$TIMEOUT_NPM_CI" "web: npm ci" npm ci || return $?
    JOB_HINT=""
  fi
  in_dir web step "$TIMEOUT_OTHER" "web: tsc -b (typecheck)" npm run typecheck || return $?
  in_dir web step "$TIMEOUT_WEB_TEST" "web: vitest run" npm test
}

job_integration() {  # $1 = package directory name under integrations/
  if ! node_ok; then skip "node >= 22 not found (found: $(node -v 2>/dev/null || echo none))"; fi
  local _dir="integrations/$1"
  [ -f "$_dir/package.json" ] || skip "integrations/$1 has no package.json"
  # Build the list in the package dir so unmatched globs never reach node as literal paths (bash 3.2 has no nullglob).
  local _tests=() _t
  while IFS= read -r _t; do _tests+=("$_t"); done \
    < <(cd "$_dir" 2>/dev/null && find test -maxdepth 1 \( -name '*.test.js' -o -name '*.test.mjs' \) -print 2>/dev/null)
  [ "${#_tests[@]}" -gt 0 ] || skip "integrations/$1 has no test/*.test.{js,mjs}"
  in_dir "$_dir" step "$TIMEOUT_OTHER" "integrations/$1: node --test" node --test "${_tests[@]}"
}

job_site() {
  if ! node_ok; then skip "node >= 22 not found (found: $(node -v 2>/dev/null || echo none))"; fi
  [ -f site/worker.test.mjs ] || skip "site/worker.test.mjs not found"
  step "$TIMEOUT_OTHER" "site: node --test worker.test.mjs" node --test site/worker.test.mjs
}

job_sdk_ts() {
  [ "$HAVE_BUN" -eq 1 ] || skip "bun not found in PATH (sdk/ts tests run on bun)"
  in_dir sdk/ts step "$TIMEOUT_OTHER" "sdk/ts: bun test" bun test
}

job_go() {  # $1 = a package dir (internal/foo, cmd/bar, plugins/baz) or ALL for ./...
  [ "$HAVE_GO" -eq 1 ] || skip "go not found"
  local _pattern _label _inner
  if [ "$1" = ALL ]; then _pattern=./...; _label="go test ./..."; else _pattern="./$1/..."; _label="go test ./$1/..."; fi
  _inner=$((TIMEOUT_GO_TEST - 10)); [ "$_inner" -lt 5 ] && _inner=5
  in_dir server step "$TIMEOUT_GO_TEST" "$_label" go test -count=1 -timeout "${_inner}s" "$_pattern"
}

# The server's repo-wide guards: tests that read other packages' source, go list output, the migrations or sdk/go, so
# a change anywhere under server/ or sdk/go/ can fail them while go test on the touched packages passes (#1936 broke
# the lazyre guard on main that way). The list is curated, not every *guard*_test.go: internal/node's pathguard and
# writeguard tests and internal/hub's ownership_guard_test.go test their own package's behaviour. GO_GUARD_PKGS run
# whole; GO_GUARD_TESTS names the guard tests of GO_GUARD_TEST_PKGS, whose other tests are slow or need Postgres.
GO_GUARD_PKGS=(./internal/lazyre ./internal/archguard ./internal/tzguard ./plugins)
GO_GUARD_TEST_PKGS=(./internal/hub ./internal/assistant)
GO_GUARD_TESTS='^(TestHubAnswersOnlyItsOwnRoutes|TestHubDoesNotImportTheManager|TestHubCallsNoModel|TestHubStoreStaysSmall'\
'|TestTheHubOpensNoneOfTheManagersFiles|TestStoresTouchTheirOwnTables|TestSettingsKeysAreOneSidesOwn'\
'|TestTheLaunchersSealedFieldsAreReSealed|TestPlacesThatSealAreKnown)$'

# job_go_guards runs both guard commands, the second one also when the first failed, so one run names every failure.
job_go_guards() {
  [ "$HAVE_GO" -eq 1 ] || skip "go not found"
  local _inner=$((TIMEOUT_GO_TEST - 10)) _rc=0
  [ "$_inner" -lt 5 ] && _inner=5
  in_dir server step "$TIMEOUT_GO_TEST" "go test ${GO_GUARD_PKGS[*]}" \
    go test -count=1 -timeout "${_inner}s" "${GO_GUARD_PKGS[@]}" || _rc=$?
  in_dir server step "$TIMEOUT_GO_TEST" "go test -run <guard tests> ${GO_GUARD_TEST_PKGS[*]}" \
    go test -count=1 -timeout "${_inner}s" -run "$GO_GUARD_TESTS" "${GO_GUARD_TEST_PKGS[@]}" || _rc=$?
  return "$_rc"
}

job_go_build() {
  [ "$HAVE_GO" -eq 1 ] || skip "go not found"
  in_dir server step "$TIMEOUT_GO_TEST" "go build ./..." go build ./...
}

job_sdk_go() {  # sdk/go is its own Go module (its own go.mod), not part of server/.
  [ "$HAVE_GO" -eq 1 ] || skip "go not found"
  local _inner=$((TIMEOUT_GO_TEST - 10)); [ "$_inner" -lt 5 ] && _inner=5
  in_dir sdk/go step "$TIMEOUT_GO_TEST" "sdk/go: go test ./..." go test -count=1 -timeout "${_inner}s" ./...
}

job_i18n() {
  [ "$HAVE_PY" -eq 1 ] || skip "python3 not found"
  step "$TIMEOUT_OTHER" "python3 scripts/app/android_i18n_check.py" python3 scripts/app/android_i18n_check.py || return $?
  # The checkers' own unittests, run from scripts/app the way CI does (their fixture paths are relative).
  in_dir scripts/app step "$TIMEOUT_OTHER" "scripts/app: test_android_i18n_check.py self-test" \
    python3 -B test_android_i18n_check.py || return $?
  in_dir scripts/app step "$TIMEOUT_OTHER" "scripts/app: test_android_token_baseline.py self-test" \
    python3 -B test_android_token_baseline.py
}

job_external_refs() {
  [ "$HAVE_PY" -eq 1 ] || skip "python3 not found"
  step "$TIMEOUT_OTHER" "python3 scripts/test/test_external_service_refs.py" \
    python3 -B scripts/test/test_external_service_refs.py || return $?
}

job_sdk_bundle() {
  [ "$HAVE_PY" -eq 1 ] || skip "python3 not found"
  step "$TIMEOUT_OTHER" "python3 scripts/test/test_sdk_bundle.py" \
    python3 -B scripts/test/test_sdk_bundle.py || return $?
}

job_scripts_test() {  # $1 = scripts/<dir>/test_<name>.py, run from its directory the way CI's Scripts tests job does
  [ "$HAVE_PY" -eq 1 ] || skip "python3 not found"
  local _dir _module
  _dir=$(dirname "$1"); _module=$(basename "$1" .py)
  in_dir "$_dir" step "$TIMEOUT_OTHER" "$1" python3 -B -m unittest -q "$_module"
}

# job_scripts_all runs every SCRIPT_TESTS suite (--all) one after another in a single job, the way CI's Scripts tests
# job loops over them, instead of starting ~60 python processes at once. Each suite runs from its own directory under
# its own TIMEOUT_OTHER; one failing or slow suite does not stop the rest. The failed and timed-out files are listed
# last so the summary's log tail names them. Any failure makes the job a FAIL; only timeouts make it a TIMEOUT.
job_scripts_all() {
  [ "$HAVE_PY" -eq 1 ] || skip "python3 not found"
  local _t _rc _failed=() _timed_out=()
  for _t in "${SCRIPT_TESTS[@]}"; do
    echo "+ $_t (timeout ${TIMEOUT_OTHER}s)"
    ( cd "$(dirname "$_t")" && "$TIMEOUT_BIN" -k 5s "$TIMEOUT_OTHER" python3 -B -m unittest -q "$(basename "$_t" .py)" ) 2>&1
    _rc=$?
    case "$_rc" in
      0) ;;
      124|137) _timed_out+=("$_t") ;;
      *) _failed+=("$_t") ;;
    esac
  done
  echo "${#SCRIPT_TESTS[@]} suite(s): ${#_failed[@]} failed, ${#_timed_out[@]} timed out"
  for _t in "${_timed_out[@]:-}"; do [ -n "$_t" ] && echo "timed out: $_t"; done
  for _t in "${_failed[@]:-}"; do [ -n "$_t" ] && echo "failed: $_t"; done
  [ "${#_failed[@]}" -eq 0 ] || return 1
  if [ "${#_timed_out[@]}" -gt 0 ]; then
    printf 'TIMEOUT\t124\t%s\t%s suite(s) timed out: %s\n' "$SECONDS" "${#_timed_out[@]}" "${_timed_out[*]}" > "$JOB_STATUS"
  fi
  return 0
}

job_pr_needs_issue() {
  if ! node_ok; then skip "node >= 22 not found (found: $(node -v 2>/dev/null || echo none))"; fi
  step "$TIMEOUT_OTHER" "node scripts/test/pr_needs_issue_test.mjs" node scripts/test/pr_needs_issue_test.mjs || return $?
}

# job_migration_freeze compares migrations already on the base with the index and the working tree. In diff mode the
# base is the merge-base check-fast already resolved (so --base agrees); --all resolves the default branch itself.
job_migration_freeze() {
  [ "$HAVE_PY" -eq 1 ] || skip "python3 not found"
  if [ "$ALL" -eq 1 ]; then
    step "$TIMEOUT_OTHER" "python3 scripts/ci/migration_freeze.py" python3 scripts/ci/migration_freeze.py || return $?
  else
    step "$TIMEOUT_OTHER" "python3 scripts/ci/migration_freeze.py --base $MB" \
      python3 scripts/ci/migration_freeze.py --base "$MB" || return $?
  fi
}

# job_sleep_ratchet counts fixed time.Sleep calls in Go tests. In diff mode the base is the merge-base check-fast
# already resolved (so --base agrees); --all resolves the default branch itself.
job_sleep_ratchet() {
  [ "$HAVE_PY" -eq 1 ] || skip "python3 not found"
  if [ "$ALL" -eq 1 ]; then
    step "$TIMEOUT_OTHER" "python3 scripts/ci/sleep_ratchet.py" python3 scripts/ci/sleep_ratchet.py || return $?
  else
    step "$TIMEOUT_OTHER" "python3 scripts/ci/sleep_ratchet.py --base $MB" \
      python3 scripts/ci/sleep_ratchet.py --base "$MB" || return $?
  fi
}

# migration_freeze_wanted is true when a migration already on the base might have changed, or when the checker itself
# changed. --name-only lists a rename's destination only, so a file moved out of the directory shows up in the
# name-status scan (status, then the old path, then the new path).
migration_freeze_wanted() {
  [ "$ALL" -eq 1 ] && return 0
  local f status path newpath
  while IFS= read -r f; do
    case "$f" in
      server/internal/db/migrations/*|scripts/ci/migration_freeze.py) return 0 ;;
    esac
  done <<< "$CHANGED"
  while IFS= read -r -d '' status; do
    IFS= read -r -d '' path || return 1
    case "$status" in
      R*|C*)
        IFS= read -r -d '' newpath || return 1
        case "$path" in server/internal/db/migrations/*) return 0 ;; esac
        path=$newpath
        ;;
    esac
    case "$path" in
      server/internal/db/migrations/*|scripts/ci/migration_freeze.py) return 0 ;;
    esac
  done < <(git diff --name-status --find-renames -z "$MB" HEAD
           git diff --name-status --find-renames -z
           git diff --cached --name-status --find-renames -z)
  return 1
}

# sleep_ratchet_wanted is true when a Go test's fixed sleeps might have changed, or when the checker or its baseline
# changed. --name-only lists a rename's destination only, so a test file moved out of server/ or sdk/go/ shows up in
# the name-status scan (status, then the old path, then the new path).
sleep_ratchet_wanted() {
  [ "$ALL" -eq 1 ] && return 0
  local f status path newpath
  while IFS= read -r f; do
    case "$f" in
      server/*_test.go|sdk/go/*_test.go|scripts/ci/sleep_ratchet.py|scripts/ci/sleep_baseline.json) return 0 ;;
    esac
  done <<< "$CHANGED"
  while IFS= read -r -d '' status; do
    IFS= read -r -d '' path || return 1
    case "$status" in
      R*|C*)
        IFS= read -r -d '' newpath || return 1
        case "$path" in
          server/*_test.go|sdk/go/*_test.go) return 0 ;;
        esac
        path=$newpath
        ;;
    esac
    case "$path" in
      server/*_test.go|sdk/go/*_test.go|scripts/ci/sleep_ratchet.py|scripts/ci/sleep_baseline.json) return 0 ;;
    esac
  done < <(git diff --name-status --find-renames -z "$MB" HEAD
           git diff --name-status --find-renames -z
           git diff --cached --name-status --find-renames -z)
  return 1
}

job_extension_schema() {
  [ "$HAVE_PY" -eq 1 ] || skip "python3 not found"
  # The codegen-current check reformats generated Go and needs gofmt.
  have gofmt || skip "gofmt not found (the schema codegen check needs Go installed)"
  step "$TIMEOUT_OTHER" "python3 scripts/test/test_extension_schema.py" python3 scripts/test/test_extension_schema.py || return $?
}

# --- plan -----------------------------------------------------------------

CORE_FAIL=0
CHANGED=""
WEB=0; SDKTS=0; ANDROID=0; PRNEEDS=0; EXTSCHEMA=0; SERVER_BUILD=0; APPLE=0; UNMAPPED=0; GO_GUARDS=0
SITE=0; SDKGO=0; EXTREFS=0; SDKBUNDLE=0
INTEG=(); GO_GROUPS=(); UNMAPPED_FILES=(); SCRIPT_TESTS=()
CORE_MISSING=()

core_missing() {  # --all treats a missing core toolchain as a failure; diff mode records a skip.
  if [ "$ALL" -eq 1 ]; then
    CORE_FAIL=1
    CORE_MISSING+=("$1")
  else
    INFO+=("SKIP  $1")
  fi
}

if [ "$ALL" -eq 1 ]; then
  echo "check-fast: --all, running every local fast check (iOS/macOS and Postgres suites excluded)"
  WEB=1; SDKTS=1; ANDROID=1; PRNEEDS=1; EXTSCHEMA=1; SITE=1; SDKGO=1; EXTREFS=1; SDKBUNDLE=1
  for _d in integrations/*/; do
    if [ -f "$_d/package.json" ]; then
      _p=$(basename "$_d")
      arr_has "$_p" "${INTEG[@]:-}" || INTEG+=("$_p")
    fi
  done
else
  # Without --base: the clone's default branch, from local refs only so the script works offline. origin/HEAD is
  # set at clone time and can name a branch that is gone since, so it counts only if it resolves to a commit. Its
  # symbolic target names the base in the output; an origin/HEAD that is a plain ref is used as it is. Full ref
  # names: a local branch called origin/main would otherwise shadow the remote-tracking one.
  if [ -z "$BASE_REF" ]; then
    _head=$(git symbolic-ref --quiet refs/remotes/origin/HEAD 2>/dev/null) || _head=refs/remotes/origin/HEAD
    for _ref in "$_head" refs/remotes/origin/main refs/remotes/origin/master; do
      if git rev-parse --verify --quiet "${_ref}^{commit}" >/dev/null 2>&1; then
        BASE_REF=$_ref
        break
      fi
    done
    if [ -z "$BASE_REF" ]; then
      echo "check-fast: no default base: none of origin/HEAD, origin/main, origin/master is a commit here" >&2
      echo "  (git fetch origin, then git remote set-head origin --auto; or pass --base <ref>)" >&2
      exit 2
    fi
  elif ! git rev-parse --verify "${BASE_REF}^{commit}" >/dev/null 2>&1; then
    echo "check-fast: base ref '$BASE_REF' not found locally (fetch it, or pass --base <ref>)" >&2
    exit 2
  fi
  MB=$(git merge-base HEAD "$BASE_REF" 2>/dev/null) || { echo "check-fast: no merge-base with $BASE_REF" >&2; exit 2; }
  # Committed since the merge-base, plus staged/unstaged/untracked work, so the script works before a commit too.
  CHANGED=$({
    git diff --name-only -z "$MB" HEAD
    git diff --name-only -z HEAD
    git diff --cached --name-only -z
    git ls-files --others --exclude-standard -z
  } | tr '\0' '\n' | sed '/^$/d' | sort -u)
  if [ -z "$CHANGED" ]; then
    echo "check-fast: no changes since $(git rev-parse --short "$MB") ($BASE_REF); nothing to check."
    exit 0
  fi
  echo "check-fast: $(printf '%s\n' "$CHANGED" | wc -l | tr -d ' ') changed file(s) against $(git rev-parse --short "$MB") (merge-base with $BASE_REF)"
  while IFS= read -r f; do
    # Repository files outside web/ that web/src and its vitest suite read: a change to one also runs the web check,
    # on top of its own mapping below. The same list is the web lane's filter in scripts/ci/paths.py.
    case "$f" in
      schema/extension/*|spec/fixtures/injected/*|spec/fixtures/sync/*|scripts/mock/inbox_events.json) WEB=1 ;;
    esac
    case "$f" in server/*|sdk/go/*) GO_GUARDS=1 ;; esac  # on top of the mapping below (job_go_guards)
    case "$f" in
      web/*) WEB=1 ;;
      site/*) SITE=1 ;;
      integrations/*/*)
        _p=$(printf '%s' "$f" | cut -d/ -f2)
        arr_has "$_p" "${INTEG[@]:-}" || INTEG+=("$_p") ;;
      sdk/ts/*) SDKTS=1 ;;
      spec/fixtures/*) ;;  # read by server tests: the reader packages are collected in a pass below
      server/*)
        _rest=${f#server/}
        case "$_rest" in
          # Package groups are collected in a second pass below; plugins/<file> is the plugins package, which the
          # repo-wide guard job runs.
          internal/*/*|plugins/*) ;;
          cmd/*)
            _after=${_rest#cmd/}
            case "$_after" in
              */*)
                _g="cmd/${_after%%/*}"
                arr_has "$_g" "${GO_GROUPS[@]:-}" || GO_GROUPS+=("$_g") ;;
              *) SERVER_BUILD=1 ;;
            esac ;;
          *) SERVER_BUILD=1 ;;
        esac
        ;;
      sdk/go/*) SDKGO=1 ;;  # sdk/go is its own module; run its tests there, not server's build
      android/*) ANDROID=1 ;;
      .github/*) PRNEEDS=1 ;;
      schema/*)
        # Mirror CI's extension-schema job: the conformance py, the sdk bundle py, and the Go wire round-trip.
        EXTSCHEMA=1; SDKBUNDLE=1; SDKGO=1 ;;
      scripts/app/*android*)
        # The i18n/token checkers and their own test_android_* unittests all belong to the android check job.
        ANDROID=1 ;;
      scripts/test/test_external_service_refs.py) EXTREFS=1 ;;
      scripts/gen/sdk_bundle.py|scripts/test/test_sdk_bundle.py) SDKBUNDLE=1 ;;
      scripts/gen/*extension*|scripts/test/test_extension_schema.py) EXTSCHEMA=1 ;;
      scripts/*)
        # Ambiguous script change: run the seconds-level repository checks. A case "*" matches "/" too, so this
        # also covers nested paths such as scripts/hooks/foo.sh, not only files directly under scripts/.
        PRNEEDS=1; EXTSCHEMA=1 ;;
      apple/*) APPLE=1 ;;
      # The suites guarding docs, markdown and deploy/docker (and the root Dockerfile) are added with the scripts/
      # unittests below.
      docs/*|deploy/docker/*|Dockerfile|*.md) ;;
      *) UNMAPPED=1; UNMAPPED_FILES+=("$f") ;;
    esac
  done <<< "$CHANGED"
fi

# Collect affected server packages: server/internal/foo/... -> ./internal/foo/..., plugins/bar/... likewise.
if [ "$ALL" -eq 0 ]; then
  while IFS= read -r f; do
    case "$f" in
      server/internal/*/*|server/plugins/*/*)
        _rest=${f#server/}
        _top=${_rest%%/*}; _rest=${_rest#*/}
        _g="$_top/${_rest%%/*}"
        arr_has "$_g" "${GO_GROUPS[@]:-}" || GO_GROUPS+=("$_g") ;;
    esac
  done <<< "$CHANGED"
fi

# server_readers prints the package directory (relative to server/) of each server _test.go that matches the extended regex $1
# outside a // comment line. Finding them at run time keeps a new reader mapped without an edit here.
server_readers() {
  git grep -nE "$1" -- 'server/*_test.go' 2>/dev/null \
    | grep -vE '^[^:]*:[0-9]+:[[:space:]]*//' | cut -d: -f1 | sort -u \
    | while IFS= read -r _file; do _dir=$(dirname "$_file"); printf '%s\n' "${_dir#server/}"; done | sort -u
}

# Repository inputs that server tests read (the extra paths in server-test.yml's filter): a change to one also runs go
# test on the packages that read it. Each regex matches the input as a path string or as filepath.Join elements.
if [ "$ALL" -eq 0 ]; then
  READER_RES=()
  while IFS= read -r f; do
    _re=""
    case "$f" in
      spec/fixtures/*) _re='spec/fixtures|"spec", *"fixtures"' ;;
      schema/extension/*) _re='schema/extension|"schema", *"extension"' ;;
      integrations/*/*) _re='integrations/|"integrations"' ;;
      scripts/test/server_test_env.py) _re='server_test_env\.py' ;;
      docs/*) _re=$(printf '%s' "$f" | sed 's/[][\.^$*+?(){}|]/\\&/g') ;;  # docs/example-guide.md -> docs/example-guide\.md
    esac
    [ -n "$_re" ] && { arr_has "$_re" "${READER_RES[@]:-}" || READER_RES+=("$_re"); }
  done <<< "$CHANGED"
  for _re in "${READER_RES[@]:-}"; do
    [ -n "$_re" ] || continue
    while IFS= read -r _g; do
      [ -n "$_g" ] && { arr_has "$_g" "${GO_GROUPS[@]:-}" || GO_GROUPS+=("$_g"); }
    done < <(server_readers "$_re")
  done
fi

# script_test_add adds a scripts/<dir>/test_*.py that exists (a deleted one is not run) and that no dedicated job above
# already runs (refs, bundle, extension schema, the android checkers). A file that never mentions unittest is a SKIP
# line instead, the rule of is_suite in scripts/ci/python-tests.py: python -m unittest runs no tests in such a
# verification script and exits 5 ("NO TESTS RAN") on Python 3.12 and later.
script_test_add() {
  case "$1" in
    scripts/test/test_external_service_refs.py|scripts/test/test_sdk_bundle.py|scripts/test/test_extension_schema.py) return 0 ;;
    scripts/app/test_android_*) return 0 ;;
  esac
  [ -f "$1" ] || return 0
  if ! grep -q unittest "$1"; then
    arr_has "SKIP  $1 (not a unittest suite)" "${INFO[@]:-}" || INFO+=("SKIP  $1 (not a unittest suite)")
    return 0
  fi
  arr_has "$1" "${SCRIPT_TESTS[@]:-}" || SCRIPT_TESTS+=("$1")
}

# The scripts/ unittests a change needs (#3968): each changed scripts/<dir>/test_*.py, and for each changed script
# <name>.py / <name>.sh anywhere under scripts/, every scripts/<dir>/test_<name>.py, and test_<name>_sh.py for a .sh
# (CI's Scripts tests job runs scripts/**/test_*.py; sim-boot.sh is tested by test_sim_boot.py, db.sh by test_db_sh.py).
# Changes to docs, markdown, apple/, deploy/docker/ and the root Dockerfile add the scripts/test suites that guard them.
# --all runs every scripts/**/test_*.py suite, the same find as CI's Scripts tests job, minus the few script_test_add
# skips because a dedicated check above runs them; renaming or dropping one of those checks must update that skip list.
# test_check_fast.py compares this selection with python-tests.py's.
if [ "$ALL" -eq 1 ]; then
  while IFS= read -r f; do
    script_test_add "$f"
  done < <(find scripts -name 'test_*.py' -not -path '*/node_modules/*' | sort)
else
  while IFS= read -r f; do
    case "$f" in
      scripts/*/test_*.py) script_test_add "$f" ;;
      scripts/*.py|scripts/*.sh)
        _name=$(basename "$f"); _name=${_name%.*}
        _name=$(printf '%s' "$_name" | tr '-' '_')
        for _t in scripts/*/"test_$_name.py"; do script_test_add "$_t"; done
        case "$f" in *.sh) for _t in scripts/*/"test_${_name}_sh.py"; do script_test_add "$_t"; done ;; esac ;;
    esac
    # The Linux suites in CI's Scripts tests job that guard files outside scripts/; a file can need several.
    case "$f" in
      docs/*|*.md)
        script_test_add scripts/test/test_doc_paths.py; script_test_add scripts/test/test_doc_go_paths.py
        script_test_add scripts/test/test_doc_wire.py ;;
      # The code side of the wire lists test_doc_wire.py compares with the docs.
      server/internal/proto/*|server/internal/hub/*|server/internal/extension/*|schema/extension/manifest.schema.json)
        script_test_add scripts/test/test_doc_wire.py ;;
    esac
    case "$f" in apple/*) script_test_add scripts/test/test_apple_l10n.py ;; esac
    # Suites whose names do not match the file they test: test_privacy_check.py copies scripts/hooks into its temp
    # repositories and runs the hooks there; check_bundle_privacy.py imports privacy_check; test_merge_pr_sh.py
    # copies privacy_check.py into its temp repositories, where merge-pr.sh runs it.
    case "$f" in
      scripts/hooks/*) script_test_add scripts/test/test_privacy_check.py ;;
      scripts/privacy_check.py)
        script_test_add scripts/release/test_check_bundle_privacy.py; script_test_add scripts/test/test_merge_pr_sh.py ;;
    esac
    case "$f" in deploy/docker/*|docs/operations/docker.md|Dockerfile) script_test_add scripts/test/test_docker_entrypoint.py ;; esac
  done <<< "$CHANGED"
fi

echo "check-fast: logs in $WORK"

# All jobs, privacy included, launch together in launch_all below; privacy is scheduled first so it is the first
# line in the summary.
if [ "$HAVE_PY" -eq 1 ]; then
  schedule "privacy scan" job_privacy ""
else
  core_missing "python3 not found; privacy scan skipped"
fi

if [ "$WEB" -eq 1 ]; then
  if node_ok || [ "$ALL" -eq 0 ]; then
    WEB_CI=noci
    if [ ! -d web/node_modules ]; then
      WEB_CI=ci
    elif [ "$ALL" -eq 0 ] && printf '%s\n' "$CHANGED" | grep -qxE 'web/(package\.json|package-lock\.json)'; then
      # A changed manifest can make the installed node_modules stale, even when the diff touched package.json only.
      WEB_CI=ci
    fi
    if node_ok; then schedule "web" job_web "$WEB_CI"; else INFO+=("SKIP  web: node >= 22 not found (found: $(node -v 2>/dev/null || echo none))"); fi
  fi
  if [ "$ALL" -eq 1 ] && ! node_ok; then core_missing "node >= 22 not found (found: $(node -v 2>/dev/null || echo none)); web and integration checks skipped"; fi
fi

if [ "${#INTEG[@]}" -gt 0 ]; then
  if node_ok; then
    for _p in "${INTEG[@]}"; do schedule "integrations/$_p" job_integration "$_p"; done
  else
    core_missing "node >= 22 not found (found: $(node -v 2>/dev/null || echo none)); integration tests (${INTEG[*]}) skipped"
  fi
fi

[ "$SDKTS" -eq 1 ] && schedule "sdk/ts (bun)" job_sdk_ts ""
if [ "$SITE" -eq 1 ]; then
  if node_ok; then
    schedule "site (node --test)" job_site ""
  elif [ "$ALL" -eq 1 ]; then
    core_missing "node >= 22 not found; site check skipped"
  else
    INFO+=("SKIP  site: node >= 22 not found (found: $(node -v 2>/dev/null || echo none))")
  fi
fi
[ "$EXTREFS" -eq 1 ] && schedule "external service refs" job_external_refs ""
if [ "${#SCRIPT_TESTS[@]}" -gt 0 ]; then
  if [ "$HAVE_PY" -eq 1 ]; then
    if [ "$ALL" -eq 1 ]; then
      schedule "scripts unittest suites (${#SCRIPT_TESTS[@]})" job_scripts_all ""
    else
      for _t in "${SCRIPT_TESTS[@]}"; do schedule "$_t" job_scripts_test "$_t"; done
    fi
  elif [ "$ALL" -eq 1 ]; then
    core_missing "python3 not found; scripts unittest suites skipped"
  else
    INFO+=("SKIP  scripts unittests (${SCRIPT_TESTS[*]}): python3 not found")
  fi
fi
[ "$SDKBUNDLE" -eq 1 ] && schedule "sdk bundle snapshot" job_sdk_bundle ""
if [ "$SDKGO" -eq 1 ]; then
  if [ "$HAVE_GO" -eq 1 ]; then
    schedule "sdk/go go test" job_sdk_go ""
  elif [ "$ALL" -eq 0 ]; then
    # In --all mode the missing-go failure is already reported by the server core_missing line below.
    INFO+=("SKIP  sdk/go tests: go not found")
  fi
fi

if [ "$ALL" -eq 1 ]; then
  if [ "$HAVE_GO" -eq 1 ]; then
    schedule "server go test" job_go ALL
  else core_missing "go not found; server and sdk/go checks skipped"; fi
else
  if [ "${#GO_GROUPS[@]}" -gt 0 ]; then
    if [ "$HAVE_GO" -eq 1 ]; then
      for _g in "${GO_GROUPS[@]}"; do schedule "server go test ./$_g/..." job_go "$_g"; done
    else
      INFO+=("SKIP  server go tests (${GO_GROUPS[*]}): go not found")
    fi
  fi
  if [ "$SERVER_BUILD" -eq 1 ]; then
    if [ "$HAVE_GO" -eq 1 ]; then schedule "server go build ./..." job_go_build ""; else INFO+=("SKIP  server go build: go not found"); fi
  fi
  if [ "$GO_GUARDS" -eq 1 ]; then
    if [ "$HAVE_GO" -eq 1 ]; then schedule "server repo-wide guards" job_go_guards ""; else INFO+=("SKIP  server repo-wide guards: go not found"); fi
  fi
fi

if [ "$ANDROID" -eq 1 ]; then
  if [ "$HAVE_PY" -eq 1 ]; then schedule "android i18n" job_i18n ""; else core_missing "python3 not found; android i18n check skipped"; fi
fi
if [ "$PRNEEDS" -eq 1 ]; then
  if node_ok; then schedule "pr-needs-issue workflow test" job_pr_needs_issue ""; else core_missing "node >= 22 not found; pr-needs-issue test skipped"; fi
fi
if [ "$EXTSCHEMA" -eq 1 ]; then
  if [ "$HAVE_PY" -eq 1 ]; then schedule "extension schema conformance" job_extension_schema ""; else core_missing "python3 not found; extension schema test skipped"; fi
fi

if [ -f scripts/ci/migration_freeze.py ] && migration_freeze_wanted; then
  if [ "$HAVE_PY" -eq 1 ]; then
    schedule "frozen migrations" job_migration_freeze ""
  elif [ "$ALL" -eq 1 ]; then
    core_missing "python3 not found; frozen migration check skipped"
  else
    INFO+=("SKIP  frozen migrations: python3 not found")
  fi
fi

if [ -f scripts/ci/sleep_ratchet.py ] && sleep_ratchet_wanted; then
  if [ "$HAVE_PY" -eq 1 ]; then
    schedule "fixed sleeps" job_sleep_ratchet ""
  elif [ "$ALL" -eq 1 ]; then
    core_missing "python3 not found; fixed sleep check skipped"
  else
    INFO+=("SKIP  fixed sleeps: python3 not found")
  fi
fi

if [ "$APPLE" -eq 1 ]; then
  INFO+=("SKIP  apple/** builds and tests: iOS/macOS checks need a Mac — scripts/app/sim.sh, PersonaKit swift test (docs/operations/testing.md)")
fi
if [ "$UNMAPPED" -eq 1 ]; then
  INFO+=("INFO  no fast check maps to: ${UNMAPPED_FILES[*]} (privacy scan still ran; full suites in docs/operations/testing.md)")
fi

if [ ${#JOB_NAME[@]} -eq 0 ] && [ "$CORE_FAIL" -eq 0 ]; then
  echo "check-fast: no related checks to run."
  for _line in "${INFO[@]:-}"; do [ -n "$_line" ] && echo "$_line"; done
  exit 0
fi

echo "check-fast: ${#JOB_NAME[@]} check(s) running in parallel..."
launch_all

# --- summary --------------------------------------------------------------

echo
echo "== check-fast summary =="
N_PASS=0; N_FAIL=0; N_TIMEOUT=0; N_SKIP=0
for _i in "${!JOB_NAME[@]}"; do
  _kind=""; _rc=""; _seconds=""; _detail=""  # don't inherit a previous job's values if this status file is missing
  IFS=$'\t' read -r _kind _rc _seconds _detail < "${JOB_STATUS[$_i]}"
  if [ -z "$_kind" ]; then
    # The job subshell was killed (SIGKILL/OOM) before it could write a status; never let that count as nothing.
    _kind=FAIL; _seconds="?"; _detail="no status: the job was killed before it could report"
  fi
  case "$_kind" in
    PASS) echo "PASS     ${JOB_NAME[$_i]} (${_seconds}s)"; N_PASS=$((N_PASS+1)) ;;
    SKIP) echo "SKIP     ${JOB_NAME[$_i]}: $_detail"; N_SKIP=$((N_SKIP+1)) ;;
    FAIL)
      echo "FAIL     ${JOB_NAME[$_i]} (${_seconds}s, $_detail); log: ${JOB_LOG[$_i]}"
      sed 's/^/         /' "${JOB_LOG[$_i]}" | tail -n 25
      N_FAIL=$((N_FAIL+1)) ;;
    TIMEOUT)
      echo "TIMEOUT  ${JOB_NAME[$_i]} (${_seconds}s, $_detail); log: ${JOB_LOG[$_i]}"
      sed 's/^/         /' "${JOB_LOG[$_i]}" | tail -n 25
      N_TIMEOUT=$((N_TIMEOUT+1)) ;;
  esac
done
for _msg in "${CORE_MISSING[@]:-}"; do
  [ -n "$_msg" ] && { echo "FAIL     core toolchain: $_msg"; N_FAIL=$((N_FAIL+1)); }
done
for _line in "${INFO[@]:-}"; do [ -n "$_line" ] && echo "$_line"; done

echo
if [ "$N_FAIL" -gt 0 ] || [ "$CORE_FAIL" -eq 1 ]; then
  echo "check-fast: $N_PASS passed, $N_SKIP skipped, $N_FAIL failed, $N_TIMEOUT timed out (FAILED); logs in $WORK"
  KEEP_WORK=1
  exit 1
fi
if [ "$N_TIMEOUT" -gt 0 ]; then
  echo "check-fast: $N_PASS passed, $N_SKIP skipped, 0 failed, $N_TIMEOUT timed out (TIMEOUT); logs in $WORK"
  echo "check-fast: a timeout is not a pass. Rerun the timed-out job directly, or run the full suite for it"
  echo "check-fast: (docs/operations/testing.md: web 'npm test', server scripts/test/test-server.sh, android scripts/app/android.sh)."
  KEEP_WORK=1
  exit 124
fi
echo "check-fast: $N_PASS passed, $N_SKIP skipped, 0 failed, 0 timed out (OK)"

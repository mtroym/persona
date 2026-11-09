#!/bin/bash
# Build personad and install it on every machine of the config (docs/operations/config.md); (re)start the hub on its machine.
#   ./scripts/deploy/deploy.sh                 # all machines
#   ./scripts/deploy/deploy.sh <hub machine>   # only the hub
#   ./scripts/deploy/deploy.sh <machine id>    # only one machine (node binary; the hub restarts its connection)
#   ./scripts/deploy/deploy.sh web             # only the web client (web/, docs/web/web.md): no restart
# The checkout must contain the remote's default branch (it fetches it first), else nothing is deployed;
# PERSONA_DEPLOY_ANY_COMMIT=1 skips the check.
# Who may open the hub's admin UI (docs/operations/admin.md): --admin-access <loopback|lan|cidrs|any> [--admin-cidrs a,b] (or
# PERSONA_ADMIN_ACCESS / PERSONA_ADMIN_CIDRS) sets it on the hub; without them a hub that never had one chosen asks on
# a terminal, else keeps the default (lan) and says so.
set -euo pipefail
cd "$(dirname "$0")/../.."
# All callers, including remote build computers, serialize against the hub's lock.
if [ "${PERSONA_OPERATION_DEPLOY_LOCKED:-}" != 1 ]; then
  exec python3 scripts/operations/operation_lock.py deploy -- bash "$PWD/scripts/deploy/deploy.sh" "$@"
fi
# Shared default-branch resolution and detached-worktree helper; sourcing it keeps this script and out-of-tree build
# orchestration from hardcoding master (#1064, #2361).
# shellcheck source=scripts/deploy/default-branch.sh
source "$PWD/scripts/deploy/default-branch.sh"
# Normalize caller overrides before reading deployment options; never print their values.
eval "$(python3 scripts/persona_identity.py --env)"
# The question and its explanations live in install.sh (the public installer asks the same).
eval "$(sed -n '/^# >>> admin access/,/^# <<< admin access/p' scripts/deploy/install.sh)"
ADMIN_ACCESS="${PERSONA_ADMIN_ACCESS:-}"
ADMIN_CIDRS="${PERSONA_ADMIN_CIDRS:-}"
ARGS=()
while [ $# -gt 0 ]; do
  case "$1" in
    --admin-access) ADMIN_ACCESS="${2:-}"; shift ;;
    --admin-access=*) ADMIN_ACCESS="${1#*=}" ;;
    --admin-cidrs) ADMIN_CIDRS="${2:-}"; shift ;;
    --admin-cidrs=*) ADMIN_CIDRS="${1#*=}" ;;
    *) ARGS+=("$1") ;;
  esac
  shift
done
if [ -n "$ADMIN_ACCESS" ] && ! admin_access_valid "$ADMIN_ACCESS" "$ADMIN_CIDRS"; then
  echo "--admin-access must be loopback, lan, cidrs (with --admin-cidrs a,b) or any" >&2
  exit 2
fi
set -- ${ARGS[@]+"${ARGS[@]}"}
# Only from a checkout that has everything already on the default branch: a hub or web deploy from an older branch
# puts back what other people deployed since (docs/lessons/server.md "Deploying from a stale branch").
# PERSONA_DEPLOY_ANY_COMMIT=1 deploys anyway, e.g. to roll back on purpose.
# The default branch is the remote's current one (remote symref, else the clone's possibly-stale origin/HEAD, else
# master), validated and fetched as refs/heads/<name>; persona_default_branch / persona_fetch_default_branch in
# default-branch.sh hold that logic and are shared with build orchestration.
if [ -z "${PERSONA_DEPLOY_ANY_COMMIT:-}" ]; then
  DEFAULT_BRANCH=$(persona_default_branch)
  if ! persona_fetch_default_branch "$DEFAULT_BRANCH" 2>/dev/null; then
    echo "deploy: could not fetch origin/$DEFAULT_BRANCH to check this checkout is up to date; PERSONA_DEPLOY_ANY_COMMIT=1 deploys anyway" >&2
    exit 2
  fi
  if ! git merge-base --is-ancestor FETCH_HEAD HEAD; then
    echo "deploy: this checkout ($(git rev-parse --short HEAD)) is missing commits on origin/$DEFAULT_BRANCH ($(git rev-parse --short FETCH_HEAD))." >&2
    echo "  Deploying it would undo what was deployed from them: merge origin/$DEFAULT_BRANCH in or rebase onto it (or deploy from a fresh" >&2
    echo "  'git worktree add --detach <dir> origin/$DEFAULT_BRANCH'); PERSONA_DEPLOY_ANY_COMMIT=1 deploys it anyway." >&2
    exit 2
  fi
fi
_env=$(python3 scripts/persona_config.py env) || exit $?
eval "$_env"; unset _env
cd server
# Straight from the default branch, for when a change cannot wait for a release: the build number is the build time
# (UTC), so an older release never replaces it and the next release does (docs/operations/hub-update.md "Direct deploys").
BUILD="$(date -u +%Y%m%d%H%M)"
VERSION="$(git describe --always --dirty 2>/dev/null || echo dev)-$BUILD"
TARGETS=("$@")
[ ${#TARGETS[@]} -eq 0 ] && read -ra TARGETS <<< "$PERSONA_MACHINES"

# web_deps (in web/): npm ci when there is no node_modules or package-lock.json changed since the last install, so a
# pull that adds or bumps a dependency builds with it. npm's own node_modules/.package-lock.json never equals
# package-lock.json (no root entry, no optional packages for other platforms): the lockfile's hash is kept instead.
web_deps() {
  local want
  want=$(python3 -c 'import hashlib, sys; print(hashlib.sha256(open(sys.argv[1], "rb").read()).hexdigest())' package-lock.json)
  [ -d node_modules ] && [ "$(cat node_modules/.persona-lock-sha256 2>/dev/null)" = "$want" ] && return 0
  npm ci --no-audit --no-fund && printf '%s\n' "$want" > node_modules/.persona-lock-sha256
}

# build_web: the web client's dependencies and build (web/dist), before any machine is touched (#1728): when it fails
# nothing is installed, and its output is printed (the prebuild privacy scan prints its hits on stdout).
build_web() {
  [ -f ../web/package.json ] || return 0
  local log
  log=$(mktemp)
  if ! (cd ../web && web_deps && npm run build) >"$log" 2>&1; then
    cat "$log" >&2
    rm -f "$log"
    echo "web: build failed (output above); nothing was deployed" >&2
    exit 1
  fi
  rm -f "$log"
  echo "built web"
}

# upload_web: the built web client (build_web) replaces the old one atomically within the hub's selected state root.
upload_web() {
  [ -f ../web/package.json ] || return 0
  # macOS tar packs each file's extended attributes (build output carries com.apple.provenance), useless on the hub:
  # COPYFILE_DISABLE stops the ._<name> AppleDouble entries GNU tar unpacks as files, --no-xattrs the LIBARCHIVE.xattr
  # pax headers GNU tar drops with a warning per file. GNU tar takes --no-xattrs too.
  local root script
  root=$(python3 ../scripts/persona_identity.py --service hub --prepare --prefix -)
  script="$root
"'set -e; root=$PERSONA_HOME; rm -rf "$root/web.new"; mkdir -p "$root/web.new"; tar -xzf - -C "$root/web.new"; rm -rf "$root/web.old"; [ ! -d "$root/web" ] || mv "$root/web" "$root/web.old"; mv "$root/web.new" "$root/web"; rm -rf "$root/web.old"'
  COPYFILE_DISABLE=1 tar --no-xattrs -C ../web/dist -czf - . | ssh "$PERSONA_HUB_SSH" "$script"
  echo "web: deployed to the hub state root"
}
if [ "${TARGETS[*]}" = web ]; then build_web; upload_web; exit 0; fi
HUB="$PERSONA_HUB_MACHINE"
# Other machines first, the hub (restart) last: the hub only picks up a new node binary when it reconnects.
ORDERED=()
for t in "${TARGETS[@]}"; do [ "$t" != "$HUB" ] && ORDERED+=("$t"); done
for t in "${TARGETS[@]}"; do [ "$t" = "$HUB" ] && ORDERED+=("$t"); done
TARGETS=("${ORDERED[@]}")
# machine <id> <field>: a field of a machine in the config (ssh host, os, arch, hostnames, via).
field() { python3 ../scripts/persona_config.py machine "$1" | python3 -c "import json,sys;v=json.load(sys.stdin).get('$2','');print(' '.join(v) if isinstance(v,list) else v)"; }
state_root() { python3 ../scripts/persona_identity.py --service "${1:-hub}" --prepare; }
state_prefix() { python3 ../scripts/persona_identity.py --service "${1:-hub}" --prepare --prefix -; }
remote_state() {
  local host=$1 command=$2 role=${3:-hub}
  ssh -o ConnectTimeout=10 "$host" "$(state_prefix "$role")
$command"
}
mkdir -p bin
# The commit it is built from, "-dirty" after it with changes in server/ not committed: a release installs itself over
# this build only when its code contains that commit (docs/operations/hub-update.md "No rollback").
COMMIT="$(git rev-parse HEAD 2>/dev/null || true)"
[ -n "$COMMIT" ] && [ -n "$(git status --porcelain -- . 2>/dev/null)" ] && COMMIT="$COMMIT-dirty"
LDFLAGS="-s -w -X main.version=$VERSION -X main.channel=release -X main.build=$BUILD -X main.commit=$COMMIT${PERSONA_PUBLISH_URL:+ -X main.updateURL=$PERSONA_PUBLISH_URL/dl}"
# The runtime packages personad carries (the DSH plugin's code, docs/integrations/runtimes.md "Auto-update"), from integrations/.
go generate ./internal/runtimepkg
# Every platform a machine can run, the same four the release and the Docker image build (docs/operations/release.md): each
# machine gets the one it runs (machine_platform), and the hub keeps all four for "Redeploy" (#3103).
PLATFORMS=(linux-amd64 linux-arm64 darwin-amd64 darwin-arm64)
# persona-node is the device's own program (cmd/persona-node): the hub keeps it beside personad, and "Redeploy" gives it
# to a machine whose node runs it (devices/deploy.go nodeBinary). Machines this script installs get personad.
for plat in "${PLATFORMS[@]}"; do
  GOOS=${plat%-*} GOARCH=${plat#*-} CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o "bin/personad-$plat" ./cmd/personad
  GOOS=${plat%-*} GOARCH=${plat#*-} CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o "bin/persona-node-$plat" ./cmd/persona-node
done
echo "built $VERSION"
# The web client goes with the hub: built here with the binaries, uploaded after the hub restarts.
if [[ " ${TARGETS[*]} " == *" $HUB "* ]]; then build_web; fi

# is_here <machine id>: the config lists this computer's host name for it. Only non-empty names count (#1725): a
# machine without hostnames is remote, and an empty `hostname -s` or scutil (none on Linux) matches nothing.
is_here() {
  local here name
  here=" $(field "$1" hostnames) "
  [[ "$here" == *[![:space:]]* ]] || return 1
  for name in "$(hostname -s 2>/dev/null)" "$(scutil --get LocalHostName 2>/dev/null)"; do
    [ -n "$name" ] && [[ "$here" == *" $name "* ]] && return 0
  done
  return 1
}

# machine_platform <machine id>: the os-arch of the binary that machine runs ("<os>-<arch>", bin/personad-<os>-<arch>).
# `os` and `arch` in the config win; whatever it leaves out is asked of the machine itself (`uname -s` / `uname -m`,
# the mapping scripts/deploy/install.sh makes), so an Intel Mac is not handed an Apple-silicon build and an ARM Linux
# box not an amd64 one. A platform there is no build for is an error, never a silent install (#3103).
# Returns 3 when the machine could not be asked at all (asleep, no route): that is an unreachable machine, which is a
# normal deploy (`unreachable, skipped`), not a platform the hub has no build for. 1 is a platform it cannot run.
machine_platform() {
  local id=$1 host via vh os arch said answered said_os said_arch
  os=$(field "$id" os)
  arch=$(field "$id" arch)
  if [ -z "$os" ] || [ -z "$arch" ]; then
    host=$(field "$id" ssh); host=${host:-$id}
    via=$(field "$id" via)
    if is_here "$id"; then
      said=$(uname -s 2>/dev/null; uname -m 2>/dev/null) || said=
      answered=1
    elif [ -n "$via" ]; then
      vh=$(field "$via" ssh); vh=${vh:-$via}
      if said=$(ssh -o ConnectTimeout=10 "$vh" "ssh -o BatchMode=yes -o ConnectTimeout=15 $host 'uname -s; uname -m'" 2>/dev/null); then answered=1; else said=; fi
    else
      if said=$(ssh -o ConnectTimeout=10 "$host" 'uname -s; uname -m' 2>/dev/null); then answered=1; else said=; fi
    fi
    # The last two lines of the answer: a login shell that prints a banner first does not hide them.
    said_arch=${said##*$'\n'}
    said_os=${said%$'\n'*}
    said_os=${said_os##*$'\n'}
    case "$said_os" in
      Linux) said_os=linux ;;
      Darwin) said_os=darwin ;;
      *) said_os= ;;
    esac
    case "$said_arch" in
      x86_64 | amd64) said_arch=amd64 ;;
      arm64 | aarch64) said_arch=arm64 ;;
      *) said_arch= ;;
    esac
    os=${os:-${said_os:-}}
    arch=${arch:-${said_arch:-}}
  fi
  if [ -z "$os" ] || [ -z "$arch" ]; then
    if [ -z "$answered" ]; then
      echo "$id: could not reach it to ask its os and arch; write os / arch for it in the config (machines[])" >&2
      return 3
    fi
    echo "$id: could not tell its os and arch; write os / arch for it in the config (machines[])" >&2
    return 1
  fi
  case "$os-$arch" in
    linux-amd64 | linux-arm64 | darwin-amd64 | darwin-arm64) printf '%s-%s\n' "$os" "$arch" ;;
    *)
      echo "$id: no personad build for $os/$arch (linux or darwin; amd64 or arm64)" >&2
      return 1 ;;
  esac
}

# install_bin <machine-id> <binary>: put the binary in place and check that it runs there before the node restarts
# onto it: a build of another platform cannot exec, and that was the deploy saying "installed" anyway (#3103).
install_bin() {
  local id=$1 bin=$2 host via root command keep out role=node plat
  plat=${bin##*/personad-}
  [ "$id" != "$HUB" ] || role=hub
  host=$(field "$id" ssh); host=${host:-$id}
  via=$(field "$id" via)
  if is_here "$id"; then
    root=$(state_root "$role")
    mkdir -p "$root/bin" || return 1
    # The hub's binary before it is replaced: hub-systemd.sh switches the running edge to the new one and goes back
    # to this one if it fails while taking over (docs/operations/hub-restart.md "Blue-green switch").
    if [ "$role" = hub ]; then
      local previous
      previous=$(python3 ../scripts/persona_identity.py --service hub --shell) || return 1
      eval "$previous"
      [ ! -f "$_PERSONA_HUB_BIN" ] || cp -p "$_PERSONA_HUB_BIN" "$root/bin/personad.prev" || return 1
    fi
    { cp "$bin" "$root/bin/personad.new" && mv "$root/bin/personad.new" "$root/bin/personad"; } || return 1
    if ! out=$(PERSONA_HOME="$root" "$root/bin/personad" version 2>&1); then
      echo "$id: the $plat build does not run there: $out" >&2
      return 1
    fi
    python3 ../scripts/persona_identity.py --alias-binaries "$root/bin" || return 1
    PERSONA_HOME="$root" restart_resident
  else
    keep=
    [ "$role" = hub ] && keep='{ [ ! -f "$_PERSONA_HUB_BIN" ] || cp -p "$_PERSONA_HUB_BIN" "$PERSONA_HOME/bin/personad.prev"; } && '
    command=$keep'mkdir -p "$PERSONA_HOME/bin" && cat > "$PERSONA_HOME/bin/personad.new" && chmod 755 "$PERSONA_HOME/bin/personad.new" && mv "$PERSONA_HOME/bin/personad.new" "$PERSONA_HOME/bin/personad" && persona_alias_binaries "$PERSONA_HOME/bin" && "$PERSONA_HOME/bin/personad" version >/dev/null'
    if [ -n "$via" ]; then
      local vh quoted
      vh=$(field "$via" ssh); vh=${vh:-$via}
      quoted=$(python3 -c 'import shlex,sys; print(shlex.quote(sys.argv[1]))' "$(state_prefix "$role")
$command")
      if ! out=$(ssh -o ConnectTimeout=10 "$vh" "ssh -o BatchMode=yes -o ConnectTimeout=15 $host $quoted" < "$bin" 2>&1); then
        echo "$id: the $plat build is not installed or does not run there: $out" >&2
        return 1
      fi
    else
      if ! out=$(remote_state "$host" "$command" "$role" < "$bin" 2>&1); then
        echo "$id: the $plat build is not installed or does not run there: $out" >&2
        return 1
      fi
      python3 ../scripts/persona_identity.py --service node --prefix ../scripts/deploy/node-launchd.sh | ssh "$host" 'bash -s restart-idle' 2>/dev/null || true
    fi
  fi
}

# install_program <hub machine id> <os-arch> <program>: a program released with personad (update.ProgramNames) next to
# the hub's personad, the same build as personad: personad-wechat (the WeChat bot, docs/extensions/wechat.md; the hub
# starts it only when `version -v` matches; Telegram's connector is an extension now, #1704), and persona-hub and
# persona-manager, the hub's and the manager's own programs (docs/server/server.md "Programs"). Those two keep a .prev
# of the one replaced (none when there was none, so no older one is left there): a hub run as persona-hub goes back
# on them with personad when the switch fails (hub-systemd.sh).
install_program() {
  local id=$1 plat=$2 name=$3 bin keep=
  bin="bin/$name-$plat"
  GOOS=${plat%-*} GOARCH=${plat#*-} CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o "$bin" "./cmd/$name" || return 1
  case $name in persona-hub | persona-manager)
    keep="{ if [ -f \"\$PERSONA_HOME/bin/$name\" ]; then cp -p \"\$PERSONA_HOME/bin/$name\" \"\$PERSONA_HOME/bin/$name.prev\"; else rm -f \"\$PERSONA_HOME/bin/$name.prev\"; fi; } && " ;;
  esac
  if is_here "$id"; then
    local root
    root=$(state_root)
    mkdir -p "$root/bin"
    if [ -n "$keep" ]; then
      PERSONA_HOME="$root" sh -c "${keep% && }" || return 1
    fi
    local target="$root/bin/$name"
    cp "$bin" "$target.new" && { [ ! -L "$target" ] || rm -f "$target"; } && mv "$target.new" "$target" || return 1
    python3 ../scripts/persona_identity.py --alias-binaries "$root/bin"
  else
    remote_state "$PERSONA_HUB_SSH" "$keep""mkdir -p \"\$PERSONA_HOME/bin\" && cat > \"\$PERSONA_HOME/bin/$name.new\" && chmod 755 \"\$PERSONA_HOME/bin/$name.new\" && { [ ! -L \"\$PERSONA_HOME/bin/$name\" ] || rm -f \"\$PERSONA_HOME/bin/$name\"; } && mv \"\$PERSONA_HOME/bin/$name.new\" \"\$PERSONA_HOME/bin/$name\" && persona_alias_binaries \"\$PERSONA_HOME/bin\"" < "$bin"
  fi
}

# herdr on a machine that has none (scripts/deploy/ensure-herdr.sh; PERSONA_SKIP_HERDR=1 skips it): a first install should
# leave a machine whose Agents show up. Never fails the deploy.
ensure_herdr() { # machine-id
  local id=$1 host via vh
  [ "${PERSONA_SKIP_HERDR:-}" = 1 ] && return 0
  host=$(field "$id" ssh); host=${host:-$id}
  via=$(field "$id" via)
  if is_here "$id"; then
    sh ../scripts/deploy/ensure-herdr.sh
  elif [ -n "$via" ]; then
    vh=$(field "$via" ssh); vh=${vh:-$via}
    ssh -o ConnectTimeout=10 "$vh" "ssh -o BatchMode=yes -o ConnectTimeout=15 $host 'sh -s'" < ../scripts/deploy/ensure-herdr.sh
  else
    ssh -o ConnectTimeout=10 "$host" 'sh -s' < ../scripts/deploy/ensure-herdr.sh
  fi 2>&1 | sed "s/^/$id: /" || true
}

# A resident node (scripts/deploy/node-launchd.sh, Mac or Linux) keeps the old binary until it restarts: it does so
# once none of the assistant's conversations is busy, which go on across hub restarts (docs/operations/hub-restart.md).
restart_resident() { ../scripts/deploy/node-launchd.sh restart-idle || true; }

# The admin UI's scope on the hub (its new binary is in place, the hub restarts right after): what was given, else a
# question when it was never chosen and there is a terminal, else the default and one line about it.
hub_admin_access() {
  local current out cmd
  if [ -z "$ADMIN_ACCESS" ]; then
    current=$(remote_state "$PERSONA_HUB_SSH" '"$PERSONA_HOME/bin/personad" hub admin access --get' 2>/dev/null || true)
    [ -n "$current" ] && return 0
    ask_admin_access || { admin_access_reminder; return 0; }
  fi
  # Both were checked against fixed shapes (admin_access_valid) before they get into a remote command line.
  cmd='"$PERSONA_HOME/bin/personad" hub admin access '"'$ADMIN_ACCESS'"
  [ "$ADMIN_ACCESS" = cidrs ] && cmd='"$PERSONA_HOME/bin/personad" hub admin access cidrs --cidrs '"'$ADMIN_CIDRS'"
  # shellcheck disable=SC2029
  if out=$(remote_state "$PERSONA_HUB_SSH" "$cmd" 2>/dev/null); then
    echo "hub: ${out%%$'\n'*}"
  else
    echo "hub: could not set the admin UI's scope (personad hub admin access on the hub machine)" >&2
  fi
}

for t in "${TARGETS[@]}"; do
  case "$t" in
    "$HUB")
      python3 ../scripts/persona_identity.py --service hub --prepare --prefix ../scripts/deploy/db.sh | ssh "$PERSONA_HUB_SSH" 'bash -s'
      for plat in "${PLATFORMS[@]}"; do
        remote_state "$PERSONA_HUB_SSH" 'mkdir -p "$PERSONA_HOME/bin/nodes"; n="$PERSONA_HOME/bin/nodes/'"personad-$plat"'"; rm -f "$n.version"; cat > "$n"' < "bin/personad-$plat"
        # This deploy's personad is the hub's version: a version file beside it (a release's build put there by hand,
        # docs/operations/docker.md "Node builds") would say it is another, so it went above.
        # persona-node says which build it is in a file beside it, and Redeploy sends it only while that is the hub's
        # version (the hub's own updates replace personad-* alone; devices/packages.go). So the old version file
        # goes first, the binary lands whole (a temporary file renamed), and the new version file comes last: a deploy
        # that stops half way leaves persona-node unused, never a new or torn binary under the old version.
        remote_state "$PERSONA_HUB_SSH" 'n="$PERSONA_HOME/bin/nodes/'"persona-node-$plat"'"; rm -f "$n.version"; cat > "$n.new" && mv "$n.new" "$n"' < "bin/persona-node-$plat"
        printf '%s\n' "$VERSION" | remote_state "$PERSONA_HUB_SSH" 'cat > "$PERSONA_HOME/bin/nodes/'"persona-node-$plat.version"'"'
      done
      remote_state "$PERSONA_HUB_SSH" 'persona_alias_binaries "$PERSONA_HOME/bin/nodes"'
      hub_plat=$(machine_platform "$t") || exit 2
      install_bin "$t" "bin/personad-$hub_plat"
      for p in personad-wechat persona-hub persona-manager; do install_program "$t" "$hub_plat" "$p"; done
      ensure_herdr "$t"
      hub_admin_access
      python3 ../scripts/persona_identity.py --service hub --prepare --prefix ../scripts/deploy/hub-systemd.sh | ssh "$PERSONA_HUB_SSH" 'bash -s'
      echo "$t: hub on the new binary"
      upload_web ;;
    *)
      [[ " $PERSONA_MACHINES " == *" $t "* ]] || { echo "unknown machine $t (not in the config)" >&2; exit 2; }
      # A machine that could not be asked is unreachable (asleep, no route), exactly as before: it is skipped and the
      # run still succeeds. A platform there is no build for is skipped by name and fails the run at the end (#3103).
      if plat=$(machine_platform "$t"); then
        if install_bin "$t" "bin/personad-$plat"; then echo "$t: installed"; ensure_herdr "$t"; else echo "$t: not installed, skipped" >&2; fi
      elif [ $? = 3 ]; then
        echo "$t: unreachable, skipped" >&2
      else
        echo "$t: skipped" >&2
        NO_PLATFORM=1
      fi ;;
  esac
done
if [ -n "${NO_PLATFORM:-}" ]; then
  echo "deploy: some machines have no personad build for their platform (see above)" >&2
  exit 2
fi

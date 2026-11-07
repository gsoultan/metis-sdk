#!/usr/bin/env bash
#
# Run the approvals sample. One command, nothing to set up:
#
#   ./examples/approvals/run.sh
#
# When no metis is answering on http://localhost:8273, this starts one for you:
# it uses a metis checkout next to this repository (../metis) or clones one,
# and runs its scripts/dev.sh, which starts PostgreSQL in an Apple container
# (https://github.com/apple/container), and sets it up with the administrator
# admin / metis-admin. Then it builds the React UI, sets metis up for the sample
# (people, groups, the decision table and the process; safe to repeat), and
# opens the app on http://localhost:8090. Ctrl-C stops the app and the metis
# it started. PostgreSQL keeps running; `container stop metis-dev-pg` stops it.
#
# Needs Go, bun (or npm), git and, the first time, Apple's container CLI.
#
#   ./examples/approvals/run.sh seed     set up only
#   ./examples/approvals/run.sh serve    start only, after an earlier seed
#   ./examples/approvals/run.sh dev      the Go backend plus Vite with hot
#                                        reload on http://localhost:5180
#
# Settings, all optional:
#   METIS_URL                  a metis that is already running (http://localhost:8273)
#   METIS_USERNAME             its administrator          (admin)
#   METIS_PASSWORD             their password             (metis-admin)
#   METIS_DIR                  the metis checkout to start (../metis, else a clone)
#   METIS_REF                  the branch a clone uses    (main)
#   METIS_PROJECT              the project to use         (the first one)
#   APPROVALS_PEOPLE_PASSWORD  the sample people's        (sample-password)
#   APPROVALS_ADDR             where the app listens      (:8090)
#   APPROVALS_PUBLIC_URL       the app as metis reaches it (http://localhost:8090)
#
# metis calls this app for the budget check, a call to localhost. The metis
# this script starts allows that; one you run yourself needs
# METIS_HTTP_ALLOW_PRIVATE_NETWORKS=true.

set -Eeuo pipefail

readonly HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly ROOT="$(cd -- "$HERE/../.." && pwd)"
readonly WEB="$HERE/web"
export METIS_URL="${METIS_URL:-http://localhost:8273}"
export METIS_USERNAME="${METIS_USERNAME:-admin}"
export METIS_PASSWORD="${METIS_PASSWORD:-metis-admin}"
export APPROVALS_WEB_DIR="$WEB/dist"
readonly METIS_REPO="https://github.com/gsoultan/metis.git"
readonly CACHE="${XDG_CACHE_HOME:-$HOME/.cache}/metis-approvals"
METIS_PID=""

die() { printf '\033[31m✗\033[0m %s\n' "$*" >&2; exit 1; }
info() { printf '\033[34m→\033[0m %s\n' "$*"; }

command -v go >/dev/null || die "Go is required: https://go.dev/dl/"

# bun if it is there, as the metis UI uses it; npm otherwise.
js() {
  if command -v bun >/dev/null; then bun "$@"; elif command -v npm >/dev/null; then npm "$@"; else die "bun or npm is required to build the UI"; fi
}

up() { curl -fsS -m 5 -o /dev/null "$METIS_URL/readyz" 2>/dev/null; }

check_metis() {
  up || die "metis is not answering at $METIS_URL. Run ./examples/approvals/run.sh with no arguments to start one"
}

# metis_dir is the checkout to start: METIS_DIR, ../metis, or a clone kept in
# the cache and brought up to date on each run.
metis_dir() {
  if [[ -n "${METIS_DIR:-}" ]]; then
    [[ -x "$METIS_DIR/scripts/dev.sh" ]] || die "METIS_DIR=$METIS_DIR is not a metis checkout"
    printf '%s' "$METIS_DIR"; return
  fi
  if [[ -x "$ROOT/../metis/scripts/dev.sh" ]]; then
    (cd "$ROOT/../metis" && pwd); return
  fi
  command -v git >/dev/null || die "git is required to fetch metis"
  local dir="$CACHE/metis" ref="${METIS_REF:-main}"
  if [[ -d "$dir/.git" ]]; then
    info "Updating metis in $dir" >&2
    git -C "$dir" fetch -q --depth 1 origin "$ref" && git -C "$dir" checkout -q --detach FETCH_HEAD \
      || die "could not update $dir; remove it to fetch again"
  else
    info "Fetching metis into $dir" >&2
    mkdir -p "$CACHE"
    git clone -q --depth 1 --branch "$ref" "$METIS_REPO" "$dir" || die "could not clone $METIS_REPO"
  fi
  printf '%s' "$dir"
}

# start_metis starts metis when none is answering, and waits until its
# administrator exists. The log goes to a file so the sample's own output
# stays readable.
start_metis() {
  up && { info "Using the metis already running at $METIS_URL"; return; }
  [[ "$METIS_URL" == "http://localhost:8273" ]] \
    || die "metis is not answering at $METIS_URL, and only one on http://localhost:8273 can be started for you"
  local dir log="$CACHE/metis.log"
  dir="$(metis_dir)"
  mkdir -p "$CACHE"
  # metis embeds its own UI, so it does not compile until that is built once.
  if [[ ! -f "$dir/ui/dist/index.html" ]]; then
    info "Building the metis UI once (a minute or two)"
    (cd "$dir/ui" && js install >/dev/null && js run build >/dev/null) || die "could not build the metis UI in $dir/ui"
  fi
  info "Starting metis and PostgreSQL (log: $log)"
  # Chosen here so the setup below can use it; dev.sh keeps a token it is given.
  export METIS_SETUP_TOKEN="${METIS_SETUP_TOKEN:-$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')}"
  (cd "$dir" && exec ./scripts/dev.sh backend) >"$log" 2>&1 &
  METIS_PID=$!
  trap stop_metis EXIT INT TERM
  local waited=0
  # The first start compiles metis, which takes a few minutes.
  until up; do
    kill -0 "$METIS_PID" 2>/dev/null || { tail -n 30 "$log" >&2; die "metis stopped while starting; its log is above and in $log"; }
    (( waited += 2 ))
    (( waited <= 600 )) || die "metis did not come up within 10 minutes; see $log"
    sleep 2
  done
  if ! set_up; then
    # metis's own sample seed runs the setup wizard, making admin / metis-admin.
    info "Setting metis up for the first time"
    SAMPLE_ADMIN_USER="$METIS_USERNAME" SAMPLE_ADMIN_PASS="$METIS_PASSWORD" "$dir/scripts/seed-sample.sh" >>"$log" 2>&1 \
      || { tail -n 20 "$log" >&2; die "setting metis up failed; see $log"; }
  fi
  info "metis is up on $METIS_URL"
}

# set_up is whether metis has an administrator yet. Asked of the setup status
# rather than by trying to sign in, so the wait cannot lock the account.
set_up() {
  curl -fsS -m 5 "$METIS_URL/api/v1/setup/status" 2>/dev/null | grep -q '"is_initialized": *true'
}

stop_metis() {
  [[ -n "$METIS_PID" ]] || return 0
  info "Stopping metis"
  kill -INT "$METIS_PID" 2>/dev/null || true
  wait "$METIS_PID" 2>/dev/null || true
  METIS_PID=""
}

build_ui() {
  info "Building the UI"
  (cd "$WEB" && js install && js run build)
}

seed() {
  info "Setting metis up for the sample"
  (cd "$ROOT" && go run ./examples/approvals seed)
}

serve() {
  info "Open ${APPROVALS_PUBLIC_URL:-http://localhost:8090}  (Ctrl-C stops it)"
  if [[ -n "$METIS_PID" ]]; then
    # Not exec: this shell stays to stop the metis it started.
    (cd "$ROOT" && go run ./examples/approvals serve) || true
  else
    cd "$ROOT" && exec go run ./examples/approvals serve
  fi
}

dev() {
  (cd "$WEB" && js install)
  (cd "$ROOT" && go run ./examples/approvals serve) &
  local backend=$!
  trap 'kill $backend 2>/dev/null' EXIT
  info "UI with hot reload on http://localhost:5180"
  (cd "$WEB" && js run dev)
}

case "${1:-all}" in
  all)   start_metis; build_ui; seed; serve ;;
  seed)  check_metis; seed ;;
  serve) check_metis; [[ -f "$WEB/dist/index.html" ]] || build_ui; serve ;;
  dev)   check_metis; dev ;;
  *)     die "unknown command $1: use seed, serve or dev, or nothing for all three" ;;
esac

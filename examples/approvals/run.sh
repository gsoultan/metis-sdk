#!/usr/bin/env bash
#
# Run the approvals sample against a metis that is already running.
#
#   METIS_PASSWORD='your-admin-password' ./examples/approvals/run.sh
#
# It builds the React UI, sets metis up for the sample (people, groups, the
# decision table and the process; safe to repeat), and starts the app on
# http://localhost:8090. Ctrl-C stops it.
#
#   ./examples/approvals/run.sh seed     set up only
#   ./examples/approvals/run.sh serve    start only, after an earlier seed
#   ./examples/approvals/run.sh dev      the Go backend plus Vite with hot
#                                        reload on http://localhost:5180
#
# Settings, all optional except the password:
#   METIS_URL                  metis's address        (http://localhost:8273)
#   METIS_USERNAME             an administrator       (admin)
#   METIS_PROJECT              the project to use     (Default Project)
#   APPROVALS_PEOPLE_PASSWORD  the sample people's    (sample-password)
#   APPROVALS_ADDR             where the app listens  (:8090)
#   APPROVALS_PUBLIC_URL       the app as metis reaches it (http://localhost:8090)
#
# metis calls this app for the budget check. On one machine that is a call to
# localhost, which metis refuses unless it was started with
# METIS_HTTP_ALLOW_PRIVATE_NETWORKS=true — see README.md.

set -Eeuo pipefail

readonly HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly ROOT="$(cd -- "$HERE/../.." && pwd)"
readonly WEB="$HERE/web"
export METIS_URL="${METIS_URL:-http://localhost:8273}"
export APPROVALS_WEB_DIR="$WEB/dist"

die() { printf '\033[31m✗\033[0m %s\n' "$*" >&2; exit 1; }
info() { printf '\033[34m→\033[0m %s\n' "$*"; }

[[ -n "${METIS_PASSWORD:-}" ]] || die "set METIS_PASSWORD to the metis administrator's password"
command -v go >/dev/null || die "Go is required: https://go.dev/dl/"

# bun if it is there, as the metis UI uses it; npm otherwise.
js() {
  if command -v bun >/dev/null; then bun "$@"; elif command -v npm >/dev/null; then npm "$@"; else die "bun or npm is required to build the UI"; fi
}

check_metis() {
  curl -fsS -m 5 -o /dev/null "$METIS_URL/readyz" 2>/dev/null \
    || die "metis is not answering at $METIS_URL. Start it first — in the metis repository: METIS_HTTP_ALLOW_PRIVATE_NETWORKS=true ./scripts/dev.sh backend"
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
  info "Starting the app on ${APPROVALS_PUBLIC_URL:-http://localhost:8090}"
  cd "$ROOT" && exec go run ./examples/approvals serve
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
  all)   check_metis; build_ui; seed; serve ;;
  seed)  check_metis; seed ;;
  serve) check_metis; [[ -f "$WEB/dist/index.html" ]] || build_ui; serve ;;
  dev)   check_metis; dev ;;
  *)     die "unknown command $1: use seed, serve or dev, or nothing for all three" ;;
esac

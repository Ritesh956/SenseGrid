#!/usr/bin/env bash
# Shared helpers for test/hardening's scripts. Thin wrapper over
# test/chaos/lib.sh rather than a duplicate: the docker-compose wrapper,
# curl/TLS flags, and MSYS_NO_PATHCONV handling are identical needs, only
# the results directory differs (test/hardening/results, not
# test/chaos/results) — see test/chaos/lib.sh's own comments for why each
# of those exists.
set -euo pipefail

HARDENING_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../chaos/lib.sh
source "$HARDENING_DIR/../chaos/lib.sh"

RESULTS_DIR="$HARDENING_DIR/results"
mkdir -p "$RESULTS_DIR"

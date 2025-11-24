#!/usr/bin/env bash

# --- begin runfiles.bash initialization v3 ---
# Copy-pasted from the Bazel Bash runfiles library v3.
set -uo pipefail; set +e; f=bazel_tools/tools/bash/runfiles/runfiles.bash
# shellcheck disable=SC1090
source "${RUNFILES_DIR:-/dev/null}/$f" 2>/dev/null || \
  source "$(grep -sm1 "^$f " "${RUNFILES_MANIFEST_FILE:-/dev/null}" | cut -f2- -d' ')" 2>/dev/null || \
  source "$0.runfiles/$f" 2>/dev/null || \
  source "$(grep -sm1 "^$f " "$0.runfiles_manifest" | cut -f2- -d' ')" 2>/dev/null || \
  source "$(grep -sm1 "^$f " "$0.exe.runfiles_manifest" | cut -f2- -d' ')" 2>/dev/null || \
  { echo>&2 "ERROR: cannot find $f"; exit 1; }; f=; set -e
# --- end runfiles.bash initialization v3 ---

set -eu

export STATE_PATH="${HOME}/bonanza_demo"
rm -rf "${STATE_PATH}/bonanza_builder_filepool" "${STATE_PATH}/bonanza_worker_filepool"
umount "${STATE_PATH}/bonanza_worker_mount" || true
mkdir -p "${STATE_PATH}/bonanza_worker_mount" || true

# Support conditionals in configuration files based on the operating system.
export OS=$(uname)

# Necessary to give the virtual file system the same ownership as the
# user running the build actions.
export USER_ID=$(id -u)
export GROUP_ID=$(id -g)
export NCPU=$(sysctl -n hw.ncpu || nproc)

# Used to create worker IDs.
export HOSTNAME=$(hostname)

# Launch processes that should be killed last.
set -m
for replica in a b; do
  for shard in 0 1 2 3; do
    mkdir -p "${STATE_PATH}/bonanza_storage_shard_${replica}${shard}/persistent_state"
    REPLICA="${replica}" SHARD="${shard}" \
    "$(rlocation build_bonanza/cmd/bonanza_storage_shard/bonanza_storage_shard_/bonanza_storage_shard)" \
        "$(rlocation build_bonanza/deployments/demo/bonanza_storage_shard.jsonnet)" &
  done
done

"$(rlocation build_bonanza/cmd/bonanza_storage_frontend/bonanza_storage_frontend_/bonanza_storage_frontend)" \
    "$(rlocation build_bonanza/deployments/demo/bonanza_storage_frontend.jsonnet)" &
"$(rlocation build_bonanza/cmd/bonanza_scheduler/bonanza_scheduler_/bonanza_scheduler)" \
    "$(rlocation build_bonanza/deployments/demo/bonanza_scheduler.jsonnet)" &
"$(rlocation build_bonanza/cmd/bonanza_browser/bonanza_browser_/bonanza_browser)" \
    "$(rlocation build_bonanza/deployments/demo/bonanza_browser.jsonnet)" &
"$(rlocation com_github_buildbarn_bb_remote_execution/cmd/bb_runner/bb_runner_/bb_runner)" \
    "$(rlocation build_bonanza/deployments/demo/bb_runner.jsonnet)" &

# Launch processes that should be killed first.
set +m
"$(rlocation build_bonanza/cmd/bonanza_builder/bonanza_builder_/bonanza_builder)" \
    "$(rlocation build_bonanza/deployments/demo/bonanza_builder.jsonnet)" &
builder_pid=$!
"$(rlocation build_bonanza/cmd/bonanza_fetcher/bonanza_fetcher_/bonanza_fetcher)" \
    "$(rlocation build_bonanza/deployments/demo/bonanza_fetcher.jsonnet)" &
fetcher_pid=$!
"$(rlocation build_bonanza/cmd/bonanza_worker/bonanza_worker_/bonanza_worker)" \
    "$(rlocation build_bonanza/deployments/demo/bonanza_worker.jsonnet)" &
worker_pid=$!

# Install trap handler that first kills the builder, fetcher and worker,
# and only kills the rest afterwards.
trap 'trap - EXIT INT TERM; wait ${builder_pid} ${fetcher_pid} ${worker_pid} || true; kill $(jobs -p); wait' EXIT INT TERM

wait

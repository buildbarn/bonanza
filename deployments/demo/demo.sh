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
mkdir -p "${STATE_PATH}/bonanza_builder_cache"
rm -rf "${STATE_PATH}/bonanza_builder_filepool"
mkdir -p "${STATE_PATH}/bonanza_builder_filepool"
umount "${STATE_PATH}/bonanza_worker_mount" || true
mkdir -p "${STATE_PATH}/bonanza_worker_mount" || true

# Launch processes that should be killed last.
set -m
for replica in a b; do
  for shard in 0 1 2 3; do
    REPLICA="${replica}" SHARD="${shard}" \
    "$(rlocation com_github_buildbarn_bonanza/cmd/bonanza_storage_shard/bonanza_storage_shard_/bonanza_storage_shard)" \
        "$(rlocation com_github_buildbarn_bonanza/deployments/demo/bonanza_storage_shard.jsonnet)" &
  done
done

"$(rlocation com_github_buildbarn_bonanza/cmd/bonanza_storage_frontend/bonanza_storage_frontend_/bonanza_storage_frontend)" \
    "$(rlocation com_github_buildbarn_bonanza/deployments/demo/bonanza_storage_frontend.jsonnet)" &
"$(rlocation com_github_buildbarn_bonanza/cmd/bonanza_scheduler/bonanza_scheduler_/bonanza_scheduler)" \
    "$(rlocation com_github_buildbarn_bonanza/deployments/demo/bonanza_scheduler.jsonnet)" &
"$(rlocation com_github_buildbarn_bonanza/cmd/bonanza_browser/bonanza_browser_/bonanza_browser)" \
    "$(rlocation com_github_buildbarn_bonanza/deployments/demo/bonanza_browser.jsonnet)" &
"$(rlocation com_github_buildbarn_bb_remote_execution/cmd/bb_runner/bb_runner_/bb_runner)" \
    "$(rlocation com_github_buildbarn_bonanza/deployments/demo/bb_runner.jsonnet)" &

# Launch processes that should be killed first.
set +m
"$(rlocation com_github_buildbarn_bonanza/cmd/bonanza_builder/bonanza_builder_/bonanza_builder)" \
    "$(rlocation com_github_buildbarn_bonanza/deployments/demo/bonanza_builder.jsonnet)" &
builder_pid=$!
"$(rlocation com_github_buildbarn_bonanza/cmd/bonanza_worker/bonanza_worker_/bonanza_worker)" \
    "$(rlocation com_github_buildbarn_bonanza/deployments/demo/bonanza_worker.jsonnet)" &
worker_pid=$!

# Install trap handler that first kills the builder and worker, and only
# kills the rest afterwards.
trap 'trap - EXIT INT TERM; wait ${builder_pid} ${worker_pid} || true; kill $(jobs -p); wait' EXIT INT TERM

wait

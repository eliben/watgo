#!/usr/bin/env bash
set -euo pipefail

# update-upstream-tests.sh refreshes watgo's checked-in test assets from their
# upstream repositories.
#
# By default it updates:
#   - tests/wasmspec/scripts        from WebAssembly/spec test/core
#   - tests/wasm-wat-samples/*      from the wasm-wat-samples repository
#
# The clone URLs and refs can be overridden with environment variables:
#   SPEC_REPO_URL     default: https://github.com/WebAssembly/spec.git
#   SPEC_REPO_REF     optional branch/tag/commit-ish for the spec repo
#   SAMPLES_REPO_URL  default: https://github.com/eliben/wasm-wat-samples.git
#   SAMPLES_REPO_REF  optional branch/tag/commit-ish for the samples repo
#
# The script clones into a temporary directory under /tmp and then rsyncs the
# tracked upstream trees into the local repository.

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${script_dir}/.." && pwd)"

spec_dst="${repo_root}/tests/wasmspec/scripts"
samples_dst="${repo_root}/tests/wasm-wat-samples"

spec_repo_url="${SPEC_REPO_URL:-https://github.com/WebAssembly/spec.git}"
spec_repo_ref="${SPEC_REPO_REF:-}"
samples_repo_url="${SAMPLES_REPO_URL:-https://github.com/eliben/wasm-wat-samples.git}"
samples_repo_ref="${SAMPLES_REPO_REF:-}"

workdir="$(mktemp -d /tmp/watgo-test-update.XXXXXX)"
trap 'rm -rf "${workdir}"' EXIT

clone_repo() {
  local url="$1"
  local ref="$2"
  local dst="$3"

  if [[ -n "${ref}" ]]; then
    git clone --depth 1 --branch "${ref}" "${url}" "${dst}"
  else
    git clone --depth 1 "${url}" "${dst}"
  fi
}

echo "Cloning spec repo into ${workdir}/spec"
clone_repo "${spec_repo_url}" "${spec_repo_ref}" "${workdir}/spec"

echo "Syncing spec core tests into ${spec_dst}"
# Only .wast files are copied from the spec tree. Directory structure is
# preserved so proposal subdirectories like gc/, simd/, etc. remain intact.
rsync -a --delete --delete-excluded \
  --exclude='.*' \
  --exclude='*/.*' \
  --include='*/' \
  --include='*.wast' \
  --exclude='*' \
  "${workdir}/spec/test/core/" "${spec_dst}/"

echo "Cloning sample repo into ${workdir}/samples"
clone_repo "${samples_repo_url}" "${samples_repo_ref}" "${workdir}/samples"

echo "Syncing sample directories into ${samples_dst}"
# Sync the sample repo with a single rsync. Hidden clone metadata is ignored,
# and the upstream helper directory "_tools" is skipped. All other contents of
# sample subdirectories are copied through, so assets such as README files or
# test fixtures remain available locally.
rsync -a \
  --exclude='.*' \
  --exclude='*/.*' \
  --exclude='_tools/' \
  "${workdir}/samples/" "${samples_dst}/"

echo "Done."

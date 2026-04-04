# Tests

This directory holds watgo's end-to-end and integration-style test corpora.
The corpora are chosen to be self-testing - the WAT modules compiled by watgo
get executed (using Node.js) and results compared to expected results.

## Test Sets

- `wasmspec/`
  - Upstream source: WebAssembly spec `test/core`
  - Repo: <https://github.com/WebAssembly/spec>
  - Content: `.wast` spec scripts
  - Harness: `tests/wasmspec/wasmspec_harness.go`

- `wabt-interp/`
  - Upstream source: WABT `test/interp`
  - Repo: <https://github.com/WebAssembly/wabt>
  - Content: `.txt` fixtures
  - Harness: `tests/wabt-interp/wabt_interp_test.go`

- `wasm-wat-samples/`
  - Upstream source: `wasm-wat-samples`
  - Repo: <https://github.com/eliben/wasm-wat-samples>
  - Content: sample directories with `.wat`, `test.js`, and related assets
  - Harness: `tests/wasm-wat-samples/wat_samples_test.go`

## Updating From Upstream

Use:

```bash
tests/update-upstream-tests.sh
```

This script clones the upstream repositories into `/tmp` and syncs their tracked
test assets into this tree. It is intended to be run manually once in a while,
not automatically as part of normal `go test` runs.

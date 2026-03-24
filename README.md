# watgo

Wasm Toolkit for Go (watgo) to parse WASM (text and binary) into internal data
structures, allowing conversions, etc.

### WASM feature support and proposals

Initially, we aim to support all the [finished proposals](https://github.com/WebAssembly/proposals/blob/main/finished-proposals.md)
without any flags or feature selection. Finished proposals are part of the WASM
spec.

If there's a request to support [active proposals](https://github.com/webassembly/proposals),
we'll consider employing explicit feature flags to gate this support.

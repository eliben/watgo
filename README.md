# watgo

**WA**SM **T**oolkit for **Go** (**watgo**) to parse WASM text or decode WASM binary into internal data
structures, allowing conversions, etc.

### WASM feature support and proposals

Initially, we aim to support all the [finished proposals](https://github.com/WebAssembly/proposals/blob/main/finished-proposals.md)
without any flags or feature selection. Finished proposals are part of the WASM
spec.

If there's a request to support [active proposals](https://github.com/webassembly/proposals),
we'll consider employing explicit feature flags to gate this support.

### Installation

To install the CLI into your Go bin directory:

```sh
go install github.com/eliben/watgo/cmd/watgo@latest
```

To run it directly from a checkout without installing:

```sh
go run ./cmd/watgo help
```

To run it straight from the module path without installing:

```sh
go run github.com/eliben/watgo/cmd/watgo@latest help
```

### CLI

`watgo` currently provides basic `parse` and `validate` subcommands.

For supported subcommands and flags, the CLI aims to stay compatible with
[`wasm-tools`](https://github.com/bytecodealliance/wasm-tools).

Examples:

```sh
# Compile WAT text to a WASM binary file.
watgo parse input.wat -o output.wasm

# Validate a WAT file.
watgo validate input.wat

# Validate a WASM binary.
watgo validate input.wasm

# Read WAT from stdin and write WASM to stdout.
cat input.wat | watgo parse > output.wasm
```

For full command-line help, run:

```sh
watgo help
```

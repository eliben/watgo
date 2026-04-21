# Go `js/wasm` Empty Program Fixture

This directory stores a committed wasm binary produced by the Go toolchain and
used by tests in [`../toolchain_test.go`](../toolchain_test.go).

The fixture is intentionally checked in so tests do not invoke external
toolchains at runtime.

## Source Program

```go
package main

func main() {}
```

## Manual Regeneration

The current fixture was produced with Go `1.26.1` using:

```sh
cat > main.go <<'EOF'
package main

func main() {}
EOF

GOOS=js GOARCH=wasm CGO_ENABLED=0 go build -o main.wasm main.go
```

Expected custom sections include:

- `go:buildid`
- `producers`
- `name`

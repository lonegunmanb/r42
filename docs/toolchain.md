# Toolchain

r42 requires Go 1.25 or newer because its Golden dependency requires Go 1.25.
CI exercises the minimum supported Go 1.25 release and Go 1.26.

The repository quality gates use:

- Go 1.25 and 1.26;
- golangci-lint 2.12.2;
- actionlint 1.7.7.

Run the same mandatory checks locally with `make check`, or run the underlying
commands directly:

```text
go vet ./...
go test ./... -count=1
golangci-lint run
```

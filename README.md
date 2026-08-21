# OCIHood

OCIHood is a command-line tool for provisioning Oracle Cloud Infrastructure resources.

## Development

The supported toolchain is Go 1.27.0 and golangci-lint 2.13.1.

Install the pinned linter:

```sh
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.1
```

Run the complete local quality gate:

```sh
go test ./...
go test -race ./...
go vet ./...
test -z "$(gofmt -l .)"
golangci-lint run
go build -o ocihood ./cmd/ocihood
```

The build and tests require no OCI credentials or external services.

## Logging

OCIHood uses the standard library's `log/slog` package. Operational logs and diagnostics use the human-readable text handler and go to stderr. Command results go to stdout. Prefer structured fields such as `logger.Info("request complete", "region", region)` over formatted messages. Never log secrets, tokens, private keys, credentials or their contents.

## License

Apache License 2.0. See [LICENSE](LICENSE).

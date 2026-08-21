# OCIHood

OCIHood is a command-line tool for provisioning Oracle Cloud Infrastructure resources.

## Configuration

OCIHood reads YAML from `--config <path>` or the OS user configuration directory at
`ocihood/config.yaml`. Values resolve in this order: built-in defaults, global `defaults`,
then per-account `overrides`.

```yaml
defaults:
  request_timeout: 30s
  retry_min: 30s
  retry_max: 15m
  shape: VM.Standard.A1.Flex
  ocpus: 2
  memory_gb: 12
  boot_volume_gb: 50
  public_ip: true
accounts:
  personal:
    oci_config_path: /home/me/.oci/config
    oci_profile: DEFAULT
    region: eu-frankfurt-1
    ssh_public_key_path: /home/me/.ssh/id_ed25519.pub
    ssh_private_key_path: /home/me/.ssh/id_ed25519
    overrides:
      memory_gb: 8
```

Credential and SSH values are references only; OCIHood never copies their contents into
project configuration or `config show` output.

```sh
ocihood config validate --config ./config.yaml
ocihood config show --config ./config.yaml --account personal
```

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

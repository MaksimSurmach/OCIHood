# OCIHood

`ocihood start` resolves configuration in this order: explicit CLI flags, account overrides, global project settings, then built-in defaults. `--config` selects a YAML file; without it, a run can be fully configured by flags and standard OCI credentials.

```sh
ocihood start --account personal --oci-profile DEFAULT \
  --compartment-id ocid1.compartment... --image-id ocid1.image... \
  --subnet-id ocid1.subnet... --ssh-public-key ~/.ssh/id_ed25519.pub
```

Use `ocihood start --help` for all operational flags. Credential and token contents are accepted only through referenced files/profiles, never raw secret flags.

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
  circuit_breaker:
    failures: 5
    half_open_requests: 1
    interval: 5m
    open_timeout: 1m
  shape: VM.Standard.A1.Flex
  ocpus: 2
  memory_gb: 12
  boot_volume_gb: 50
  public_ip: true
  policy:
    allowed_shapes: [VM.Standard.A1.Flex]
    max_ocpus: 2
    max_memory_gb: 12
    max_boot_volume_gb: 50
    allow_exceed: false
  notifications:
    enabled: true
    telegram_chat_id: "123456789"
    telegram_token_env: OCIHOOD_TELEGRAM_TOKEN
accounts:
  personal:
    oci_config_path: /home/me/.oci/config
    oci_profile: DEFAULT
    region: eu-frankfurt-1
    ssh_public_key_path: /home/me/.ssh/id_ed25519.pub
    ssh_private_key_path: /home/me/.ssh/id_ed25519
    compartment_id: ocid1.compartment.oc1..example
    image_id: ubuntu # or an exact ocid1.image...
    vcn_name: main
    subnet_name: public
    overrides:
      memory_gb: 8
```

Credential and SSH values are references only; OCIHood never copies their contents into
project configuration or `config show` output.

Telegram notifications are optional and best-effort. `telegram_token_env` names the environment
variable containing the bot token; the token itself is never stored in YAML or durable state.
Account `overrides.notifications.enabled: false` disables inherited global notifications.

The local policy is a configurable safety ceiling, not an Oracle price or Free Tier guarantee.
Built-in limits match OCIHood's built-in resource defaults. `plan` reports the resolved values,
policy decision and every violated rule. `start` rejects violations before capacity or launch;
set `policy.allow_exceed: true` explicitly in global or account configuration to permit them.
There is no interactive or implicit unattended override, and capacity retries never change the
requested shape, OCPUs, memory or boot volume.

Discovery is read-only and paginates every OCI list operation. `image_id` is required; OCIHood
does not select images by mutable names or OS metadata. `ocihood images list` returns the current
`AVAILABLE` images compatible with the configured shape, newest first, so an exact OCID can be
chosen before `plan` or `start`. The reserved value `ubuntu` resolves to the newest standard image
of the highest compatible `Canonical Ubuntu` version. Public platform image OCIDs are valid
even when OCI returns an empty image compartment. VCN/subnet names must resolve uniquely. All
availability domains are retained in sorted order for later rotation.

## Desired-resource identity and reconciliation

`TargetID` is the SHA-256 digest of canonical JSON containing the normalized account and
region, compartment/subnet/image identifiers, shape, OCPUs, memory, boot volume and public-IP
intent. Strings are trimmed; account and region are lowercased. Runtime timestamps, retry
counters, request IDs and launch attempts are excluded.

Config supplies the target inputs. Durable state stores `TargetID`, the observed instance ID,
and any in-flight `AttemptID`, OCI retry token and validity deadline. OCI free-form tags store
`ocihood.managed=true`, `ocihood.target-id` and `ocihood.account`; all three must match before
an instance is considered owned. Reconciliation compares durable state with owned provider
observations and fails safe on incompatible state or multiple active matches.

## Durable state

Each account/TargetID has one schema-versioned JSON state file under `state_dir`. Writes use
an exclusive same-host lock, a restrictive temporary file, fsync and atomic rename. Missing,
corrupt, truncated and unsupported-version state are distinct failures; none is interpreted as
permission to create an instance. State contains lifecycle/progress identifiers only, never OCI
private keys or notification credentials.

```sh
ocihood config validate --config ./config.yaml
ocihood config show --config ./config.yaml --account personal
ocihood images list --config ./config.yaml --account personal
ocihood images list --config ./config.yaml --account personal --output=json
ocihood plan --config ./config.yaml --account personal
ocihood status --config ./config.yaml --account personal
```

`plan` uses the same authentication and discovery path as `start`, then reports the resolved
resources, shape architecture, selected image name/version and reconciliation action without writing state, waiting for
capacity, or mutating OCI. Use `--output=json` for schema `ocihood.plan/v1`. Resource defaults are
configurable under `defaults`/account `overrides`; exact CLI values `--shape`, `--ocpus`,
`--memory-gb` and `--boot-volume-gb` take precedence. If a configured default exceeds the built-in
12 GiB policy ceiling, raise `policy.max_memory_gb` explicitly as well; no larger quota/cost profile
is selected implicitly.
`images list` authenticates and reads only the Compute image inventory for the resolved compartment
and shape. Text output is tabular; JSON uses schema `ocihood.images/v1`. It does not read or write
provisioning state and does not discover VCNs, subnets, instances, or capacity.
`status` reads the sole persisted target for the account without contacting OCI or mutating state.
If multiple target states exist, it fails instead of choosing one.
`start` performs discovery, loads this state under the target lock, runs the reconciliation
decision, and persists the resulting lifecycle before any future launch step may proceed.

## Execution modes and output

Normal `start` waits and retries until success or cancellation. `--once` probes each eligible
availability domain at most once and returns `no_capacity` without sleeping or starting another
cycle. `--max-runtime 10m` bounds either mode through context cancellation.

`--output text|json` controls the final stdout result. JSON uses schema `ocihood.start/v1` and
contains account, TargetID, outcome, region, instance identity/state/public IP, and a sanitized
error category/message when applicable. `--log-format text|json` and
`--log-level debug|info|warn|error` independently control diagnostics on stderr.

Exit codes are stable: `0` success/already satisfied, `3` one-shot no capacity, `4` retryable
provider failure, `1` fatal configuration/authentication/provider failure, `124` deadline, and
`130` cancellation.

## Capacity watcher

For create-safe decisions, `start` probes OCI Compute Capacity Report using the root tenancy,
requested shape/OCPUs/memory, and every discovered availability domain in sorted round-robin
order. A positive report is advisory. Unsupported or unauthorized probing returns a distinct
fallback result for the later launch step; it is not treated as no capacity.

Full rotations use exponential backoff from `retry_min` through `retry_max` with bounded 20%
jitter. `request_timeout` bounds each probe. Throttling guidance can extend the delay. The last
AD, retry count, next attempt, and status are stored atomically, so restart resumes after the
persisted delay and continues with the next AD. Cancellation interrupts requests and waits.

Circuit breakers are runtime-only and keyed by account, region, OCI service, and operation family
(`compute/capacity` or `compute/instance`). AD rotation cannot bypass an open breaker, while one
family cannot block the other. Existing retry/backoff owns timing; the breaker only fast-fails
sustained transport, timeout, throttling, or OCI 5xx failures. No-capacity, expected empty/not-found,
other 4xx, and cancellation outcomes do not trip it.

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

For optional MAX-360 live-OCI validation, safely map boundaries 1, 5-8, and 10 to controlled
cancellation/restart runs. Boundaries 2-4 require injected response or persistence failures, and
boundary 9 requires controlled capacity exhaustion, so they remain deterministic local tests.

## Logging

OCIHood uses the standard library's `log/slog` package. Operational logs and diagnostics go to stderr; final command results go to stdout. Prefer structured fields such as `logger.Info("request complete", "region", region)` over formatted messages. Never log secrets, tokens, private keys, credentials or their contents.

## License

Apache License 2.0. See [LICENSE](LICENSE).

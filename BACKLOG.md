# OCIHood Backlog

Single-file backlog migrated from the OCIHood Linear project.

## Problem 1 — Bootstrap Go CLI with Cobra and `start` command

**Status:** Done

### Problem

Create the initial OCIHood CLI entrypoint. `ocihood start` is the canonical command that starts one provisioning run through an application runner interface.

### Task

* Build on the repository/CI baseline from Problem 17.
* Add Cobra root command and `start` subcommand under the `ocihood` binary.
* Keep Cobra responsible only for argument parsing, command wiring, help and mapping application results to process output/exit status.
* Define a small runner/orchestrator interface; no provisioning logic belongs in command handlers.
* Root command with no subcommand shows useful help and exits successfully.
* Unknown commands/flags produce a clear error and non-zero exit status.
* `start` invokes the runner exactly once per command execution.
* SIGINT/SIGTERM cancel the root context and are propagated to the runner.
* Runner errors are returned to the CLI and written as diagnostics to stderr; normal command results stay on stdout.

---

## Problem 2 — Add configuration model and multi-account profiles

**Status:** Done

### Problem

Introduce the typed OCIHood configuration model with explicit defaults, validation and support for multiple OCI accounts.

### Task

* Use YAML for project configuration.
* Support an explicit `--config <path>` and a documented default path derived from the OS user config directory (for example `<UserConfigDir>/ocihood/config.yaml`).
* Define global defaults for retry/backoff bounds, request timeout, state/log directory, target shape/resources and other shared runtime values.
* Define named `accounts`; each account can reference its own OCI config path/profile, SSH key paths and account-specific overrides.
* Resolve effective values deterministically: built-in defaults < global config < account overrides. Later CLI flags may override this model but are not implemented here.
* Reject unknown YAML fields instead of silently ignoring likely typos.
* Validate required values, ranges and cross-field constraints before any provider work starts.
* Treat credential/key fields as references/paths only; do not copy OCI private key material or provider secrets into project YAML.
* Add `ocihood config validate` and `ocihood config show --account <name>`.
* `config show` prints effective non-secret configuration and identifies the selected account without exposing credential contents.

---

## Problem 3 — Implement OCI authentication package

**Status:** Done

### Problem

Provide the OCI-specific authentication layer that turns one resolved account configuration into authenticated OCI SDK clients without leaking credentials into the rest of the application.

### Task

* Add `internal/provider/oci/auth` (or equivalent) package behind a small testable boundary.
* Read standard OCI SDK config/profile from the configured OCI config path/profile; use the OCI SDK configuration provider rather than reimplementing signing/key parsing.
* Resolve tenancy, user, region, fingerprint and signing-key reference through the SDK provider.
* Allow the effective OCIHood account configuration to override region when explicitly configured; otherwise use the OCI profile region.
* Construct the provider clients needed by subsequent tasks without global mutable SDK state.
* Validate authentication/connectivity with one lightweight read-only OCI Identity API call before provisioning continues.
* Support multiple account/profile instances in the same process without credential or region leakage between them.
* Classify missing/local credential errors separately from OCI authentication/authorization/network errors where practical.
* Never log private-key contents, passphrases, tokens, fingerprints as secrets, or complete credential config contents.
* Respect caller context/timeouts for the validation call.

---

## Problem 4 — Add Provisioner core and wire `start` to OCI connectivity

**Status:** Done

### Problem

Create the provider-independent Provisioner orchestration core and make `ocihood start --account <name>` execute a real authenticated, read-only OCI bootstrap run.

### Task

* Define Provisioner/application orchestration interfaces in `internal/provisioner` without importing Cobra.
* Wire `start` through the existing layers in this order: CLI -> effective config/account resolution -> OCI auth/client construction -> Provisioner run.
* Build a run context containing the selected account identity, effective non-secret settings, logger and provider-facing dependencies.
* Perform only a safe read-only bootstrap/connectivity operation in this task; no discovery loop, capacity polling or resource writes.
* Propagate the caller context through all orchestration/provider calls.
* Stop immediately on config/auth/bootstrap failure; do not continue into later phases after an earlier phase fails.
* Return a typed application result/error to the CLI instead of printing from core packages.
* Emit meaningful structured progress logs for start/bootstrap/success/failure without logging secrets.

---

## Problem 5 — Implement OCI resource discovery

**Status:** Done

### Problem

Discover and validate all OCI resources required for provisioning deterministically and read-only, without hard-coded OCIDs or unsafe first-item selection.

### Task

* Consume the TargetID/ownership/reconciliation contract from Problem 18.
* Resolve tenancy/root compartment context, configured target compartment and current region explicitly.
* Discover all availability domains relevant to the tenancy/region and preserve them for later rotation.
* Discover compatible images for the configured OS/version/architecture/shape.
* Image selection must be deterministic: explicit image OCID wins; otherwise apply configured filters and a documented deterministic selection rule. Never silently use `items[0]`.
* Discover/select VCN and subnet using explicit deterministic rules. Explicit OCID overrides win; ambiguous matches fail with actionable guidance instead of arbitrary selection.
* Validate that explicitly supplied image/network OCIDs are compatible with the selected region/compartment/target rather than accepting them blindly.
* Inspect existing instances relevant to the TargetID using OCIHood ownership metadata from Problem 18.
* Preserve lifecycle/identity information needed by reconciliation; unrelated instances must remain unrelated even when shape/name match.
* Correctly handle paginated OCI list APIs; discovery must not assume the first response page is complete.
* Return one typed Discovery result consumed by planning/state/watcher/launch.
* Perform no mutating OCI calls.

---

## Problem 6 — Implement OCI Capacity Watcher with AD rotation and backoff

**Status:** Done

### Problem

Implement a safe long-running OCI capacity watcher with AD rotation, bounded API pressure and durable/resumable progress.

### Task

* Use OCI Compute Capacity Report as a read-only pre-check where supported/authorized for the requested shape/resource configuration.
* Build capacity reports for each candidate availability domain using the tenancy/root compartment as required by OCI, not the target workload compartment.
* Treat capacity report output as advisory rather than a guarantee that the subsequent launch will succeed.
* Rotate through all eligible discovered ADs fairly; do not permanently hammer one AD while others are eligible.
* Add configurable request timeout, initial retry interval, maximum interval, exponential backoff and jitter.
* Distinguish at least: capacity unavailable, capacity available, capacity probe unavailable/unsupported, throttling, transient provider/network error, fatal auth/config error and cancellation.
* If capacity-report probing is unavailable/unsupported but provisioning is otherwise valid, return a typed `unknown/probe-unavailable` result that lets the orchestration/launch layer make a safely rate-limited real launch attempt rather than making OCIHood unusable solely because the advisory probe is unavailable.
* Respect OCI throttling/Retry-After style guidance where exposed; never busy-loop.
* Persist meaningful watcher state (selected/last AD, retry count/next attempt/status) without writing state on every insignificant poll.
* Respect caller cancellation immediately, including during backoff sleep.
* Emit concise transition/progress logs without one noisy log/notification per unchanged poll.

---

## Problem 7 — Implement OCI instance launch and end-to-end provisioning loop

**Status:** Done

### Problem

Implement the first complete OCIHood provisioning loop that safely turns a validated desired target into at most one managed OCI Free Tier instance.

### Task

* Add OCI `LaunchInstance` behind a small provider interface.
* Build the launch request only from the effective config + Discovery + capacity/placement result; do not re-resolve resources ad hoc inside the launch adapter.
* Apply OCIHood ownership markers/tags from Problem 18 to every launched managed instance.
* Populate target shape, OCPU, memory, image, subnet, boot volume, SSH public key and public-IP behavior exactly from the resolved plan/config.
* Persist the logical AttemptID and OCI `opc-retry-token` before/with the in-flight launch transition so a crash/timeout can be reconciled safely.
* Reuse the same retry token for retries of the same ambiguous logical launch when appropriate; reconcile provider state before starting a new logical attempt.
* Classify launch outcomes at least as: success/accepted, out-of-capacity, throttled/transient, ambiguous/timeout, fatal invalid configuration/request, service-limit conflict and cancellation.
* `OutOfHostCapacity`/equivalent capacity loss after a positive report must return to the watcher/rotation path rather than fail the entire run permanently.
* If capacity probing was unavailable/unknown, allow safely rate-limited launch attempts; capacity failure returns to watcher/backoff.
* On throttling/transient server/network failure, retry only through the configured retry/backoff policy and preserve idempotency.
* On timeout/unknown outcome, reconcile by retry token/ownership observations before any new create decision.
* On LimitExceeded/service-limit errors, reconcile first; if no intended managed instance exists and the account limit really blocks creation, terminate with actionable diagnostics instead of looping forever.
* Poll the created instance lifecycle with bounded request timeouts until RUNNING or a terminal failure/cancellation.
* Resolve/report public IP when available, but do not fail an otherwise successful instance solely because a public IP was intentionally disabled/not yet assigned.
* Persist state transitions needed for restart/resume.
* `ocihood start --account <name>` must execute the full flow without creating a second active instance for the same TargetID.

---

## Problem 8 — Add durable per-account provisioning state

**Status:** Done

### Problem

Persist enough local state before the long-running watcher/launch flow so retries and restarts are observable, resumable and safe from duplicate creation.

### Task

* Store state under the configured state directory and isolate it by account + stable TargetID from Problem 18.
* Define and persist a schema version so future state migrations/incompatibilities can be detected explicitly.
* Persist at least: lifecycle status, TargetID, logical AttemptID/OCI retry token when applicable, last attempt/result, selected AD, retry counters/timestamps, created instance OCID/public IP, last error and update timestamp.
* Use atomic replacement semantics; a crash/interruption during write must not leave a partially written state accepted as valid.
* Use file locking suitable for one host so two writers for the same account/TargetID cannot concurrently mutate the same state.
* A second conflicting writer must fail clearly rather than proceed unlocked.
* State files should use restrictive permissions where supported and must never contain OCI private keys, Telegram tokens or other credentials.
* Missing state means `no local state`; corrupted/truncated/unsupported-version state must be detected and must not be silently treated as empty.
* On restart, reconcile persisted state with OCI/provider observations before any new create decision.
* Add `ocihood status --account <name>` that can read persisted state without a running daemon and does not mutate provider/state.
* Scheduling/pause tasks may extend the schema later without breaking older valid state unexpectedly.

---

## Problem 9 — Add notification interface and Telegram notifier

**Status:** Done

### Problem

Add a provider-independent notification layer with Telegram as the first implementation, without allowing notification failures/noise to affect provisioning correctness.

### Task

* Define typed notification events independent of OCI/provider packages.
* Support at least meaningful state transitions for: run started, waiting/no-capacity, important retry/degraded condition, terminal failure, paused/resumed, capacity found and instance created/running.
* Do not emit one notification per unchanged watcher poll; deduplicate/rate-limit repeated equivalent state where needed.
* Add a notifier interface that can support multiple future channels without changing Provisioner core behavior.
* Implement Telegram Bot API notifier with bounded request timeout and caller context.
* Support global notification config plus per-account override/disable.
* Notification sending must be best-effort and asynchronous/bounded enough that a slow notifier cannot indefinitely block provisioning.
* Notification failure is logged/observable but must never change a successful provisioning result into failure.
* Keep Telegram bot token outside durable state and redact it from logs/config/status/errors.
* Message content must include enough context to identify account/region/TargetID and, on success, instance identity/public IP when available.

---

## Problem 10 — Support configless execution with complete CLI flag overrides

**Status:** Done

### Problem

Allow a complete OCIHood provisioning run without a project YAML file by exposing operational configuration through Cobra flags while preserving one typed effective-config model.

### Task

* Every non-secret operational field required for a complete run must have a CLI override where practical: OCI config/profile/account reference, region, compartment/network/image selectors, SSH key reference, shape/OCPU/RAM, boot volume, public IP, retry/backoff/timeouts, state/log paths and optional feature settings.
* Define one precedence model: CLI flags > account overrides > global project config > built-in defaults.
* Distinguish an unset flag from an explicit zero/false value so boolean/numeric overrides work correctly.
* `ocihood start` must work with no project config file when all required inputs are supplied by flags/defaults/referenced standard OCI credentials.
* Config-file and configless execution must resolve through the same typed validation model; do not maintain two separate option structs with divergent behavior.
* An explicit `--config` path remains supported, and flags may override values from that file.
* Secret values should not be encouraged as raw command-line arguments when they would be exposed via shell history/process listings. Prefer environment/file/reference inputs for tokens/password-like values.
* Help text must document defaults/precedence and clearly identify required references without printing secret values.
* `config show`/logs/error output must redact sensitive references/values consistently regardless of whether the value originated from file or flag.

---

## Problem 11 — Add foreground, one-shot and machine-readable execution modes

**Status:** Done

### Problem

Make OCIHood usable both by humans and automation with predictable foreground, one-shot, bounded-runtime and machine-readable behavior.

### Task

* Keep normal `ocihood start` as foreground long-running mode with concise human-readable progress logs.
* Add `--once`: perform exactly one discovery/reconciliation/capacity/provision decision cycle and then exit. It must not enter the indefinite watcher loop.
* Add `--max-runtime <duration>` that cancels the run through context when the configured runtime expires.
* Add `--log-level` and `--log-format text|json` for diagnostics.
* Add `--output text|json` for the final command result independently of log format.
* Preserve stdout/stderr contract: final command result only on stdout; logs/diagnostics only on stderr.
* Define and document stable typed result categories and process exit-code mapping for at least: success/already-satisfied, no-capacity in one-shot mode, retryable/transient failure, fatal config/auth/provider error and cancellation/deadline.
* JSON output must have a documented stable top-level schema/version and include enough fields to identify account/TargetID, outcome, instance identity/state/public IP where applicable and sanitized error category/message where applicable.
* Human and JSON modes must represent the same underlying typed result; formatting must not change core behavior.
* Cancellation/max-runtime must not convert an already completed success into failure after the fact.

---

## Problem 12 — Implement long-running daemon runtime and local control commands

**Status:** Todo

### Problem

Add a first-class long-running daemon runtime that can manage configured OCIHood jobs and be inspected/controlled locally without coupling core provisioning to systemd.

### Task

* Add `ocihood daemon run` as a foreground daemon entrypoint suitable for a service manager.
* Support multiple configured account/TargetID jobs in one process while isolating each job's context, state and failures.
* Reuse the existing durable state/status model; do not create a second incompatible daemon-only state source.
* Add a local-only control transport, preferably a Unix domain socket on Linux.
* Socket/control endpoint must use restrictive local permissions and must not be exposed on a network interface by default.
* Extend/reuse CLI controls for `status`, `pause`, `resume` and `stop` with optional account targeting.
* Starting a second daemon against the same control/state scope must fail clearly rather than create competing controllers.
* Detect/clean up a stale local socket only when it is safe to prove no live daemon owns it.
* Pausing one account stops new watcher/launch work for that account without cancelling unrelated account jobs.
* Resume continues from persisted/reconciled state rather than starting a fresh blind provisioning attempt.
* Daemon shutdown cancels child jobs, waits for bounded cleanup/state flush and then exits.
* Expose daemon PID/version/start time and per-account lifecycle/next-action state through status.
* Control protocol requests/responses must be typed/versionable enough to reject malformed/unsupported requests cleanly.
* Do not transmit or expose provider secrets through the local control protocol/status response.

---

## Problem 13 — Add systemd installation and service integration

**Status:** Backlog

### Problem

Make the OCIHood daemon easy and safe to install/run under systemd without putting service-manager behavior into Provisioner core.

### Task

* Add `ocihood daemon install` that renders/installs a systemd unit using absolute executable/config/state/control paths.
* Support an explicit service mode (system service and/or user service); do not silently guess when privileges/target differ.
* Provide a documented way to install only versus install+enable+start (for example explicit `--enable`/`--now` semantics). The command behavior must be idempotent and documented.
* Add `daemon uninstall`, `daemon service-status` and restart/start/stop helpers only where they add clear value beyond normal systemctl usage.
* Use `Restart=on-failure` with sensible bounded restart behavior; SIGTERM must reach OCIHood for graceful shutdown.
* Do not embed OCI credentials, Telegram tokens, proxy passwords or other secrets directly into the unit file/command line.
* Validate referenced executable/config/state directories before installation and fail before partially installing an invalid unit when possible.
* Correctly quote/escape filesystem paths in rendered unit configuration.
* Run the required systemd daemon-reload/enable/start operations only when requested and surface failures clearly.
* Uninstall must remove only OCIHood service-manager artifacts; user config/state/logs are preserved unless a separate explicit destructive option is ever added.
* Repeated install/uninstall should be safe and predictable.
* Document an equivalent manual unit for users who do not want CLI-managed installation.

---

## Problem 14 — Add scheduling windows, delayed start and pause-until controls

**Status:** Backlog

### Problem

Add deterministic scheduling windows, delayed start and pause-until behavior shared by foreground and daemon execution.

### Task

* Add scheduling model for `start-at`, `stop-at`/`finish-at`, optional `max-runtime` and `pause-until`.
* Accept unambiguous timestamps: RFC3339 with offset, or local wall time together with an explicit IANA timezone. Document the default timezone behavior when no zone is supplied.
* Define boundary semantics explicitly: start time is inclusive; once stop/finish time is reached, no new capacity probe/launch attempt may begin.
* If an OCI launch was already accepted before the stop deadline, do not destroy the instance merely because the window ended; finish only the reconciliation/state update needed to know the outcome.
* `pause-until` suppresses new watcher/launch work for the selected job until the timestamp, then resumes automatically from persisted/reconciled state.
* Foreground and daemon modes must use the same scheduling decision component.
* Persist schedule/pause intent required to survive daemon restart.
* Sleeping until start/pause/retry deadlines must be context-cancellable and use injectable clock/timer abstractions for tests.
* `max-runtime` is measured from actual run start and composes predictably with absolute stop-at; the earliest applicable deadline wins.
* Status must distinguish waiting-for-schedule, paused, waiting-for-capacity, running/provisioning and finished-window states, and show the next relevant timestamp.
* Define behavior for expired windows, invalid ranges and daylight-saving ambiguous/nonexistent local times; never silently reinterpret an invalid local timestamp.

---

## Problem 15 — Add HTTP(S) and SOCKS5 proxy support

**Status:** Backlog

### Problem

Allow OCI and notification traffic to use an explicitly configured HTTP(S) or SOCKS5 proxy without duplicating transport logic or leaking proxy credentials.

### Task

* Define one reusable outbound transport/proxy factory used by OCI SDK HTTP clients and notification HTTP clients.
* Support HTTP proxy, HTTPS requests through an HTTP CONNECT-capable proxy and SOCKS5.
* Support global proxy configuration plus per-account override.
* Support `no_proxy`/bypass host rules with deterministic documented matching behavior.
* Define precedence explicitly: CLI override (when provided by Problem 10) > per-account proxy > global proxy > no proxy. Standard environment proxy variables must not silently change behavior unless explicitly documented/enabled.
* Allow proxy credentials to be referenced from environment/secret reference where practical; avoid encouraging raw password-bearing command-line arguments.
* Validate proxy URL/scheme/credential-reference errors before the watcher starts where possible.
* Redact userinfo/password/token data from logs, errors, status and effective-config output.
* Context, request timeouts, OCI retry behavior and notifier behavior must continue to work through the proxy.
* `no_proxy` bypassed hosts connect directly while non-bypassed hosts use the configured proxy.
* No proxy remains the default and must produce the same behavior as before this feature.

---

## Problem 16 — Add interactive config builder and account wizard

**Status:** Todo

### Problem

Provide an interactive `ocihood config init` builder that creates or updates a valid OCIHood configuration safely without requiring manual YAML editing.

### Task

* Add `ocihood config init` using the same typed config model/validation rules as non-interactive configuration.
* Detect standard OCI config path/profile candidates and allow the user to choose or enter an explicit path/profile.
* Prompt for account name, OCI profile/config path, SSH key reference, target resources, networking/image preferences, retry settings and optional feature configuration.
* Show documented defaults and let Enter accept a default only when that default is valid for the field.
* Validate individual input where practical and validate the complete effective configuration before writing anything.
* Show a final redacted review summary before save.
* Existing valid config must be loaded and preserved; adding/updating one account must not silently remove unrelated accounts/global settings/comments where preservation is feasible with the chosen YAML approach.
* Never overwrite an existing named account with materially different values without explicit user confirmation.
* Write updates atomically. If validation, cancellation, EOF or write fails, the previous config remains intact.
* When replacing an existing file, create a documented backup or otherwise provide a safe recovery path before destructive replacement.
* Secret/token values must not be echoed in prompts/review output/logs and should be stored as references rather than plaintext when supported by the config model.
* In a non-interactive/non-TTY context, fail clearly and point users to config/CLI options rather than hanging for input.
* Keep prompt/UI mechanics behind an interface so tests can drive the builder without a terminal and a future TUI can reuse the builder model.

---

## Problem 17 — Establish OCIHood repository engineering baseline and CI

**Status:** Done

### Problem

Establish the repository quality baseline before feature development so every later PR is built and validated consistently.

### Task

* Use Go module `github.com/MaksimSurmach/OCIHood` and binary name `ocihood`.
* Pin and document the supported Go version.
* Establish the initial repository layout, including `cmd/ocihood` and `internal/`.
* Add baseline repository files: `.gitignore`, README skeleton and license.
* Use standard `log/slog` for application logging. Text is the default human-readable format; structured fields are preferred over formatted strings.
* Operational logs and diagnostics go to stderr. Command result output goes to stdout.
* Secrets, private keys, tokens and credential contents must never be logged.
* Add GitHub Actions CI for pull requests and the default branch.
* Keep build tooling intentionally small; do not introduce a large task/build framework without a concrete need.

---

## Problem 18 — Define desired-resource identity, idempotency and reconciliation model

**Status:** Done

### Problem

Define and implement the small domain contract that lets OCIHood identify one logical desired instance and reconcile local intent with OCI safely across retries, crashes and ambiguous API outcomes.

### Task

* Define a deterministic stable `TargetID`. Its exact normalized inputs must be documented and tested; transient process data, retry counters and generated request IDs must not affect it.
* Define deterministic OCI ownership markers/tags for managed instances, including a managed marker and stable target/account identity.
* Discovery/reconciliation must never claim an unrelated instance solely because shape, region, display name or other non-ownership properties happen to match.
* Define a logical launch `AttemptID` and OCI `opc-retry-token` lifecycle for an in-flight create attempt.
* Reuse the same OCI retry token when retrying the same ambiguous logical create while the token remains valid/applicable; reconcile OCI state before generating a new logical attempt after ambiguity/expiry/conflict.
* Define reconciliation decisions as typed outcomes such as: `create`, `already-satisfied`, `resume/reconcile`, `retry-same-attempt`, `new-attempt-safe`, `conflict/fail-safe`.
* Reconciliation must consider both durable local state and provider-observed managed instances rather than trusting either side alone.
* Multiple active managed matches for the same TargetID are a conflict and must never trigger another create automatically.
* A terminated/deleted managed instance must not by itself satisfy the desired running target.
* Document what identity/attempt fields belong in config, durable state and OCI tags.

---

## Problem 19 — Add provisioning plan and dry-run mode

**Status:** Done

### Problem

Expose the exact resolved provisioning intent before any OCI write so users and tests can validate what OCIHood would do safely.

### Task

* Add `ocihood plan --account <name>` using the same effective config, authentication, identity and discovery code paths as `start`.
* Build a typed Plan object that later provisioning can consume; do not maintain a separate planning-only interpretation of configuration.
* The plan must show at least: account/TargetID, region, target compartment, shape/OCPU/memory, image, VCN/subnet, boot-volume/public-IP settings, candidate ADs, relevant managed-instance observations and intended action.
* Intended action must distinguish at least `create`, `already-satisfied` and `blocked/conflict/ambiguous`.
* `plan` is read-only. It may perform read-only provider calls but must never invoke resource create/update/delete APIs.
* `plan` must not enter an indefinite capacity-wait loop. Capacity may be shown only as an instantaneous/read-only observation if later included.
* Secrets/private-key/token contents are never rendered.
* Optional `ocihood start --dry-run` may reuse the same Plan path; if implemented, it must be behaviorally equivalent to planning and perform zero writes.
* Provider/config/discovery errors must fail the command rather than rendering a misleading successful plan.

---

## Problem 20 — Add optional managed OCI network setup

**Status:** Backlog

### Problem

Provide an explicit opt-in managed OCI networking path for users who do not already have a suitable VCN/subnet, without mutating unrelated user networking.

### Task

* Existing-network discovery remains the default. Managed networking activates only through explicit configuration/CLI intent.
* Never create a VCN/subnet merely because normal discovery returned no usable network.
* Define the minimal managed public-network resource set required by the MVP: VCN, subnet, internet gateway, route to the internet gateway and the minimum required security policy/attachment model.
* CIDRs and relevant network settings must have documented deterministic defaults and be configurable/validated before writes.
* Apply OCIHood ownership identity/tags to every OCIHood-created network resource where OCI supports tagging.
* Reconcile/reuse previously created OCIHood-managed network resources by identity on restart; never duplicate a partially created network stack blindly.
* Detect partial/conflicting managed-network state and either complete the missing safe pieces or fail with an actionable conflict; never attach to similarly named unrelated resources.
* User-owned existing VCN/subnet/security resources must not be modified, retagged or deleted by managed-network reconciliation.
* Surface all intended network creates/reuses/conflicts in `ocihood plan` before mutation.
* Public ingress policy must be explicit. Do not silently open broad inbound access such as SSH from `0.0.0.0/0`; if inbound SSH is requested, require/document the configured source CIDR/rule.
* Managed networking must not open unrelated application ports by default.
* No automatic network cleanup/destruction is implemented in this task.

---

## Problem 21 — Package and release OCIHood binary and container

**Status:** Todo

### Problem

Package OCIHood as reproducible versioned binaries and a multi-arch container with release automation that verifies the produced artifacts rather than only building them.

### Task

* Add `ocihood version` that reports semantic version, commit SHA and build metadata from injected build-time values; development builds must be distinguishable from tagged releases.
* Produce release binaries for supported Linux amd64/arm64 targets. Add macOS amd64/arm64 only if those platforms remain supported by the runtime/features at release time.
* Use a minimal release tool/workflow such as GoReleaser; keep release automation separate from normal PR CI.
* Tagged semantic-version releases (`vX.Y.Z`) publish archives/binaries and checksums to GitHub Releases.
* Build/publish a multi-arch container image to GHCR matching the release version.
* Container must run as a normal foreground CLI/daemon process and accept configuration/OCI credential/SSH key/state mounts rather than baking user data into the image.
* Document image tags. A release version tag must be immutable; `latest` behavior, if used, must be explicit and must not point to prereleases unintentionally.
* Produced artifacts must not contain user credentials/configuration or repository-local secrets.
* Release workflow must fail if artifact smoke tests fail.
* Prefer reproducible/static-enough Go builds appropriate for the codebase; any CGO/platform requirement must be explicit rather than accidental.

---

## Problem 22 — Validate OCIHood against real OCI with opt-in smoke and E2E tests

**Status:** Todo

### Problem

Provide a controlled opt-in real-OCI validation layer before the first OCIHood release so mocked/unit tests cannot hide SDK, IAM, request-shape or lifecycle differences in the actual OCI service.

### Task

* Keep all real-OCI tests opt-in. They must never run automatically for ordinary pull requests or forks.
* Use dedicated test configuration/credentials and an explicitly isolated test compartment/target identity where mutating tests are enabled.
* Separate read-only smoke tests from mutating provisioning E2E tests.
* Read-only smoke coverage must validate at least real authentication, tenancy/region/AD discovery, image/network discovery and capacity-report behavior where the account/region supports it.
* Mutating E2E coverage must exercise one controlled `ocihood start` flow through real `LaunchInstance`, lifecycle reconciliation and final state using a uniquely identifiable disposable test TargetID.
* A mutating test must first verify its isolation/ownership markers and must refuse to modify/delete unrelated resources.
* Live tests must use explicit bounded timeouts/max-runtime and must not poll OCI aggressively.
* Record sanitized diagnostics sufficient to debug SDK/service failures without leaking credentials.
* Re-run/idempotency validation must prove a second execution for the same active test TargetID does not create a second active instance.
* Exercise at least one restart/reconciliation scenario against real provider state where practical.
* Cleanup, if provided, must target only resources proven to be created/owned by the test identity and must require explicit opt-in; cleanup failure must be reported rather than broadening deletion scope.
* Document required IAM permissions, expected possible cost/free-tier impact and exact commands/environment variables for running read-only versus mutating suites.

---

## Problem 23 — Add OCI SDK HTTP contract integration test harness

**Status:** Done

### Problem

Close the gap between interface-level fake tests and real OCI by exercising OCIHood through the real Oracle OCI Go SDK against a fully local fake OCI HTTP service.

This suite must verify request serialization, response/error handling and production provider wiring without requiring OCI credentials, network access or cloud resources.

### Task

* Provide a reusable local fake OCI HTTP server/test harness that can serve Identity, Compute and Virtual Network API responses required by OCIHood.
* Production OCI SDK clients must be pointed at the local server; do not bypass the SDK with fake provider interfaces for these tests.
* Exercise the real provider adapters used by `ocihood start`/`plan` through SDK request/response serialization.
* Support deterministic scripted responses, pagination and request capture.
* Verify OCI request paths, query parameters, JSON bodies and relevant headers such as `opc-retry-token`.
* Cover OCI service error classification for 401/403/404/409/429 and representative 5xx responses.
* Cover throttling/Retry-After behavior and malformed/unexpected responses.
* Requests must remain context-cancellable and respect configured request timeouts.
* The harness must never require or load real OCI credentials and must never make external network calls.
* Keep this layer separate from Problem 22 live OCI tests.

---

## Problem 24 — Validate crash-safe provisioning across every launch boundary

**Status:** Done

### Problem

Prove that OCIHood remains idempotent and never creates duplicate managed instances when the process crashes or loses responses at any critical provisioning boundary.

The invariant is: for one account/TargetID, restart/reconciliation must converge safely and the number of active OCIHood-owned instances must never exceed one.

### Task

* Add deterministic failure/crash injection points around reconciliation, state persistence, capacity handling, launch acceptance and lifecycle completion.
* Exercise the compiled/application production orchestration path rather than only pure reconciliation functions.
* Simulate ambiguous provider outcomes where LaunchInstance may have succeeded but OCIHood did not receive or persist the response.
* Restart from the exact durable state left by the interrupted run and reconcile against provider observations.
* Verify retry-token/AttemptID continuity where the same logical launch attempt must be retried.
* Verify a new attempt is created only where the reconciliation contract proves it is safe.
* Concurrent duplicate runners for the same account/TargetID must not produce competing launch writes.
* The suite must be deterministic and run without real sleeps.

---

## Problem 25 — Add provisioning safety and resource policy guardrails

**Status:** Done

### Problem

Prevent accidental provisioning of an unexpectedly large or chargeable OCI resource configuration while keeping OCIHood independent from volatile Oracle pricing/free-tier limits.

### Task

* Introduce an explicit local resource safety policy covering at least allowed shapes, maximum OCPUs, memory and boot-volume size.
* Keep policy limits configurable; do not hard-code current Oracle Free Tier limits as permanent product truth.
* `ocihood plan` must show whether the resolved target is within the configured safety policy and why.
* Foreground interactive execution may require explicit acknowledgement when policy is exceeded; unattended/daemon execution must require an explicit preconfigured opt-in and must never silently accept an override.
* OCIHood must never automatically increase shape, OCPUs, memory or boot volume while searching for capacity.
* Policy checks happen before any mutating OCI call.
* CLI/config precedence must remain deterministic and machine-readable output must expose the sanitized policy decision.
* Policy failures must not leak credentials or secret references.

---

## Problem 26 — Add `ocihood doctor` environment and OCI readiness diagnostics

**Status:** Backlog

### Problem

Give users one safe diagnostic command that validates local configuration and read-only OCI readiness before they start a long-running provisioning session.

### Task

* Add `ocihood doctor --account <name>` using the same effective config/auth/discovery components as production execution where applicable.
* Report individual checks with stable identifiers and status: pass, warning, fail, skipped.
* Validate at least config resolution, OCI config/profile, private-key reference readability, SSH public-key reference, OCI authentication, region/tenancy, compartment access, AD discovery, image resolution, VCN/subnet usability, state directory readability/writability and capacity-report support/permission.
* Never perform LaunchInstance or any other mutating OCI operation.
* Distinguish optional/degraded conditions such as unavailable Capacity Report from blocking failures.
* Support human-readable and JSON output using the same typed result model/format conventions from Problem 11.
* Return documented process exit categories suitable for scripts.
* Redact credentials, private-key contents, tokens and secret-bearing paths/values where appropriate.
* Keep each check independently testable and do not let one failed optional check hide subsequent safe diagnostics where continuing is meaningful.

---

## Problem 27 — Define durable state compatibility and migration policy

**Status:** Todo

### Problem

Make durable state upgrades predictable once users begin running released OCIHood versions for long periods.

### Task

* Define a documented compatibility policy for persisted state schema versions.
* Add explicit migration support when a future schema change requires transforming supported older state.
* Never treat an unsupported/newer/corrupt state file as empty state or permission to launch a new instance.
* Migration must preserve ownership identity, TargetID, AttemptID/retry-token semantics, lifecycle and scheduling/pause intent where present.
* Before an in-place migration, preserve a recoverable backup or use an atomic migration strategy that leaves the old valid state intact on failure.
* Make migration idempotent and safe under process interruption.
* Define downgrade behavior explicitly; fail safely when a binary cannot understand newer state.
* Keep secrets out of state and migration logs.
* Maintain golden fixtures for released state schema versions once public releases begin.

---

## Problem 28 — Harden OCIHood release supply chain and security checks

**Status:** Todo

### Problem

Add practical security and software-supply-chain controls appropriate for a public cloud automation tool and its published binaries/container images.

### Task

* Add `govulncheck` to an appropriate CI/security workflow and fail on actionable vulnerabilities affecting built code according to a documented policy.
* Enable CodeQL or an equivalent static security analysis workflow for Go.
* Add automated dependency update configuration with controlled grouping/cadence rather than uncontrolled churn.
* Generate an SBOM for release artifacts/container images.
* Produce verifiable release provenance/signatures using GitHub-native keyless signing/attestations or another minimal maintained approach.
* Keep workflow token permissions least-privilege and separate PR validation permissions from release publishing permissions.
* Pin or otherwise intentionally control third-party GitHub Actions used in security/release-sensitive workflows.
* Document vulnerability reporting and supported-version expectations in SECURITY.md.
* Security checks must not expose OCI credentials, release tokens or repository secrets in logs/artifacts.

---

## Problem 29 — Prepare OCIHood repository for public open-source launch

**Status:** Todo

### Problem

Turn the technically public repository into a clear, trustworthy and contributor-friendly open-source project ready for a first public release.

### Task

### README and product positioning

* Rewrite the top of README around the user problem, value proposition and a minimal quick start before internal architecture details.
* Show the main capabilities concisely: safe capacity waiting, AD rotation, retries/backoff, crash-safe reconciliation/idempotency, supported execution modes, notifications when available and binary/container installation.
* Include a realistic minimal usage example and links to deeper documentation.
* Move deep TargetID/state/reconciliation details into dedicated architecture documentation where appropriate.
* Add an explicit independent-project disclaimer: OCIHood is not affiliated with or endorsed by Oracle Corporation.

### Documentation

Add/organize at least:

* `docs/quick-start.md`;
* `docs/configuration.md`;
* `docs/architecture.md`;
* `docs/iam.md` with least-privilege-oriented OCI permissions for read-only and provisioning paths;
* `docs/troubleshooting.md`;
* `docs/development.md`;
* safe examples for minimal config, multi-account config, Docker and systemd when those features exist.

Documentation commands/config examples must be validated in CI where practical so examples do not silently rot.

### Community/repository files

Add at least:

* `CONTRIBUTING.md`;
* `SECURITY.md`;
* `CODE_OF_CONDUCT.md`;
* `SUPPORT.md` or equivalent support expectations;
* structured bug and feature issue templates;
* pull request template.

### GitHub presentation

* Set a useful repository description.
* Add relevant topics such as `oracle-cloud`, `oci`, `oracle-cloud-infrastructure`, `free-tier`, `golang`, `provisioning`, `automation`, `cli`, `devops`.
* Decide and document whether GitHub Discussions is enabled; if enabled, define what belongs there versus Issues.
* Add a simple original OCIHood visual identity/logo that does not use Oracle trademarks/logos or imply affiliation.
* Add appropriate CI/release/security badges only when they link to real maintained workflows.

---

## Problem 30 — Add ownership-guarded destroy lifecycle for managed resources

**Status:** Backlog

### Problem

Provide an explicit destructive lifecycle command for resources that OCIHood can prove it owns, primarily for controlled cleanup and future operational workflows.

### Task

* Add an explicit `ocihood destroy --account <name>` flow; never destroy as a side effect of normal start/reconciliation.
* Resolve the exact TargetID and require complete OCIHood ownership tags/identity before any destructive call.
* Never select resources for deletion by display name alone.
* Refuse on zero, ambiguous or conflicting owned matches unless the command semantics explicitly and safely resolve the case.
* Show a deterministic destruction plan before mutation.
* Require explicit confirmation for interactive use and explicit non-interactive opt-in for automation.
* Poll deletion/termination to a documented terminal state with bounded timeout/cancellation.
* Update/remove local state only after provider outcome is safely known; ambiguous deletion outcomes must remain reconcilable.
* Cleanup of managed networking, if ever supported, must be separately ownership-guarded and dependency-aware; do not delete user-owned networking.

---


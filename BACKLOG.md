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


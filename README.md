# Server Fan Control

A small Linux service for server cooling, providing a Svelte dashboard for NVIDIA GPUs, local IPMI fans and SMART HDD temperatures, with editable GPU and HDD fan curves. Go owns NVML, configuration and the HTTP API; the UI is embedded in the binary. CUDA and packaged NVIDIA drivers are absent from the runtime image.

This project began as a fork of [Max Zanoga’s NVIDIA Fan Control](https://github.com/ZanMax/nvidia-fan-control). The `server-fan-control` name reflects its IPMI system-fan and HDD-temperature scope. Local IPMI monitoring, a guarded ASRock actuator and an average-HDD-temperature curve are available. The upstream MIT license and attribution are retained.

New GPUs remain in firmware automatic mode. Saving a draft does not activate it. Applying a curve explicitly selects a GPU by UUID and requires confirmation of that hardware's minimum fan duty and critical temperature. Unsupported and passively cooled GPUs remain visible for monitoring.

The GPU MVP includes software tests and a mock mode. **Real fan writes, reset behavior, permissions and thermal response still require supervised validation on your target GPU/TrueNAS host.** Local IPMI is opt-in and remains monitoring-only until a board-specific verification record is saved. HDD control additionally requires verified disk identity, transport/standby behavior, thermal limits and shared-zone cooling.

## Try the dashboard without hardware

Requires Linux, Go 1.20 or later, a C compiler, Node 24 and npm. Container builds use pinned Go/Node versions from the Dockerfile.

```bash
make build
FAN_CONTROL_IPMI_ENABLED=true FAN_CONTROL_SMART_ENABLED=true \
  ./bin/server-fan-control --mock --dev-no-auth \
  --listen 127.0.0.1:8080 --config data/mock/config.json
```

Open **http://127.0.0.1:8080**. The mock badge is always visible; two simulated GPUs demonstrate dual-fan control and passive cooling. With both environment variables set to `true`, the system-fan and HDD panels also use simulated hardware. Mock mode never loads NVML or runs hardware commands. The authentication bypass is accepted only with `--mock` and a literal loopback listen IP. Keep mock and real data in separate directories.

The dashboard provides GPU selection, temperature/activity/VRAM/power cards, per-fan percentages, a short history graph, and a curve graph with draggable points and a keyboard-accessible numeric editor. Point dragging changes the local draft only. Saved and applied revisions are shown separately. Polling does not overwrite unsaved edits; concurrent saves return a revision conflict.

For authenticated local use, create a secret and omit `--dev-no-auth`:

```bash
mkdir -p secrets
(umask 077; openssl rand -hex 32 > secrets/admin-token)
./bin/server-fan-control --mock --config data/mock/config.json \
  --admin-token-file secrets/admin-token
```

Enter the contents of the secret file in the login form. The server exchanges it for a two-hour HttpOnly, SameSite session cookie and uses CSRF and Origin checks for mutations. Secrets are not part of configuration exports or the frontend bundle.

## Build and deploy the container

Build Linux amd64 without a GPU or NVIDIA driver:

```bash
docker build --platform linux/amd64 -t server-fan-control:local .
sh scripts/check-image.sh server-fan-control:local
python3 scripts/smoke-service.py server-fan-control:local
```

The multi-stage build uses Node for the Svelte bundle, an official Go/CGO builder, and Debian Trixie slim at runtime. The Go and Debian stages use matching Debian generations. All base images are pinned by digest; refresh pins and rerun checks together. The NVML dependency remains `v0.12.4-1`.

The runtime host needs its NVIDIA driver and the [NVIDIA Container Toolkit configured for Docker](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html). The toolkit supplies `libnvidia-ml.so.1` and device nodes. `NVIDIA_DRIVER_CAPABILITIES=utility` enables NVML; a separate GPU reservation selects the device. See [NVIDIA's runtime contract](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/docker-specialized.html) and [Compose GPU reservations](https://docs.docker.com/compose/how-tos/gpu-support/).

Prepare a persistent directory and secret, then select the UUID reported by the host:

```bash
mkdir -p data secrets
# Generate secrets/admin-token once using the command above; preserve existing secrets.
nvidia-smi --query-gpu=uuid,name,temperature.gpu --format=csv
export NVIDIA_GPU_UUID=GPU-REPLACE-WITH-YOUR-UUID
export FAN_CONTROL_CONFIG_DIR="$(pwd)/data"
export FAN_CONTROL_TOKEN_FILE="$(pwd)/secrets/admin-token"
docker compose config
docker compose up -d
```

The default image is `server-fan-control:local`; `FAN_CONTROL_IMAGE` can select an image you have published. Compose uses a root process, a read-only root filesystem, a writable **directory** mounted at `/data`, and a read-only secret file. The data directory must remain writable for atomic configuration updates and the recovery journal. No configuration or secrets are baked into the image. Logs go to stdout/stderr.

All startup options have environment-variable equivalents. Compose and the image defaults use these directly, so a TrueNAS Custom App needs no command arguments. Explicit CLI flags override their environment variables. Unset or empty values received by the process use native defaults; the image and Compose supply the container defaults shown below. Empty host or `.env` values also select Compose's container defaults. Invalid boolean settings fail service startup.

| Environment variable | CLI equivalent | Native default | Container/Compose default |
| --- | --- | --- | --- |
| `FAN_CONTROL_CONFIG` | `--config` | `data/config.json` | `/data/config.json` |
| `FAN_CONTROL_LISTEN` | `--listen` | `127.0.0.1:8080` | `0.0.0.0:8080` |
| `FAN_CONTROL_ADMIN_TOKEN_FILE` | `--admin-token-file` | Required unless using development auth bypass | `/run/secrets/admin-token` |
| `FAN_CONTROL_PUBLIC_ORIGIN` | `--public-origin` | Empty | Empty |
| `FAN_CONTROL_SECURE_COOKIES` | `--secure-cookies` | `false` | `false` |
| `FAN_CONTROL_MOCK` | `--mock` | `false` | `false` |
| `FAN_CONTROL_DEV_NO_AUTH` | `--dev-no-auth` | `false` | `false` |
| `FAN_CONTROL_DIAGNOSE` | `--diagnose` | `false` | `false` |
| `FAN_CONTROL_DIAGNOSE_IPMI` | `--diagnose-ipmi` | `false` | `false` |

`FAN_CONTROL_CONFIG` and `FAN_CONTROL_ADMIN_TOKEN_FILE` refer to paths **inside the container**. The existing `FAN_CONTROL_CONFIG_DIR` and `FAN_CONTROL_TOKEN_FILE` select the host mount sources; they are Compose settings, not application options. If you change a container path or listen port, adjust its mount or port mapping too. `FAN_CONTROL_DEV_NO_AUTH=true` still requires mock mode and a literal loopback listen IP. Diagnostic modes read once and exit, and cannot be combined with mock mode or each other. IPMI and SMART retain their existing environment variables described below.

HTTP is published only on `127.0.0.1:8080` by default. For remote access, use an HTTPS reverse proxy on the chosen management network and set these variables in the Compose `.env` file or the TrueNAS app's environment settings:

```dotenv
FAN_CONTROL_PUBLIC_ORIGIN=https://fans.example.net
FAN_CONTROL_SECURE_COOKIES=true
```

Use the actual browser origin, without a path. Forwarded headers are deliberately not trusted to determine it. Keep the backend loopback/private, configure the proxy to forward the browser's Origin header unchanged, and terminate TLS at the proxy. The application listens on all container interfaces; host publication is a separate setting.

Only one process should control each physical GPU. The data-directory lock prevents duplicate instances sharing a directory; it cannot detect an unrelated controller with another directory. Successful telemetry does not establish fan-write permission. The default configuration does not add `privileged`, `SYS_ADMIN`, host PID access or the Docker socket; record any demonstrated capability requirement for your driver rather than granting capabilities speculatively.

## Read-only GPU diagnostic

```bash
docker run --rm --read-only --gpus "device=${NVIDIA_GPU_UUID}" \
  -e NVIDIA_DRIVER_CAPABILITIES=utility \
  server-fan-control:local --diagnose
```

This command initializes NVML, reads UUID/name/temperature, shuts down NVML and exits without fan writes or configuration access. Compare its output with host `nvidia-smi`. The service stays available with a visible adapter error when real NVML is unavailable; it never substitutes mock telemetry. The diagnostic instead returns a nonzero exit status on a required read/initialization failure.

## Configuration, activation and recovery

The default native config path is `data/config.json`; `FAN_CONTROL_CONFIG` or `--config` accepts an explicit path. A missing configuration creates schema version 1 with a named example curve and **no managed GPUs**. Example values are editable drafts, not hardware-certified thresholds.

The JSON contains `schema_version`, `revision`, `sampling_seconds`, `freshness_seconds`, `failure_limit`, named `curves`, and UUID-keyed `gpus` policies. A policy selects `automatic` or `curve`, a curve name, fan indices (empty means all discovered fans), minimum duty, critical temperature, downward hysteresis and cooling hold. `limits_confirmed` records the owner's explicit confirmation of the hardware limits; it is not a hardware certification.

- **Save draft** stores settings while the GPU is automatic. For an active GPU, use Save/apply for intentional live changes or Return to automatic before saving a draft.
- **Save/apply** persists a validated policy, then asks the controller to reconcile it. A successful save is not proof that hardware accepted every command; check the applied revision and per-fan results.
- **Return to automatic** latches manual control off and attempts restoration before saving automatic intent. It works even when configuration storage is full/read-only. The response separates restoration and persistence errors; failed persistence means the saved curve can return after restart. Resolve storage and retry automatic before reactivating.

Each save requires the current revision. Updates use same-directory temporary files, file/directory sync, atomic rename, a last-good `.bak` and a `.pending` transaction record. Startup rolls back an interrupted configuration transaction. Invalid files are rejected rather than silently replaced. Keep backups and inspect disk errors before restarting after an uncertain persistence failure.

To import the original `config.json`, copy it into your persistent directory before the first start. Migration creates `config.json.legacy.bak`, preserves the original `min < temperature <= max` step mapping and sample interval, and exposes an **Imported legacy** draft. It does not automatically assign that curve to any GPU. Invalid/overlapping/gapped ranges are rejected. The intentional changes from the old loop are endpoint saturation, stateful cooling hysteresis, all-fan ownership, fallback and checked restoration.

Before any manual command, the controller persists each selected fan in `ownership.json` (`ownership.mock.json` in simulation). A failed journal write refuses takeover. Removed policies/fans remain owned until reset succeeds. Startup restores pending ownership before applying any curves, including fans no longer in desired configuration. If configuration cannot be parsed, or another service startup error occurs (such as a missing secret), journal-only recovery still attempts restoration under the instance lock and exits with an error. Do not delete the journal to suppress an error.

SIGTERM/SIGINT stop HTTP and restore managed fans before NVML shutdown. A partial fan-write failure or stale/failed temperature input latches automatic fallback and reports any failed reset; explicit automatic persistence and reactivation are required to resume. Hardware restoration is attempted even if journal/config writes fail. Graceful shutdown is bounded at 20 seconds; a hung native NVML call cannot be interrupted by a Go context.

SIGKILL, OOM kill, host failure and native crashes can prevent cleanup. Restart recovery is not a hardware fan lease and does not guarantee cooling during downtime. Validate the GPU's actual behavior under your own supervised procedure.

## Curve and telemetry rules

Point curves use strictly increasing temperatures and nondecreasing duties. Step points are inclusive lower thresholds; imported ranges keep their original inclusive upper boundaries. Linear curves interpolate between points and round duty upward. Both saturate below/above their endpoints; activated curves require a 100% upper endpoint. The user-confirmed minimum duty is enforced, and reported device bounds are checked when available.

Rising demand increases immediately. Step cooling must cross the active lower boundary minus hysteresis; legacy ranges include their imported hysteresis. Linear cooling becomes eligible when `F(temperature + hysteresis)` is lower than the current target. Cooling eligibility must hold continuously for the configured duration. Critical temperature commands 100% immediately and bypasses normal smoothing. Invalid/stale readings never become zero or lower a target; repeated failures or the freshness budget trigger fallback. Identical commands are coalesced; observed loss of manual policy triggers reconciliation where NVML supports it.

Metrics include nullable value, unit, collection time, last-success time and status/reason. Polling the API does not manufacture new samples. Reported NVML fan percentages are intended operating speeds, not independent proof of rotation, and can exceed 100%; requested duties remain 0–100. RPM is not exposed by this pinned adapter. Unsupported optional NVML symbols and sensor/permission errors remain visible. History is in-memory and bounded to roughly 30 minutes; it resets when the process restarts.

## Optional local IPMI and ASRock system fans

Enable local IPMI independently of the GPU loop:

```bash
docker compose -f compose.yaml -f compose.ipmi.yaml up -d
```

The override sets `FAN_CONTROL_IPMI_ENABLED=true` and maps only `/dev/ipmi0`. It inherits the base environment, including authentication and reverse-proxy settings. The host must already have a working OpenIPMI device; no host drivers are installed by this app. The runtime includes `ipmitool`, invokes it with `-I open`, and needs no BMC network credentials. Native users can install `ipmitool` and set `FAN_CONTROL_IPMI_ENABLED=true` before launching the service. Missing IPMI hardware is reported as unavailable; it never becomes simulated data. Setting that variable with `FAN_CONTROL_MOCK=true` simulates both adapters without accessing hardware.

For a **TrueNAS Custom App**, add these entries under **Environment Variables** as needed:

| Variable | Value to enable | Default |
| --- | --- | --- |
| `FAN_CONTROL_IPMI_ENABLED` | `true` | `false` |
| `FAN_CONTROL_SMART_ENABLED` | `true` | `false` |

Unset or empty values disable the integration; invalid boolean values fail startup. SMART requires IPMI to be enabled. These variables are read at process startup, so save the app settings and recreate/restart the container to apply changes. Device access is also required: expose `/dev/ipmi0` and the selected HDDs individually, or use the TrueNAS-style host-device setup below. Enabling either integration starts monitoring; fan takeover still requires the saved hardware verification and an explicit apply. The base Compose file defaults both variables to `false`; its optional overrides enable the corresponding integrations.

Start with the read-only diagnostic, which needs neither a configuration nor an admin token:

```bash
docker run --rm --read-only --device /dev/ipmi0:/dev/ipmi0:rw \
  server-fan-control:local --diagnose-ipmi
# Or, on a native development installation:
./bin/server-fan-control --diagnose-ipmi
```

It reports built-in FRU board identity, BMC firmware, fan sensor RPM and collection time. On the recognized ASRock Rack E3C246D2I only, it also reads the eight OEM bytes. Unsupported boards expose monitoring without OEM fan commands. The adapter runs fixed arguments with a three-second timeout per subprocess, bounded stdout/stderr, and an overall operation deadline. There is no arbitrary command or raw-IPMI API. Fan polling runs every 15 seconds on an independent worker; it cannot hold up GPU thermal decisions.

The first profile is `asrock-e3c246d2i`, based on the owner's ASRock support instructions:

```text
READ: ipmitool raw 0x3a 0x02
SET:  ipmitool raw 0x3a 0x01 00 00 35 00 00 00 00 00
```

**Bare `35` is decimal 35 (hexadecimal `0x23`), while `0x35` is decimal 53.** This follows ipmitool's [base-0 numeric parsing](https://github.com/ipmitool/ipmitool/blob/master/lib/helper.c). The app accepts percentage numbers and emits explicitly encoded hexadecimal bytes. It reads all eight bytes, replaces the selected byte, writes the complete vector and checks every byte on readback, preserving the other seven settings. All eight bytes must be 0–100; unknown readback encodings are rejected before any write. Another writer could still race a read/write; only one controller may own the BMC fans.

The [board manual](https://download.asrock.com/Manual/E3C246D2I.pdf) lists FAN1 (CPU), FAN2 (front) and FAN3 (rear). Eight protocol bytes do **not** prove eight physical headers. This profile offers FAN1–FAN3, provisionally mapped to payload slots 1–3; the supplied example uses slot 3. The sensor name for each header is configurable, defaulting to `FAN1`, `FAN2`, `FAN3`. Verify the physical header/slot/sensor correspondence on your host. Static analysis of the supplied **01.64.00 firmware image confirms zero as automatic, 1–100 as manual percentage, and the eight-byte request/readback format**. The third byte drives PWM2, consistent with the vendor FAN3 example. FAN2's physical PWM association remains unresolved; its RPM input is tach4, which alone does not establish its PWM slot. Verify the installed firmware and physical mapping; no real hardware actuation has been validated in this workspace. See [firmware analysis and addresses](CODEX_HANDOVER.md#firmware-analysis-update--bmc-016400-31-october-2026) and [machine-readable evidence](docs/firmware/e3c246d2i-1.64-evidence.json).

The System fans panel starts in monitoring mode. Before enabling manual targets, save a verification record containing the exact discovered board/manufacturer/firmware, the selected profile, a tested minimum duty and explicit confirmations that header mapping, automatic restoration and shared-component cooling have been validated. In particular, establish a known-good restoration procedure before any application takeover. Do not mark these confirmations based only on an API readback. A firmware or board change invalidates writes against the record. The safe minimum applies to every selected header; use a floor safe for all headers you intend to control. Hardware verification settings cannot change while fans are owned: restore automatic first.

After verification, select a header and a duty between the verified floor and 100%, then apply a manual target. The UI shows mode/target readback separately from RPM. On firmware 1.64, automatic slots read back zero even while the fan is spinning; manual slots report the stored target, which is not a hardware PWM measurement. RPM confirms a sensor reading, not a particular duty percentage or safe thermal response. A selected sensor must report healthy positive RPM; missing/failed sensors, lost duty readback or changed identity cause latched fallback and attempted automatic restoration. Manual targets are for supervised operation. HDD automation uses the separate policy below, including an explicitly verified floor for shared CPU/HBA/board cooling.

**Return owned fans to automatic** writes zero only to headers taken over by this application, verifies readback, and reports hardware and journal persistence outcomes separately. Manual targets are not saved for automatic reapplication. The service journals verified identity and ownership before takeover, restores on failures and graceful shutdown, and recovers outstanding ownership at startup. Disabling `FAN_CONTROL_IPMI_ENABLED` still attempts recovery if an IPMI ownership journal exists; disable SMART too, and retain device access until restoration succeeds. SIGKILL, OOM, host failure and BMC failure can prevent restoration; restart recovery is not a hardware lease.

IPMI verification settings live in `ipmi.json` beside the main GPU configuration (`ipmi.mock.json` in mock mode). They have their own schema/revision, last-good backup and pending-transaction rollback under the same data-directory lock. Their ownership journal uses `.ownership.json` (or `.ownership.mock.json`) appended to that path. Keep these files with the persistent directory and never remove a pending journal to hide an error. Keeping separate settings and workers allows IPMI operations without waiting on GPU hardware calls.

| Route | Purpose |
| --- | --- |
| `GET /api/v1/ipmi` | Enabled/mock state, identity, fan sensors, raw duty bytes, data age and ownership |
| `GET /api/v1/ipmi/config`, `PUT /api/v1/ipmi/config` | Verification settings with their own quoted revision/If-Match |
| `POST /api/v1/ipmi/target` | Apply `{ "fan": 3, "duty": 35 }` after verification |
| `POST /api/v1/ipmi/default` | Restore all owned headers; no revision precondition |

These endpoints use the existing session, Origin and CSRF protection. Saving hardware verification or an automatic HDD draft does not issue manual commands. Explicitly saving an HDD policy in `curve` mode arms its controller. A successful HTTP response to a target/default request still requires checking `applied`/`restored`, `persisted`, `hardware_error` and `storage_error`. A timeout can leave an operation finishing in the worker; refresh status before retrying.

Target-host acceptance remains outstanding: record TrueNAS/BMC versions and read-only output; confirm the installed firmware matches the analyzed protocol and verify physical FAN3 mapping; verify the safe floor under load, automatic restoration, SIGTERM and restart recovery. Do not infer unattended thermal safety from the software tests. HDD sensing and curve acceptance are described below.

## HDD temperatures and case-fan curve

Set `FAN_CONTROL_SMART_ENABLED=true` alongside `FAN_CONTROL_IPMI_ENABLED=true` to collect the explicitly configured HDDs. Both default to `false`; setting SMART alone fails startup. The runtime includes `smartctl` from smartmontools; native installations need that package too. No smartd daemon is started. The SMART worker is independent of both GPU and IPMI work. The default interval is 60 seconds after a scan completes, with two concurrent reads, a 10-second per-command timeout and 128 KiB output limits. Up to 32 selected disks are supported; a worst-case scan can take about 160 seconds before the next interval. Choose freshness and grace budgets for the measured scan duration; old samples can expire during a slow scan and trigger fallback. The dashboard/API reads cached snapshots, so refreshing it does not wake disks or manufacture new samples.

The initial provider accepts verified **ATA and SAT** paths only. It invokes fixed arguments `smartctl -j -i -A -n standby,3,5 -d TYPE DEVICE`, reads normalized `temperature.current`, and requires a rotating disk and the configured serial on each successful read. It never scans/autodetects devices, runs self-tests, changes SMART settings or standby timers, or writes firmware. SCSI/SAS, RAID selectors and NVMe are not supported in this increment. Fixture tests are synthetic; real drive/controller captures and standby behavior still need target-host validation. See the [packaged smartctl manual](https://manpages.debian.org/trixie/smartmontools/smartctl.8.en.html) for exit bitmasks and transport-specific power checks.

Configure the HDD group in the dashboard using an explicit ID, expected serial, container device path and `ata` or `sat` transport. A new or replacement disk never joins automatically. Choose one of these container access setups; both enable IPMI and SMART and preserve the base environment, authentication, GPU reservation and persistent storage.

**TrueNAS-style setup, without per-disk mappings.** [compose.smart.truenas.yaml](compose.smart.truenas.yaml) follows the [TrueNAS Scrutiny app template](https://github.com/truenas/apps/blob/master/ix-dev/community/scrutiny/templates/docker-compose.yaml): run as root in privileged mode, bind-mount host `/dev` and `/run/udev` read-only. Use it directly with the base file:

```bash
docker compose -f compose.yaml -f compose.smart.truenas.yaml config
docker compose -f compose.yaml -f compose.smart.truenas.yaml up -d
```

No `FAN_CONTROL_HDD_DEVICE` variable or separate IPMI override is needed. In the dashboard, use the host path (prefer `/dev/disk/by-id/ata-...`), expected serial and transport for each selected HDD. Host `/dev/ipmi0` is available through the same mount and must already work on the host. When moving from the per-disk setup, update `/dev/hdd-selected` to the corresponding host path in the saved HDD settings. For a TrueNAS Custom App configured through the UI, set user/group to `0:0`, enable **Privileged**, add read-only host-path mounts `/dev` → `/dev` and `/run/udev` → `/run/udev`, and set both integration environment variables to `true`.

Privileged mode gives this container access to all host devices, including GPUs beyond the reserved UUID; a read-only `/dev` mount does not prevent device IOCTLs. The application still reads only the configured HDDs and verifies their serials. Device visibility does not activate a fan curve.

**Selected-device setup.** [compose.smart.yaml](compose.smart.yaml) follows [Scrutiny's Docker setup](https://github.com/AnalogJ/scrutiny#docker): `SYS_RAWIO`, read-only `/run/udev` metadata and explicit disk mappings. Combine it with the IPMI override:

```bash
# Use a host device path or /dev/disk/by-id path after checking the disk's serial.
export FAN_CONTROL_HDD_DEVICE=/dev/REPLACE_WITH_VERIFIED_DEVICE
docker compose -f compose.yaml -f compose.ipmi.yaml -f compose.smart.yaml config
docker compose -f compose.yaml -f compose.ipmi.yaml -f compose.smart.yaml up -d
```

The chosen device is exposed as `/dev/hdd-selected`; enter that path and its verified serial in the dashboard. Add other drives to `devices`, for example `/dev/sdb:/dev/sdb:rw`. Docker resolves a by-id path used as a device mapping source; mounting only the `/dev/disk/by-id` directory does not expose its device targets. This setup supplies `SYS_RAWIO` for SMART queries without privileged mode. Scrutiny also documents `SYS_ADMIN` for NVMe, but this collector supports ATA/SAT HDDs only, so that capability is unnecessary here. `/run/udev` supplies host device metadata; the current collector reads explicitly configured paths rather than using Scrutiny's discovery.

With either setup, confirm on the target host that SMART identity, temperature and standby checks work and that repeated reads preserve the intended sleep behavior.

Save the disk selection and curve as a draft first. The dashboard shows per-drive status, completed/last-valid times, SMART health reasons, mean, maximum, coverage, target and override reason. The API also exposes attempt timestamps and numeric health flags. Last-known values remain labeled with their original timestamps after errors or standby; they are not fresh readings. Collection completion timestamps do not establish the disk firmware sensor's internal sample time. Health exit bits are reported separately from transport failures.

After validating the IPMI profile, choose a case-fan header and enter the HDD critical threshold, freshness/grace budgets, downward hysteresis/hold and the shared-zone minimum duty. These values require explicit hardware acceptance. The shared-zone floor must cool every affected CPU, HBA, NIC and board component across the intended workload even when the disks are cool. This implementation uses that conservative floor; it does not read CPU/HBA thermal guards. If such a floor cannot be validated, leave automation disabled.

**Save and apply** arms the selected header using the arithmetic mean of fresh selected HDD readings. The target is the maximum of the curve demand, the IPMI profile floor and the verified shared-zone floor. Step and linear curves reuse the GPU controller's endpoint saturation and asymmetric cooling logic: rising demand acts on the next IPMI evaluation, while reductions require hysteresis and a cooling hold. The hottest selected HDD reaching the configured critical threshold bypasses smoothing and triggers the verified automatic fallback. The upper curve endpoint must request 100% at or below that threshold.

Missing, failed, replaced or expired sensors never become zero. Incomplete active-disk coverage prevents reductions during a bounded grace period, then triggers automatic fallback. Sleeping disks are excluded from the mean; a mixed sleeping/awake group cannot justify reducing the last target. An initially mixed sleeping/awake group starts at 100% because there is no prior target to preserve. All selected disks asleep requests verified firmware automatic operation, not an average of zero drives. If the controller has no prior target and coverage is incomplete, it stays automatic. Fallback and write/readback/rotation errors latch until the owner explicitly restores automatic and reapplies after resolving the problem. An unreachable BMC is reported as unconfirmed restoration, not successful cooling.

Changing an active HDD policy first restores the owned header, then saves and applies the replacement; a failed restoration prevents the change. Hardware verification changes still require returning to automatic first. Manual duty commands are rejected while the HDD curve is armed. **Restore system fans to automatic** disarms HDD control before attempting configuration persistence and hardware restoration, so storage failure cannot leave a curve running in that process. Settings and curve drafts persist in `ipmi.json` with revision checks. On every restart, ownership recovery runs first and the curve requires explicit reapplication; a saved `curve` mode alone does not resume control. SIGTERM restores owned fans. Abrupt process/host failure has no guaranteed hardware fallback or lease.

`GET /api/v1/disks` is authenticated and read-only. `GET /api/v1/ipmi` includes the HDD policy state and aggregates. HDD settings use the existing revision-checked `/api/v1/ipmi/config`, and automatic restoration uses `/api/v1/ipmi/default` without an ETag requirement. SMART collection is disabled unless explicitly opted in; enabling the collector does not activate a fan curve.

Before unattended use, validate real ATA/SAT JSON, identity/replacement handling, permissions, no unintended wakeups, selected header mapping, loaded-system temperatures, full scan and control response with a stalled selected drive, unaffected GPU sampling, hottest/missing sensor scenarios, restoration and restart behavior on the actual TrueNAS/BMC versions. Passing mock tests establishes software behavior only.

## API and development

The production UI and API share one origin. During frontend development, `npm --prefix web run dev` proxies `/api` to `127.0.0.1:8080`; start the Go service separately and use `--public-origin http://127.0.0.1:5173` when opening the Vite origin.

| Route | Purpose |
| --- | --- |
| `GET /healthz` | Process liveness; no hardware probe or authentication |
| `POST /api/v1/session` | Exchange `{ "token": "…" }` for session and CSRF token |
| `GET /api/v1/session`, `DELETE /api/v1/session` | Session state / logout |
| `GET /api/v1/status` | Mock/adapter state and desired/applied revisions |
| `GET /api/v1/gpus` | UUID identities, telemetry and per-fan outcomes |
| `GET /api/v1/config`, `PUT /api/v1/config` | Configuration and revision-checked save |
| `POST /api/v1/gpus/{uuid}/default` | Restore automatic; return hardware/storage outcomes |
| `GET /api/v1/history?uuid=…` | Bounded history for one GPU |

All `/api` routes except login require a session. Mutations require a matching Origin and `X-CSRF-Token`. PUT config requires quoted `If-Match` and the same body revision: missing preconditions return 428, conflicts 412, invalid policies 400. Default restoration does not require an ETag and returns its separate outcomes even when persistence fails. HTTP mutations wait up to 10 seconds. A 503 timeout can mean an already-started hardware operation is still running; refresh status before retrying. IPMI uses the separate worker and endpoints described above. `GET /api/v1/disks` provides the independent SMART snapshot without initiating a read.

```bash
make test                 # frontend check/build, Go race tests and vet
make e2e                  # build, then browser tests against real Go mock mode
```

Install Playwright Chromium first with `cd web && npx playwright install chromium`, or set `PLAYWRIGHT_CHROME_PATH` to a compatible installed Chrome executable. Tests cover login, editing, apply/reset, conflict handling, persistence failures and responsive layout. `make build` always produces frontend assets before compiling the `go:embed` package. Generated assets, node_modules and local binaries are ignored by Git.

## Image publishing

[Docker Image CI and Publish](.github/workflows/docker-image.yml) runs the Go/container and browser checks on pull requests and pushes to `main`. Pull requests never publish. After both jobs pass on `main`, the publish job loads the exact tested image artifact and publishes Linux amd64 images to `ghcr.io/dochevviktor/server-fan-control:<version>` and `:latest` using `GITHUB_TOKEN`. The version comes from `web/package.json` (currently `0.1.1`); `latest` is updated only if the tested commit is still the head of `main`, so an older rerun cannot roll it back.

The workflow creates a matching annotated Git tag, such as `0.1.1`. An existing tag must point to the same commit; otherwise publication fails before pushing images. Bump the version for each new release, updating both package files with `npm --prefix web version patch --no-git-tag-version` (or `minor`/`major`) and commit them. Rerunning the same tagged commit is allowed. Image artifacts are retained for seven days; after expiry, rerun all jobs to rebuild and validate the image before publishing. Versions must fit Docker's tag syntax; SemVer `+build` metadata is rejected.

Only the publish job has `contents: write` and `packages: write`; the other jobs keep read-only repository permissions. Repository/package policies must allow the workflow token to create tags and publish the package. No personal access token is required for normal repository-owned GHCR publishing. Make the GHCR package public if the NAS should pull anonymously, or configure registry credentials for a private package. The workflow does not deploy the application.

## TrueNAS and target-host acceptance

Use Docker-based TrueNAS Apps (24.10 or later). **No TrueNAS release or real GPU control/reset behavior has been validated in this workspace.** Record the exact host release, GPU model/UUID, driver, tested duty floor and critical temperature when accepting a deployment.

1. Enable NVIDIA support through **Apps > Installed > Configuration > Settings > Install NVIDIA Drivers**, following [TrueNAS initial setup](https://apps.truenas.com/getting-started/initial-setup/). Do not modify the appliance OS from the container.
2. Build/publish an amd64 image in development/CI and use a tag/digest actually available to the NAS. Pushes to `main` publish to GHCR after CI passes; deployment to the NAS remains manual.
3. Create a persistent dataset/directory and a separate admin-secret file with appropriate ownership. Mount the entire configuration directory writable.
4. Use **Apps > Discover Apps > Install via YAML**, following [Installing Custom Apps](https://apps.truenas.com/managing-apps/installing-custom-apps/). Paste Compose and replace image, GPU UUID, data and secret interpolation expressions with actual values. Configure the management-network HTTPS proxy/public origin explicitly; loopback host publication alone does not provide remote browser access.
5. First run the read-only diagnostic and compare telemetry. Under supervision, verify each selected physical fan accepts a conservative target, Return to automatic resets it, and SIGTERM/restart restores ownership correctly. Confirm no other software owns the same fans. Validate thermal response and failure scenarios before unattended operation.

## Image measurements

Phase 4 measured **108,038,946 bytes (103.03 MiB) unpacked** on 31 October 2026 with rootless Podman, Linux amd64 (`server-fan-control:hdd`). Frontend type checking/build, all 17 browser tests, Go race tests/vet both locally and in the pinned Go builder, Compose validation and runtime-content checks passed. Packaged mock tests cover SMART collection, mean/max calculation, selected-zone target/readback, SIGTERM restoration, recreation without automatic curve reactivation, and explicit return to automatic. SMART protocol fixtures are synthetic; no real HDD/BMC/GPU or TrueNAS thermal acceptance was performed.

Phase 3 measured **105,241,879 bytes (100.37 MiB) unpacked** on 31 October 2026 with rootless Podman, Linux amd64. Go race tests/vet, frontend type checking/build, all 13 browser tests, Compose validation, image-content checks and packaged mock GPU/IPMI lifecycle tests passed. Four IPMI browser tests were rerun after final UI refinements. The packaged tests exercise FAN3 target/readback, automatic reset, SIGTERM ownership cleanup and recreation without resuming a manual IPMI target. The image remains below the 150 MiB budget. No real GPU/BMC actuation or TrueNAS host acceptance was performed for this phase.

The GPU MVP measured **89,370,325 bytes (85.23 MiB) unpacked** on 30 October 2026 with rootless Podman 5.8.4, Linux amd64. Frontend type checking/build, nine Playwright tests, Go race tests/vet (including in the pinned Go builder), image-content/size checks, and container mock lifecycle tests passed. The container tests exercised authentication, both-fan apply, SIGTERM restoration, recreation and persisted automatic mode. UI failure-presentation fixtures supplement mocked-controller failure tests; none of these are real GPU actuation results. Docker Engine CI and target-host hardware/TrueNAS acceptance remain unrun here.

Phase 1 measured the unchanged CUDA image at **2,423,705,886 bytes (2311.43 MiB)** and the diagnostic-only Debian image at **84,928,386 bytes (80.99 MiB)** using rootless Podman 5.8.4 on Linux amd64. These are unpacked `.Size` measurements, not compressed transfer sizes. The GPU MVP adds the embedded UI and HTTP service; measure it with:

```bash
docker image inspect server-fan-control:local --format '{{.Os}}/{{.Architecture}} {{.Size}} bytes'
sh scripts/check-image.sh server-fan-control:local
```

The script enforces the 150 MiB unpacked budget, excludes runtime compilers/CUDA/packaged drivers, checks direct linked libraries, and checks diagnostic failure without NVML. `ldd` alone cannot verify dynamically loaded NVML. Set `CONTAINER_ENGINE=podman` to use Podman.

## License

Server Fan Control is licensed under the [MIT License](LICENSE).

- Copyright (c) 2025 Max Zanoga — original upstream work.
- Copyright (c) 2026 Viktor Dochev — additions and modifications.

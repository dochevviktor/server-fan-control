# NVIDIA Fan Control

A small Linux service with a Svelte dashboard for NVIDIA GPU telemetry and editable fan curves. Go owns NVML, configuration and the HTTP API; the UI is embedded in the binary. CUDA and packaged NVIDIA drivers are absent from the runtime image.

New GPUs remain in firmware automatic mode. Saving a draft does not activate it. Applying a curve explicitly selects a GPU by UUID and requires confirmation of that hardware's minimum fan duty and critical temperature. Unsupported and passively cooled GPUs remain visible for monitoring.

The GPU MVP includes software tests and a mock mode. **Real fan writes, reset behavior, permissions and thermal response still require supervised validation on your target GPU/TrueNAS host.** IPMI and disk-temperature control are later phases.

## Try the dashboard without hardware

Requires Linux, Go 1.20 or later, a C compiler, Node 24 and npm. Container builds use pinned Go/Node versions from the Dockerfile.

```bash
make build
./bin/nvidia-fan-control --mock --dev-no-auth \
  --listen 127.0.0.1:8080 --config data/mock/config.json
```

Open **http://127.0.0.1:8080**. The mock badge is always visible; two simulated GPUs demonstrate dual-fan control and passive cooling. Mock mode never loads NVML. The authentication bypass is accepted only with `--mock` and a literal loopback listen IP. Keep mock and real data in separate directories.

The dashboard provides GPU selection, temperature/activity/VRAM/power cards, per-fan percentages, a short history graph, and a curve graph with draggable points and a keyboard-accessible numeric editor. Point dragging changes the local draft only. Saved and applied revisions are shown separately. Polling does not overwrite unsaved edits; concurrent saves return a revision conflict.

For authenticated local use, create a secret and omit `--dev-no-auth`:

```bash
mkdir -p secrets
(umask 077; openssl rand -hex 32 > secrets/admin-token)
./bin/nvidia-fan-control --mock --config data/mock/config.json \
  --admin-token-file secrets/admin-token
```

Enter the contents of the secret file in the login form. The server exchanges it for a two-hour HttpOnly, SameSite session cookie and uses CSRF and Origin checks for mutations. Secrets are not part of configuration exports or the frontend bundle.

## Build and deploy the container

Build Linux amd64 without a GPU or NVIDIA driver:

```bash
docker build --platform linux/amd64 -t nvidia-fan-control:local .
sh scripts/check-image.sh nvidia-fan-control:local
python3 scripts/smoke-service.py nvidia-fan-control:local
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

The default image is `nvidia-fan-control:local`; `FAN_CONTROL_IMAGE` can select an image you have published. Compose uses a root process, a read-only root filesystem, a writable **directory** mounted at `/data`, and a read-only secret file. The data directory must remain writable for atomic configuration updates and the recovery journal. No configuration or secrets are baked into the image. Logs go to stdout/stderr.

HTTP is published only on `127.0.0.1:8080` by default. For remote access, use an HTTPS reverse proxy on the chosen management network and add these command arguments in Compose:

```yaml
- --public-origin
- https://fans.example.net
- --secure-cookies
```

Use the actual browser origin, without a path. Forwarded headers are deliberately not trusted to determine it. Keep the backend loopback/private, configure the proxy to forward the browser's Origin header unchanged, and terminate TLS at the proxy. The application listens on all container interfaces; host publication is a separate setting.

Only one process should control each physical GPU. The data-directory lock prevents duplicate instances sharing a directory; it cannot detect an unrelated controller with another directory. Successful telemetry does not establish fan-write permission. The default configuration does not add `privileged`, `SYS_ADMIN`, host PID access or the Docker socket; record any demonstrated capability requirement for your driver rather than granting capabilities speculatively.

## Read-only GPU diagnostic

```bash
docker run --rm --read-only --gpus "device=${NVIDIA_GPU_UUID}" \
  -e NVIDIA_DRIVER_CAPABILITIES=utility \
  nvidia-fan-control:local --diagnose
```

This command initializes NVML, reads UUID/name/temperature, shuts down NVML and exits without fan writes or configuration access. Compare its output with host `nvidia-smi`. The service stays available with a visible adapter error when real NVML is unavailable; it never substitutes mock telemetry. The diagnostic instead returns a nonzero exit status on a required read/initialization failure.

## Configuration, activation and recovery

The default native config path is `data/config.json`; `--config` accepts an explicit path. A missing configuration creates schema version 1 with a named example curve and **no managed GPUs**. Example values are editable drafts, not hardware-certified thresholds.

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

All `/api` routes except login require a session. Mutations require a matching Origin and `X-CSRF-Token`. PUT config requires quoted `If-Match` and the same body revision: missing preconditions return 428, conflicts 412, invalid policies 400. Default restoration does not require an ETag and returns its separate outcomes even when persistence fails. HTTP mutations wait up to 10 seconds. A 503 timeout can mean an already-started hardware operation is still running; refresh status before retrying. IPMI and disk endpoints are not implemented in this phase.

```bash
make test                 # frontend check/build, Go race tests and vet
make e2e                  # build, then browser tests against real Go mock mode
```

Install Playwright Chromium first with `cd web && npx playwright install chromium`, or set `PLAYWRIGHT_CHROME_PATH` to a compatible installed Chrome executable. Tests cover login, editing, apply/reset, conflict handling, persistence failures and responsive layout. `make build` always produces frontend assets before compiling the `go:embed` package. Generated assets, node_modules and local binaries are ignored by Git.

## TrueNAS and target-host acceptance

Use Docker-based TrueNAS Apps (24.10 or later). **No TrueNAS release or real GPU control/reset behavior has been validated in this workspace.** Record the exact host release, GPU model/UUID, driver, tested duty floor and critical temperature when accepting a deployment.

1. Enable NVIDIA support through **Apps > Installed > Configuration > Settings > Install NVIDIA Drivers**, following [TrueNAS initial setup](https://apps.truenas.com/getting-started/initial-setup/). Do not modify the appliance OS from the container.
2. Build/publish an amd64 image in development/CI and use a tag/digest actually available to the NAS. This repository's CI builds and tests; it does not automatically publish or deploy.
3. Create a persistent dataset/directory and a separate admin-secret file with appropriate ownership. Mount the entire configuration directory writable.
4. Use **Apps > Discover Apps > Install via YAML**, following [Installing Custom Apps](https://apps.truenas.com/managing-apps/installing-custom-apps/). Paste Compose and replace image, GPU UUID, data and secret interpolation expressions with actual values. Configure the management-network HTTPS proxy/public origin explicitly; loopback host publication alone does not provide remote browser access.
5. First run the read-only diagnostic and compare telemetry. Under supervision, verify each selected physical fan accepts a conservative target, Return to automatic resets it, and SIGTERM/restart restores ownership correctly. Confirm no other software owns the same fans. Validate thermal response and failure scenarios before unattended operation.

## Image measurements

The GPU MVP measured **89,370,325 bytes (85.23 MiB) unpacked** on 30 October 2026 with rootless Podman 5.8.4, Linux amd64. Frontend type checking/build, nine Playwright tests, Go race tests/vet (including in the pinned Go builder), image-content/size checks, and container mock lifecycle tests passed. The container tests exercised authentication, both-fan apply, SIGTERM restoration, recreation and persisted automatic mode. UI failure-presentation fixtures supplement mocked-controller failure tests; none of these are real GPU actuation results. Docker Engine CI and target-host hardware/TrueNAS acceptance remain unrun here.

Phase 1 measured the unchanged CUDA image at **2,423,705,886 bytes (2311.43 MiB)** and the diagnostic-only Debian image at **84,928,386 bytes (80.99 MiB)** using rootless Podman 5.8.4 on Linux amd64. These are unpacked `.Size` measurements, not compressed transfer sizes. The GPU MVP adds the embedded UI and HTTP service; measure it with:

```bash
docker image inspect nvidia-fan-control:local --format '{{.Os}}/{{.Architecture}} {{.Size}} bytes'
sh scripts/check-image.sh nvidia-fan-control:local
```

The script enforces the 150 MiB unpacked budget, excludes runtime compilers/CUDA/packaged drivers, checks direct linked libraries, and checks diagnostic failure without NVML. `ldd` alone cannot verify dynamically loaded NVML. Set `CONTAINER_ENGINE=podman` to use Podman.

## License

[MIT](LICENSE), copyright 2025 Max Zanoga.

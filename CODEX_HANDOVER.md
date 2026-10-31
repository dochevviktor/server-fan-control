# Codex handover for Server Fan Control on TrueNAS

Prepared 29 October 2026. Target repository: https://github.com/dochevviktor/server-fan-control

Naming updated 31 October 2026: this fork of [ZanMax/nvidia-fan-control](https://github.com/ZanMax/nvidia-fan-control) is now **Server Fan Control**, reflecting the planned IPMI system-fan and HDD-temperature scope. The reviewed snapshots and source permalinks below retain their historical repository names. This handover describes the original plan and baseline; consult the README for current implementation status.

## Phase-4 implementation update — 31 October 2026

The hardware-independent phase-4 implementation adds an opt-in ATA/SAT smartctl JSON collector, explicit serial-verified HDD selection, nullable/timestamped readings, and an adjustable arithmetic-mean curve for one verified case-fan header. The policy includes a hottest-drive critical guard, complete active-drive coverage with grace/fallback, standby handling, hysteresis/cooling hold and an explicitly validated shared-component cooling floor. It reuses IPMI ownership journaling/restoration and requires explicit reapplication after restart. The dashboard exposes disk selection, curve editing, coverage and override state. See the README's HDD section for the implementation and deployment contract. Real SMART fixtures, disk/controller standby behavior and target-host thermal acceptance remain outstanding; SAS/controller-specific transports are intentionally unsupported. No real disk command, fan actuation or NAS deployment is performed by the implementation work.

Integration opt-ins use startup environment variables `FAN_CONTROL_IPMI_ENABLED` and `FAN_CONTROL_SMART_ENABLED`, both defaulting to `false`; set them to `true` in the TrueNAS Custom App's Environment Variables settings. Unset/empty values are false and invalid booleans fail startup. SMART requires IPMI. Restart/recreate after changes and retain the required individual device mappings. Compose overrides set these variables without replacing the base command. Disabling IPMI still attempts pending ownership recovery; keep BMC access until recovery succeeds. The read-only `--diagnose-ipmi` command remains available.

Validation: frontend check/build, 17 browser tests, local and pinned-builder Go race tests/vet, Compose validation, container-content checks and packaged mock HDD/IPMI/GPU lifecycle tests passed. The phase-4 runtime image measured 108,038,946 bytes (103.03 MiB) unpacked with rootless Podman on Linux amd64. The image is local only; no image publication or host deployment was performed.

## Phase-3 implementation update — 31 October 2026

Optional local IPMI monitoring and the `asrock-e3c246d2i` actuator are implemented with an independent worker, authenticated dashboard/API, fixed bounded subprocesses, read-modify-write/readback, explicit host verification settings, and ownership recovery. See the README's “Optional local IPMI and ASRock system fans” section for the deployment and acceptance contract. The owner's support commands are `raw 0x3a 0x02` and `raw 0x3a 0x01 00 00 35 00 00 00 00 00`; bare `35` is decimal. The board has three physical fan headers, while the OEM vector has eight bytes. Static analysis of the supplied BMC 1.64 image now confirms eight-byte requests, zero-as-automatic and percentage scaling; see the firmware analysis below. Physical header mapping, safe minimum and actual host behavior still require supervised target-host verification. No real IPMI write or TrueNAS deployment has been performed in this workspace. HDD-temperature control was subsequently implemented in phase 4, as described above.

## Firmware analysis update — BMC 01.64.00, 31 October 2026

The owner supplied `/home/agentic/Downloads/E3C246D2I(01.64.00)BMC.zip`. It contains the 32 MiB image `E3C246D2I_1.64.00.ima`. Analysis used CramFS extraction, ARM ELF symbols/relocations, disassembly and SDR parsing. No firmware binary was executed, no image was flashed and no BMC command was sent. This establishes the protocol in the supplied image; it does not establish which firmware is currently installed on the NAS.

Reproducible provenance and addresses are recorded in [the firmware evidence JSON](docs/firmware/e3c246d2i-1.64-evidence.json). Archive SHA-256: `d9b9e1948e25b1745f6f8711ef0784c4db7709ce0a537ded1f8a243942e25d14`. Image SHA-256: `e574878294e74043aa02bf3cbdc458898df3a34d20a1c0dc35b4d63196e4e5bd`. The root filesystem starts at image offset `0x260000`; the web filesystem starts at `0x1660000`. Extracted files and annotated disassembly are retained locally in `/tmp/e3c246d2i-bmc-01.64-analysis`.

The following addresses are ELF virtual addresses, not flash-image offsets:

| Finding | Binary evidence |
| --- | --- |
| NetFn `0x3a`, command `0x01`, exactly eight request bytes | `libipmipdkcmds.so.2.9.0`: `AsRockRack_MsgHndlrTbl` at `0xb408`, relocation at `0xb414`, command table at `0x148b8`, `PDK_PreProcessCmd` at `0x730c`. |
| NetFn `0x3a`, command `0x02`, no request payload | Same dispatch table; entry points to `GetSmartFan` at `0x3a8c`. |
| Every SET byte must be 0–100 | `SetSmartFan` at `0x69b0` checks all eight bytes before updating state; a value above 100 returns completion `0xcc`. Wrong request length returns `0xc7` in dispatch. The table requires privilege level 4. |
| Zero selects automatic for that channel; 1–100 selects manual duty | `SetSmartFan` stores per-channel manual flags in `AutoFanControlData[1..8]`, and nonzero targets in `[9..16]`. Every request updates all eight flags. |
| GET returns eight mode/target bytes | `GetSmartFan` returns zero for automatic channels, otherwise their stored target. Its nine-byte internal response includes the completion byte, which is separate from the eight data bytes printed by `ipmitool raw`. This reads policy state, not PWM hardware or RPM. |
| Automatic demand resumes when the manual flag is cleared | `libipmipdk.so.2.22.0`: `ASRR_FanOperation` at `0x14bd4` writes computed duty only to channels with a zero manual flag. |
| Payload bytes 1–8 select PWM channels 0–7 | `ASRR_FanControlTask` at `0x14cc0` passes the corresponding stored duty and channel number to `g_HALFanPwmHandle[5]`. Third byte reaches PWM2. |
| Nonzero duty uses percentage units | The HAL entry resolves to `libpwmtach.so.2.5.0:set_pwm_dutycycle` at `0xe70`. Its ioctl `0x40045004` reaches `pwmtach.ko`, validates 1–100 and converts to `floor(percent × 255 / 100)`. A nominal 35% becomes register compare value 89; electrical duty and RPM were not measured. |

The derived handler logic is:

```text
SET: reject unless length == 8 and every byte <= 100
     for each channel i:
         manual[i] = request[i] != 0
         if manual[i]: target[i] = request[i]
GET: response[i] = target[i] if manual[i] else 0
CONTROL TASK: update automatic targets; write each target to its PWM channel
```

The supplied SET example therefore selects manual 35% on the third channel and automatic mode on the other seven. Zero is an active mode selection, so preserving other settings requires the application's read-modify-write approach. The adapter now rejects readback values above 100 before a SET: the earlier test accepting an opaque `0xff` byte contradicted the firmware's whole-request validation and has been corrected.

The bundled SDR and `libipmipar.so.2.22.0` establish these RPM sensor mappings:

| Sensor label | IPMI sensor number | Tachometer input |
| --- | --- | --- |
| FAN1 | `0x60` | 0 |
| FAN2 | `0x62` | 4 |
| FAN3 | `0x66` | 2 |

The FAN3/PWM2/tach2 combination agrees with the owner's vendor-supplied third-byte example. Tachometer input numbering alone does not establish physical PWM wiring. In particular, the binary does **not** establish whether FAN2 uses payload byte 2 or 5. The current UI's header-to-slot association remains provisional and requires the owner to validate each selected header; do not derive a new association merely from the tach number.

The compiled automatic curve uses CPU Temp sensor `0x33`, with temperature steps `[45, 50, 55, 60, 65, 70, 74, 78, 82, 86, 90]` and duties `[30, 55, 60, 65, 70, 75, 80, 85, 90, 95, 100]` in `OpenLoopFanData` at `0x22960`. Firmware formula commands can alter this data. It contains no HDD-temperature input in the traced default path. Neither the 30% default nor acceptance of a 1% manual target proves a stable or thermally safe floor for attached fans.

The SET success path only changes RAM. It stores no lease or expiry. In the traced control task, host-power-off clears the task's active state while preserving manual flags; reactivation retains those manual choices. `ASRR_FanInit` at `0x1491c` clears the flags and seeds 30% duties during BMC/IPMI initialization. This supports retaining application recovery and graceful restoration; it does not prove real BMC-reset or host-failure behavior. Target-host acceptance still needs installed-firmware identification, physical mapping, a tested floor, observed automatic restoration and lifecycle validation. The existing activation confirmations remain in place.

## 1. Objective and decisions

Deliver a small, maintainable TrueNAS application in four product phases:

1. Replace the CUDA-based container with a lightweight standard Linux runtime.
2. Add a Svelte UI for GPU telemetry and editable fan curves, after hardening the controller.
3. Add optional local IPMI access and a verified system-fan actuator.
4. Drive the selected case-fan zone from an adjustable curve of average HDD temperature.

The owner's follow-up favors S.M.A.R.T. as a live HDD temperature source. **Use direct smartctl JSON readings as the preferred phase-4 provider**, subject to drive/controller and standby validation. Keep the TrueNAS temperature API as an optional alternative; its documented sampling cadence is unsuitable for promising rapid live readings.

These are implementation decisions for Codex:

| Area | Decision |
| --- | --- |
| Runtime | Official Debian slim, initially debian:trixie-slim. |
| Build | Official Go image from the same Debian generation, CGO enabled, ordinary C compiler. No CUDA SDK. |
| Frontend | Svelte 5, TypeScript, Vite; static bundle served by Go. |
| Process model | One Go process owns HTTP, telemetry and GPU control; independent workers keep slower disk/IPMI calls off the GPU loop. |
| Persistence | Versioned JSON in a mounted directory, atomic writes, last-good configuration and revision checks. |
| Telemetry | Direct go-nvml calls; nullable values with status and timestamps. |
| Device identity | GPU UUID; disk serial/WWN with verified device mapping; explicit BMC profile and zone IDs. |
| Normal fan policy | Firmware automatic until the owner explicitly activates a validated curve. |
| HDD input | Arithmetic mean of selected HDDs, with hottest-drive and shared-zone thermal guards. |
| Initial target | Linux amd64 and Docker-based TrueNAS Apps; record the exact tested host version. |
| Later actuator support | One verified board/BMC profile first; generic IPMI monitoring remains useful without fan writes. |
| Scope boundaries | No GPU process termination, host shutdown, GPU reset, generic shell console, or arbitrary raw-IPMI web endpoint. |

Start phase 1 and the hardware-independent work in phase 2 immediately. Missing motherboard and disk details do not block those tasks. Hardware-specific activation and unattended system-fan control have separate acceptance gates below.

## 2. Reviewed baseline and verification

### Repository snapshots

| Repository | Reviewed commit | Commit date |
| --- | --- | --- |
| dochevviktor/nvidia-fan-control | 9491b8dd8c206cf0c0c10a25910a7a4f0dca2900 | 27 February 2025 |
| ZanMax/nvidia-fan-control | 7f2315d3eb6af952f88c53168052bb5feed9e019 | 6 February 2026 |
| NVIDIA/go-nvml, existing dependency v0.12.4-1 | 8a7f6b796317a8c7cd5347a25be164108967e7b4 | Tag inspected |
| NVIDIA/go-nvml, inspected release v0.13.4-1 | 1a1e86da474d340d7cf58274588f4a4afc8772e7 | Tag inspected |

Refresh the target branch before implementation and reconcile any newer changes. Preserve the MIT license and both copyright notices: `Copyright (c) 2025 Max Zanoga` for the original upstream work and `Copyright (c) 2026 Viktor Dochev` for his additions and modifications. The reviewed fork snapshot's module path named github.com/ZanMax/nvidia-fan-control. The current project uses github.com/dochevviktor/server-fan-control consistently for its module and package imports.

The fork has one Go source file, a JSON configuration, Dockerfile and a build-only GitHub Actions workflow. It has no Compose definition, API, UI or tests. Its README describes native systemd deployment. [R1–R5]

### A real non-CUDA build passed

The unmodified fork was compiled successfully in the research workspace using:

- Go 1.27.3, downloaded from go.dev with the published SHA-256 verified.
- GCC 13.3.0 on Ubuntu 24.04.
- CGO_ENABLED=1.
- The repository's existing go-nvml v0.12.4-1.
- Build flags: -trimpath -ldflags="-s -w".

The resulting amd64 binary was 3,526,152 bytes. Its direct dynamic dependencies were libc and the ELF loader. The environment had no nvcc, no /usr/local/cuda directory, and no NVIDIA/CUDA libraries in its loader cache. The source checkout remained clean.

This verifies that the existing source can compile without CUDA. It does not verify the proposed Debian container, runtime NVML injection, fan-write permissions or real hardware behavior. Docker and GPU/BMC hardware were unavailable for this research. The original historical compilation error cannot be identified without its log.

NVIDIA's documentation independently confirms that go-nvml can compile without an installed NVIDIA driver; the driver library is required when the application runs. [R6]

### Current Dockerfile: the relevant distinction

The existing Dockerfile is already multi-stage:

- Builder: nvidia/cuda:12.2.2-devel-ubuntu22.04.
- Final runtime: nvidia/cuda:12.2.2-runtime-ubuntu22.04.
- Final runtime additionally installs libnvidia-compute-525.
- Builder manually installs an amd64 Go 1.21.6 archive.
- Runtime forces LD_LIBRARY_PATH=/usr/lib/x86_64-linux-gnu.

Replacing only the builder would leave the deployed CUDA runtime image large. Replace both stages and remove the packaged NVIDIA driver library and forced library path. [R2]

### Controller findings that must precede UI writes

| Finding | Evidence | Required behavior |
| --- | --- | --- |
| Hysteresis is ineffective across speed boundaries | main.go, getFanSpeedForTemperature: the OR condition accepts every changed target | Explicit stateful hysteresis with regression tests. |
| Unmatched temperatures keep the old speed | Same function returns prevSpeed | Defined endpoint saturation, invalid-sensor handling and thermal overrides. |
| Only fan index 0 is controlled | DeviceSetFanControlPolicy and DeviceSetFanSpeed_v2 calls | Enumerate and manage every selected fan. |
| No explicit automatic-mode restoration | Only nvml.Shutdown is deferred | Restore managed fans before library shutdown. |
| Partial transition can leave manual control active | Policy succeeds, speed-setting fails | Attempt fallback and expose the uncertain/failed state. |
| No configuration validation | JSON decode only | Reject invalid intervals, duties, gaps, overlaps and malformed curves. |
| One startup-only configuration for every GPU | Relative config.json and arrays indexed by GPU index | Persistent UUID policies and controlled updates. |
| Errors merely continue the loop | Failed temperature/read/write paths | Track last valid samples and enter fallback after defined failures. |
| Fatal logging can bypass cleanup | log.Fatalf after initialization | Return errors through a run boundary with ordered cleanup. |
| No signal handling | Main loop runs forever | SIGTERM/SIGINT handling, bounded shutdown and reset results. |

A replay of the existing expression with the checked-in configuration produces 30%, 40%, 30%, 40% at 40, 41, 40, 41°C, despite a 3°C hysteresis setting. A temperature beyond all ranges also preserves the old duty. These are source-level regressions to cover, not measurements from a GPU. [R1]

### Use upstream selectively

Current upstream adds validation, fan enumeration, signals, reset logic and tests. It also adds killGPUProcesses, invoked from its critical-temperature path, using NVML PIDs with syscall.Kill. Do not import that behavior. Terminating GPU workloads is outside this project scope, and host-reported PIDs require namespace handling in containers.

Upstream also retains assumptions unsuitable for this dashboard, including fixed file logging and skipping monitoring for devices without controllable fans. Use its improvements and tests as references, then implement the agreed behavior deliberately. [R7]

## 3. Phase 1: lightweight container and deployment

### Build and runtime requirements

go-nvml contains generated C bindings and its NVML header. The inspected loader opens libnvidia-ml.so.1 dynamically; its dl package links libdl. Keep CGO_ENABLED=1 and a functioning C toolchain. A static pure-Go build is not an appropriate shortcut. [R6, R8]

Use matching Go-builder and runtime Debian generations to avoid introducing a newer-glibc/older-runtime mismatch. Current official manifests include golang:1.27.3-trixie, debian:trixie-slim and node:24.22.2-trixie-slim. These are researched starting versions; resolve and pin image digests in the implementation, and maintain updates. [R9–R11]

Debian slim is the recommended baseline because it supplies glibc and can later install ipmitool and smartmontools normally. Alpine introduces compatibility work for host NVIDIA libraries; scratch would require reconstructing the dynamic runtime. Distroless can be revisited only if measured image size warrants the additional packaging work.

The final image contains the Go binary with embedded UI, required shared libraries and certificates where needed. Add ipmitool in phase 3 and smartmontools in phase 4 with --no-install-recommends. The application invokes smartctl; it does not launch another smartd daemon.

### Illustrative final Dockerfile structure

This sketch assumes the package layout in section 4 and the new CLI contract. It is not a patch for the current one-file repository.

~~~dockerfile
# syntax=docker/dockerfile:1

FROM node:24.22.2-trixie-slim AS frontend
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.27.3-trixie AS backend
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend /src/web/dist ./internal/web/dist
ENV CGO_ENABLED=1
RUN mkdir -p /out && go build -trimpath -ldflags="-s -w"     -o /out/server-fan-control ./cmd/server-fan-control

FROM debian:trixie-slim AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends     ca-certificates     && rm -rf /var/lib/apt/lists/*
COPY --from=backend /out/server-fan-control /usr/local/bin/server-fan-control
WORKDIR /data
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/server-fan-control"]
CMD ["--config", "/data/config.json", "--listen", "0.0.0.0:8080"]
~~~

For phase 1, use a simpler two-stage build of the current Go entrypoint; add the frontend stage with phase 2. Retain go-nvml v0.12.4-1 in this first container PR so the image change is isolated from a dependency upgrade. Add a .dockerignore that excludes Git metadata, node_modules, local binaries, generated output, test artifacts, live configuration and secrets. Copy module/lock files ahead of source for useful caching.

PR1 is a build/deployment checkpoint, not an unattended control release. The existing loop cannot meet the restoration requirements. Add/use a telemetry-only diagnostic path for its runtime smoke test, and release GPU control only after PR2's validation, fallback and shutdown work passes. Any earlier write probe must be separately supervised with an explicit tested reset operation.

Keep initial releases amd64. Adding arm64 later needs a real cgo-compatible build/cross toolchain and hardware validation.

### NVIDIA runtime contract

The host's NVIDIA Container Toolkit supplies the driver library and device nodes. Set NVIDIA_DRIVER_CAPABILITIES=utility for NVML; a Docker GPU reservation selects the device. These settings have different purposes. Do not bake a host driver version into the image. [R12, R13]

GPU telemetry access does not prove fan-write access. Start with an explicitly documented root process for the hardware service and test the actual driver. Reduce privileges against a tested matrix. Do not promise that root with all capabilities dropped works, or add SYS_ADMIN universally. Record any demonstrated requirement for a specific capability. NVML documents privileged fan-policy operations; NVIDIA's kernel capability checks also make a blanket root-only guarantee inappropriate. [R14, R15]

Successful ldd output for the Go binary does not verify dynamically loaded NVML. Test nvml.Init, enumerate the selected UUID, read telemetry, set a conservative fan target under supervision, and restore automatic mode on the real host.

### Compose design

Provide a GPU-only compose.yaml and separate opt-in overrides for IPMI and selected disks. The following is a deployment template for the planned application; placeholders and auth settings must be supplied before deployment. A release image at the illustrated GHCR path does not exist merely because it is named here.

~~~yaml
services:
  fan-control:
    image: ghcr.io/dochevviktor/server-fan-control:<published-version>
    user: "0:0"
    restart: unless-stopped
    read_only: true
    stop_grace_period: 30s
    environment:
      NVIDIA_DRIVER_CAPABILITIES: utility
    command:
      - --config
      - /data/config.json
      - --listen
      - 0.0.0.0:8080
      - --admin-token-file
      - /run/secrets/admin-token
    deploy:
      resources:
        reservations:
          devices:
            - driver: nvidia
              device_ids: ["GPU-REPLACE-WITH-UUID"]
              capabilities: [gpu]
    ports:
      - "127.0.0.1:8080:8080"
    volumes:
      - /mnt/POOL/app-config/fan-control:/data
      - /mnt/POOL/app-secrets/fan-control-token:/run/secrets/admin-token:ro
    tmpfs:
      - /tmp:rw,noexec,nosuid,size=16m
~~~

The loopback port binding is a conservative starting configuration. Document an authenticated HTTPS reverse-proxy route or the explicitly selected management-network binding for browser access. The application listens on all interfaces inside the container; host publication is a separate setting.

Use a mounted directory for configuration, not an individual bind-mounted config file: atomic replacement must work inside that directory. Check ownership and permissions. No Docker socket, host PID namespace, whole-/dev mount or privileged container is required by the proposed default design.

Docker-based TrueNAS Apps starts at 24.10. Document the current Install via YAML route and the exact release tested. Enable host NVIDIA support through supported TrueNAS settings; do not install packages or reconfigure the appliance OS from the app. Build images in development/CI and deploy prebuilt tags to TrueNAS. [R16–R18]

### Phase-1 acceptance

- Build succeeds on a machine with no NVIDIA driver/GPU.
- Image contains no CUDA SDK, CUDA runtime, compiler, Node runtime or packaged NVIDIA driver.
- Record final image size and compare against the old final stage using the same measurement. Keep compressed transfer size separate from unpacked size.
- Aim for an amd64 GPU-only image below 150 MiB unpacked, as an engineering budget, not an already measured result.
- Host NVML loading, device selection and telemetry pass on the target machine through a read-only diagnostic. GPU write/reset acceptance belongs to PR2 and the complete first release.
- Config survives restart/recreation; logs use stdout/stderr.
- CI builds PRs without publishing; release publishing uses explicit tags and appropriate permissions.

## 4. Phase 2: Go service architecture and API

### Package boundaries

| Location | Responsibility |
| --- | --- |
| cmd/server-fan-control | CLI, dependency wiring, lifecycle and signals. |
| internal/config | Schema, migration, validation, atomic persistence and revisions. |
| internal/curve | Pure step/linear curve evaluation and controller math. |
| internal/control | Mode/state ownership, scheduling, safety overrides and reconciliation. |
| internal/gpu | NVML adapter, capabilities and mock implementation. |
| internal/httpapi | Authentication, JSON API, request validation and snapshots. |
| internal/web | Embedded production assets. |
| web | Svelte/TypeScript UI and frontend tests. |
| internal/ipmi | Later: bounded command runner and verified BMC profiles. |
| internal/disks | Later: smartctl provider; optional TrueNAS/hwmon providers. |

Use interfaces at hardware boundaries, not a large general plugin framework. A GPU adapter needs discovery, metrics, setting a selected fan target and restoring defaults. The case-fan adapter needs profile discovery, zone state, target setting and automatic restoration. Disk providers supply identity, temperature, collection time and status.

All hardware writes flow through the controller. HTTP handlers read snapshots and submit validated intent. Do not call NVML, smartctl or ipmitool directly from request handlers.

Serialize GPU control and configuration transitions. Run disk and IPMI collection on separate bounded workers; an unresponsive BMC or disk must not stop GPU thermal control. Bound subprocess execution and output. Do not claim that cancelling a Go context can forcibly interrupt an arbitrary in-process NVML C call; document driver-hang limits and escalate isolation only if testing demonstrates the need.

A mock mode should support development and CI without hardware, and be visibly identified in the UI. Missing hardware in real mode is a visible unavailable/degraded state, never fabricated telemetry.

### API contract

| Method and path | Purpose |
| --- | --- |
| GET /healthz | Process/HTTP liveness; no expensive hardware probe. |
| GET /api/v1/status | Controller states, adapter errors, data age and build version. |
| GET /api/v1/gpus | UUIDs, names, capabilities, per-fan state and metric snapshots. |
| GET /api/v1/config | Desired configuration and revision/ETag. |
| PUT /api/v1/config | Validate and atomically persist a complete configuration. Require revision/If-Match. |
| POST /api/v1/gpus/{uuid}/default | Latch automatic mode, request restoration and persist intent; report hardware and storage outcomes separately. |
| GET /api/v1/history | Bounded, sampled history for the chosen device. |
| GET /api/v1/disks | Phase 4: selected/available disks and telemetry quality. |
| GET /api/v1/system-fans | Phase 3: profile, zones, sensor RPM and status. |

Simple polling is sufficient initially. Start with a two-second UI refresh and a separately configured GPU sampling interval; preserve the old five-second interval when importing an existing config. A refresh must not manufacture a new hardware timestamp.

Expose configuration revision separately from the revision successfully reconciled to hardware. A saved configuration is not proof that every fan accepted it. Return validation errors consistently, reject stale updates with a conflict/precondition status, and keep the previous valid configuration when a write fails.

Safety fallback and Return to automatic remain executable if configuration storage is full, read-only or otherwise failing. Latch the running controller into the safe/default transition, attempt hardware restoration, and report/retry persistence separately. The old saved curve must not be reapplied by that running process. If intent cannot be persisted, explain the restart implications explicitly rather than claiming a durable mode change.

Use bounded in-memory history, initially around 30 minutes. Database deployment and indefinite telemetry retention are outside the initial scope.

### Telemetry mapping

The pinned dependency already provides the core APIs needed for the dashboard. An NVML upgrade is not required merely to show these metrics. [R8]

| Information | Go NVML API | Contract |
| --- | --- | --- |
| Identity | GetUUID, GetName | Store by UUID; display current index only as metadata. |
| GPU temperature | GetTemperature(TEMPERATURE_GPU) | Celsius with a timestamp/status. |
| VRAM occupancy | GetMemoryInfo, optionally GetMemoryInfo_v2 | Bytes used/free/total; label displayed MiB/GiB clearly. |
| Activity | GetUtilizationRates | GPU activity and memory-controller activity are separate from occupied VRAM. |
| Power | GetPowerUsage | Convert milliwatts to watts. |
| Fans | GetNumFans, GetFanSpeed_v2 | One record per fan. |
| Valid target bounds | GetMinMaxFanSpeed | Combine supported bounds with configured safe minimums. |
| Custom target | SetFanSpeed_v2 | Per-fan target, with outcome tracking. |
| Firmware defaults | SetDefaultFanSpeed_v2 | Per managed fan, checked for success. |

NVIDIA describes fan-speed readings as intended operating speed; they are not independent proof of physical rotation. Read percentages can exceed 100, unlike valid setter inputs. Keep reported values separate from desired and last-successfully-commanded values. [R19]

RPM is optional. v0.12.4-1 has no GetFanSpeedRPM. The inspected v0.13.4-1 wrapper has no fan-index argument and leaves the fan field at zero. Do not label one fan-0 reading as all fans' RPM. Defer RPM or implement and validate appropriately scoped support. [R20]

Gate optional symbols before invoking them using the binding's available symbol-lookup facility. Handle missing symbols, unsupported hardware and permission failures separately. Successful NVML initialization does not prove every optional function is present. [R8]

For every metric use a nullable value, unit, collection timestamp, last successful timestamp and a status/reason. A monitoring-only or passively cooled GPU should remain visible.

### Authentication and frontend contract

Provide a simple single-admin authentication mechanism backed by a mounted secret. An implementation can exchange the admin token for a short-lived server-side session with an HttpOnly, same-site cookie, CSRF/Origin checks and HTTPS-aware secure-cookie settings. No multi-user account system is needed. Restrict any development auth bypass to explicit local/mock use. Protect hardware mutations and configuration endpoints from the first UI release.

Build the frontend with Svelte, TypeScript and Vite, then embed the output into the Go binary with go:embed. Keep the production UI and API on the same origin. During development, proxy /api through Vite. Bundle assets locally. [R21–R23]

UI requirements:

- GPU selector and cards for temperature, VRAM, activity, power and per-fan percentage.
- Explicit Automatic, Custom curve, Monitoring only, Degraded and Offline states.
- Curve graph plus an accessible numeric point/range table.
- Current operating point and requested target on the graph.
- Add/delete/edit points, validation and visible unsaved changes.
- Explicit Save/apply and Return to automatic actions.
- Clear saved-versus-applied status, failures and data age.
- Short history chart and modest responsive layout.
- Later: system-fan zones, disk selection, average/max HDD temperature and the active override reason.

Dragging a point previews locally. Send an update only on explicit save/apply. Supply keyboard-accessible editing and precise numeric inputs; a graph alone is insufficient.

## 5. Control policy, persistence and migration

### Configuration design

Use schema_version, revision, per-GPU UUID policies, named curves, sampling intervals and later independent case-fan/disk sections. Make curve type explicit: step or linear. New devices default to automatic, with custom settings saved as a draft until activated.

Suggested policy fields include:

- mode: automatic or curve.
- selected fan indices, defaulting to all discovered controllable fans only for an explicitly managed GPU.
- curve type and temperature/duty points or legacy ranges.
- permitted duty floor, critical-temperature rule and fallback policy.
- downward hysteresis, cooling hold duration and optional downward slew limit.
- sample freshness budget and consecutive failure limits.

Hardware-dependent critical thresholds and minimum duties require validation. Do not copy a universal GPU critical threshold into HDD settings or assume the upstream 105°C default is suitable.

Persist changes by writing a temporary file in the same directory, syncing, renaming and syncing the directory where supported. Protect concurrent updates and retain a usable backup. Reject malformed configuration and failed writes without silently replacing the last-good state. Keep secrets outside exports and ordinary curve JSON.

### Migration of existing config.json

The existing ranges use min < temperature <= max and time_to_update in seconds. Preserve the intended step mapping when importing. Do not silently convert it into a different linear curve. Validate ordering, continuity and duty bounds; report invalid configurations for correction.

Keep a backup before migration. Offer the legacy curve to explicitly selected GPUs rather than automatically taking over every newly discovered device. Explain the intentional fixes to hysteresis, out-of-range behavior and shutdown restoration.

### Deterministic controller rules

Define and test the policy precisely:

1. A valid rising thermal demand increases duty promptly.
2. Cooling reduces duty only after the configured hysteresis/hold condition.
3. Above the highest valid curve point, saturate at the configured maximum; require a full-cooling upper endpoint for an activated thermal curve.
4. Below the first point, retain its target subject to the verified floor.
5. Safety overrides bypass normal smoothing and cooling delays.
6. Invalid or stale temperatures never become zero and never justify reducing cooling.
7. Track the active range/state explicitly; identical duties in adjacent ranges must not confuse hysteresis.
8. Coalesce identical commands, but reconcile after initialization/recovery and detect lost manual state when supported.

For step curves, use immediate upward transitions and lower-bound-minus-hysteresis downward transitions. For linear curves, choose a documented asymmetric deadband and cooling hold; a viable approach is to compare the current duty against F(T + downward_hysteresis) for downward eligibility. Specify the final algorithm with temperature-sequence tests before enabling hardware.

### Ownership and restoration

SetFanSpeed_v2 itself changes the policy to manual. Avoid an unnecessary separate manual-policy call where the tested API supports this direct operation. Restore defaults using the documented per-fan reset API. [R14]

Persist a small ownership/pending-restoration journal separately from desired configuration, before attempting manual takeover. If that initial journal write fails, refuse takeover. Keep each touched or uncertain fan in the journal until restoration succeeds. Removing a GPU/fan from desired config must not erase recovery ownership. On restart, reconcile journaled restoration before applying curves, including devices no longer selected in desired config. This closes the crash window between saving config removal and actually resetting the fan.

Track every fan the app attempted to take over, including uncertain partial operations. On a failed multi-fan transition, stop normal control for the affected GPU, attempt the validated fallback for affected fans, and surface each failure. Do not declare automatic mode active until restoration succeeds. Failure to update the journal must not block a hardware restoration attempt; retain/report the unresolved record so a later startup can retry conservatively.

Restoration is required when control is disabled, a managed device is removed from config, the process receives SIGTERM/SIGINT, or normal shutdown occurs. Reset before nvml.Shutdown. On restart, reconcile previously managed devices into a known safe state before resuming curves; leave unselected devices alone.

Use only one fan-control writer for each physical device/zone. A data-directory lock can prevent duplicate instances sharing that directory, but cannot detect every external controller. Other GPU compute/transcoding workloads may continue.

The implementation must state the limits of recovery. Graceful cleanup cannot run after SIGKILL, OOM kill, host failure or some native crashes. Docker restart policies are process recovery, not a hardware fan lease. Do not claim guaranteed safe fallback without verifying the actual GPU/BMC behavior or adding a separately designed independent mechanism. [R24]

## 6. Phase 3: IPMI system-fan support

### Device and command integration

The TrueNAS host must already expose a functional local IPMI device. Linux provides the user-space interface through ipmi_devintf and a hardware-appropriate interface driver. ipmitool supports the local OpenIPMI interface. [R25, R26]

Provide an optional override:

~~~yaml
services:
  fan-control:
    devices:
      - "/dev/ipmi0:/dev/ipmi0:rw"
~~~

Install ipmitool in the application image for this phase. Verify device ownership/access on the real host. Broad privileged mode and access to all device nodes are not justified by this single-device interface.

Begin with read-only inventory and sensor diagnostics:

~~~text
ipmitool -I open mc info
ipmitool -I open fru print
ipmitool -I open sensor list
ipmitool -I open sdr type Fan
~~~

Run fixed argv via exec.CommandContext, with timeout, bounded output, cancellation and structured result handling. Cache sensor discovery and avoid repeatedly scanning the entire SDR table on every GPU poll.

### Verified hardware profiles

Fan-duty control is vendor/board/BMC-specific. Implement a profile containing identity checks, supported zones, duty encoding, minimum duty, manual/automatic transitions and sensor associations. Record whether duty can be read back and how the BMC behaves after reset.

The first supported profile must match the owner's actual motherboard/server and BMC firmware. No generic Supermicro/Dell/ASRock raw-command sequence should be guessed from the brand. Unsupported profiles expose monitoring only.

A valid profile must establish automatic restoration before custom control is activated. Confirm whether a zone includes CPU, HBA, NIC or motherboard cooling. Where those components share the controlled zone, their thermal demand or verified conservative floor must be part of the policy.

Phase-3 acceptance requires supervised proof that the intended physical fans respond, the floor is safe, automatic restoration works, and graceful stop/restart behave as documented. An abrupt-kill/reboot test needs a separately reviewed procedure on the owner's hardware. Do not enable an IPMI watchdog casually: its action may concern chassis power/reset rather than fan restoration.
## 7. Phase 4: direct SMART temperatures and the HDD curve

### Preferred provider

Use smartctl JSON as the first implementation. Read the drive's temperature telemetry without running self-tests. The normal poll is configurable; start at 60 seconds with bounded concurrency and per-command timeouts, then validate its thermal response under NAS workload.

For a verified ATA/SAT path, an illustrative bounded query is:

~~~text
smartctl -j -i -A -n standby,3,5 -d <verified-type> <mapped-device>
~~~

The third -n parameter is ATA-specific. Adapt the command for SCSI/SAS and controller-specific transports. The packaged smartctl manual documents JSON, identity/attribute queries, standby handling and exit bitmasks. Device autodetection can wake a drive; unsupported power checks require explicit treatment. Parse stdout even when the process returns an error, then classify transport status and health flags separately. [R27]

Prefer normalized temperature.current where emitted; the ATA implementation populates this field through its decoder. Do not assume raw attribute 194 is always a plain Celsius integer. Require real ATA and SAS fixtures before declaring both supported. Missing fields remain unavailable. [R28]

Store attempted_at, read_completed_at, last_valid_at, temperature_c and a status such as ok, standby, unsupported, permission_denied, timeout, invalid_data or missing. Command completion time is a collection timestamp, not proof of the firmware sensor's internal update instant.

### Stable identity and device access

The owner selects an explicit HDD group. Persist serial/WWN or a verified stable identifier; resolve current device paths separately. Verify identity at discovery and after reconnect/replacement. Do not silently include boot SSDs, NVMe devices or newly installed disks in an existing average.

Map only the selected devices. A by-id symlink alone does not grant access to its target; map the resolved device and preserve expected identity in configuration. Some HBAs need a generic/controller device and a specific smartctl selector. Validate the actual topology before writing the final Compose override.

Test required ownership and any additional capability, potentially SYS_RAWIO, per transport. A read-only device mapping is not a general guarantee against all powerful IOCTLs. Keep a fixed read-command allowlist and no arbitrary command arguments in the API. If acceptable access cannot be scoped, defer to an explicitly restricted collector rather than silently broadening the web application's privileges.

Do not start self-tests, modify SMART settings, change standby timers or write drive firmware. Coordinate observation with TrueNAS's existing monitoring and test schedules.

### Standby and missing-data policy

Treat intentionally sleeping disks separately from failed reads. Define whether a sleeping disk's last known temperature can remain eligible for a bounded period. If it cannot, display it as excluded/asleep and apply a conservative group policy. Never insert a zero-degree reading.

The default active-disk coverage rule should require every selected, expected-awake drive to have valid data. If a drive disappears or its sample expires, inhibit downward cooling changes during a short grace period, then use the verified fallback. Do not let losing the hottest sensor make the average look safely lower.

If all selected drives are sleeping, use the explicitly verified automatic/minimum-cooling policy. This is not an average of zero drives. Show valid/expected/asleep counts and the reason for any fallback.

### Case-fan demand

Normal operation follows the owner's selected average:

~~~text
mean_hdd_c = arithmetic mean of eligible selected HDD samples
base_duty = hdd_curve(mean_hdd_c)

required_duty = maximum of:
  base_duty
  hottest_drive_guard
  required shared-zone thermal guards
  verified hardware minimum
~~~

This is an engineering policy to validate, not a universal vendor thermal guarantee. Use a separate maximum-temperature guard because an average can hide one hot drive. For example, 30, 30, 30 and 54°C average 36°C while one disk is much hotter; the numerical example illustrates aggregation, not a prescribed critical threshold.

Reuse the curve editor and pure interpolation code, with separate HDD temperature ranges and settings. Apply prompt upward changes and slower reductions. Show mean, maximum, sample age, selected drives, target, sensor RPM and the override reason.

Enter the verified fallback on excessive temperature, stale coverage, invalid data, required CPU/board sensor loss or IPMI write failure. If the BMC is unreachable, report that fallback could not be applied; do not label the system safe merely because the software attempted it.

### Optional alternatives

The TrueNAS 25.10 disk.temperatures method documents retrieval no more often than every five minutes and does not specify a source-sample timestamp in its return schema. It can be an optional provider if its measured cadence suits the enclosure. Fresh RPC receipt must not be relabeled as a fresh disk sample. [R29]

If implemented, use a version-compatible persistent authenticated WebSocket API connection, keep NAS credentials in the Go backend, and verify TLS. Authentication and API shapes must match the actual host release. This optional integration is not needed to ship the SMART provider. [R30]

If suitable host hwmon drive sensors already exist, a later adapter can read them with narrower access. Linux drivetemp uses SCT or SMART, but its documentation notes possible spindown-timer effects. It still requires identity and standby validation. [R31]

## 8. Delivery backlog and acceptance gates

Deliver reviewable increments. Suggested PR sequence:

| PR | Work | Acceptance gate |
| --- | --- | --- |
| 1 | Replace CUDA stages, add .dockerignore, explicit config path, Compose and Docker/TrueNAS docs | No-GPU build, measured image size, target-host telemetry-only NVML smoke test; build checkpoint only. |
| 2 | Controller extraction, config migration, UUID/fan handling, hysteresis and fallback | Pure logic/mocked hardware tests; supervised all-fan write/reset validation. |
| 3 | Snapshot API, atomic config revisions, auth, Svelte UI and embedded build | Mock-mode E2E and real supported-GPU dashboard/curve validation. |
| 4 | IPMI package, read-only diagnostics, adapter and first verified board profile | Correct zone mapping and proven restoration; unsupported boards monitor-only. |
| 5 | smartctl provider, HDD selection, timestamps, standby/error states | Real device fixtures, stable identity and no unintended wakeups on tested paths. |
| 6 | HDD average curve and thermal guards connected to verified IPMI actuator | Loaded-system thermal test, failure scenarios and documented abrupt-failure behavior. |

Phases 1 and 2 are the first useful GPU release. Keep IPMI and disk automation disabled until their gates pass. Image publication can be implemented in CI without automatically deploying to the NAS.

### Focused tests

Use fixtures and fake adapters for:

- Curve endpoints, interpolation, hysteresis oscillation and repeated equal-duty segments.
- Invalid/overlapping/gapped ranges and invalid intervals/duties.
- Critical threshold and stale-sensor paths.
- Two fans with one failed write, failed restore and lost GPU.
- Unsupported fan control with otherwise available telemetry.
- Concurrent config edits, revision conflicts, failed persistence and migration.
- Config removal saved followed by a crash before reset: restart restores the journaled fan.
- Full/read-only configuration storage during Return to automatic: restoration still runs and the previous curve is not reapplied by the running process.
- Fresh versus stale/null metrics and correct units.
- UI draft/save/apply/default behavior and authentication failures.
- SMART exit bitmasks, invalid JSON, standby, timeout and permission failures.
- Disk replacement, missing hottest disk, all-asleep group and average/max arithmetic.
- IPMI timeout, profile mismatch, wrong zone rejection and partial failures.

Run frontend type checking/build and meaningful UI tests, Go tests including the race detector, and go vet. The frontend production build must exist before compiling packages that embed it; provide one documented command that establishes this order. Use mock mode for CI; CI must not actuate real fans.

Hardware tests are distinct: compare telemetry with nvidia-smi/smartctl diagnostics, verify all intended fans, restore defaults, recreate the container, and observe error recovery. A passing build is not a thermal acceptance result.

## 9. Inputs needed before hardware activation

Collect these without delaying initial coding:

- Exact TrueNAS release, GPU model/UUID and host NVIDIA driver.
- Whether existing GPU fan writes already work and any historical compilation log.
- Motherboard/server model, BMC firmware and verified local IPMI access.
- Case/CPU fan header and zone map; relevant CPU/HBA/board sensors.
- Known-good manual and automatic restoration commands from vendor documentation or prior validation.
- HDD identities, SATA/SAS/HBA/USB topology and intended disk group.
- Desired standby policy and whether all disks normally remain spinning.
- Validated thermal limits, noise preference and permitted minimum fan duty.
- Management-network/proxy arrangement for the UI.

Unresolved hardware details must result in diagnostics and monitor-only behavior, not guessed actuator support.

## 10. Pasteable starting instruction for Codex

> Work in dochevviktor/server-fan-control and implement the first GPU release described in this handover. Recheck the current repository against reviewed commit 9491b8dd8c206cf0c0c10a25910a7a4f0dca2900 and preserve unrelated changes and upstream license attribution.
>
> First replace both CUDA stages with an official Go builder using CGO and a matching Debian slim runtime. Remove the image-installed NVIDIA driver package and forced library path. Add persistent configuration, a GPU-only Compose example and measurable image-size validation. Use the host NVIDIA runtime with utility capability.
>
> Then extract and harden the controller: UUID identity, all selected fans, validated step/linear curves, correct hysteresis, fresh-sample tracking, safe partial-failure handling and checked restoration on disable/SIGTERM before NVML shutdown. Keep unsupported GPUs visible for monitoring. Do not import upstream GPU-process termination.
>
> Add a same-origin Go JSON API and a static Svelte 5/TypeScript/Vite UI embedded into Go. Support telemetry cards, a numeric and graphical curve editor, explicit save/apply and return-to-automatic controls, data age and per-fan failures. Use versioned JSON with atomic writes/revision checks and simple admin authentication. Include a clearly marked mock mode and focused tests.
>
> Leave the architecture ready for IPMI and HDD providers. Implement those in later increments: one verified BMC profile using /dev/ipmi0 and bounded ipmitool calls, followed by direct smartctl JSON for the selected HDDs and a mean-temperature curve with hottest-drive/shared-zone guards. Do not invent OEM commands, disk mappings or hardware thresholds.
>
> Complete all hardware-independent implementation and verification. Report exactly which build/container/hardware checks ran, actual image sizes, and any target-host checks still required. Treat real fan actuation and NAS deployment as separate hardware validation work; do not claim those checks passed from mock tests.

## 11. Sources

Repository findings above are from read-only Git checkouts at the listed commits. The public code permalinks preserve the reviewed versions. Documentation links were checked on 29 October 2026.

- **R1 — Fork controller and configuration:** [main.go](https://github.com/dochevviktor/nvidia-fan-control/blob/9491b8dd8c206cf0c0c10a25910a7a4f0dca2900/main.go), [config.json](https://github.com/dochevviktor/nvidia-fan-control/blob/9491b8dd8c206cf0c0c10a25910a7a4f0dca2900/config.json).
- **R2 — Existing Dockerfile:** [pinned source](https://github.com/dochevviktor/nvidia-fan-control/blob/9491b8dd8c206cf0c0c10a25910a7a4f0dca2900/Dockerfile).
- **R3 — Module metadata:** [go.mod](https://github.com/dochevviktor/nvidia-fan-control/blob/9491b8dd8c206cf0c0c10a25910a7a4f0dca2900/go.mod).
- **R4 — Current CI:** [docker-image.yml](https://github.com/dochevviktor/nvidia-fan-control/blob/9491b8dd8c206cf0c0c10a25910a7a4f0dca2900/.github/workflows/docker-image.yml).
- **R5 — README and license:** [README](https://github.com/dochevviktor/nvidia-fan-control/blob/9491b8dd8c206cf0c0c10a25910a7a4f0dca2900/README.md), [LICENSE](https://github.com/dochevviktor/nvidia-fan-control/blob/9491b8dd8c206cf0c0c10a25910a7a4f0dca2900/LICENSE).
- **R6 — NVIDIA build requirements:** [go-nvml overview](https://github.com/NVIDIA/go-nvml).
- **R7 — Reviewed upstream behavior:** [upstream main.go](https://github.com/ZanMax/nvidia-fan-control/blob/7f2315d3eb6af952f88c53168052bb5feed9e019/main.go).
- **R8 — Pinned go-nvml implementation:** [loader](https://github.com/NVIDIA/go-nvml/blob/8a7f6b796317a8c7cd5347a25be164108967e7b4/pkg/nvml/lib.go), [dl package](https://github.com/NVIDIA/go-nvml/blob/8a7f6b796317a8c7cd5347a25be164108967e7b4/pkg/dl/dl.go), [device wrappers](https://github.com/NVIDIA/go-nvml/blob/8a7f6b796317a8c7cd5347a25be164108967e7b4/pkg/nvml/device.go).
- **R9 — Published Go image tags:** [official image manifest](https://github.com/docker-library/official-images/blob/master/library/golang); [Go downloads](https://go.dev/dl/).
- **R10 — Debian official image:** [supported tags](https://hub.docker.com/_/debian).
- **R11 — Node official image:** [supported tags](https://hub.docker.com/_/node).
- **R12 — NVIDIA driver injection:** [Docker specialized configurations](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/docker-specialized.html).
- **R13 — Compose GPU requests:** [Docker GPU support](https://docs.docker.com/compose/how-tos/gpu-support/).
- **R14 — Fan command and restoration semantics:** [NVML Device Commands](https://docs.nvidia.com/deploy/nvml-api/latest/api/group__nvmlDeviceCommands.html).
- **R15 — NVIDIA privilege-check context:** [nv-linux.h](https://github.com/NVIDIA/open-gpu-kernel-modules/blob/main/kernel-open/common/inc/nv-linux.h).
- **R16 — TrueNAS Docker transition:** [24.10 release notes](https://www.truenas.com/docs/scale/24.10/gettingstarted/scalereleasenotes/).
- **R17 — TrueNAS deployment:** [Installing Custom Apps](https://apps.truenas.com/managing-apps/installing-custom-apps/).
- **R18 — TrueNAS NVIDIA setup:** [Apps Initial Setup](https://apps.truenas.com/getting-started/initial-setup/).
- **R19 — GPU telemetry semantics:** [NVML Device Queries](https://docs.nvidia.com/deploy/nvml-api/latest/api/group__nvmlDeviceQueries.html).
- **R20 — Inspected newer RPM wrapper:** [v0.13.4-1 device.go](https://github.com/NVIDIA/go-nvml/blob/1a1e86da474d340d7cf58274588f4a4afc8772e7/pkg/nvml/device.go).
- **R21 — Svelte:** [official overview](https://svelte.dev/docs/svelte/overview).
- **R22 — Vite:** [official guide](https://vite.dev/guide/).
- **R23 — Go static assets:** [embed package](https://pkg.go.dev/embed).
- **R24 — Docker process recovery:** [restart policies](https://docs.docker.com/engine/containers/start-containers-automatically/).
- **R25 — Host IPMI interface:** [Linux IPMI driver](https://docs.kernel.org/driver-api/ipmi.html).
- **R26 — Local IPMI tool:** [ipmitool README](https://github.com/ipmitool/ipmitool/blob/master/README).
- **R27 — Packaged SMART command behavior:** [smartctl manual for Debian trixie](https://manpages.debian.org/trixie/smartmontools/smartctl.8.en.html).
- **R28 — Normalized ATA temperature JSON:** [smartmontools 7.4 ATA source](https://sources.debian.org/src/smartmontools/7.4-3/ataprint.cpp/).
- **R29 — TrueNAS temperature cadence:** [25.10 disk.temperatures](https://api.truenas.com/v25.10/api_methods_disk.temperatures.html).
- **R30 — Versioned NAS connection guidance:** [official TrueNAS API client](https://github.com/truenas/api_client).
- **R31 — Optional kernel temperature source:** [drivetemp documentation](https://docs.kernel.org/hwmon/drivetemp.html).


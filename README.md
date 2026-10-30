# NVIDIA Fan Control

A Linux utility for NVIDIA GPU telemetry and fan control through NVML.

This phase-1 build replaces the CUDA container with a standard Debian slim runtime. The container and Compose example default to a **one-shot, telemetry-only diagnostic**. The existing controller still needs configuration validation, fallback handling and automatic fan restoration before it is suitable for unattended use.

## Build the container

Build on Linux amd64 with Docker; neither an NVIDIA GPU nor an installed NVIDIA driver is needed to compile:

```bash
docker build --platform linux/amd64 -t nvidia-fan-control:local .
docker run --rm nvidia-fan-control:local --help
```

The two stages use an official Go builder and Debian slim from the same Debian generation. Go builds with CGO enabled and a C compiler. The runtime contains neither CUDA nor a packaged NVIDIA driver. The existing `go-nvml` dependency remains at `v0.12.4-1`.

Base images are pinned by digest in [Dockerfile](Dockerfile). Refresh the Go and Debian pins together, retaining the same Debian generation, then rerun the build and runtime checks below. Initial container support is Linux amd64.

## Docker GPU diagnostic

The runtime host needs a supported NVIDIA GPU, its NVIDIA driver, and the [NVIDIA Container Toolkit configured for Docker](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html). The toolkit supplies `libnvidia-ml.so.1` and the selected device nodes. `NVIDIA_DRIVER_CAPABILITIES=utility` requests NVML support; GPU selection is a separate setting. See [NVIDIA's runtime configuration](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/docker-specialized.html).

Find the target UUID on the host and supply it explicitly:

```bash
nvidia-smi --query-gpu=uuid,name,temperature.gpu --format=csv
export NVIDIA_GPU_UUID=GPU-REPLACE-WITH-YOUR-UUID
docker run --rm --read-only --user 0:0 \
  --gpus "device=${NVIDIA_GPU_UUID}" \
  -e NVIDIA_DRIVER_CAPABILITIES=utility \
  nvidia-fan-control:local --diagnose
```

The diagnostic initializes NVML, reports GPU identity and temperature, shuts down NVML, and exits. It does not change fan policy or speed and needs no configuration file. Compare its UUID and temperature with the host's `nvidia-smi` output. An initialization, enumeration or required telemetry failure returns a nonzero exit status. A normal invocation without GPU access should fail to initialize NVML; this is distinct from a compilation failure.

The hardware process runs as root explicitly. Successful telemetry does not establish fan-write permissions or restoration behavior. This example does not add `privileged`, `SYS_ADMIN`, a Docker socket mount or host PID access.

## Compose and persistent configuration

[compose.yaml](compose.yaml) selects one GPU by UUID and runs the same diagnostic without automatic restarts. It uses an existing image, so build it first. Docker's [Compose GPU documentation](https://docs.docker.com/compose/how-tos/gpu-support/) describes the device reservation.

Prepare a persistent directory, keeping any existing configuration:

```bash
mkdir -p data
test -e data/config.json || cp config.json data/config.json
export NVIDIA_GPU_UUID=GPU-REPLACE-WITH-YOUR-UUID
export FAN_CONTROL_CONFIG_DIR="$(pwd)/data"
docker compose config
docker compose run --rm fan-control
```

`FAN_CONTROL_IMAGE` optionally selects a prebuilt image; its default is `nvidia-fan-control:local`. The configuration directory defaults to `./data`. Compose refuses to create a missing source directory. The entire directory is mounted at `/data`, read-only for this checkpoint, and persists independently of container recreation. The legacy controller reads `/data/config.json` when passed `--config /data/config.json`; the diagnostic ignores configuration. Live configuration is excluded from image builds. Logs and diagnostic output go to stdout/stderr.

To check persistence without GPU access or fan writes:

```bash
docker run --rm --read-only --entrypoint cat \
  --mount "type=bind,src=${FAN_CONTROL_CONFIG_DIR},dst=/data,readonly" \
  nvidia-fan-control:local /data/config.json
```

Repeat after recreating the container and compare with the host file. Configuration is still the original JSON schema: `time_to_update` in seconds and `temperature_ranges` entries containing `min_temperature`, `max_temperature`, `fan_speed` and `hysteresis`. The checked-in [config.json](config.json) is an example, not a validated thermal policy for your hardware. Versioned configuration, editing and controller hardening belong to phase 2.

## TrueNAS

Use Docker-based TrueNAS Apps (24.10 or later). **No TrueNAS release has yet been validated for this change.** Record the exact TrueNAS release, GPU model/UUID and host driver when completing target-host acceptance.

1. Enable NVIDIA support through the supported TrueNAS UI: **Apps > Installed > Configuration > Settings > Install NVIDIA Drivers**. Follow the [TrueNAS initial setup documentation](https://apps.truenas.com/getting-started/initial-setup/); do not install driver packages inside this image or modify the appliance OS from the container.
2. Build the amd64 image on a development or CI machine and make a versioned image available in a registry accessible to the NAS. This repository's build does not itself publish an image. Use an image tag or digest that you have actually published.
3. Create a dataset/directory such as `/mnt/POOL/app-config/fan-control`, copy your configuration into it as `config.json`, and check its permissions.
4. Open **Apps > Discover Apps**, then the menu's **Install via YAML** option, as described in [Installing Custom Apps](https://apps.truenas.com/managing-apps/installing-custom-apps/). Paste `compose.yaml`, replacing the `image`, GPU `device_ids` and bind-mount `source` interpolation expressions with your actual prebuilt image, GPU UUID and absolute dataset path. Do not depend on a development machine's environment variables in the TrueNAS editor.
5. Keep `--diagnose` and `restart: "no"`. Read the app logs and compare UUID/temperature with host telemetry. This one-shot app exits after the check; it is not a persistent dashboard or fan-control service.

No network ports, IPMI devices or disk devices are needed for this phase. A hardware smoke test remains required on the target NAS; a successful container build does not validate NVML injection, fan writes or fan restoration.

## Native build and legacy controller

A native build requires Go and a working C compiler:

```bash
CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o nvidia_fan_control .
./nvidia_fan_control --diagnose
```

The native diagnostic also requires host NVML at runtime. `--config /absolute/path/config.json` selects the legacy controller's configuration; its default remains `config.json` in the working directory. Omitting `--diagnose` runs that existing controller and can write fan settings. It currently controls fan index 0 and does not reliably restore automatic control on exit. Use it only for separately supervised validation with a tested hardware-specific reset operation; unattended systemd/container control waits for phase 2.

## Validation and image size

Validation on 30 October 2026 used Linux amd64 and rootless Podman 5.8.4, without GPU/NVML access. Both images built successfully and were measured with `podman image inspect --format '{{.Size}}'`:

| Final image | Unpacked bytes | MiB |
| --- | ---: | ---: |
| Original CUDA image, unchanged Dockerfile at `9491b8d` | 2,423,705,886 | 2311.43 |
| New Debian slim image | 84,928,386 | 80.99 |

The new image is **96.50% smaller** by this measurement. The build, race tests and vet in the pinned Go builder, runtime checks, Compose validation, and configuration reads across two container recreations passed. The original application was never run. Actual Docker Engine CI and target-host GPU/TrueNAS checks remain to be run.

Run the Go checks without hardware, then inspect the built image:

```bash
CGO_ENABLED=1 go test -race ./...
CGO_ENABLED=1 go vet ./...
sh scripts/check-image.sh nvidia-fan-control:local
docker image inspect nvidia-fan-control:local --format '{{.Os}}/{{.Architecture}} {{.Size}} bytes'
docker run --rm --entrypoint sh nvidia-fan-control:local -c \
  'ldd /usr/local/bin/nvidia-fan-control && dpkg-query -W'
```

`scripts/check-image.sh` enforces the size budget, checks runtime contents and linked libraries, and confirms that the default diagnostic fails clearly without injected NVML. It never requests a GPU. Set `CONTAINER_ENGINE=podman` to run the same checks with Podman. CI also tests and vets inside the pinned Go builder and validates Compose; it does not publish or deploy an image.

The engineering budget is below 150 MiB unpacked (`157286400` bytes). Record Docker's image `.Size` in bytes and divide by `1048576` for MiB. Compare the new and original final images using the same command and engine; registry compressed download size is a different measurement. The original Dockerfile is at commit `9491b8dd8c206cf0c0c10a25910a7a4f0dca2900`:

```bash
legacy_context=$(mktemp -d)
git archive 9491b8dd8c206cf0c0c10a25910a7a4f0dca2900 | tar -x -C "$legacy_context"
docker build --platform linux/amd64 -t nvidia-fan-control:legacy "$legacy_context"
docker image inspect nvidia-fan-control:legacy nvidia-fan-control:local \
  --format '{{.RepoTags}} {{.Size}} bytes'
```

Building the legacy image can require several GB and depends on the old repositories still providing its packages. Record a failed legacy build rather than inventing a size comparison. Do not run the legacy image for this check: its default command actuates fans. A passing `ldd` check only verifies directly linked libraries; the target-host `--diagnose` check verifies dynamically loaded NVML separately.

## License

[MIT](LICENSE), copyright 2025 Max Zanoga.

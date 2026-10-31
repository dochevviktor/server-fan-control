#!/bin/sh
# Hardware-independent checks; deliberately do not request a GPU.
set -eu
engine=${CONTAINER_ENGINE:-docker}
image=${1:-server-fan-control:local}

size=$("$engine" image inspect "$image" --format '{{.Size}}')
awk -v bytes="$size" 'BEGIN { printf "Unpacked image: %s bytes (%.2f MiB)\n", bytes, bytes / 1048576 }'
if [ "$size" -ge 157286400 ]; then
    echo 'Image exceeds the phase-1 budget of 150 MiB unpacked.' >&2
    exit 1
fi

# Expand these variables inside the container, not in this host shell.
# shellcheck disable=SC2016
"$engine" run --rm --read-only --env NVIDIA_VISIBLE_DEVICES=void \
    --entrypoint /bin/sh "$image" -ec '
    test "$(dpkg --print-architecture)" = amd64
    test "$NVIDIA_DRIVER_CAPABILITIES" = utility
    test -z "${LD_LIBRARY_PATH:-}"
    test ! -e /usr/local/cuda
    test ! -e /data/config.json
    test ! -e /src
    command -v ipmitool >/dev/null
    for tool in go gcc cc nvcc node npm; do
        if command -v "$tool" >/dev/null 2>&1; then
            echo "Unexpected runtime tool: $tool" >&2
            exit 1
        fi
    done
    if dpkg-query -W -f="\${binary:Package}\n" | grep -Ei "^(cuda|libcuda|libcublas|libcufft|libcurand|libcusolver|libcusparse|libnpp|libnvjitlink|libnvjpeg|libnvidia|nvidia|nodejs)([-0-9:]|$)"; then
        echo "Unexpected CUDA, driver or Node package in runtime" >&2
        exit 1
    fi
    dependencies=$(ldd /usr/local/bin/server-fan-control)
    printf "%s\n" "$dependencies"
    if printf "%s\n" "$dependencies" | grep -q "not found"; then
        exit 1
    fi
    '

"$engine" run --rm --read-only --env NVIDIA_VISIBLE_DEVICES=void "$image" --help
status=0
output=$("$engine" run --rm --read-only --env NVIDIA_VISIBLE_DEVICES=void "$image" --diagnose 2>&1) || status=$?
printf '%s\n' "$output"
if [ "$status" -ne 1 ]; then
    echo "Expected --diagnose to fail (exit 1) without host NVML; got $status" >&2
    exit 1
fi
printf '%s\n' "$output" | grep -q 'initialize NVML: ERROR_LIBRARY_NOT_FOUND'
status=0
output=$("$engine" run --rm --read-only --env NVIDIA_VISIBLE_DEVICES=void "$image" --diagnose-ipmi 2>&1) || status=$?
printf '%s\n' "$output"
if [ "$status" -ne 1 ]; then
    echo "Expected --diagnose-ipmi to fail (exit 1) without a local IPMI device; got $status" >&2
    exit 1
fi
echo 'No-GPU image checks passed. Target-host NVML control/reset still requires validation.'

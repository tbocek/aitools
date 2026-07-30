#!/usr/bin/env bash

set -Eeuo pipefail

SCRIPT_DIR="$(dirname "$0")"
MODELS_DIR="/mnt/models"
# ROCM0 for HIP/ROCm, Vulkan0 for Vulkan
LLAMA_DEVICE=Vulkan0
SD_DEVICE=ROCM0

no_cache_arch=''
no_cache_llama=''
skip_build=false
sd_args=''

while :; do
    case "${1-}" in
    -h | --help)
        echo "Usage: $(basename "$0") [-h] [--no-cache] [--no-cache-llama] [--skip-build] [--sd-args ARGS]"
        echo ""
        echo "  --no-cache          Rebuild all images without Docker cache"
        echo "  --no-cache-llama    Rebuild only llama image without Docker cache"
        echo "  --skip-build        Skip building entirely"
        echo "  --sd-args ARGS      Extra args passed verbatim to sd-server"
        echo "                      (e.g. \"--diffusion-model /mnt/models/sd/foo.safetensors --vae /mnt/models/sd/bar.safetensors\")"
        echo "  -h, --help          Print this help and exit"
        exit 0 ;;
    --no-cache) no_cache_arch='--no-cache'; no_cache_llama='--no-cache' ;;
    --no-cache-llama) no_cache_llama='--no-cache' ;;
    --skip-build) skip_build=true ;;
    --sd-args) sd_args="${2-}"; shift ;;
    -?*) echo "Unknown option: $1" >&2; exit 1 ;;
    *) break ;;
    esac
    shift
done

export LLAMA_DEVICE
export SD_ARGS="$sd_args"

# sd-server has no --device flag; parse SD_DEVICE (e.g. ROCM0, Vulkan0) and
# hide the unwanted backend via env vars. Empty string disables: ggml-vulkan
# parses GGML_VK_VISIBLE_DEVICES as unsigned and throws on -1; HIP runtime
# treats empty visibility list as zero devices.
if [[ "$SD_DEVICE" =~ ^[Vv]ulkan([0-9]+)$ ]]; then
    export SD_HIP_VISIBLE=""
    export SD_VK_VISIBLE="${BASH_REMATCH[1]}"
elif [[ "$SD_DEVICE" =~ ^ROCM([0-9]+)$ ]]; then
    export SD_HIP_VISIBLE="${BASH_REMATCH[1]}"
    export SD_VK_VISIBLE=""
else
    echo "SD_DEVICE must be ROCMn or Vulkann, got: $SD_DEVICE" >&2
    exit 1
fi

# Download missing models from HuggingFace in the background
download_models() {
    current_url=""

    download_file() {
        local url="$1"
        local dest="$2"
        [[ -f "$dest" ]] && return 0
        echo "Downloading $(basename "$dest")..."
        curl -L --fail -C - -o "${dest}.part" "$url"
        mv "${dest}.part" "$dest"
    }

    while IFS= read -r line; do
        # Reset on new section
        if [[ "$line" =~ ^\[ ]]; then
            current_url=""
        fi

        # A "# url" comment applies to the next /mnt/models path, whatever its key
        if [[ "$line" =~ ^#\ url\ =\ (.*) ]]; then
            current_url="${BASH_REMATCH[1]}"
            continue
        fi

        # Any key pointing into /mnt/models (model, mmproj, spec-draft-model, ...)
        if [[ "$line" =~ ^[a-z-]+\ =\ /mnt/models/(.+) ]]; then
            filename="${BASH_REMATCH[1]}"
            [[ -z "$current_url" ]] && continue
            remote="${current_url##*/}"

            # ---- SHARDED GGUF HANDLING ----
            if [[ "$filename" =~ -([0-9]+)-of-([0-9]+)\.gguf$ ]]; then
                url_dir="${current_url%/*}"
                total="${BASH_REMATCH[2]}"
                base=$(echo "$filename" | sed -E 's/-[0-9]+-of-[0-9]+\.gguf//')
                remote_base=$(echo "$remote" | sed -E 's/-[0-9]+-of-[0-9]+\.gguf//')

                echo "Detected sharded GGUF ($total parts): $base"

                for i in $(seq -f "%05g" 1 "$total"); do
                    shard="${remote_base}-${i}-of-${total}.gguf"
                    download_file "${url_dir}/${shard}" "${MODELS_DIR}/${base}-${i}-of-${total}.gguf"
                done

            # ---- SINGLE FILE ----
            else
                download_file "$current_url" "${MODELS_DIR}/${filename}"
            fi

            # Consumed: don't let this url leak onto the next path in the section
            current_url=""
        fi

    done < "$SCRIPT_DIR/config.ini"
}

# Expose the voice changer output as a virtual mic for other apps (Discord,
# OBS, Zoom, ...): the browser plays the converted audio into the VoiceChanger
# null sink, and VoiceChangerMic republishes its monitor as a real capture
# device -- most apps hide raw ".monitor" sources, hence the remap.
setup_virtual_mic() {
    command -v pactl >/dev/null 2>&1 || { echo "pactl not found, skipping virtual mic"; return 0; }
    pactl info >/dev/null 2>&1     || { echo "no PipeWire/Pulse session, skipping virtual mic"; return 0; }

    if ! pactl list short sinks | awk '{print $2}' | grep -qx VoiceChanger; then
        pactl load-module module-null-sink \
            media.class=Audio/Sink \
            sink_name=VoiceChanger \
            channel_map=stereo \
            sink_properties=device.description=VoiceChanger >/dev/null
        echo "Created virtual sink: VoiceChanger"
    fi

    if ! pactl list short sources | awk '{print $2}' | grep -qx VoiceChangerMic; then
        pactl load-module module-remap-source \
            master=VoiceChanger.monitor \
            source_name=VoiceChangerMic \
            source_properties=device.description=VoiceChangerMic >/dev/null
        echo "Created virtual mic: VoiceChangerMic"
    fi
}

# Create bind-mount dirs up front: docker would create missing ones as root,
# but the containers run as uid 1000 and could not write into them.
mkdir -p "${MODELS_DIR}"/{sd,ace,vc} "${MODELS_DIR}"/applio/{models,logs}
cp "$SCRIPT_DIR/config.ini" "${MODELS_DIR}/config.ini"

download_models &
DOWNLOAD_PID=$!

if [ "$skip_build" = false ]; then
    buildx_args="--output type=docker,compression=zstd,compression-level=3"
    docker buildx build $no_cache_arch  $buildx_args -t arch:latest  -f "$SCRIPT_DIR/Dockerfile.arch"  "$SCRIPT_DIR"
    #docker buildx build $no_cache_llama $buildx_args -t llama:latest -f "$SCRIPT_DIR/Dockerfile.llama" "$SCRIPT_DIR"
    #docker buildx build $no_cache_llama $buildx_args -t sd:latest    -f "$SCRIPT_DIR/Dockerfile.sd"    "$SCRIPT_DIR"
    docker buildx build $no_cache_llama $buildx_args -t vc:latest    -f "$SCRIPT_DIR/Dockerfile.vc"    "$SCRIPT_DIR"
    docker buildx build $no_cache_llama $buildx_args -t vc2:latest   -f "$SCRIPT_DIR/Dockerfile.vc2"   "$SCRIPT_DIR"
fi
setup_virtual_mic
docker compose up -d vc vc2

wait "$DOWNLOAD_PID"

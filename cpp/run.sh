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
    current_mmproj_url=""

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
            current_mmproj_url=""
        fi

        # Capture URLs
        if [[ "$line" =~ ^#\ url\ =\ (.*) ]]; then
            current_url="${BASH_REMATCH[1]}"
            continue
        fi
        if [[ "$line" =~ ^#\ mmproj-url\ =\ (.*) ]]; then
            current_mmproj_url="${BASH_REMATCH[1]}"
            continue
        fi

        # Capture model or mmproj path
        if [[ "$line" =~ ^(model|mmproj)\ =\ /mnt/models/(.+) ]]; then
            kind="${BASH_REMATCH[1]}"
            filename="${BASH_REMATCH[2]}"

            # ---- SHARDED GGUF HANDLING ----
            if [[ "$filename" =~ -([0-9]+)-of-([0-9]+)\.gguf$ ]]; then
                [[ -z "$current_url" ]] && continue
                url_dir="${current_url%/*}"
                total="${BASH_REMATCH[2]}"
                base=$(echo "$filename" | sed -E 's/-[0-9]+-of-[0-9]+\.gguf//')

                echo "Detected sharded GGUF ($total parts): $base"

                for i in $(seq -f "%05g" 1 "$total"); do
                    shard="${base}-${i}-of-${total}.gguf"
                    download_file "${url_dir}/${shard}" "${MODELS_DIR}/${shard}"
                done

            # ---- SINGLE FILE (GGUF or mmproj) ----
            else
                if [[ "$kind" == "mmproj" && -n "$current_mmproj_url" ]]; then
                    file_url="$current_mmproj_url"
                elif [[ -n "$current_url" ]]; then
                    file_url="${current_url%/*}/${filename}"
                else
                    continue
                fi
                download_file "$file_url" "${MODELS_DIR}/${filename}"
            fi
        fi

    done < "$SCRIPT_DIR/config.ini"
}

mkdir -p "${MODELS_DIR}"
cp "$SCRIPT_DIR/config.ini" "${MODELS_DIR}/config.ini"

download_models &
DOWNLOAD_PID=$!

if [ "$skip_build" = false ]; then
    buildx_args="--output type=docker,compression=zstd,compression-level=3"
    docker buildx build $no_cache_arch  $buildx_args -t arch:latest  -f "$SCRIPT_DIR/Dockerfile.arch"  "$SCRIPT_DIR"
    docker buildx build $no_cache_llama $buildx_args -t llama:latest -f "$SCRIPT_DIR/Dockerfile.llama" "$SCRIPT_DIR"
    docker buildx build $no_cache_llama $buildx_args -t sd:latest    -f "$SCRIPT_DIR/Dockerfile.sd"    "$SCRIPT_DIR"
fi
docker compose up -d llama #sd

wait "$DOWNLOAD_PID"

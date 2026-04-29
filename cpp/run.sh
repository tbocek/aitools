#!/usr/bin/env bash

set -Eeuo pipefail

SCRIPT_DIR="$(dirname "$0")"
MODELS_DIR="/mnt/models"
# ROCM0 for HIP/ROCm, Vulkan0 for Vulkan
LLAMA_DEVICE=ROCM0
SD_DEVICE=0
SD_BACKEND=rocm

no_cache_arch=''
no_cache_llama=''
skip_build=false

while :; do
    case "${1-}" in
    -h | --help)
        echo "Usage: $(basename "$0") [-h] [--no-cache] [--no-cache-llama] [--skip-build]"
        echo ""
        echo "  --no-cache          Rebuild all images without Docker cache"
        echo "  --no-cache-llama    Rebuild only llama image without Docker cache"
        echo "  --skip-build        Skip building entirely"
        echo "  -h, --help          Print this help and exit"
        exit 0 ;;
    --no-cache) no_cache_arch='--no-cache'; no_cache_llama='--no-cache' ;;
    --no-cache-llama) no_cache_llama='--no-cache' ;;
    --skip-build) skip_build=true ;;
    -?*) echo "Unknown option: $1" >&2; exit 1 ;;
    *) break ;;
    esac
    shift
done

export LLAMA_DEVICE
export SD_DEVICE
export SD_BACKEND

# Download missing models from HuggingFace in the background
download_models() {
    current_url=""

    while IFS= read -r line; do
        # Reset on new section
        if [[ "$line" =~ ^\[ ]]; then
            current_url=""
        fi

        # Capture URL
        if [[ "$line" =~ ^#\ url\ =\ (.*) ]]; then
            current_url="${BASH_REMATCH[1]}"
            continue
        fi

        # Capture model or mmproj path
        if [[ "$line" =~ (model|mmproj)\ =\ /mnt/models/(.+) ]]; then
            filename="${BASH_REMATCH[2]}"
            [[ -z "$current_url" ]] && continue

            # Extract repo_id and repo_path from URL
            repo_id=$(echo "$current_url" | sed -E 's|https://huggingface.co/([^/]+/[^/]+)/.*|\1|')
            repo_base_path=$(echo "$current_url" | sed -E 's|.*/resolve/main/||')
            subdir=$(dirname "$repo_base_path")

            # ---- SHARDED GGUF HANDLING ----
            if [[ "$filename" =~ -([0-9]+)-of-([0-9]+)\.gguf$ ]]; then
                total="${BASH_REMATCH[2]}"
                base=$(echo "$filename" | sed -E 's/-[0-9]+-of-[0-9]+\.gguf//')

                echo "Detected sharded GGUF ($total parts): $base"

                for i in $(seq -f "%05g" 1 "$total"); do
                    shard="${base}-${i}-of-${total}.gguf"

                    test -f "${MODELS_DIR}/${shard}" || {
                        echo "Downloading ${shard}..."
                        huggingface-cli download "$repo_id" \
                            "${subdir}/${shard}" \
                            --local-dir "$MODELS_DIR" \
                            --local-dir-use-symlinks False
                    }
                done

            # ---- SINGLE FILE (GGUF or mmproj) ----
            else
                test -f "${MODELS_DIR}/${filename}" || {
                    echo "Downloading ${filename}..."
                    huggingface-cli download "$repo_id" \
                        "${subdir}/${filename}" \
                        --local-dir "$MODELS_DIR" \
                        --local-dir-use-symlinks False
                }
            fi
        fi

    done < "$SCRIPT_DIR/config.ini"
}

mkdir -p "${MODELS_DIR}"
cp "$SCRIPT_DIR/config.ini" "${MODELS_DIR}/config.ini"
mkdir -p "${MODELS_DIR}/sd/{checkpoints,unet,vae,text_encoders,embeddings,loras,taesd,photomaker,upscale_models,controlnet}" 2>/dev/null || true

download_models &
DOWNLOAD_PID=$!

if [ "$skip_build" = false ]; then
    docker build $no_cache_arch -t arch:latest -f "$SCRIPT_DIR/Dockerfile.arch" "$SCRIPT_DIR"
    docker compose build $no_cache_llama llama sd
fi
docker compose up -d llama sd

wait "$DOWNLOAD_PID"

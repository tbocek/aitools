#!/usr/bin/env bash
set -Eeuo pipefail

BACKEND="${1:-vulkan}"

if [[ "$BACKEND" != "vulkan" && "$BACKEND" != "rocm" ]]; then
  echo "Usage: $(basename "$0") [vulkan|rocm]"
  exit 1
fi

export LLAMA_BACKEND="$BACKEND"
export SD_BACKEND="$BACKEND"
export ACE_BACKEND="$BACKEND"

docker build . -f Dockerfile.arch -t arch
docker-compose up --build

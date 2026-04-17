#!/usr/bin/env bash
set -Eeuo pipefail

# ROCM0 for HIP/ROCm, Vulkan0 for Vulkan
export LLAMA_DEVICE=Vulkan0
# rocm or vulkan
export SD_BACKEND=rocm
export ACE_BACKEND=vulkan

docker build . -f Dockerfile.arch -t arch
docker-compose up --build

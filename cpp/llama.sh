#!/bin/bash
set -e

docker build . -t cpp
docker run -it \
    --device=/dev/kfd \
    --device=/dev/dri \
    --group-add=$(getent group render | cut -d: -f3) \
    --group-add=$(getent group video | cut -d: -f3) \
    --security-opt seccomp=unconfined \
    -e CUDA_VISIBLE_DEVICES=0 \
    -e HIP_VISIBLE_DEVICES=0 \
    -e HSA_OVERRIDE_GFX_VERSION=gfx1100 \
    -p 9001:9001 \
    -v /mnt/models:/models \
    cpp:latest /home/cpp/llama.cpp/build/bin/llama-server \
        --model /models/mistralai_Devstral-Small-2505-Q5_K_L.gguf \
        --ctx-size 60000 \
        --n-gpu-layers 9999 \
        --threads 64 \
        --flash-attn \
        --cache-type-k f16 \
        --cache-type-v q8_0 \
        --port 9001 \
        --mlock \
        --no-mmap \
        --jinja

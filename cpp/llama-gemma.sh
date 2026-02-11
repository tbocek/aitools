#!/bin/bash
set -e

docker build . -t cpp
docker run -it \
    --device=/dev/kfd \
    --device=/dev/dri \
    --group-add render \
    --group-add video \
    --security-opt seccomp=unconfined \
    --ulimit memlock=-1:-1 \
    -e GGML_VK_VISIBLE_DEVICES=0 \
    -e CUDA_VISIBLE_DEVICES=0 \
    -p 9001:9001 \
    -v /mnt/models:/models \
    cpp:latest /home/arch/llama.cpp/build/bin/llama-server \
        --model /models/gemma-3-27b-it-abliterated.q5_k_m.gguf \
        --mmproj /models/mmproj-mlabonne_gemma-3-27b-it-abliterated-f16.gguf \
        --ctx-size 20000 \
        --n-gpu-layers 9999 \
        --threads 64 \
        --flash-attn \
        --cache-type-k f16 \
        --cache-type-v q8_0 \
        --port 9001 \
        --host 0.0.0.0 \
        --mlock \
        --no-mmap \
        --jinja

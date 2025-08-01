#!/bin/bash
set -e

docker build . -t cpp
docker run -it \
    --device=/dev/kfd \
    --device=/dev/dri \
    --cap-add=SYS_PTRACE \
    --group-add render \
    --group-add video \
    --security-opt seccomp=unconfined \
    -e CUDA_VISIBLE_DEVICES=0 \
    -p 9001:9001 \
    -v /mnt/models:/models \
    -v /opt/rocm/lib:/opt/rocm/lib:ro \
    cpp:latest /home/cpp/llama.cpp/build/bin/llama-server \
        --model /models/mistralai_Devstral-Small-2505-Q5_K_L.gguf \
        --ctx-size 60000 \
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

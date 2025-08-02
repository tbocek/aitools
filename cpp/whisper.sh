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
    -e CUDA_VISIBLE_DEVICES=0 \
    -p 9000:9000 \
    -v /mnt/models:/models \
    -v /opt/rocm/lib:/opt/rocm/lib:ro \
    cpp:latest /home/arch/whisper.cpp/build/bin/whisper-server \
        --model /models/ggml-large-v3.bin \
        --flash-attn \
        --port 9000 \
        --host 0.0.0.0 \
        --inference-path /v1/audio/transcriptions \
        --convert \
        --no-context
    
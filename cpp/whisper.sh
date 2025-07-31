#!/bin/bash
set -e

docker build . -t cpp
docker run -it \
    --device=/dev/kfd \
    --device=/dev/dri \
    --group-add=video \
    -e CUDA_VISIBLE_DEVICES=0
    -p 9000:9000 \
    -v /mnt/models:/models \
    cpp:latest /home/cpp/whisper.cpp/build/bin/whisper-server \
        --model /models/ggml-large-v3.bin \
        --flash-attn \
        --port 9000 \
        --inference-path /v1/audio/transcriptions \
        --convert \
        --no-context
    
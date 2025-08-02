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
    -p 7860:7860 \
    -v /mnt/models/sd:/home/arch/sd.cpp-webui/models \
    cpp:latest /home/arch/sd.cpp-webui/sdcpp_webui.sh --listen
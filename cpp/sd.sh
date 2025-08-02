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
    -e GGML_VK_VISIBLE_DEVICES=1 \
    -p 7860:7860 \
    -v /mnt/models/sd:/home/cpp/sd.cpp-webui/models \
    -v /opt/rocm/lib:/opt/rocm/lib:ro \
    cpp:latest /home/cpp/sd.cpp-webui/sdcpp_webui.sh --listen
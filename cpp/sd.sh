#!/bin/bash
set -e

docker build . -t cpp
#Multi GPU does not work well, so only expose one card
docker run -it \
    --device=/dev/kfd \
    --device=/dev/dri/card1 \
    --device=/dev/dri/renderD128 \
    --group-add render \
    --group-add video \
    --security-opt seccomp=unconfined \
    --ulimit memlock=-1:-1 \
    -p 7860:7860 \
    -v /mnt/models/sd:/home/cpp/sd.cpp-webui/models \
    -v /opt/rocm/lib:/opt/rocm/lib:ro \
    cpp:latest /home/cpp/sd.cpp-webui/sdcpp_webui.sh --listen
#!/bin/bash
set -e

docker build . -t cpp
docker run -it \
    --device=/dev/kfd \
    --device=/dev/dri \
    --group-add=video \
    -e CUDA_VISIBLE_DEVICES=0
    -p 7860:7860 \
    -v /mnt/models/sd:/home/cpp/sd.cpp-webui/models \
    cpp:latest /home/cpp/sd.cpp-webui/sdcpp_webui.sh \
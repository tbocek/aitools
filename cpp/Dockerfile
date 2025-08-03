FROM archlinux:latest

RUN pacman -Syu --noconfirm && \
    pacman -S --noconfirm \
        git \
        go \
        libxml2 \
        libxml2-legacy \
        base-devel \
        ninja \
        python3 \
        linux-headers \
        clang \
        cmake && \
    pacman -Scc --noconfirm   

# Create non-root user and add to video, render, and wheel groups
RUN useradd -m -s /bin/bash arch && \
    usermod -aG video arch && \
    usermod -aG render arch && \
    usermod -aG wheel arch && \
    echo '%wheel ALL=(ALL) NOPASSWD: ALL' >> /etc/sudoers

USER arch
WORKDIR /home/arch

# Install yay AUR helper
RUN git clone https://aur.archlinux.org/yay.git && \
    cd yay && makepkg -si --noconfirm && \
    cd .. && rm -rf yay
    
# Base installed, lets get specific
RUN yay -S --noconfirm \
  rocwmma \
  rocm-hip-sdk \
  ffmpeg \
  vulkan-headers \
  vulkan-icd-loader \
  vulkan-radeon \
  && sudo pacman -Scc --noconfirm


#LLAMA.CPP
#https://github.com/ggml-org/llama.cpp/blob/master/docs/build.md
RUN git clone https://github.com/ggml-org/llama.cpp
RUN mv llama.cpp llama.cpp.rocm && cp -r llama.cpp.rocm llama.cpp.vulkan
ENV HIPCXX="/opt/rocm/lib/llvm/bin/clang" 
ENV HIP_PATH="/opt/rocm"
RUN cd llama.cpp.rocm && cmake -B build \
    -DLLAMA_CURL=ON \
    -DGGML_HIP_ROCWMMA_FATTN=ON \
    -DGGML_HIP=ON \
    -DAMDGPU_TARGETS=gfx1100 \
    -DGGML_CUDA_FA_ALL_QUANTS=1 \
    -DCMAKE_BUILD_TYPE=Release
RUN cd llama.cpp.rocm && cmake --build build --config Release --parallel
RUN cd llama.cpp.vulkan && cmake -B build \
    -DLLAMA_CURL=ON \
    -DGGML_VULKAN=ON \
    -DCMAKE_BUILD_TYPE=Release
RUN cd llama.cpp.vulkan && cmake --build build --config Release --parallel


#WHISPER.CPP
#https://github.com/ggml-org/whisper.cpp
RUN git clone https://github.com/ggml-org/whisper.cpp.git
RUN mv whisper.cpp whisper.cpp.rocm && cp -r whisper.cpp.rocm whisper.cpp.vulkan
ENV HIPCXX="/opt/rocm/lib/llvm/bin/clang" 
ENV HIP_PATH="/opt/rocm"
RUN cd whisper.cpp.rocm && cmake -B build \
    -DLLAMA_CURL=ON \
    -DGGML_HIP_ROCWMMA_FATTN=ON \
    -DGGML_HIP=ON \
    -DAMDGPU_TARGETS=gfx1100 \
    -DGGML_CUDA_FA_ALL_QUANTS=1 \
    -DWHISPER_FFMPEG=yes \
    -DCMAKE_BUILD_TYPE=Release
RUN cd whisper.cpp.rocm && cmake --build build --config Release --parallel
RUN cd whisper.cpp.vulkan && cmake -B build \
    -DLLAMA_CURL=ON \
    -DGGML_VULKAN=ON \
    -DWHISPER_FFMPEG=yes \
    -DCMAKE_BUILD_TYPE=Release
RUN cd whisper.cpp.vulkan && cmake --build build --config Release --parallel

#STABLE-DIFFUSION.CPP
#https://github.com/leejet/stable-diffusion.cpp
RUN git clone --recursive https://github.com/leejet/stable-diffusion.cpp
RUN mv stable-diffusion.cpp stable-diffusion.cpp.rocm && cp -r stable-diffusion.cpp.rocm stable-diffusion.cpp.vulkan
RUN cd stable-diffusion.cpp.rocm && cmake -B build -G "Ninja" \
    -DCMAKE_C_COMPILER=/opt/rocm/lib/llvm/bin/clang \
    -DCMAKE_CXX_COMPILER=/opt/rocm/lib/llvm/bin/clang++ \
    -DSD_HIPBLAS=ON \
    -DCMAKE_BUILD_TYPE=Release \
    -DAMDGPU_TARGETS=gfx1100
RUN cd stable-diffusion.cpp.rocm && cmake --build build --config Release --parallel
RUN cd stable-diffusion.cpp.vulkan && cmake -B build -G "Ninja" \
    -DSD_VULKAN=ON \
    -DCMAKE_BUILD_TYPE=Release \
    -DAMDGPU_TARGETS=gfx1100
RUN cd stable-diffusion.cpp.vulkan && cmake --build build --config Release --parallel
#https://github.com/daniandtheweb/sd.cpp-webui
RUN git clone https://github.com/daniandtheweb/sd.cpp-webui.git
RUN cp stable-diffusion.cpp/build/bin/sd sd.cpp-webui
RUN cd sd.cpp-webui && \
    chmod a+x sdcpp_webui.sh && \
    python3 -m venv venv && \
    source venv/bin/activate && \
    pip install -r requirements.txt --upgrade pip

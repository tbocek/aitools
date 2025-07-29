# Dockerfile with GGUF support using optimized compilation flags
FROM archlinux:latest

# Update system and install base packages including AUR helper dependencies
RUN pacman -Syu --noconfirm && \
    pacman -S --noconfirm \
        python \
        python-pip \
        python-virtualenv \
        git \
        wget \
        curl \
        base-devel \
        cmake \
        pkgconf \
        libjpeg-turbo \
        libpng \
        libwebp \
        opencv \
        mesa \
        glib2 \
        libsm \
        libxext \
        libxrender \
        ffmpeg \
        bc \
        sudo \
        cmake \
        libxml2 \
    && pacman -Scc --noconfirm

RUN ln -s /usr/lib/libxml2.so.16.0.5 /usr/lib/libxml2.so.2
    
# Create non-root user and add to video, render, and wheel groups
RUN useradd -m -s /bin/bash comfyui && \
    usermod -aG video comfyui && \
    usermod -aG render comfyui && \
    usermod -aG wheel comfyui && \
    echo '%wheel ALL=(ALL) NOPASSWD: ALL' >> /etc/sudoers

# Switch to comfyui user for AUR operations
USER comfyui
WORKDIR /home/comfyui

# Install yay AUR helper
RUN git clone https://aur.archlinux.org/yay.git && \
    cd yay && \
    makepkg -si --noconfirm && \
    cd .. && \
    rm -rf yay

# Install opencl-amd-dev from AUR (repackaged Ubuntu ROCm with HIP support)
RUN yay -S --noconfirm opencl-amd-dev sentencepiece

# Verify ROCm installation
RUN /opt/rocm/bin/rocminfo || echo "ROCm info not available (expected in container without GPU)"

# Switch back to root to set proper permissions, then back to comfyui user
USER root
RUN chown -R comfyui:comfyui /home/comfyui
USER comfyui
WORKDIR /home/comfyui

# Create Python virtual environment
RUN python -m venv venv
ENV PATH="/home/comfyui/venv/bin:$PATH"

# Upgrade pip and install base Python packages
RUN pip install --upgrade pip setuptools wheel build installer

# Install PyTorch with ROCm support
RUN pip install --pre torch torchvision torchaudio \
    --index-url https://download.pytorch.org/whl/nightly/rocm6.4/

# Clone ComfyUI
RUN git clone https://github.com/comfyanonymous/ComfyUI.git

# Install ComfyUI requirements
WORKDIR /home/comfyui/ComfyUI
RUN pip install -r requirements.txt

# Install additional dependencies for GGUF support
RUN pip install \
    gguf \
    huggingface-hub \
    safetensors \
    accelerate \
    transformers \
    sentencepiece \
    protobuf

# Install llama-cpp-python with your optimized ROCm compilation flags
ENV CMAKE_ARGS="-DLLAMA_CURL=ON -DGGML_HIP_ROCWMMA_FATTN=ON -DGGML_HIP=ON -DAMDGPU_TARGETS=gfx1100 -DGGML_CUDA_FA_ALL_QUANTS=1 -DCMAKE_BUILD_TYPE=Release"
ENV FORCE_CMAKE=1
ENV HIP_PLATFORM=amd
ENV HIP_PATH="/opt/rocm"
ENV HIPCXX="/opt/rocm/lib/llvm/bin/clang++"
RUN pip install llama-cpp-python --no-cache-dir

# Clone ComfyUI-GGUF extension
WORKDIR /home/comfyui/ComfyUI/custom_nodes
RUN git clone https://github.com/city96/ComfyUI-GGUF.git

# Install GGUF extension requirements
WORKDIR /home/comfyui/ComfyUI/custom_nodes/ComfyUI-GGUF
RUN if [ -f requirements.txt ]; then \
        pip install -r requirements.txt; \
    else \
        pip install numpy pillow; \
    fi

# Create models directories (including GGUF)
WORKDIR /home/comfyui/ComfyUI
RUN mkdir -p models/checkpoints \
    models/vae \
    models/loras \
    models/controlnet \
    models/upscale_models \
    models/embeddings \
    models/diffusers \
    models/unet \
    models/clip \
    models/gligen \
    models/hypernetworks \
    models/photomaker \
    models/classifiers \
    models/gguf \
    input \
    output \
    temp

# Set proper permissions
USER root
RUN chown -R comfyui:comfyui /home/comfyui
USER comfyui

# Copy startup script
COPY start_comfyui.sh /home/comfyui/start_comfyui.sh
RUN sudo chmod +x /home/comfyui/start_comfyui.sh

# Expose ComfyUI port
EXPOSE 8188

# Set working directory
WORKDIR /home/comfyui/ComfyUI

# Default command
CMD ["/home/comfyui/start_comfyui.sh"]
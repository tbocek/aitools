#!/bin/bash
set -e

echo "Starting ComfyUI with ROCm 6.4 support..."
echo "ROCm Path: $ROCM_PATH"
echo "HIP Visible Devices: $HIP_VISIBLE_DEVICES"

# Activate virtual environment
source /home/comfyui/venv/bin/activate

# Check ROCm installation
echo "=== ROCm Information ==="
rocminfo | head -20 || echo "ROCm info not available"
echo

# Check PyTorch ROCm availability
echo "=== PyTorch ROCm Check ==="
python3 -c "import torch; print(f'PyTorch version: {torch.__version__}'); print(f'ROCm available: {torch.cuda.is_available()}'); print(f'Device count: {torch.cuda.device_count()}')"
if python3 -c "import torch; torch.cuda.is_available()" 2>/dev/null; then
    python3 -c "import torch; [print(f'GPU {i}: {torch.cuda.get_device_name(i)}') for i in range(torch.cuda.device_count())]"
fi
echo

# Set ROCm environment variables for different GPU architectures
export HSA_OVERRIDE_GFX_VERSION=11.0.0  # Default for RDNA3 (gfx1100)
export PYTORCH_ROCM_ARCH=gfx1100

# Auto-detect GPU architecture if possible
if command -v rocminfo >/dev/null 2>&1; then
    GPU_ARCH=$(rocminfo | grep -o 'gfx[0-9a-f]*' | head -1)
    if [ ! -z "$GPU_ARCH" ]; then
        echo "Detected GPU architecture: $GPU_ARCH"
        export HSA_OVERRIDE_GFX_VERSION=${GPU_ARCH#gfx}
        export PYTORCH_ROCM_ARCH=$GPU_ARCH
        
        # Set architecture-specific optimizations
        case $GPU_ARCH in
            gfx1100|gfx1101|gfx1102)
                echo "RDNA3 GPU detected - enabling RDNA3 optimizations"
                export HSA_OVERRIDE_GFX_VERSION=11.0.0
                ;;
            gfx1030|gfx1031|gfx1032)
                echo "RDNA2 GPU detected - enabling RDNA2 optimizations"
                export HSA_OVERRIDE_GFX_VERSION=10.3.0
                ;;
            gfx900|gfx906|gfx908|gfx90a)
                echo "Vega/MI GPU detected - using native architecture"
                ;;
            *)
                echo "Unknown GPU architecture, using default settings"
                ;;
        esac
    fi
fi

echo "Using GPU architecture: $PYTORCH_ROCM_ARCH"
echo "HSA override: $HSA_OVERRIDE_GFX_VERSION"
echo

# Set additional ROCm optimizations
export ROCM_PATH=/opt/rocm
export HIP_VISIBLE_DEVICES=${HIP_VISIBLE_DEVICES:-0}
export GPU_MAX_ALLOC_PERCENT=90
export GPU_SINGLE_ALLOC_PERCENT=90

# Start ComfyUI
cd /home/comfyui/ComfyUI
echo "Starting ComfyUI server..."
echo "Access ComfyUI at: http://localhost:8188"
echo "Press Ctrl+C to stop"
echo
python3 main.py --listen 0.0.0.0 --port 8188 "${@}"
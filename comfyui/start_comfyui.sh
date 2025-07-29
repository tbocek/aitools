#!/bin/bash
set -e

echo "Starting ComfyUI with ROCm 6.4 support..."
echo "ROCm Path: $ROCM_PATH"
echo "HIP Visible Devices: $HIP_VISIBLE_DEVICES"

# Activate virtual environment
source /home/comfyui/venv/bin/activate

# Check PyTorch ROCm availability
echo "=== PyTorch ROCm Check ==="
python3 -c "import torch; print(f'PyTorch version: {torch.__version__}'); print(f'ROCm available: {torch.cuda.is_available()}'); print(f'Device count: {torch.cuda.device_count()}')"
if python3 -c "import torch; torch.cuda.is_available()" 2>/dev/null; then
    python3 -c "import torch; [print(f'GPU {i}: {torch.cuda.get_device_name(i)}') for i in range(torch.cuda.device_count())]"
fi

# Set ROCm environment variables for different GPU architectures
export HSA_OVERRIDE_GFX_VERSION=11.0.0  # Default for RDNA3 (gfx1100)
export PYTORCH_ROCM_ARCH=gfx1100

# Set additional ROCm optimizations
export ROCM_PATH=/opt/rocm
export HIP_VISIBLE_DEVICES=${HIP_VISIBLE_DEVICES:-1}
export GPU_MAX_ALLOC_PERCENT=90
export GPU_SINGLE_ALLOC_PERCENT=90

# Start ComfyUI
cd /home/comfyui/ComfyUI
echo "Starting ComfyUI server..."
echo "Access ComfyUI at: http://localhost:8188"
echo "Press Ctrl+C to stop"
echo
python3 main.py --listen 0.0.0.0 --port 8188 "${@}"
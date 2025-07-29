#!/bin/bash

# ComfyUI ROCm Docker Runner Script
# Based on: https://www.archy.net/self-hosted-comfyui-install-ubuntu-24-02/

set -e

# Configuration
IMAGE_NAME="comfyui-rocm-gguf"
CONTAINER_NAME="comfyui-rocm-gguf-container"
HOST_PORT="8188"
CONTAINER_PORT="8188"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
MODELS_DIR="$SCRIPT_DIR/comfyui-models"
INPUT_DIR="$SCRIPT_DIR/comfyui-input"
OUTPUT_DIR="$SCRIPT_DIR/comfyui-output"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

print_status() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

print_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

print_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

print_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Function to check if Docker is running
check_docker() {
    if ! command -v docker &> /dev/null; then
        print_error "Docker is not installed"
        exit 1
    fi

    if ! systemctl is-active --quiet docker; then
        print_error "Docker service is not running"
        print_status "Start Docker service with: sudo systemctl start docker"
        exit 1
    fi
}

# Function to build Docker image
build_image() {
    print_status "Building ComfyUI ROCm Docker image..."

    docker build -t "$IMAGE_NAME" .

    if [ $? -eq 0 ]; then
        print_success "Docker image '$IMAGE_NAME' built successfully"
    else
        print_error "Failed to build Docker image"
        exit 1
    fi
}

# Function to run the container
run_container() {
    print_status "Starting ComfyUI container..."

    docker run \
        --name "$CONTAINER_NAME" \
        --device=/dev/kfd \
        --device=/dev/dri \
        --group-add video \
        --cap-add=SYS_PTRACE \
        --security-opt seccomp=unconfined \
        --ipc=host \
        --shm-size=8G \
        -p "${HOST_PORT}:${CONTAINER_PORT}" \
        -v "$MODELS_DIR:/home/comfyui/ComfyUI/models" \
        -v "$INPUT_DIR:/home/comfyui/ComfyUI/input" \
        -v "$OUTPUT_DIR:/home/comfyui/ComfyUI/output" \
        -e HIP_VISIBLE_DEVICES=0 \
        -e ROCM_PATH=/opt/rocm \
        -it "$IMAGE_NAME"

    if [ $? -eq 0 ]; then
        print_success "Container started successfully"
        print_success "ComfyUI will be available at: http://localhost:${HOST_PORT}"
        print_status "Container name: $CONTAINER_NAME"
    else
        print_error "Failed to start container"
        exit 1
    fi
}

# Help function
show_help() {
    echo "ComfyUI ROCm Docker Runner"
    echo "Usage: $0 [COMMAND]"
    echo ""
    echo "Commands:"
    echo "  run        Run the ComfyUI container (default)"
    echo "  build      Build the Docker image"
    echo "  help       Show this help message"
    echo ""
    echo "Environment variables:"
    echo "  MODELS_DIR    Directory for models (default: \$HOME/comfyui-models)"
    echo "  INPUT_DIR     Directory for input files (default: \$HOME/comfyui-input)"
    echo "  OUTPUT_DIR    Directory for output files (default: \$HOME/comfyui-output)"
    echo "  HOST_PORT     Host port for ComfyUI (default: 8188)"
}

# Main script logic
case "${1:-run}" in
    "build")
        check_docker
        build_image
        ;;
    "run")
        check_docker
        if ! docker image inspect "$IMAGE_NAME" > /dev/null 2>&1; then
            build_image
        fi
        run_container "${@:2}"
        ;;
    "help"|"-h"|"--help")
        show_help
        ;;
    *)
        print_error "Unknown command: $1"
        show_help
        exit 1
        ;;
esac

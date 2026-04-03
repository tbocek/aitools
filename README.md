# aitools

Docker-based build system for running AI inference on AMD GPUs (gfx1151 / RDNA 3.5). Uses a multi-stage Dockerfile approach with an Arch Linux base image containing ROCm and Vulkan.

## Services

All services are managed via `cpp/docker-compose.yml` and built with `cpp/run.sh`.

### llama.cpp (`Dockerfile.llama`)
LLM inference server with HIP (ROCm) and Vulkan backends. Serves an OpenAI-compatible API on port **9001**.

### stable-diffusion.cpp (`Dockerfile.sd`)
Image generation server built with both ROCm and Vulkan backends, using the [sd.cpp-webui](https://github.com/daniandtheweb/sd.cpp-webui) frontend on port **7860**.

### ACE-Step (`Dockerfile.ace-step`)
Music generation server using [acestep.cpp](https://github.com/ServeurpersoCom/acestep.cpp) with ROCm and Vulkan backends on port **8082**.

## Base Image (`Dockerfile.arch`)
Arch Linux with ROCm HIP SDK, rocWMMA, Vulkan, cmake, ninja, and Python/uv. All service Dockerfiles build `FROM arch:latest`.

## Quick Start

```bash
cd cpp
# Edit run.sh to select backends (LLAMA_DEVICE, SD_BACKEND, ACE_BACKEND)
./run.sh
```

This builds the base image, then brings up all services via docker-compose.

## Configuration

Environment variables in `run.sh`:
- `LLAMA_DEVICE` — `ROCM0` or `Vulkan0`
- `SD_BACKEND` — `rocm` or `vulkan`
- `ACE_BACKEND` — `rocm` or `vulkan`

Models are expected at `/mnt/models` on the host (mounted into containers).

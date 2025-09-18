#!/usr/bin/env bash
# Tool calling script for service management
set -Eeuo pipefail
trap cleanup SIGINT SIGTERM ERR EXIT

# Global variables
SERVICES=()
DETACHED=false
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

cleanup() {
  trap - SIGINT SIGTERM ERR EXIT
  exit 0
}

usage() {
  cat <<EOF
Usage: $(basename "${BASH_SOURCE[0]}") [OPTIONS]
Manage and run services in the Docker environment.
OPTIONS:
  -s SERVICE[:DEVICE] [ARGS]  Specify service with optional device and arguments
  -d                          Run in detached mode
  -h, --help                  Print this help and exit
AVAILABLE SERVICES:
  llama-rocm     Run LLaMA server with ROCM GPU acceleration
  llama-vulkan   Run LLaMA server with Vulkan GPU acceleration
  sd-rocm        Run Stable Diffusion web UI with ROCM GPU acceleration
  sd-vulkan      Run Stable Diffusion web UI with Vulkan GPU acceleration
  whisper-rocm   Run Whisper server with ROCM GPU acceleration
  whisper-vulkan Run Whisper server with Vulkan GPU acceleration
  tts-rocm       Run Chatterbox-TTS-Server with ROCM GPU acceleration
EXAMPLES:
  $(basename "${BASH_SOURCE[0]}") -s "llama-rocm:1 --model model_name --ctx-size 2048" -s "sd-rocm:0"
  $(basename "${BASH_SOURCE[0]}") -d -s "whisper-rocm:1" -s "tts-rocm:0"
EOF
  exit
}

msg() {
  echo >&2 -e "${1-}"
}
err_msg() {
 echo >&2 -e "${RED}Error: ${1-}${NOFORMAT}"
}

die() {
  local msg=$1
  local code=${2-1}
  err_msg "$msg"
  exit "$code"
}

setup_colors() {
  if [[ -t 2 ]] && [[ -z "${NO_COLOR-}" ]] && [[ "${TERM-}" != "dumb" ]]; then
    NOFORMAT='\033[0m' RED='\033[0;31m' GREEN='\033[0;32m' ORANGE='\033[0;33m' BLUE='\033[0;34m'
  else
    NOFORMAT='' RED='' GREEN='' ORANGE='' BLUE=''
  fi
}

parse_service_spec() {
  local service_spec="$1"
  local service_name device args
  
  # Split on first space to separate service:device from args
  if [[ "$service_spec" =~ ^([^[:space:]]+)[[:space:]]+(.*)$ ]]; then
    service_name="${BASH_REMATCH[1]}"
    args="${BASH_REMATCH[2]}"
  else
    service_name="$service_spec"
    args=""
  fi
  
  # Split service:device
  if [[ "$service_name" =~ ^([^:]+):([0-9]+)$ ]]; then
    service="${BASH_REMATCH[1]}"
    device="${BASH_REMATCH[2]}"
  else
    service="$service_name"
    device=""
  fi
  
  # Validate service name
  case "$service" in
    llama-rocm|llama-vulkan|sd-rocm|sd-vulkan|whisper-rocm|whisper-vulkan|tts-rocm)
      ;;
    *)
      die "Invalid service: $service. Use -h for help."
      ;;
  esac
  
  # Export service-specific environment variables
  case "$service" in
    llama-*)
      [[ -n "$device" ]] && export LLAMA_DEVICE="$device"
      [[ -n "$args" ]] && export LLAMA_ARGS="$args"
      ;;
    sd-*)
      [[ -n "$device" ]] && export SD_DEVICE="$device"
      [[ -n "$args" ]] && export SD_ARGS="$args"
      ;;
    whisper-*)
      [[ -n "$device" ]] && export WHISPER_DEVICE="$device"
      [[ -n "$args" ]] && export WHISPER_ARGS="$args"
      ;;
    tts-*)
      [[ -n "$device" ]] && export TTS_DEVICE="$device"
      [[ -n "$args" ]] && export TTS_ARGS="$args"
      ;;
  esac
  
  SERVICES+=("$service")
}

parse_params() {
  # Check if no arguments provided
  if [[ $# -eq 0 ]]; then
    usage
  fi
  
  while [[ $# -gt 0 ]]; do
    case "${1}" in
    -h | --help) usage ;;
    -d | --detach)
      DETACHED=true
      shift ;;
    -s)
      if [[ $# -lt 2 ]]; then
        err_msg "Option -s requires an argument"
        usage
      fi
      parse_service_spec "${2}"
      shift 2 ;;
    -?*)
      err_msg "Unknown option: $1"
      usage 
      ;;
    *)
      err_msg "Unexpected argument: $1"
      usage 
      ;;
    esac
  done
  
  # Validate required parameters
  [[ ${#SERVICES[@]} -eq 0 ]] && die "No service specified. Use -h for help."
  return 0
}

setup_colors
parse_params "$@"
  
msg "${GREEN}Building services${NOFORMAT}"
docker build . -f Dockerfile.arch -t arch

msg "${GREEN}Starting services: ${SERVICES[*]}${NOFORMAT}"
# Prepare docker-compose command
local compose_args=("up" "--build" "--abort-on-container-failure")
if [[ "$DETACHED" == true ]]; then
  compose_args+=("-d")
fi
compose_args+=("${SERVICES[@]}")

docker-compose "${compose_args[@]}"

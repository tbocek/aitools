#!/usr/bin/env bash
# Tool calling script for service management
set -Eeuo pipefail
trap cleanup SIGINT SIGTERM ERR EXIT

# Global variables
SERVICES=()
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
  -s SERVICE-BACKEND[:DEVICE]  Specify service with backend and optional device
  -h, --help                   Print this help and exit
AVAILABLE SERVICES:
  llama-rocm     Run LLaMA server with ROCM GPU acceleration
  llama-vulkan   Run LLaMA server with Vulkan GPU acceleration
  sd-rocm        Run Stable Diffusion web UI with ROCM GPU acceleration
  sd-vulkan      Run Stable Diffusion web UI with Vulkan GPU acceleration
  whisper-rocm   Run Whisper server with ROCM GPU acceleration
  whisper-vulkan Run Whisper server with Vulkan GPU acceleration
  tts-rocm       Run Chatterbox-TTS-Server with ROCM GPU acceleration
EXAMPLES:
  $(basename "${BASH_SOURCE[0]}") -s "llama-rocm:1" -s "sd-rocm:0"
  $(basename "${BASH_SOURCE[0]}") -s "whisper-rocm:1" -s "tts-rocm:0"
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
  local service_name device service backend

  # Split service:device
  if [[ "$service_spec" =~ ^([^:]+):([0-9]+)$ ]]; then
    service_name="${BASH_REMATCH[1]}"
    device="${BASH_REMATCH[2]}"
  else
    service_name="$service_spec"
    device=""
  fi

  # Split service-backend (e.g. llama-rocm -> llama + rocm)
  if [[ "$service_name" =~ ^(.+)-(rocm|vulkan)$ ]]; then
    service="${BASH_REMATCH[1]}"
    backend="${BASH_REMATCH[2]}"
  else
    die "Invalid service: $service_name. Must end in -rocm or -vulkan. Use -h for help."
  fi

  # Validate service name
  case "$service" in
    llama|sd|whisper|tts)
      ;;
    *)
      die "Invalid service: $service_name. Use -h for help."
      ;;
  esac

  # Export service-specific environment variables
  local prefix="${service^^}"
  export "${prefix}_BACKEND=$backend"
  [[ -n "$device" ]] && export "${prefix}_DEVICE=$device"

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
docker-compose up --build -d "${SERVICES[@]}"

msg "${GREEN}Following logs (Ctrl+C to detach)${NOFORMAT}"
docker-compose logs -f "${SERVICES[@]}"

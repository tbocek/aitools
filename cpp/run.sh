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
  -s SERVICE   Specify service to run (e.g., -s llama-server-rocm -s sd-webui-rocm)
  -la LLAMA_ARGS   Specify arguments for llama.cpp (e.g., "--model model_name --mmproj mmproj_name --ctx-size 2048")
  -ld DEV_NUM   Specify device for llama.cpp (-ld 1)
  -sd DEV_NUM   Specify device for sd.cpp (-sd 0)
  -wd DEV_NUM   Specify device for whisper.cpp (-wd 1)
  -h, --help              Print this help and exit
AVAILABLE SERVICES:
  llama-server-rocm     - Run LLaMA server with ROCM GPU acceleration
  llama-server-vulkan   - Run LLaMA server with Vulkan GPU acceleration
  sd-webui-rocm         - Run Stable Diffusion web UI with ROCM GPU acceleration
  sd-webui-vulkan       - Run Stable Diffusion web UI with Vulkan GPU acceleration
  whisper-server-rocm   - Run Whisper server with ROCM GPU acceleration
  whisper-server-vulkan - Run Whisper server with Vulkan GPU acceleration
EXAMPLES:
  $(basename "${BASH_SOURCE[0]}") -s llama-server-rocm -s sd-webui-rocm -la "--model model_name --mmproj mmproj_name --ctx-size 2048" -ld 1 -sd 0
EOF
  exit
}
msg() {
  echo >&2 -e "${1-}"
}
die() {
  local msg=$1
  local code=${2-1}
  msg "${RED}Error: $msg${NOFORMAT}"
  exit "$code"
}
setup_colors() {
  if [[ -t 2 ]] && [[ -z "${NO_COLOR-}" ]] && [[ "${TERM-}" != "dumb" ]]; then
    NOFORMAT='\033[0m' RED='\033[0;31m' GREEN='\033[0;32m' ORANGE='\033[0;33m' BLUE='\033[0;34m'
  else
    NOFORMAT='' RED='' GREEN='' ORANGE='' BLUE=''
  fi
}
parse_params() {
  # Default values
  LLAMA_ARGS=""
  LD=""
  SD=""
  WD=""
  
  # Check if no arguments provided
  if [[ $# -eq 0 ]]; then
    usage
  fi
  
  while [[ $# -gt 0 ]]; do
    case "${1}" in
    -h | --help) usage ;;
    -s)
      if [[ $# -lt 2 ]]; then
        die "Option -s requires an argument"
      fi
      SERVICES+=("${2}")
      shift 2 ;;
    -la)
      if [[ $# -lt 2 ]]; then
        die "Option -la requires an argument"
      fi
      LLAMA_ARGS="${2}"
      shift 2 ;;
    -ld)
      if [[ $# -lt 2 ]]; then
        die "Option -ld requires an argument"
      fi
      LD="${2}"
      shift 2 ;;
    -sd)
      if [[ $# -lt 2 ]]; then
        die "Option -sd requires an argument"
      fi
      SD="${2}"
      shift 2 ;;
    -wd)
      if [[ $# -lt 2 ]]; then
        die "Option -wd requires an argument"
      fi
      WD="${2}"
      shift 2 ;;
    -?*)
      die "Unknown option: $1" ;;
    *)
      die "Unexpected argument: $1"
      ;;
    esac
  done
  
  # Validate required parameters
  [[ ${#SERVICES[@]} -eq 0 ]] && die "No service specified. Use -h for help."
  # Validate service names
  for service in "${SERVICES[@]}"; do
    case "$service" in
      llama-server-rocm|llama-server-vulkan|sd-webui-rocm|sd-webui-vulkan|whisper-server-rocm|whisper-server-vulkan)
        ;;
      *)
        die "Invalid service: $service. Use -h for help."
        ;;
    esac
  done
  return 0
}
validate_docker() {
  if ! command -v docker &> /dev/null; then
    die "Docker is not installed. Please install Docker first."
  fi
  if ! command -v docker-compose &> /dev/null; then
    die "Docker Compose is not installed. Please install Docker Compose first."
  fi
  # Test docker access
  if ! docker info &> /dev/null; then
    die "Cannot access Docker daemon. Please ensure Docker is running and you have proper permissions."
  fi
}
main() {
  setup_colors
  parse_params "$@"
  validate_docker

  msg "${GREEN}Starting services: ${SERVICES[*]}${NOFORMAT}"

  # Export the variables
  export LLAMA_ARGS="$LLAMA_ARGS"
  export LD_DEVICE="$LD"
  export SD_DEVICE="$SD"
  export WD_DEVICE="$WD"
  docker-compose up --abort-on-container-failure "${SERVICES[@]}"
}
# Run main function
main "$@"
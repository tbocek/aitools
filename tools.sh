#!/usr/bin/env bash

# Tool calling script for llama.cpp
set -Eeuo pipefail
trap cleanup SIGINT SIGTERM ERR EXIT

# Global variables
total_prompt_tokens=0
total_completion_tokens=0
total_tokens=0
tools_folder="./tools"
max_iterations=10
temperature=0.1
model="model-name"

cleanup() {
  msg "${GREEN}Done. Total tokens - Prompt: $total_prompt_tokens, Completion: $total_completion_tokens, Total: $total_tokens${NOFORMAT}"
  trap - SIGINT SIGTERM ERR EXIT
}

usage() {
  cat <<EOF
Usage: $(basename "${BASH_SOURCE[0]}") [OPTIONS] "Your prompt here"

AI tool calling system that executes bash scripts as tools.

OPTIONS:
  -h, --help              Print this help and exit
  -t, --tools-folder      Folder containing tool scripts (default: ./tools)
  -m, --max-iterations    Maximum tool calling iterations (default: 10)
  -u, --url               API endpoint (default: https://ai.jos.li/v1/chat/completions)
  -k, --key               API key (default: from OPENAI_API_KEY env var)
  -M, --model             Model name (default: model-name)
  -T, --temperature       Temperature for generation (default: 0.1)
  -v, --verbose           Enable verbose output

EXAMPLE:
  $(basename "${BASH_SOURCE[0]}") "What's the weather in Paris?"
  $(basename "${BASH_SOURCE[0]}") -t /path/to/tools -m 5 "Calculate 25 * 4"

TOOL FORMAT:
  Tools should be bash scripts with a special header comment:

  #!/usr/bin/env bash
  # TOOL_NAME: get_weather
  # TOOL_DESCRIPTION: Get current weather for a location
  # TOOL_PARAMETERS: {"type": "object", "properties": {"location": {"type": "string", "description": "City name"}}, "required": ["location"]}

EOF
  exit
}

setup_colors() {
  if [[ -t 2 ]] && [[ -z "${NO_COLOR-}" ]] && [[ "${TERM-}" != "dumb" ]]; then
    NOFORMAT='\033[0m' RED='\033[0;31m' GREEN='\033[0;32m' ORANGE='\033[0;33m' BLUE='\033[0;34m'
  else
    NOFORMAT='' RED='' GREEN='' ORANGE='' BLUE=''
  fi
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

parse_params() {
  # Default values
  url="https://ai.jos.li/v1/chat/completions"
  api_key="${OPENAI_API_KEY:-}"
  verbose=0
  user_prompt=""

  while :; do
    case "${1-}" in
    -h | --help) usage ;;
    -t | --tools-folder)
      tools_folder="${2-}"
      shift ;;
    -m | --max-iterations)
      max_iterations="${2-}"
      shift ;;
    -u | --url)
      url="${2-}"
      shift ;;
    -k | --key)
      api_key="${2-}"
      shift ;;
    -M | --model)
      model="${2-}"
      shift ;;
    -T | --temperature)
      temperature="${2-}"
      shift ;;
    -v | --verbose)
      verbose=1 ;;
    -?*)
      die "Unknown option: $1" ;;
    *)
      user_prompt="${1-}"
      break ;;
    esac
    shift
  done

  # Validate required parameters
  [[ -z "$user_prompt" ]] && die "No prompt provided. Use -h for help."
  [[ -z "$api_key" ]] && die "No API key provided. Set OPENAI_API_KEY or use -k option."
  [[ ! -d "$tools_folder" ]] && die "Tools folder not found: $tools_folder"

  return 0
}

# Extract tool information from scripts
get_tool_info() {
  local script="$1"
  local info_type="$2"

  # Execute the script with the info flag
  local result=$("$script" "--$info_type" 2>/dev/null)
  echo "$result"
}

# Build tools JSON array from scripts in tools folder
build_tools_json() {
  local tools_json="[]"

  for script in "$tools_folder"/*.sh; do
    [[ ! -f "$script" ]] && continue
    [[ ! -x "$script" ]] && continue

    # Get tool information
    local name=$(get_tool_info "$script" "name")
    local description=$(get_tool_info "$script" "description")
    local parameters=$(get_tool_info "$script" "parameters")

    # Skip if any required info is missing
    [[ -z "$name" || -z "$description" || -z "$parameters" ]] && continue

    # Validate that parameters is valid JSON
    if ! echo "$parameters" | jq '.' >/dev/null 2>&1; then
      msg "${ORANGE}Warning: Invalid parameters JSON for tool $name${NOFORMAT}"
      continue
    fi

    # Build tool JSON object
    local tool_obj=$(jq -n \
      --arg name "$name" \
      --arg desc "$description" \
      --argjson params "$parameters" \
      '{
        type: "function",
        function: {
          name: $name,
          description: $desc,
          parameters: $params
        }
      }')

    # Add to tools array
    tools_json=$(echo "$tools_json" | jq ". += [$tool_obj]")

    [[ $verbose -eq 1 ]] && msg "Registered tool: $name"
  done

  echo "$tools_json"
}

# Execute a tool call
execute_tool() {
  local tool_name="$1"
  local arguments="$2"

  # Find the script for this tool
  local script_path=""
  for script in "$tools_folder"/*.sh; do
    [[ ! -f "$script" ]] && continue

    local name=$(get_tool_info "$script" "name")
    if [[ "$name" == "$tool_name" ]]; then
      script_path="$script"
      break
    fi
  done

  if [[ -z "$script_path" ]]; then
    echo "Error: Tool script not found for $tool_name"
    return 1
  fi

  # Convert JSON arguments to command line arguments
  local cmd_args=()

  if [[ -n "$arguments" && "$arguments" != "{}" ]]; then
    # Parse JSON and convert to --key value format
    while IFS= read -r line; do
      cmd_args+=("$line")
    done < <(echo "$arguments" | jq -r 'to_entries | .[] | "--\(.key)", .value')
  fi

  [[ $verbose -eq 1 ]] && msg "Executing: $script_path ${cmd_args[*]}"

  # Execute the script with arguments
  local result=$("$script_path" "${cmd_args[@]}" 2>&1)

  echo "$result"
}

# Call the API
call_api() {
  local messages="$1"
  local tools="$2"
  local include_tools="$3"

  local request_body=""

  if [[ "$include_tools" == "true" ]] && [[ "$tools" != "[]" ]]; then
    request_body=$(jq -n \
      --argjson messages "$messages" \
      --argjson tools "$tools" \
      --arg model "$model" \
      --arg temp "$temperature" \
      '{
        model: $model,
        messages: $messages,
        tools: $tools,
        tool_choice: "auto",
        temperature: ($temp | tonumber),
        stream: false
      }')
  else
    request_body=$(jq -n \
      --argjson messages "$messages" \
      --arg model "$model" \
      --arg temp "$temperature" \
      '{
        model: $model,
        messages: $messages,
        temperature: ($temp | tonumber),
        stream: false
      }')
  fi

  [[ $verbose -eq 1 ]] && msg "${BLUE}Request:${NOFORMAT}" && echo "$request_body" | jq '.' >&2

  local response=$(curl -s -X POST \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer $api_key" \
    -d "$request_body" \
    "$url")

  [[ $verbose -eq 1 ]] && msg "${BLUE}Response:${NOFORMAT}" && echo "$response" | jq '.' >&2

  # Update token counts
  if echo "$response" | jq -e '.usage' > /dev/null 2>&1; then
    local prompt_tokens=$(echo "$response" | jq -r '.usage.prompt_tokens // 0')
    local completion_tokens=$(echo "$response" | jq -r '.usage.completion_tokens // 0')

    total_prompt_tokens=$((total_prompt_tokens + prompt_tokens))
    total_completion_tokens=$((total_completion_tokens + completion_tokens))
    total_tokens=$((total_tokens + prompt_tokens + completion_tokens))

    msg "${GREEN}Tokens: prompt=$prompt_tokens, completion=$completion_tokens${NOFORMAT}"
  fi

  echo "$response"
}

# Main tool calling loop
main() {
  setup_colors
  parse_params "$@"

  msg "${GREEN}Starting tool calling session...${NOFORMAT}"
  msg "Tools folder: $tools_folder"

  # Build available tools
  local tools=$(build_tools_json)
  local tool_count=$(echo "$tools" | jq '. | length')
  msg "Found $tool_count tools"

  if [[ $tool_count -eq 0 ]]; then
    msg "${ORANGE}Warning: No valid tools found in $tools_folder${NOFORMAT}"
  fi

  # Initialize conversation with user prompt
  local messages=$(jq -n --arg prompt "$user_prompt" '[{role: "user", content: $prompt}]')

  local iteration=0
  while [[ $iteration -lt $max_iterations ]]; do
    iteration=$((iteration + 1))
    msg "${ORANGE}Iteration $iteration/$max_iterations${NOFORMAT}"

    # Call API with tools on first iteration or if we just processed tool results
    local include_tools="true"
    local response=$(call_api "$messages" "$tools" "$include_tools")

    # Check for errors
    if echo "$response" | jq -e '.error' > /dev/null 2>&1; then
      local error_msg=$(echo "$response" | jq -r '.error.message // .error // "Unknown error"')
      die "API error: $error_msg"
    fi

    # Check if response has tool calls
    if echo "$response" | jq -e '.choices[0].message.tool_calls' > /dev/null 2>&1; then
      # Extract assistant message with tool calls
      local assistant_msg=$(echo "$response" | jq '.choices[0].message')
      messages=$(echo "$messages" | jq ". += [$assistant_msg]")

      # Process each tool call
      local tool_calls=$(echo "$response" | jq -r '.choices[0].message.tool_calls')
      local num_calls=$(echo "$tool_calls" | jq '. | length')

      msg "Processing $num_calls tool call(s)..."

      for i in $(seq 0 $((num_calls - 1))); do
        local tool_call=$(echo "$tool_calls" | jq ".[$i]")
        local call_id=$(echo "$tool_call" | jq -r '.id')
        local tool_name=$(echo "$tool_call" | jq -r '.function.name')
        local arguments=$(echo "$tool_call" | jq -r '.function.arguments')

        msg "Executing tool: $tool_name"
        [[ $verbose -eq 1 ]] && msg "Arguments: $arguments"

        # Execute the tool
        local result=$(execute_tool "$tool_name" "$arguments")

        msg "Tool result: ${result:0:100}..."

        # Add tool result to messages
        local tool_msg=$(jq -n \
          --arg id "$call_id" \
          --arg content "$result" \
          '{
            role: "tool",
            tool_call_id: $id,
            content: $content
          }')

        messages=$(echo "$messages" | jq ". += [$tool_msg]")
      done
    else
      # No tool calls, just a regular response
      local content=$(echo "$response" | jq -r '.choices[0].message.content // empty')

      if [[ -n "$content" ]]; then
        msg "${GREEN}Final response:${NOFORMAT}"
        echo "$content"
      else
        msg "${RED}No content in response${NOFORMAT}"
        [[ $verbose -eq 1 ]] && echo "$response" | jq '.' >&2
      fi

      break
    fi
  done

  if [[ $iteration -ge $max_iterations ]]; then
    msg "${RED}Reached maximum iterations ($max_iterations)${NOFORMAT}"
  fi
}

# Run main function
main "$@"
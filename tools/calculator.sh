#!/usr/bin/env bash

set -Eeuo pipefail

# Tool metadata
TOOL_NAME="calculate"
TOOL_DESCRIPTION="Perform mathematical calculations using bc calculator. Input is the expression directly in plain text"
TOOL_PARAMETERS='{
  "type": "object",
  "properties": {
    "expression": {
      "type": "string",
      "description": "Mathematical expression to evaluate (examples: \"2+2\", \"sqrt(16)\", \"3^2\")"
    }
  },
  "required": ["expression"]
}'

usage() {
  cat <<EOF
Usage: $(basename "${BASH_SOURCE[0]}") [OPTIONS]

$TOOL_DESCRIPTION

OPTIONS:
  -h, --help              Print this help and exit
  --name                  Print tool name
  --description           Print tool description
  --parameters            Print parameters JSON schema
  --expression VALUE      Mathematical expression to evaluate

SUPPORTED OPERATIONS:
  Basic: +, -, *, /, % (modulo), ^ (power)
  Functions: sqrt(), s() (sine), c() (cosine), a() (arctangent),
            l() (natural log), e() (exponential)

EXAMPLES:
  $(basename "${BASH_SOURCE[0]}") --expression "2 + 2"
  $(basename "${BASH_SOURCE[0]}") --expression "sqrt(144)"
  $(basename "${BASH_SOURCE[0]}") --expression "3.14159 * 2^2"
  $(basename "${BASH_SOURCE[0]}") --expression "scale=10; 22/7"

EOF
  exit
}

# Default values
expression=""

# Parse arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    -h|--help)
      usage
      ;;
    --name)
      echo "$TOOL_NAME"
      exit 0
      ;;
    --description)
      echo "$TOOL_DESCRIPTION"
      exit 0
      ;;
    --parameters)
      echo "$TOOL_PARAMETERS"
      exit 0
      ;;
    --expression)
      expression="$2"
      shift 2
      ;;
    *)
      echo "Unknown option: $1" >&2
      exit 1
      ;;
  esac
done

# Tool execution logic
if [[ -z "$expression" ]]; then
  echo "Error: Expression is required. Use --expression EXPR"
  exit 1
fi

# Check if bc is available
if ! command -v bc &> /dev/null; then
  echo "Error: bc calculator is not installed"
  exit 1
fi

# Sanitize input to prevent code injection
# Allow: numbers, operators, parentheses, decimal points, spaces, and bc functions
sanitized=$(echo "$expression" | grep -E '^[0-9+*/(),.[:space:]-]+$' || true)

if [[ -z "$sanitized" ]]; then
  echo "Error: Invalid mathematical expression. Only numbers, basic operators, and bc functions are allowed."
  exit 1
fi

# Load bc math library for functions like sine, cosine, etc.
# Use scale=6 for decimal precision unless scale is already set in expression
if [[ "$expression" =~ scale= ]]; then
  result=$(echo "$expression" | bc -l 2>&1 || true)
else
  result=$(echo "scale=6; $expression" | bc -l 2>&1 || true)
fi

# Check if bc succeeded
if [[ $? -eq 0 ]]; then
  # Clean up the result (remove trailing zeros after decimal point)
  if [[ "$result" =~ \. ]]; then
    result=$(echo "$result" | sed 's/0*$//' | sed 's/\.$//')
  fi
  echo "$expression = $result"
else
  # Parse bc error messages to provide better feedback
  if [[ "$result" =~ "syntax error" ]]; then
    echo "Error: Syntax error in expression '$expression'"
  elif [[ "$result" =~ "divide by zero" ]]; then
    echo "Error: Division by zero in expression"
  else
    echo "Error: Failed to calculate expression. $result"
  fi
  exit 1
fi
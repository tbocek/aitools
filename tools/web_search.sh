#!/usr/bin/env bash
set -Euo pipefail

# Tool metadata
TOOL_NAME="web_search"
TOOL_DESCRIPTION="Perform web search using DuckDuckGo and extract content from results."
TOOL_PARAMETERS='{
  "type": "object",
  "properties": {
    "query": {
      "type": "string",
      "description": "Search query to perform (examples: \"weather in Paris\", \"latest news\")"
    },
    "num_results": {
      "type": "integer",
      "description": "Number of results to fetch and process (default: 10)",
      "minimum": 1,
      "maximum": 50
    }
  },
  "required": ["query"]
}'

usage() {
  cat <<EOF
Usage: $(basename "${BASH_SOURCE[0]}") [OPTIONS]
$TOOL_DESCRIPTION

OPTIONS:
  -h, --help              Print this help and exit
  -v, --verbose           Enable debug outputs
  --name                  Print tool name
  --description           Print tool description
  --parameters            Print parameters JSON schema
  --query VALUE           Search query to perform
  --num-results VALUE     Number of results to fetch (default: 10)

EXAMPLES:
  $(basename "${BASH_SOURCE[0]}") --query "weather in Paris"
  $(basename "${BASH_SOURCE[0]}") --query "latest news" --num-results 5
  $(basename "${BASH_SOURCE[0]}") --query "python tutorials" --num-results 20
EOF
  exit
}

# Default values
query=""
num_results=10
verbose=false

# Parse arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    -h|--help)
      usage
      ;;
    -v|--verbose)
      verbose=true
      shift
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
    --query)
      query="$2"
      shift 2
      ;;
    --num-results)
      num_results="$2"
      shift 2
      ;;
    *)
      echo "Unknown option: $1" >&2
      exit 1
      ;;
  esac
done

debug() {
  if $verbose; then
    echo "DEBUG: $1"
  fi
}

# Validate num_results
if ! [[ "$num_results" =~ ^[0-9]+$ ]] || [ "$num_results" -lt 1 ] || [ "$num_results" -gt 50 ]; then
  echo "Error: num_results must be a number between 1 and 50"
  exit 1
fi

# Tool execution logic
if [[ -z "$query" ]]; then
  echo "Error: Query is required. Use --query QUERY"
  exit 1
fi

# Check if node and playwright are available
if ! command -v node &> /dev/null; then
  echo "Error: node is not installed"
  exit 1
fi

if ! node -e "require('playwright')" &> /dev/null; then
  echo "Error: playwright is not installed. Run: npm install playwright && npx playwright install firefox"
  exit 1
fi

# URL encode the query
encoded_query=$(jq -rn --arg q "$query" '$q|@uri')
debug "Encoded query: $encoded_query"
debug "Number of results to fetch: $num_results"

# Create temporary files
temp_html=$(mktemp)
results_dir=$(mktemp -d)

debug "Using temporary HTML file: $temp_html"
debug "Using results directory: $results_dir"

debug "Starting search for '$query'"
node -e "
const { firefox } = require('playwright');
(async () => {
  const browser = await firefox.launch({ headless: true });
  try {
    const page = await browser.newPage();
    await page.setViewportSize({ width: 1920, height: 1080 });
    await page.goto('https://duckduckgo.com/?q=$encoded_query', {
      waitUntil: 'networkidle',
      timeout: 10000  // 10 seconds timeout
    });
    const content = await page.content();
    console.log(content);
  } catch (e) {
    console.error('Timeout or error:', e.message);
    process.exit(1);
  } finally {
    await browser.close();
  }
})();
" > "$temp_html" 2>/dev/null

debug "Stored result: $temp_html"

# Extract search result URLs based on num_results parameter
urls=$(grep -oP '(?<=href=").+?(?=")' "$temp_html" | grep -oP 'https?://\S+' | grep -v duckduckgo | grep -v .jpg | awk '!seen[$0]++' | head -n "$num_results")
debug "Found URLs: $urls"

# Clean up temporary file
rm "$temp_html"

if [[ -z "$urls" ]]; then
  echo "Error: No search results found"
  exit 1
fi

pids=()
debug "Found $(echo "$urls" | wc -l) results for '$query':"
while IFS= read -r url; do
  debug "Processing URL: $url"

  # Create a filename-safe URL
  safe_url=$(echo "$url" | tr '/:' '_')
  output_file="$results_dir/$safe_url.html"

  # Fetch the content of the page
  debug "Fetching content for $url"
  node -e "
  const { firefox } = require('playwright');
  (async () => {
    const browser = await firefox.launch({ headless: true });
    try {
      const page = await browser.newPage();
      await page.setViewportSize({ width: 1920, height: 1080 });
      await page.goto('$url', {
        waitUntil: 'networkidle',
        timeout: 10000  // 10 seconds timeout
      });
      const content = await page.content();
      console.log(content);
    } catch (e) {
      console.error('Failed to load page:', e.message);
      process.exit(1);
    } finally {
      await browser.close();
    }
  })();
  " > "$output_file" 2>/dev/null &

  pid=$!  # Capture the PID of the background process
  pids+=($pid)
  debug "Started fetching: $url (PID: $pid)"
done < <(echo "$urls")

wait ${pids[@]}

debug "Extracted content saved to $results_dir"

DELIMITER="---...---RESULT_SEPARATOR_8723c3b3---...---"
first=true
echo "BEGIN_RESULTS"
while IFS= read -r url; do
  safe_url=$(echo "$url" | tr '/:' '_')
  output_file="$results_dir/$safe_url.html"

  if [[ -f "$output_file" ]]; then
    # Extract title and first paragraph
    content=$(cat "$output_file" | html2markdown | sed 's/(data:image[^)]*)//g')
    if $first; then
      first=false
    else
      echo "$DELIMITER"
    fi
    echo "URL: $url"
    echo "Content: $content"
  fi
done <<< "$urls"
echo "END_RESULTS"

debug "All content extraction complete."
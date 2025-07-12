#!/usr/bin/env bash
set -Eeuo pipefail

# Tool metadata
TOOL_NAME="weather"
TOOL_DESCRIPTION="Get current weather and forecast data for a location using latitude/longitude coordinates"
TOOL_PARAMETERS='{
  "type": "object",
  "properties": {
    "lat": {
      "type": "number",
      "description": "Latitude coordinate (e.g., 52.52 for Berlin)"
    },
    "lon": {
      "type": "number",
      "description": "Longitude coordinate (e.g., 13.41 for Berlin)"
    }
  },
  "required": ["lat", "lon"]
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
  --lat VALUE             Latitude coordinate (required)
  --lon VALUE             Longitude coordinate (required)

EXAMPLES:
  $(basename "${BASH_SOURCE[0]}") --lat 52.52 --lon 13.41

NOTES:
  - Weather data is provided by BrightSky API (DWD - German Weather Service)
  - Temperature is in Celsius, wind speed in km/h, pressure in hPa
  - Precipitation is in mm, visibility in meters
EOF
  exit
}

# Default values
lat=""
lon=""
date=""
hours=24

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
    --lat)
      lat="$2"
      shift 2
      ;;
    --lon)
      lon="$2"
      shift 2
      ;;
    --date)
      date="$2"
      shift 2
      ;;
    --hours)
      hours="$2"
      shift 2
      ;;
    *)
      echo "Error: Unknown option: $1" >&2
      exit 1
      ;;
  esac
done

# Tool execution logic
if [[ -z "$lat" ]] || [[ -z "$lon" ]]; then
  echo "Error: Latitude and longitude are required. Use --lat LAT --lon LON"
  exit 1
fi

# Validate coordinates
if ! [[ "$lat" =~ ^-?[0-9]+\.?[0-9]*$ ]] || ! [[ "$lon" =~ ^-?[0-9]+\.?[0-9]*$ ]]; then
  echo "Error: Invalid coordinates. Latitude and longitude must be numbers."
  exit 1
fi

# Validate latitude range (-90 to 90)
if (( $(echo "$lat < -90 || $lat > 90" | bc -l) )); then
  echo "Error: Latitude must be between -90 and 90"
  exit 1
fi

# Validate longitude range (-180 to 180)
if (( $(echo "$lon < -180 || $lon > 180" | bc -l) )); then
  echo "Error: Longitude must be between -180 and 180"
  exit 1
fi

# Check if curl is available
if ! command -v curl &> /dev/null; then
  echo "Error: curl is not installed"
  exit 1
fi

# Check if jq is available
if ! command -v jq &> /dev/null; then
  echo "Error: jq is not installed. Please install jq to parse JSON output."
  exit 1
fi

# Build API URL
API_URL="https://api.brightsky.dev/current_weather?lat=${lat}&lon=${lon}&max_dist=500000"

response=$(curl -s "$API_URL" 2>/dev/null)
exit_code=$?

if [[ $exit_code -ne 0 ]]; then
  echo "Error: Failed to fetch weather data. Please check your internet connection and coordinates."
  exit 1
fi

# Check if response is valid JSON
if ! echo "$response" | jq . >/dev/null 2>&1; then
  echo "Error: Invalid response from weather API"
  exit 1
fi

# Extract weather data
weather_data=$(echo "$response" | jq -r '.weather' 2>/dev/null)

if [[ -z "$weather_data" ]] || [[ "$weather_data" == "null" ]]; then
  echo "Error: No weather data available for the specified location"
  exit 1
fi

# Display location header
echo "Current Weather Report"
echo "====================="
echo "Location: ${lat}°, ${lon}°"

# Extract station information
station_name=$(echo "$response" | jq -r '.sources[0].station_name // "Unknown"')
station_distance=$(echo "$response" | jq -r '.sources[0].distance // 0' | awk '{printf "%.1f", $1/1000}')
echo "Station: ${station_name} (${station_distance} km away)"

# Extract timestamp and format it
timestamp=$(echo "$weather_data" | jq -r '.timestamp // ""')
if [[ -n "$timestamp" ]]; then
  formatted_time=$(echo "$timestamp" | sed 's/T/ /; s/+.*//')
  echo "Time: ${formatted_time} UTC"
fi
echo ""

# Main weather data
echo "Conditions"
echo "----------"
temp=$(echo "$weather_data" | jq -r '.temperature // "N/A"')
condition=$(echo "$weather_data" | jq -r '.condition // "N/A"')
humidity=$(echo "$weather_data" | jq -r '.relative_humidity // "N/A"')
pressure=$(echo "$weather_data" | jq -r '.pressure_msl // "N/A"')
dew_point=$(echo "$weather_data" | jq -r '.dew_point // "N/A"')
visibility=$(echo "$weather_data" | jq -r '.visibility // "N/A"' | awk '{if ($1 != "N/A") printf "%.1f", $1/1000; else print $1}')
cloud_cover=$(echo "$weather_data" | jq -r '.cloud_cover // "N/A"')

printf "Temperature: %s°C\n" "$temp"
printf "Condition: %s\n" "$condition"
printf "Humidity: %s%%\n" "$humidity"
printf "Pressure: %s hPa\n" "$pressure"
printf "Dew Point: %s°C\n" "$dew_point"
printf "Visibility: %s km\n" "$visibility"
printf "Cloud Cover: %s%%\n" "$cloud_cover"
echo ""

# Wind data
echo "Wind"
echo "----"
wind_speed_10=$(echo "$weather_data" | jq -r '.wind_speed_10 // "N/A"')
wind_dir_10=$(echo "$weather_data" | jq -r '.wind_direction_10 // "N/A"')
wind_gust_10=$(echo "$weather_data" | jq -r '.wind_gust_speed_10 // "N/A"')

printf "Speed (10 min avg): %s km/h from %s°\n" "$wind_speed_10" "$wind_dir_10"
printf "Gusts (10 min max): %s km/h\n" "$wind_gust_10"
echo ""

# Precipitation data
echo "Precipitation"
echo "-------------"
precip_10=$(echo "$weather_data" | jq -r '.precipitation_10 // "0"')
precip_30=$(echo "$weather_data" | jq -r '.precipitation_30 // "0"')
precip_60=$(echo "$weather_data" | jq -r '.precipitation_60 // "0"')

printf "Last 10 min: %s mm\n" "$precip_10"
printf "Last 30 min: %s mm\n" "$precip_30"
printf "Last 60 min: %s mm\n" "$precip_60"
echo ""

# Solar/Sunshine data
echo "Solar Radiation"
echo "---------------"
solar_60=$(echo "$weather_data" | jq -r '.solar_60 // "N/A"')
sunshine_60=$(echo "$weather_data" | jq -r '.sunshine_60 // "N/A"')

printf "Solar radiation (60 min): %s kW/m²\n" "$solar_60"
printf "Sunshine duration (60 min): %s min\n" "$sunshine_60"
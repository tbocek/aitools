#!/usr/bin/env bash
# transcribe.sh — split audio, transcribe chunks via llama-mtmd-cli
# Usage: ./transcribe.sh input.mp4 [chunk_mins] [overlap_mins]
#
# Uses the llama:latest image directly via docker run.
# /tmp is shared between host and container for file access.

set -Eeuo pipefail

INPUT="$1"
CHUNK=${2:-25}
OVERLAP=${3:-2}
CONTAINER="${CONTAINER:-cpp-llama-1}"
BACKEND="${LLAMA_BACKEND:-vulkan}"
BINDIR="/home/arch/llama.cpp.${BACKEND}/build/bin"
BASENAME=$(basename "$INPUT" | sed 's/\.[^.]*$//')

WORKDIR=$(mktemp -d /tmp/transcribe.XXXXXX)

echo "Splitting $INPUT into ${CHUNK}m chunks with ${OVERLAP}m overlap..."

CHUNK_S=$((CHUNK * 60))
OVERLAP_S=$((OVERLAP * 60))
STEP_S=$((CHUNK_S - OVERLAP_S))
DURATION=$(ffprobe -v error -show_entries format=duration -of csv=p=0 "$INPUT" | cut -d. -f1)

# Split with overlap
i=0
START=0
while [ "$START" -lt "$DURATION" ]; do
  OUTFILE=$(printf "%s/chunk_%03d.wav" "$WORKDIR" "$i")
  ffmpeg -y -loglevel error -i "$INPUT" \
    -ss "$START" -t "$CHUNK_S" \
    -ar 16000 -ac 1 -c:a pcm_s16le "$OUTFILE"
  echo "  chunk_$(printf '%03d' $i).wav  start=${START}s"
  START=$((START + STEP_S))
  i=$((i + 1))
done

NCHUNKS=$i
echo "Created $NCHUNKS chunks in $WORKDIR"

# Transcribe each chunk via llama-mtmd-cli
echo "Transcribing chunks..."
for j in $(seq 0 $((NCHUNKS - 1))); do
  CHUNK_FILE=$(printf "%s/chunk_%03d.wav" "$WORKDIR" "$j")
  TRANSCRIPT_FILE=$(printf "%s/transcript_%03d.txt" "$WORKDIR" "$j")

  docker exec "$CONTAINER" \
    "$BINDIR/llama-mtmd-cli" \
    -m /mnt/models/Voxtral-Mini-3B-2507-Q8_0.gguf \
    --mmproj /mnt/models/Voxtral-Mini-3B-2507-mmproj-F16.gguf \
    --audio "$CHUNK_FILE" \
    -p "Transcribe the following audio. Output one line per segment in this exact format: [HH:MM:SS - HH:MM:SS] text. Example: [00:00:05 - 00:00:12] Hello everyone, welcome." \
    --temp 0 --top-p 1 \
    -ngl 99 --flash-attn on \
    > "$TRANSCRIPT_FILE"

  echo "  chunk $j transcribed ($(wc -w < "$TRANSCRIPT_FILE") words)"
done

# Concatenate all chunk transcripts
echo "Concatenating transcripts..."
> "${BASENAME}_transcript.txt"
for j in $(seq 0 $((NCHUNKS - 1))); do
  TRANSCRIPT_FILE=$(printf "%s/transcript_%03d.txt" "$WORKDIR" "$j")
  echo "--- CHUNK $((j+1)) ---" >> "${BASENAME}_transcript.txt"
  cat "$TRANSCRIPT_FILE" >> "${BASENAME}_transcript.txt"
  echo "" >> "${BASENAME}_transcript.txt"
done

echo "Done: ${BASENAME}_transcript.txt ($(wc -w < "${BASENAME}_transcript.txt") words)"
echo "Chunks preserved in $WORKDIR"

# aitools

Local AI inference servers for an AMD Strix Halo machine (Ryzen AI Max+ 395,
Radeon 8060S, **gfx1151**, 128 GB unified memory), run as Docker containers.
Every server speaks HTTP; nothing here is a client. The editor that uses them
is [naivepost](https://github.com/tbocek/naivepost).

| service | what it is | port | image |
|---|---|---|---|
| `llama` | [llama.cpp](https://github.com/ggml-org/llama.cpp) `llama-server`, OpenAI-compatible text and vision | **9001** | built: `Dockerfile.llama` |
| `halogen` | [halogen-flash-server](https://github.com/peonist-ai/halogen-flash-server), Qwen3.8-Flash-Next only, OpenAI-compatible | **8731** | pulled: `ghcr.io/peonist-ai/halogen-flash-server` |
| `sd` | [stable-diffusion.cpp](https://github.com/leejet/stable-diffusion.cpp) `sd-server`, image generation and editing | **1234** | built: `Dockerfile.sd` |
| `audio` | [audio.cpp](https://github.com/0xShug0/audio.cpp): speech-to-text, forced alignment, diarization, source separation, TTS | **127.0.0.1:8765** | built: `Dockerfile.audio` |
| `acestep` | [acestep.cpp](https://github.com/ServeurpersoCom/acestep.cpp), music generation | **8082** | built: `Dockerfile.acestep` |

`audio` is bound to loopback only; the others listen on every interface.

## Quick start

```bash
./run.sh
```

That builds every image, starts the model downloads in the background, and
runs `docker compose up -d llama sd audio acestep halogen`.

```
./run.sh --skip-build          start without rebuilding
./run.sh --no-cache            rebuild every image from scratch
./run.sh --no-cache-llama      rebuild the service images, reuse the base
./run.sh --sd-args "…"         replace the sd-server weights and sampling settings
```

A single service can be (re)started directly with `docker compose up -d audio`.
The compose project is named after this folder, `aitools`.

## Files

| file | what it is |
|---|---|
| `Dockerfile.arch` | the base every built image starts `FROM arch:latest`: Arch Linux, a build toolchain, Vulkan (RADV), and ROCm for gfx1151 from the AUR (`rocm-gfx1151-bin`). Runs as user `arch`, uid 1000. |
| `Dockerfile.llama` | llama.cpp with both the HIP and Vulkan backends, and its web UI. |
| `Dockerfile.sd` | stable-diffusion.cpp's `sd-server` and its frontend. |
| `Dockerfile.audio` | audio.cpp at a pinned ref, with every `*.patch` in this folder applied. A patch that no longer applies fails the build rather than being fuzzed in. |
| `Dockerfile.acestep` | acestep.cpp built twice, once per backend (`acestep.cpp.rocm`, `acestep.cpp.vulkan`). |
| `audiocpp-transcriptions-timing.patch` | makes audio.cpp's `/v1/audio/transcriptions` return the word timings it already computes. |
| `docker-compose.yml` | the five services. |
| `run.sh` | build, download, start. |
| `config-llamacpp.ini` | the models `llama` offers (`--models-preset`), one section each, with the weights path and a `# url =` line `run.sh` downloads from. Copied to `/mnt/models/` on every run. |
| `config-audiocpp.json` | the models `audio` offers, each an id, a family, a task and a path. |

## What `run.sh` does, in order

1. **Picks the GPU backend** for `llama` (`LLAMA_DEVICE`, set to `Vulkan0` at the
   top of the script; `ROCM0` for HIP) and for `sd` (`SD_DEVICE`, `ROCM0`).
   `sd-server` has no device flag, so the unwanted backend is hidden through
   `HIP_VISIBLE_DEVICES` / `GGML_VK_VISIBLE_DEVICES`.
2. **Reads the GPU's group ids off the device nodes**:
   `stat -c %g /dev/dri/renderD*` and `/dev/dri/card*`, exported as
   `RENDER_GID` and `VIDEO_GID` for `group_add`. Numbers rather than names,
   because `group_add` resolves a name inside the *container*: the Arch images
   carry render/video at Arch's own ids, which are not the host's, and the
   halogen image has no `render` group at all. The compose file falls back to
   989/985 when started without `run.sh`.
3. **Creates the model directories** under `/mnt/models`. Docker would create a
   missing bind-mount source as root, and the containers run as uid 1000.
4. **Downloads missing weights** listed in `config-llamacpp.ini`, in the
   background, resuming a partial file (`curl -C -`) and fetching every shard
   of a split GGUF.
5. **Sets up a virtual microphone** (PipeWire/Pulse, when available): a
   `VoiceChanger` sink and a `VoiceChangerMic` source, so `audio`'s voice
   conversion output can be picked as a mic in other applications.
6. **Builds the images** with `docker buildx` (zstd-compressed), unless
   `--skip-build`.
7. **Starts the stack**, then waits for the downloads to finish.

## Models

All weights live under `/mnt/models` on the host. None are in any image.

| service | reads | from |
|---|---|---|
| `llama` | `/mnt/models/*.gguf` | `config-llamacpp.ini`, downloaded by `run.sh` |
| `halogen` | `/mnt/models/halogen/` | see below |
| `sd` | `/mnt/models/sd/` | chosen in `run.sh` (`sd_args`) |
| `audio` | `/mnt/models/audiocpp/`, and RVC voices from `/mnt/models/vc/` | `config-audiocpp.json` |
| `acestep` | `/mnt/models/acestep/` | downloaded by the container on first start |

**halogen** runs unsloth's `UD-Q4_K_XL` GGUF rather than its own 118 GiB
checkpoint (`HALOGEN_CHECKPOINT`). The four shards are hard links to the ones
`llama` reads in `/mnt/models`, so the file is on disk once. Beside them it
needs its own draft head (`qwen38-flash-next-mtp.hgn`), the `tokenizer/`
directory, and the vision tower (`qwen38-flash-next-vision.hgn`) for images.
One caveat of the hard links: if the downloader ever replaces a shard with a
new file, the copy in `halogen/` keeps pointing at the old one.

**llama** holds one model at a time (`--models-max 1`) and loads whichever one a
request names.

**audio** loads models on first use, keeps at most three resident
(`max_loaded_models`), and unloads everything after five idle minutes
(`idle_unload_ms`). Its registry currently holds ASR (`qwen3-asr`,
`nemotron-asr`, `parakeet-tdt`), forced alignment (`qwen3-aligner`,
`mms-aligner`), diarization (`sortformer-diar`), separation (`bs-roformer`)
and voice cloning TTS (`index-tts2`).

## Host requirements

- **gfx1151.** The ROCm build is gfx1151-only, and halogen refuses any other
  architecture.
- **Kernel 7.0 or newer** for halogen, per its README: it registers the
  checkpoint with the GPU as a read-only mapping, which older kernels refuse.
- **The device nodes** `/dev/kfd` and `/dev/dri`, passed to every service.
- **`/mnt/models`** with room for the weights: the halogen/llama GGUF alone is
  ~104 GiB.
- For `audio`: a PipeWire/Pulse session for uid 1000 at
  `/run/user/1000/pulse/native`, and its cookie. The compose file names the
  cookie by its path under `/home/draft`, so another user has to edit that line.

Memory: halogen holds most of a 128 GB machine once loaded (the weights stay
resident and its KV pool is reserved up front), and its own README says so.
`free` and `MemAvailable` overstate what is left while it runs. Its startup
log prints the real figure.

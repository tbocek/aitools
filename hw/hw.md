# LLM Software & Hardware Guide - Self-Hosted Solutions
(tb, 22.08.2025, update 17.10.2025)
This guide compares popular self-hosted LLM software solutions and hardware options for running large language models locally.

## LLM Software - Self Hosted

**vLLM**
vLLM is a high-performance inference library for large language models using advanced memory optimization techniques. It's designed for production deployments requiring maximum speed and efficiency. vLLM excels in high-throughput batch processing scenarios - [single request performance actually favors llama.cpp](https://github.com/ggml-org/llama.cpp/discussions/6730). vLLM requires Python dependencies, GPU-specific setups, and [doesn't support CPU inference or Apple Silicon](https://robert-mcdermott.medium.com/performance-vs-practicality-a-comparison-of-vllm-and-ollama-104acad250fd), making it more complex to deploy than simpler alternatives. It is a popular choice for serving LLMs at scale in enterprise environments with multiple concurrent users.

**Hardware Support:** [CUDA (primary focus)](https://docs.vllm.ai/en/stable/getting_started/installation/gpu.html), [ROCm (AMD GPUs)](https://rocm.blogs.amd.com/artificial-intelligence/vllm/README.html) - No CPU, Apple Metal, Vulkan, or SYCL support

**LM Studio**
LM Studio provides a user-friendly desktop GUI for running local language models without technical expertise, with its primary focus being the graphical interface experience. It handles setup automatically and offers an intuitive chat interface for various open-source models. However, it's not open source (proprietary freeware), so users can't make quick fixes when issues arise, though it is now free for work use. LM Studio supports both llama.cpp and MLX backends, allowing users to choose optimal performance for their hardware.

**Hardware Support:** CPU, CUDA, Metal (Apple Silicon) - Uses llama.cpp and MLX backends so inherits their hardware support

**Ollama**
Ollama simplifies LLM deployment with Docker-like commands and is primarily command-line focused. It uses llama.cpp for text-only models but has developed its own engine for multimodal models that uses ggml directly. This architectural shift allows each multimodal model to be fully self-contained. Ollama supports structured output through JSON schema enforcement. For beginners, Ollama remains easier as it simplifies many technical aspects.

**Hardware Support:** [CPU, CUDA](https://markaicode.com/install-ollama-nvidia-gpu-cuda-support/), Metal (Apple Silicon), [ROCm](https://llm-tracker.info/howto/AMD-GPUs), [Intel GPUs via IPEX-LLM](https://github.com/NikolasEnt/ollama-webui-intel) - [ONNX backend has been requested](https://github.com/ollama/ollama/issues/6502) but not yet implemented

**llama.cpp**
llama.cpp is a C++ implementation built on ggml (a lightweight C++ tensor library designed for machine learning inference that provides foundational mathematical operations and memory management) that focuses primarily on server-side functionality, though it does include UI components. It excels in simplicity - ["installs in minutes, runs on laptops or workstations"](https://robert-mcdermott.medium.com/performance-vs-practicality-a-comparison-of-vllm-and-ollama-104acad250fd) with zero dependencies and compiles cleanly on any system. Unlike vLLM which [doesn't support CPU inference or Apple Silicon GPUs](https://robert-mcdermott.medium.com/performance-vs-practicality-a-comparison-of-vllm-and-ollama-104acad250fd), llama.cpp runs everywhere - CPUs, GPUs, Apple Silicon, and embedded devices. While it requires more technical knowledge and sometimes manual code modifications until fixes get merged, it offers the most control for advanced users and remains the foundation for many other tools.

**Hardware Support:** [CPU, CUDA, ROCm (HIP), Metal (Apple Silicon), Vulkan, SYCL (Intel), OpenCL (Qualcomm Adreno)](https://github.com/ggml-org/llama.cpp/blob/master/docs/build.md) - Most comprehensive hardware support of all tools

**MLX (Apple-Specific)**
MLX is Apple's array framework for machine learning on Apple Silicon, designed specifically to leverage unified memory architecture. It provides excellent performance for LLM inference on Apple devices through optimized Metal integration and dynamic memory management. MLX and llama.cpp are competing frameworks rather than integrated solutions - they're alternative backends for running LLMs on Apple Silicon. MLX typically shows slightly lower performance than llama.cpp (15-25% slower for most workloads) but offers good Python ecosystem integration.

**Hardware Support:** Metal (Apple Silicon only) - Requires macOS and Apple M-series chips

**ONNXRuntime**
ONNXRuntime is Microsoft's cross-platform, high-performance ML inference engine that supports LLMs and generative AI models across cloud, edge, web, and mobile platforms. It converts models from various frameworks (PyTorch, TensorFlow) to ONNX format for optimized inference with hardware acceleration support. However, setup complexity is significant - [running models like Gemma3 requires multiple separate ONNX files and session management](https://huggingface.co/onnx-community/gemma-3n-E2B-it-ONNX), unlike the single-command simplicity of tools like Ollama. Performance improvements are highly variable and context-dependent rather than universally applicable.

**Hardware Support:** [CPU, CUDA, DirectML (Windows), CoreML (Apple), OpenVINO (Intel), ROCm (AMD), Vulkan, QNN (Qualcomm), NNAPI (Android), Web Assembly](https://onnxruntime.ai/docs/execution-providers/) - Broadest platform support including mobile and web

### Key Technical Facts
- **ggml foundation**: llama.cpp is built on ggml, a C++ tensor library that provides the computational foundation
- **Architectural evolution**: Ollama uses llama.cpp for text models but developed its own ggml-based engine for improved multimodal support
- **Performance focus**: vLLM prioritizes maximum throughput for production serving, while ONNXRuntime focuses on cross-platform optimization
- **User experience spectrum**: Ranges from LM Studio's no-code GUI approach to llama.cpp's technical flexibility
- **Open source status**: Most tools are open source except LM Studio (proprietary but free)

### Use Case Recommendations
- **Choose vLLM** (Python) for high-throughput production serving where you can handle complex setup requirements
- **Choose LM Studio** (GUI application) for beginners wanting a simple graphical interface without technical setup
- **Choose Ollama** (Go) for command-line users who want simplicity with modern multimodal support
- **Choose llama.cpp** (C++) for maximum hardware compatibility, performance control, and zero-dependency deployment
- **Choose MLX** (Python) for Apple Silicon users prioritizing Python ecosystem integration and unified memory optimization
- **Choose ONNXRuntime** (Python/C++/C#/JavaScript) for cross-platform deployment where you need mobile/web support

### My Recommendation
I have hands-on experience with llama.cpp, Ollama, and LM Studio, and currently rely on llama.cpp for my setup. The project has very active development, rapid bug fixes, strong performance, and comprehensive hardware compatibility. My primary use case involves running it as a server that multiple clients connect to, which makes llama.cpp's capabilities perfectly sufficient. While I occasionally use the user interface, I mostly operate it in server mode.

## LLM Hardware Guide - Self-Hosted Solutions
Since I recommend llama.cpp, we have greater flexibility on the hardware side. Currently, I have 2 GPUs: an older RX580 where I can use Vulkan, and an RX 7900 XTX where I can use both ROCm and Vulkan. This guide explores options for running self-hosted LLMs and compares them to cloud alternatives like Azure.

### Why Self-Hosting vs Cloud?
On Azure, you can rent Nvidia/AMD professional GPUs. However, I argue these are expensive compared to buying consumer-grade hardware. More importantly than price, cloud providers control your data—with self-hosting, you maintain complete data control and privacy.

## VRAM vs Performance
For certain models you need sufficient VRAM. Consumer GPUs typically offer 24GB (7900XTX, 4090) or 32GB (5090), while professional GPU cards have more RAM. With 24GB you can run medium-sized LLMs with quantization (e.g., Mistral 24B models, Google's Gemma 24B model, and Alibaba's Qwen3 30A3/32B models). However, these are quantized versions and not the full 128k context. Quantization reduces memory requirements but also accuracy, though Q8 quantization shows minimal degradation with perplexity scores very close to FP16 (typically within 0.01 difference), making it a good [balance](https://medium.com/@furkangozukara/comprehensive-analysis-of-gguf-variants-fp8-and-fp16-gguf-q8-vs-fp8-vs-fp16-c212fc077fb1) between memory efficiency and maintaining model quality. Be aware that multimodal support ([mmproj](https://simonwillison.net/2025/May/10/llama-cpp-vision/)) also consumes VRAM (typically ~1-2GB additional for most models).

For larger models like OpenAI's GPT-OSS 120B, which is a Mixture of Experts (MoE) model with 117B total parameters, you need approximately 61GB VRAM for the MXFP4 quantized version. While MoE models like GPT-OSS 120B only activate ~5.1B parameters per token (4 out of 128 experts), they still require substantial VRAM to store all expert weights but need less computation than equivalent dense 120B models.

With more RAM, you can serve more users without losing context (keeping data in VRAM is faster). Multi-user scenarios benefit from more VRAM. However, the A100 (Nvidia) with 80GB costs [$2'700/month](https://cloudprice.net/vm/Standard_NC24ads_A100_v4) or [$3.70/hour](https://instances.vantage.sh/azure/vm/nc24ads-v4?currency=USD). On the other hand, one could use [6 x 7900X for $15k](https://tinygrad.org/#tinygrad), providing 6x performance and 2x total RAM (144GB vs 80GB) when load can be perfectly distributed. The [RTX6000 (~$8'400)](https://www.digitec.ch/de/s1/product/nvidia-rtx-pro-6000-blackwell-max-q-wo-96-gb-grafikkarte-60695800) has more RAM than the A100, with benchmarks showing it's approximately 1.8x faster than the 7900XTX and A100.

**Note**: Azure doesn't currently offer dedicated MI100 instances - they've transitioned to newer architectures (V100, A100, H100). Historical MI100 cloud pricing was similar to A100 rates. The [A100 can be purchased for $10'000-$13'000](https://directmacro.com/blog/post/nvidia-a100-in-2025) but cloud rental remains more practical for most users.

With more VRAM, you can improve GPU utilization by batching multiple requests and serving more concurrent users. Modern optimization techniques can provide significant throughput improvements, though gains are limited by memory bandwidth rather than achieving linear scaling with VRAM size.

### PP vs TG Performance: Focus on Text Generation
Before looking into benchmarks, lets look at 2 important metrics: PP and TG.

**PP (Prompt Processing)** is the initial phase where the model processes your input prompt - this is typically fast across all hardware. **TG (Text Generation)** is the iterative token-by-token generation phase - this is the bottleneck that determines real-world usability.

Since PP happens only once per conversation while TG runs for every generated token, **TG performance is what matters most** for practical LLM usage. A model that processes prompts at 15,000 t/s but generates text at only 20 t/s will feel slow, while one with 3,000 t/s PP and 150 t/s TG will feel much more responsive.

When evaluating hardware, prioritize TG scores over PP scores - TG performance determines how quickly you'll see responses during actual use.

### Performance Benchmarks
LLaMA 7B, Q4, NO FA (Flash Attention). PP vs TG: PP is fast, so focus on TG, which is the bottleneck.

| Hardware | Memory | Backend | PP t/s | TG t/s | Price |
|----------|--------|---------|--------|--------|-------|
| **Apple M3U** | 96/256/512GB | [Metal](https://github.com/ggml-org/llama.cpp/discussions/4167) | 1471 | 92 | [7'300 CHF (256GB)](https://www.apple.com/ch-de/shop/buy-mac/mac-studio/apple-m3-ultra-mit-28-core-cpu,-60-core-gpu,-32-core-neural-engine-96-gb-arbeitsspeicher-1tb) |
| **Apple M3U** | 96/256/512GB | [MoltenVK](https://github.com/ggml-org/llama.cpp/discussions/10879) | 1116 | 115 | [7'300 CHF (256GB)](https://www.apple.com/ch-de/shop/buy-mac/mac-studio/apple-m3-ultra-mit-28-core-cpu,-60-core-gpu,-32-core-neural-engine-96-gb-arbeitsspeicher-1tb) |
| **RTX 5090** | 32GB | [CUDA](https://github.com/ggml-org/llama.cpp/discussions/15013) | 14751 | 239 | [2'124 CHF](https://www.digitec.ch/de/s1/product/zotac-geforce-rtx-5090-solid-oc-32-gb-grafikkarte-53945558) |
| **RTX 6000** | 96GB | [CUDA](https://github.com/ggml-org/llama.cpp/discussions/15013) | 14401 | 268 | [6'783 CHF](https://www.digitec.ch/de/s1/product/nvidia-rtx-pro-6000-blackwell-max-q-wo-96-gb-grafikkarte-60695800) |
| **RX 7900 XTX** | 24GB | [Vulkan](https://github.com/ggml-org/llama.cpp/discussions/10879) | 3831 | 130 | [778 CHF](https://www.digitec.ch/de/s1/product/xfx-radeon-rx-7900-xtx-merc310-black-gaming-24-gb-grafikkarte-23471756) |
| **RX 7900 XTX** | 24GB | [ROCm](https://github.com/ggml-org/llama.cpp/discussions/15021) | 3529 | 153 | [778 CHF](https://www.digitec.ch/de/s1/product/xfx-radeon-rx-7900-xtx-merc310-black-gaming-24-gb-grafikkarte-23471756) |
| **A100** | 80GB | [Vulkan](https://github.com/ggml-org/llama.cpp/discussions/10879) | 3103 | 121 | [$2'700/month](https://cloudprice.net/vm/Standard_NC24ads_A100_v4) (2'160 CHF) |
| **MI100** | 32GB | [ROCm](https://github.com/ggml-org/llama.cpp/discussions/15021) | 2732 | 110 | - |
| **H100**  | 96GB | [CUDA](https://github.com/ggml-org/llama.cpp/discussions/15013)| 9918 | 267 | [27'500 CHF](https://www.digitec.ch/de/s1/product/nvidia-h100-nvl-94-gb-grafikkarte-47130491)|
| **DGX Spark**  | 128GB | [CUDA](https://github.com/ggml-org/llama.cpp/discussions/15013)| 3062 | 57 | [4'000 CHF](https://www.digitec.ch/de/s1/product/pny-workstation-nvidia-dgx-spark-prozessorfamilie-nvidia-4000-gb-128-gb-pc-59656752) |
| **AIMAX395** | 128GB | [Vulkan](https://llm-tracker.info/AMD-Strix-Halo-(Ryzen-AI-Max+-395)-GPU-Performance) | 1288 | 54 | [$2'000](https://frame.work/products/desktop-diy-amd-aimax300/configuration/new) (1'600 CHF)|

Performance with AMD Ryzen AI MAX+ 395 should increase with [latest ROCm updates](https://github.com/geerlingguy/beowulf-ai-cluster/issues/7), addressing [known issues](https://github.com/ROCm/ROCm/issues/4748) and [optimization problems](https://github.com/ROCm/ROCm/issues/4499). But will it reach Apple M3 Ultra performance? Maybe for PP, but I guess not for TG.

## Apple M3 Ultra Memory Bandwidth Insights
Memory bandwidth on M3 Ultra is **identical** across all RAM configurations: 96GB, 256GB, 512GB all have **819GB/s bandwidth**. Performance is identical for workloads that fit in the smaller configuration. The 512GB advantage only applies for models requiring >400GB (like DeepSeek R1 with 671B parameters).

- **96GB**: Sufficient for most current LLMs up to 70B parameters
- **256GB**: Handles larger models and multi-model workflows  
- **512GB**: Required only for massive models (400GB+ like DeepSeek R1)

**Cost implication**: No huge performance benefit paying for 512GB unless you need >250GB models. For running e.g., gpt-oss 120B with a few users concurrently, 512GB might make sense, on the other hand 512GB costs 2'400 CHF more than with 256GB.

## Recommendations for buying
This recommendation targets a dedicated server build optimized for AI workloads.

1) **Apple M3 Ultra 256GB** for 7300 CHF - Energy efficient with ease of use and unified memory advantage. No memory copying between CPU/GPU, excellent power efficiency, simple setup with MLX framework, quiet operation. Alternatively, to run larger MoE models, 512GB could make sense.

1) **RTX 6000** for 6783 CHF - Maximum raw performance but requires powerful host machine and high power consumption. Specialized for AI workloads.

2) **RX 7900 XTX** for 778 CHF - Best budget option. Cheapest option with good performance, but only 24GB VRAM.

**Resale considerations**: All hardware can be resold, while the RTX6000 is specialized for AI workloads, other hardware has multiple purposes (Gaming, CAD). Apple M3 Ultra can be used for any computing task.

## Not Recommended for buying
**H100**: Expensive at 27'000 CHF, while the RTX6000 seems to deliver similar performance for much less.

**AMD Ryzen AI MAX+ 395**, **DGX Spark**: Both have currently a low tg/s around 50-60. While pp is much faster with DGX Spark, which helps if you have lots of input, its also twice as expensive, with similar tg/s, which is in many application the drivinng factor. 

This recommendation targets a dedicated server build optimized for AI workloads. For non-dedicated servers or development machines, the AMD Ryzen AI MAX+ 395 offers better value—the DGX Spark costs twice as much while only excelling at prompt processing. The Apple M3 Ultra 256GB is more versatile and can be used for both dev and dedicated server.

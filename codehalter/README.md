# llama-acp

Minimal ACP agent that bridges Zed to a local llama.cpp server.

## Prerequisites

- Go 1.22+
- llama.cpp running with `--port 8080` (OpenAI-compatible API)

## Build

```sh
just build
```

## Run llama.cpp

```sh
llama-server -m your-model.gguf --port 8080
```

## Configure Zed

Add to your Zed settings (`~/.config/zed/settings.json`):

```json
{
  "agent_servers": {
    "Llama Local": {
      "type": "custom",
      "command": "/absolute/path/to/llama-acp",
      "args": [],
      "env": {}
    }
  }
}
```

Then open the agent panel (`Cmd+?` / `Ctrl+?`), click `+`, and select "Llama Local".

## What this does

- Zed spawns `llama-acp` as a subprocess
- Communicates via JSON-RPC over stdio
- User prompts are forwarded to llama.cpp's `/v1/chat/completions` endpoint
- Response tokens stream back to Zed in real-time

## Limitations

- No conversation history (single-turn only — extend `Prompt` to accumulate messages)
- No tool calling / file editing (add `conn.ReadTextFile` / `conn.WriteTextFile` calls)
- No auth (fine for local use, blocks ACP registry submission)
- Hardcoded to localhost:8080 (make it configurable via env var if needed)

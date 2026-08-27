# 🎤 Voxtral Real-time Streaming Transcription Server

A TypeScript/Node.js server that provides real-time audio transcription using Voxtral models with configurable chunking and WebSocket streaming.

## ✨ Features

- **🎯 Real-time Streaming**: Process audio chunks as you speak (configurable 0.5-5s chunks)
- **🔄 Persistent Server**: Uses `llama-server` for zero-startup-delay transcription
- **📡 WebSocket Support**: Real-time bidirectional communication
- **🎛️ Configurable Chunking**: Adjust chunk duration for your use case
- **📝 Context Awareness**: Maintains conversation context across chunks
- **🌐 OpenAI Compatible**: Uses standard `/v1/chat/completions` API
- **📁 File Upload**: Traditional file-based transcription support
- **🔧 TypeScript**: Full type safety and excellent developer experience

## 🚀 Quick Start

### Prerequisites

```bash
# Install llama.cpp with multimodal support
# Follow instructions at: https://github.com/ggml-org/llama.cpp

# Ensure you have llama-server available
llama-server --version
```

### Installation

```bash
# Clone and install
git clone <your-repo>
cd voxtral-typescript-server
npm install

# Copy environment template
cp .env.example .env

# Build the project
npm run build

# Start the server
npm start
```

### Development

```bash
# Development with hot reload
npm run dev

# Watch mode for TypeScript compilation
npm run watch

# Lint code
npm run lint
```

## 📖 Usage

### 1. Start the Server

```bash
# Production
npm start

# Development
npm run dev

# With custom configuration
PORT=3000 npm start
```

### 2. Access the Demo

Open your browser to `http://localhost:8080` to see the interactive demo with:

- **🎤 Live Microphone Recording** with configurable chunk duration
- **📁 File Upload Transcription**
- **📊 Real-time Status Monitoring**
- **🔧 Configuration Panel**

### 3. WebSocket API

```typescript
const ws = new WebSocket('ws://localhost:8080');

// Send audio chunk
ws.send(JSON.stringify({
  type: 'audio_chunk',
  data: {
    audio_data: base64AudioData,
    chunk_index: 0,
    timestamp: Date.now()
  }
}));

// Receive real-time results
ws.onmessage = (event) => {
  const msg = JSON.parse(event.data);
  switch(msg.type) {
    case 'partial':
      console.log('Chunk result:', msg.data.transcription);
      break;
    case 'session_complete':
      console.log('Final:', msg.data.transcription);
      break;
  }
};
```

### 4. HTTP API

```bash
# Health check
curl http://localhost:8080/health

# File transcription
curl -X POST http://localhost:8080/transcribe/file \
  -F "audio=@audio.mp3" \
  -F "prompt=Transcribe this audio"
```

## ⚙️ Configuration

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `HOST` | `0.0.0.0` | Server bind address |
| `PORT` | `8080` | Server port |
| `CLI_PATH` | `llama-server` | Path to llama-server binary |
| `VOXTRAL_SERVER_PORT` | `8081` | Internal Voxtral server port |
| `TEMP_DIR` | `/tmp/voxtral` | Temporary file directory |
| `TIMEOUT_SECS` | `300` | Request timeout (5 minutes) |

### Runtime Configuration

The web UI provides real-time configuration:

- **Chunk Duration**: Slider from 0.5s to 5s
- **Context Display**: Toggle context visibility
- **Connection Status**: Monitor WebSocket health

## 🏗️ Architecture

```
🎤 Browser Microphone → MediaRecorder → WebSocket → TypeScript Server → llama-server → Voxtral Model
                                                        ↓
📝 Real-time Results ← WebSocket ← Context Management ← OpenAI API ← Transcription
```

### Components

1. **🎤 Audio Capture**: Browser MediaRecorder API with configurable chunking
2. **📡 WebSocket Server**: Real-time bidirectional communication
3. **🔄 Session Management**: Per-connection audio sessions with context
4. **🤖 Voxtral Integration**: Persistent `llama-server` with OpenAI API
5. **📝 Context Management**: Maintains conversation flow across chunks

## 🎯 API Endpoints

### WebSocket Messages

#### Client → Server

```typescript
// Start streaming session
{
  type: 'start_session',
  data: { chunk_duration: 1.0, prompt: 'Transcribe clearly' }
}

// Send audio chunk
{
  type: 'audio_chunk',
  data: { audio_data: 'base64...', chunk_index: 0 }
}

// End session
{ type: 'end_session' }
```

#### Server → Client

```typescript
// Connection established
{
  type: 'connected',
  data: { session_id: 'uuid', chunk_duration: 1 }
}

// Partial transcription
{
  type: 'partial',
  data: { 
    transcription: 'Hello world',
    full_context: 'Previous text Hello world'
  }
}

// Session complete
{
  type: 'session_complete',
  data: { transcription: 'Full conversation text' }
}
```

### HTTP Endpoints

- `GET /health` - Server health and status
- `POST /transcribe/file` - File-based transcription
- `
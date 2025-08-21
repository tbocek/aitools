import express from 'express';
import { WebSocketServer, WebSocket } from 'ws';
import { createServer } from 'http';
import { v4 as uuidv4 } from 'uuid';
import path from 'path';
import fs from 'fs';
import axios from 'axios';
import dotenv from 'dotenv';
import { spawn } from "child_process";

// Load environment variables from .env file
dotenv.config();

// Configuration interface
interface Config {
  host: string;
  port: number;
  tempDir: string;
  maxFileSize: number;
  timeoutSecs: number;
  voxtralServerURL: string;
}

// WebSocket message types
interface WSMessage {
  type: string;
  data?: any;
  error?: string;
  status?: string;
  partial?: boolean;
}

interface VoxtralServerRequest {
  model?: string;
  messages: Array<{
    role: string;
    content: Array<{
      type: string;
      text?: string;
      input_audio?: {
        data: string;    // base64 encoded audio data
        format: string;  // audio format: 'wav', 'mp3', 'webm', etc.
      };
    }>;
  }>;
  stream?: boolean;
  max_tokens?: number;
  temperature?: number; 
}

interface VoxtralServerResponse {
  choices: Array<{
    message: {
      content: string;
    };
  }>;
  error?: {
    message: string;
    type?: string;
    code?: number;
  };
}

// Voxtral Server Manager
class VoxtralServerManager {
  private config: Config;

  constructor(config: Config) {
    this.config = config;
  }
  
  async transcribe2(audioData: Buffer, prompt: string): Promise<string> {
    try {
      // Create form data for whisper.cpp
      const formData = new FormData();
      
      // Add the audio file as a blob
      const audioBlob = new Blob([audioData], { type: 'audio/mpeg' });
      formData.append('file', audioBlob, 'audio.mp3');
      
      // Add the custom prompt with anti-hallucination instructions
      const whisperPrompt = `You are Voxtral, a specialized audio transcription assistant. Your ONLY task is to convert actual spoken audio into accurate written text. Follow these critical guidelines:
  
 * NEVER generate fictional content - Only transcribe what is actually spoken in the audio
 * If there is no clear speech, silence, or unintelligible audio: Return {"transcription": "[silence]"}
 * If audio contains actual speech: Return {"transcription": "exact words spoken"}
 * Output format: Clean JSON only - no markdown, no code blocks
 * Language preservation: Keep the original language of user (English, Deutsch, Italiano, etc.)
  
  CRITICAL ANTI-HALLUCINATION MEASURES:
  - DO NOT invent dialogue, conversations, or speech that isn't present
  - DO NOT create plausible-sounding content when audio is unclear
  - DO NOT guess what someone "might have said"
  - DO NOT fill silence with imaginary speech
  - VERIFY actual audio content exists before transcribing anything
  
  AUDIO QUALITY GUIDELINES:
  - Clear speech with distinguishable words → Transcribe exactly
  - Mumbled/unclear speech → {"transcription": "[unclear]"}
  - Background noise only → {"transcription": "[silence]"}
  - No audio/empty file → {"transcription": "[silence]"}
  - Mixed (some clear, some unclear) → Transcribe clear parts, mark unclear as [unclear]
  
  REMEMBER: Your credibility depends on accuracy. Never manufacture content. If in doubt, return "[silence]" or "[unclear]".
  
  ${prompt}`;
  
      formData.append('prompt', whisperPrompt);
      
      // Optional parameters for whisper.cpp
      formData.append('response_format', 'json');
      //formData.append('language', 'auto'); // or specify: 'en', 'de', 'it', etc.
      formData.append('temperature', '0.0'); // Lower temperature for more consistent output
      
      const response = await axios.post(
        `http://${this.config.voxtralServerURL}/v1/audio/transcriptions`,
        formData,
        {
          headers: {
            'Content-Type': 'multipart/form-data'
          },
          timeout: this.config.timeoutSecs * 1000
        }
      );
  
      if (response.data.error) {
        throw new Error(`Whisper error: ${response.data.error}`);
      }
  
      // Whisper.cpp returns { "text": "transcribed content" }
      const transcriptionText = response.data.text || '';
      
      // Try to parse as JSON if it follows our format, otherwise return as-is
      try {
        const parsed = JSON.parse(transcriptionText);
        if (parsed.transcription) {
          return JSON.stringify(parsed); // Return the JSON format we expect
        }
      } catch (parseError) {
        // If not JSON, wrap it in our expected format
        return JSON.stringify({ transcription: transcriptionText });
      }
      
      return JSON.stringify({ transcription: transcriptionText });
  
    } catch (error) {
      console.error('Transcription error:', error);
      throw error;
    }
  }

  async transcribe(audioData: Buffer, prompt: string): Promise<string> {
    try {
      // Convert audio buffer to base64
      const audioBase64 = audioData.toString('base64');

      const request: VoxtralServerRequest = {
        messages: [{
            role: 'system',
            content: [
              {
                type: 'text',
                text: `You are an audio transcription system. Your only job is to convert speech to text.
                
                RULES:
                - Output ONLY raw JSON: {"transcription": "text"}
                - Do NOT use markdown, code blocks, or backticks
                - If no speech detected: {"transcription": "[silence]"}
                - If unclear speech: {"transcription": "[unclear]"}
                - Never invent or guess words
                - Keep original language
                
                EXAMPLES:
                Good: {"transcription": "Hello world"}
                Good: {"transcription": "[silence]"}
                Bad: \`\`\`json{"transcription": "text"}\`\`\`
                Bad: Here is the transcription: {"transcription": "text"}
                
                Output MUST start with { and end with } - nothing else.`
              }
            ]
          },
          {
          role: 'user',
          content: [
            {
              type: 'text',
              text: prompt
            },
            {
              type: 'input_audio',
              input_audio: {
                data: audioBase64,
                format: 'mp3'
              }
            }
          ]
        }],
        stream: false,
        temperature: 0
      };

      const response = await axios.post<VoxtralServerResponse>(
        `http://${this.config.voxtralServerURL}/v1/chat/completions`,
        request,
        {
          headers: {
            'Content-Type': 'application/json'
          },
          timeout: this.config.timeoutSecs * 1000
        }
      );

      if (response.data.error) {
        throw new Error(`Voxtral error: ${response.data.error.message}`);
      }

      const content = response.data.choices[0]?.message?.content || '';
      return content;

    } catch (error) {
      console.error('Transcription error:', error);
      throw error;
    }
  }

  async isServerRunning(): Promise<boolean> {
    try {
      await axios.get(`http://${this.config.voxtralServerURL}/health`, {
        timeout: 1000
      });
      return true;
    } catch (error) {
      return false;
    }
  }
}

class AudioSession {
  public id: string;
  public ws: WebSocket;
  private webmBuffer: Buffer[] = []; 
  private isActive: boolean = true;
  private server: VoxtralServer;
  private tempDir: string;
  private fullTranscription: string = '';
  
  private ffmpegProcess: any = null;
  private isProcessing: boolean = false;
  
  // Add speech detection state
  private isSpeechActive: boolean = false;
  private speechStartTime: number = 0;
  private speechChunkCount: number = 0;  // Track chunks during speech
  private mp3Buffer: Buffer[] = [];  // Collect MP3 output continuously

  constructor(id: string, ws: WebSocket, server: VoxtralServer) {
    this.id = id;
    this.ws = ws;
    this.server = server;
    this.tempDir = `/tmp/voxtral/session_${this.id}`;
    
    if (!fs.existsSync(this.tempDir)) {
      fs.mkdirSync(this.tempDir, { recursive: true });
    }
    
    this.startFFmpegProcess();
  }

  private startFFmpegProcess(): void {
    this.ffmpegProcess = spawn("ffmpeg", [
      "-f", "webm",
      "-i", "pipe:0",
      "-ar", "16000",
      "-ac", "1",
      "-f", "mp3",
      "-reset_timestamps", "1",
      "pipe:1"
    ]);

    // Continuously collect MP3 output
    this.ffmpegProcess.stdout.on("data", (data: Buffer) => {
      this.mp3Buffer.push(data);
      // Keep only the last N chunks to prevent memory bloat
      //if (this.mp3Buffer.length > 100) {
      //  this.mp3Buffer.shift(); // Remove oldest chunk
      // }
    });

    this.ffmpegProcess.stderr.on("data", (data: any) => {
      console.log(`FFmpeg [${this.id}]:`, data.toString());
    });

    this.ffmpegProcess.on("close", (code: number) => {
      console.log(`FFmpeg process for session ${this.id} exited with code ${code}`);
    });
  }

  start(): void {
    console.log(`Session ${this.id} started`);
  }

  // Add method to handle speech detection
  handleSpeechDetected(): void {
    if (!this.isSpeechActive) {
      console.log(`Speech detected for session ${this.id}`);
      this.isSpeechActive = true;
      this.speechStartTime = Date.now();
      this.speechChunkCount = 0;
      // Clear any existing MP3 data to start fresh for this speech segment
      //this.mp3Buffer = [];
      if (this.mp3Buffer.length > 25) {
        this.mp3Buffer = this.mp3Buffer.slice(-25);
      }
    }
  }

  // Always send audio to FFmpeg, but track speech periods
  addAudioData(data: Buffer): void {
    if (!this.isActive || !this.ffmpegProcess) return;
    
    try {
      // Always write to FFmpeg to maintain stream continuity
      this.ffmpegProcess.stdin.write(data);
      
      if (this.isSpeechActive) {
        this.speechChunkCount++;
        console.log(`Sent audio chunk to FFmpeg for session ${this.id}, speech chunk: ${this.speechChunkCount}`);
      } else {
        console.log(`Sent audio chunk to FFmpeg for session ${this.id} (silence)`);
      }
    } catch (error) {
      console.error(`Failed to write audio data to FFmpeg for session ${this.id}:`, error);
    }
  }

  async processSilenceDetected(): Promise<void> {
    if (this.isProcessing) {
      console.log(`Skipping silence processing for session ${this.id} - already processing`);
      return;
    }
    
    // Only process if we had speech activity
    if (!this.isSpeechActive) {
      console.log(`Silence detected but no speech was active for session ${this.id}`);
      return;
    }
    
    if (this.speechChunkCount === 0) {
      console.log(`No speech chunks recorded for session ${this.id}`);
      this.isSpeechActive = false;
      return;
    }

    // Mark speech as inactive and start processing
    this.isSpeechActive = false;
    const speechDuration = Date.now() - this.speechStartTime;
    console.log(`Processing speech segment of ${speechDuration}ms with ${this.speechChunkCount} audio chunks`);
    
    this.isProcessing = true;
    
    try {
      // Wait a moment for FFmpeg to process the latest chunks
      await new Promise(resolve => setTimeout(resolve, 500));
      
      // Collect the MP3 data that accumulated during speech
      const mp3Data = Buffer.concat(this.mp3Buffer);
      
      // Check if we got valid MP3 data
      if (!mp3Data || mp3Data.length === 0) {
        console.log(`No MP3 data available for session ${this.id}`);
        return;
      }
      
      console.log(`Processing ${mp3Data.length} bytes of MP3 data for session ${this.id}`);
      
      const prompt = "Transcribe this audio segment";
      const transcription = await this.server.getServerManager().transcribe(mp3Data, prompt);
      
      try {
        const timestamp = new Date().toISOString().replace(/[:.]/g, '-');
        const mp3FileName = `/tmp/audio/${timestamp}_${this.id.slice(0, 8)}.mp3`;
        fs.writeFileSync(mp3FileName, mp3Data);
        console.log(`Saved ${mp3Data.length} bytes to ${mp3FileName}`);
      } catch (error) {
        console.error(`Failed to save MP3 data:`, error);
      }
      
      if (this.fullTranscription) {
        this.fullTranscription += " " + transcription;
      } else {
        this.fullTranscription = transcription;
      }

      this.sendMessage({
        type: 'partial',
        data: {
          transcription,
          full_transcription: this.fullTranscription
        },
        partial: true
      });

    } catch (error) {
      console.error("Error processing audio:", error);
    } finally {
      this.isProcessing = false;
      // Clear the MP3 buffer for the next speech segment
      this.mp3Buffer = [];
    }
  }

  stop(): void {
    this.isActive = false;
    this.isSpeechActive = false;
    
    if (this.ffmpegProcess) {
      this.ffmpegProcess.stdin.end();
      this.ffmpegProcess.kill();
    }
  }

  private sendMessage(message: WSMessage): void {
    if (this.ws.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify(message));
    }
  }

  getFinalTranscription(): string {
    return this.fullTranscription;
  }
}

// Main Server Class
class VoxtralServer {
  private config: Config;
  private app: express.Application;
  private server: any;
  private wss: WebSocketServer;
  private serverManager: VoxtralServerManager;
  private audioSessions: Map<string, AudioSession> = new Map();

  constructor(config: Config) {
    this.config = config;
    this.app = express();
    this.server = createServer(this.app);
    this.wss = new WebSocketServer({ server: this.server });
    this.serverManager = new VoxtralServerManager(config);

    this.setupMiddleware();
    this.setupRoutes();
    this.setupWebSocket();
  }

  private setupMiddleware(): void {
    this.app.use(express.json({ limit: '50mb' }));
    
    // Create temp directory if it doesn't exist
    if (!fs.existsSync(this.config.tempDir)) {
      fs.mkdirSync(this.config.tempDir, { recursive: true });
    }
  }

  private setupRoutes(): void {
    // Serve HTML interface on root path
    this.app.get('/', (req, res) => {
      const htmlPath = path.join(__dirname, 'index.html');
      
      if (fs.existsSync(htmlPath)) {
        res.sendFile(htmlPath);
      } else {
        res.status(404).send('index.html not found');
      }
    });
  }

  private setupWebSocket(): void {
    this.wss.on('connection', (ws) => {
      const sessionId = uuidv4();
      const session = new AudioSession(sessionId, ws, this);
      
      this.audioSessions.set(sessionId, session);

      // Send connection confirmation
      ws.send(JSON.stringify({
        type: 'connected',
        status: 'ready',
        data: {
          session_id: sessionId
        }
      }));

      ws.on('message', (data) => {
        try {
          const message: WSMessage = JSON.parse(data.toString());
          this.handleWebSocketMessage(session, message);
        } catch (error) {
          ws.send(JSON.stringify({
            type: 'error',
            error: 'Invalid message format'
          }));
        }
      });

      ws.on('close', () => {
        session.stop();
        this.audioSessions.delete(sessionId);
        console.log(`Session ${sessionId} disconnected`);
      });

      session.start();
    });
  }

  private handleWebSocketMessage(session: AudioSession, message: WSMessage): void {
    switch (message.type) {
      case 'audio_chunk':
        this.handleAudioChunk(session, message);
        break;
        
      case 'speech_detected':
        session.handleSpeechDetected();
        break;
        
      case 'silence_detected':
        this.handleSilenceDetected(session);
        break;
        
      case 'start_session':
        session.ws.send(JSON.stringify({
          type: 'session_started',
          status: 'recording'
        }));
        break;
        
      case 'end_session':
        this.handleEndSession(session);
        break;
        
      case 'ping':
        session.ws.send(JSON.stringify({ type: 'pong' }));
        break;
        
      default:
        session.ws.send(JSON.stringify({
          type: 'error',
          error: 'Unknown message type'
        }));
    }
  }

  private handleAudioChunk(session: AudioSession, message: WSMessage): void {
    if (!message.data || !message.data.audio_data) {
      session.ws.send(JSON.stringify({
        type: 'error',
        error: 'Missing audio_data'
      }));
      return;
    }

    try {
      // Decode base64 audio data
      const audioData = Buffer.from(message.data.audio_data, 'base64');
      session.addAudioData(audioData);
    } catch (error) {
      session.ws.send(JSON.stringify({
        type: 'error',
        error: 'Failed to decode audio data'
      }));
    }
  }

  private async handleSilenceDetected(session: AudioSession): Promise<void> {
    console.log(`Silence detected for session ${session.id}`);
    await session.processSilenceDetected();
  }

  private async handleEndSession(session: AudioSession): Promise<void> {
    // Process any remaining audio
    await session.processSilenceDetected();
    
    session.stop();

    // Send final transcription
    const finalTranscription = session.getFinalTranscription();
    
    session.ws.send(JSON.stringify({
      type: 'session_complete',
      data: {
        transcription: finalTranscription
      }
    }));
  }

  async start(): Promise<void> {
    // Start HTTP/WebSocket server
    this.server.listen(this.config.port, this.config.host, () => {
      console.log(`Voxtral API server listening on ${this.config.host}:${this.config.port}`);
      console.log(`WebSocket endpoint: ws://${this.config.host}:${this.config.port}`);
      console.log(`Voxtral server: ${this.config.voxtralServerURL}`);
    });
  }

  async stop(): Promise<void> {
    // Stop all audio sessions
    for (const session of this.audioSessions.values()) {
      session.stop();
    }
    this.audioSessions.clear();

    // Stop server
    this.server.close();
  }

  getConfig(): Config {
    return this.config;
  }

  getServerManager(): VoxtralServerManager {
    return this.serverManager;
  }
}

// Main execution
async function main() {
  const config: Config = {
    host: process.env.HOST || '0.0.0.0',
    port: parseInt(process.env.PORT || '8080'),
    tempDir: process.env.TEMP_DIR || '/tmp/voxtral',
    maxFileSize: 50 * 1024 * 1024, // 50MB
    timeoutSecs: parseInt(process.env.TIMEOUT_SECS || '300'),
    voxtralServerURL: process.env.VOXTRAL_SERVER_URL || '127.0.0.1:8081',
  };

  const server = new VoxtralServer(config);

  // Graceful shutdown
  process.on('SIGINT', async () => {
    console.log('\nShutting down...');
    await server.stop();
    process.exit(0);
  });

  process.on('SIGTERM', async () => {
    console.log('\nShutting down...');
    await server.stop();
    process.exit(0);
  });

  try {
    await server.start();
  } catch (error) {
    console.error('Failed to start server:', error);
    process.exit(1);
  }
}

// Run the server
if (require.main === module) {
  main().catch(console.error);
}

export { VoxtralServer, Config };
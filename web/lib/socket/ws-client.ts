import { WSEventMessage } from '../../types/aegis';

export type WSEventHandler = (event: WSEventMessage) => void;

export class AegisWebSocketClient {
  private url?: string;
  private ws: WebSocket | null = null;
  private handlers: Set<WSEventHandler> = new Set();
  private isConnected: boolean = false;
  private reconnectAttempts: number = 0;
  private maxReconnectDelay: number = 30000;
  private heartbeatInterval: number = 15000;
  private heartbeatTimer: NodeJS.Timeout | null = null;

  constructor(url?: string) {
    this.url = url;
  }

  private getEffectiveURL(): string {
    if (this.url) return this.url;
    if (typeof window !== 'undefined') {
      const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
      return `${protocol}//${window.location.host}/v1/ws`;
    }
    return 'wss://api.aegis.internal/v1/ws';
  }

  public connect(): void {
    if (typeof window === 'undefined') return;

    try {
      const targetUrl = this.getEffectiveURL();
      this.ws = new WebSocket(targetUrl);

      this.ws.onopen = () => {
        this.isConnected = true;
        this.reconnectAttempts = 0;
        this.startHeartbeat();
      };

      this.ws.onmessage = (event: MessageEvent) => {
        try {
          const message: WSEventMessage = JSON.parse(event.data);
          this.handlers.forEach((handler) => handler(message));
        } catch {
          // Ignore malformed WS frames
        }
      };

      this.ws.onerror = () => {
        this.ws?.close();
      };

      this.ws.onclose = () => {
        this.isConnected = false;
        this.stopHeartbeat();
        this.scheduleReconnect();
      };
    } catch {
      // Fallback for offline/demo environment
      this.scheduleReconnect();
    }
  }

  public subscribe(handler: WSEventHandler): () => void {
    this.handlers.add(handler);
    return () => {
      this.handlers.delete(handler);
    };
  }

  private startHeartbeat(): void {
    this.stopHeartbeat();
    this.heartbeatTimer = setInterval(() => {
      if (this.ws?.readyState === WebSocket.OPEN) {
        this.ws.send(JSON.stringify({ type: 'PING', timestamp: new Date().toISOString() }));
      }
    }, this.heartbeatInterval);
  }

  private stopHeartbeat(): void {
    if (this.heartbeatTimer) {
      clearInterval(this.heartbeatTimer);
      this.heartbeatTimer = null;
    }
  }

  private scheduleReconnect(): void {
    // Exponential backoff with random jitter: Initial 1s -> Max 30s
    const baseDelay = Math.min(1000 * Math.pow(2, this.reconnectAttempts), this.maxReconnectDelay);
    const jitter = Math.floor(Math.random() * 1000);
    const delay = baseDelay + jitter;

    this.reconnectAttempts++;
    setTimeout(() => {
      this.connect();
    }, delay);
  }

  public disconnect(): void {
    this.stopHeartbeat();
    this.ws?.close();
    this.ws = null;
  }
}

export const aegisWS = new AegisWebSocketClient();

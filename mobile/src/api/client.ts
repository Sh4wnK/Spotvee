import * as SecureStore from 'expo-secure-store';

// ---------------------------------------------------------------------------
// Types mirroring the Go server's JSON API.

export interface DeviceInfo {
  id: string;
  sku: string;
  name: string;
  selected: boolean;
}

export interface Color {
  r: number;
  g: number;
  b: number;
}

export interface State {
  track: {
    id: string;
    name: string;
    artist: string;
    album: string;
    art_url: string;
    duration_ms: number;
  };
  color: {
    dominant: Color;
    palette: Color[];
    weights: number[];
  };
  syncing: boolean;
  error?: string;
  updated_at: string;
}

export interface Metrics {
  count: number;
  sum: number;
  p50: number;
  p95: number;
  p99: number;
  max: number;
}

// ---------------------------------------------------------------------------
// Settings persistence (expo-secure-store).

const SERVER_URL_KEY = 'spotvee.serverUrl';
const APP_TOKEN_KEY = 'spotvee.appToken';

export async function getStoredUrl(): Promise<string | null> {
  return SecureStore.getItemAsync(SERVER_URL_KEY);
}

export async function setStoredUrl(url: string): Promise<void> {
  await SecureStore.setItemAsync(SERVER_URL_KEY, url);
}

export async function getStoredToken(): Promise<string | null> {
  return SecureStore.getItemAsync(APP_TOKEN_KEY);
}

export async function setStoredToken(token: string): Promise<void> {
  await SecureStore.setItemAsync(APP_TOKEN_KEY, token);
}

// ---------------------------------------------------------------------------
// HTTP client.

export class ApiError extends Error {
  readonly status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

export class Api {
  readonly baseUrl: string;
  private token: string;

  constructor(baseUrl: string, token: string) {
    this.baseUrl = baseUrl.replace(/\/+$/, '');
    this.token = token;
  }

  setToken(token: string) {
    this.token = token;
  }

  getToken(): string {
    return this.token;
  }

  private headers(): Record<string, string> {
    return {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${this.token}`,
    };
  }

  private async request<T>(method: string, path: string, body?: unknown): Promise<T> {
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), 15000);
    try {
      const res = await fetch(`${this.baseUrl}${path}`, {
        method,
        headers: this.headers(),
        body: body === undefined ? undefined : JSON.stringify(body),
        signal: controller.signal,
      });
      if (!res.ok) {
        let msg = `HTTP ${res.status}`;
        try {
          const j = (await res.json()) as { error?: string };
          if (j.error) msg = j.error;
        } catch {
          // keep default message
        }
        throw new ApiError(res.status, msg);
      }
      const text = await res.text();
      return (text ? JSON.parse(text) : undefined) as T;
    } catch (e) {
      if (e instanceof ApiError) throw e;
      throw new ApiError(0, e instanceof Error ? e.message : 'network error');
    } finally {
      clearTimeout(timeout);
    }
  }

  async health(): Promise<boolean> {
    try {
      await this.request<{ status: string }>('GET', '/healthz');
      return true;
    } catch {
      return false;
    }
  }

  async authStatus(): Promise<boolean> {
    const r = await this.request<{ authenticated: boolean }>('GET', '/api/auth/status');
    return r.authenticated;
  }

  async loginUrl(): Promise<string> {
    const r = await this.request<{ url: string }>('GET', '/api/auth/login-url');
    return r.url;
  }

  async devices(): Promise<DeviceInfo[]> {
    return this.request<DeviceInfo[]>('GET', '/api/devices');
  }

  async selectDevices(ids: string[]): Promise<void> {
    await this.request('POST', '/api/select', { ids });
  }

  async setSync(enabled: boolean, cycle: boolean): Promise<void> {
    await this.request('POST', '/api/sync', { enabled, cycle });
  }

  async state(): Promise<State> {
    return this.request<State>('GET', '/api/state');
  }

  async metrics(): Promise<Metrics> {
    return this.request<Metrics>('GET', '/api/metrics');
  }
}

// ---------------------------------------------------------------------------
// WebSocket state stream.

export type StateListener = (s: State) => void;

export class StateStream {
  private ws: WebSocket | null = null;
  private listeners = new Set<StateListener>();
  private baseUrl: string;
  private token: string;
  private closed = false;

  constructor(baseUrl: string, token: string) {
    this.baseUrl = baseUrl;
    this.token = token;
  }

  connect() {
    if (this.ws) return;
    this.closed = false;
    const url = this.baseUrl
      .replace(/^http/, 'ws')
      .replace(/\/+$/, '')
      .concat('/api/ws');
    this.ws = new WebSocket(url);

    this.ws.onmessage = (ev) => {
      try {
        const state = JSON.parse(String(ev.data)) as State;
        this.listeners.forEach((l) => l(state));
      } catch {
        // malformed frame; ignore
      }
    };
    this.ws.onclose = () => {
      this.ws = null;
      if (!this.closed) {
        setTimeout(() => this.connect(), 2000); // auto-reconnect
      }
    };
    this.ws.onerror = () => {
      // onclose follows; the reconnect timer handles it
    };
  }

  onState(l: StateListener): () => void {
    this.listeners.add(l);
    return () => this.listeners.delete(l);
  }

  close() {
    this.closed = true;
    if (this.ws) {
      this.ws.close();
      this.ws = null;
    }
  }
}

// ---------------------------------------------------------------------------
// Color helpers.

export function hexColor(c: Color): string {
  const to2 = (v: number) => Math.round(Math.max(0, Math.min(255, v))).toString(16).padStart(2, '0');
  return `#${to2(c.r)}${to2(c.g)}${to2(c.b)}`;
}
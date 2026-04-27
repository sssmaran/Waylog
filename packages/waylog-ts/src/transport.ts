import type { Status, WaylogConfig, WideEvent } from "./types.js";

const defaultMaxBatch = 256;
const defaultMaxBatchSize = 1 << 20;
const defaultBatchAgeMs = 50;
const defaultInFlightCap = 10 << 20;
const defaultOkBudgetPct = 70;
const defaultMaxRetries = 5;

type Queued = {
  ev: WideEvent;
  bytes: number;
  attempts: number;
};

type DeliveryResult = {
  success: boolean;
  retryable: boolean;
  retryAfterMs: number;
};

export class Transport {
  private readonly url: string;
  private readonly apiKey?: string;
  private readonly fetchImpl: typeof fetch;
  private readonly maxBatch: number;
  private readonly maxBatchSize: number;
  private readonly batchAgeMs: number;
  private readonly inFlightCap: number;
  private readonly okBudgetPct: number;
  private readonly maxRetries: number;
  private readonly batchMode: boolean;

  private okQ: Queued[] = [];
  private priorityQ: Queued[] = [];
  private okBytes = 0;
  private priorityBytes = 0;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private flushing = false;
  private closed = false;
  private dropped = 0;
  private failures = 0;
  private jsonPosts = new Set<Promise<void>>();

  constructor(config: WaylogConfig) {
    if (!config.ingestUrl) throw new Error("Transport: ingestUrl is required");
    this.url = normalizeIngestUrl(config.ingestUrl);
    this.apiKey = config.apiKey;
    this.fetchImpl = config.fetch ?? fetch;
    this.maxBatch = defaultMaxBatch;
    this.maxBatchSize = defaultMaxBatchSize;
    this.batchAgeMs = defaultBatchAgeMs;
    this.inFlightCap = config.maxInFlightBytes && config.maxInFlightBytes > 0 ? config.maxInFlightBytes : defaultInFlightCap;
    this.okBudgetPct = defaultOkBudgetPct;
    this.maxRetries = defaultMaxRetries;
    this.batchMode = config.batchMode ?? true;
  }

  submit(ev: WideEvent): boolean {
    if (this.closed) {
      this.dropped++;
      return false;
    }
    if (!this.batchMode) {
      const post = this.postJSON(ev);
      this.jsonPosts.add(post);
      void post.finally(() => this.jsonPosts.delete(post));
      return true;
    }
    const item: Queued = { ev, bytes: estimateEventSize(ev), attempts: 0 };
    const accepted = isPriorityStatus(ev.status) ? this.enqueuePriority(item) : this.enqueueOK(item);
    if (!accepted) {
      this.dropped++;
      return false;
    }
    if (this.ready()) void this.flush(false);
    else this.armTimer();
    return true;
  }

  droppedCount(): number {
    return this.dropped;
  }

  failureCount(): number {
    return this.failures;
  }

  async shutdown(timeoutMs = 0): Promise<void> {
    this.closed = true;
    if (this.timer) clearTimeout(this.timer);
    const drain = Promise.all([
      this.flush(true),
      Promise.allSettled([...this.jsonPosts]).then(() => undefined),
    ]).then(() => undefined);
    if (timeoutMs <= 0) {
      await drain;
      return;
    }
    await Promise.race([drain, new Promise<void>((resolve) => setTimeout(resolve, timeoutMs))]);
  }

  private enqueueOK(item: Queued): boolean {
    const okCap = Math.floor((this.inFlightCap * this.okBudgetPct) / 100);
    if (item.bytes > okCap) return false;
    while (this.okBytes + item.bytes > okCap && this.okQ.length > 0) this.dropOK();
    if (this.okBytes + item.bytes > okCap) return false;
    this.okQ.push(item);
    this.okBytes += item.bytes;
    return true;
  }

  private enqueuePriority(item: Queued): boolean {
    if (item.bytes > this.inFlightCap) return false;
    while (this.totalBytes() + item.bytes > this.inFlightCap && this.okQ.length > 0) this.dropOK();
    while (this.totalBytes() + item.bytes > this.inFlightCap && this.priorityQ.length > 0) this.dropPriority();
    if (this.totalBytes() + item.bytes > this.inFlightCap) return false;
    this.priorityQ.push(item);
    this.priorityBytes += item.bytes;
    return true;
  }

  private ready(): boolean {
    return this.priorityQ.length >= this.maxBatch || this.okQ.length >= this.maxBatch || this.priorityBytes >= this.maxBatchSize || this.okBytes >= this.maxBatchSize;
  }

  private armTimer(): void {
    if (this.timer) return;
    this.timer = setTimeout(() => {
      this.timer = undefined;
      void this.flush(true);
    }, this.batchAgeMs);
  }

  private async flush(force: boolean): Promise<void> {
    if (this.flushing) return;
    this.flushing = true;
    if (this.timer) {
      clearTimeout(this.timer);
      this.timer = undefined;
    }
    try {
      while (true) {
        const batch = this.takeBatch(force);
        if (batch.items.length === 0) break;
        const result = await this.post(batch.items.map((item) => item.ev));
        if (!result.success && result.retryable) {
          await this.retry(batch.items, batch.priority, result.retryAfterMs);
          continue;
        }
      }
    } finally {
      this.flushing = false;
      if (!this.closed && (this.okQ.length > 0 || this.priorityQ.length > 0)) this.armTimer();
    }
  }

  private takeBatch(force: boolean): { priority: boolean; items: Queued[] } {
    const priority = this.takeFrom(this.priorityQ, force);
    if (priority.length > 0) {
      this.priorityBytes -= sumBytes(priority);
      return { priority: true, items: priority };
    }
    const ok = this.takeFrom(this.okQ, force);
    if (ok.length > 0) {
      this.okBytes -= sumBytes(ok);
      return { priority: false, items: ok };
    }
    return { priority: false, items: [] };
  }

  private takeFrom(q: Queued[], force: boolean): Queued[] {
    const bytes = sumBytes(q);
    if (q.length === 0 || (!force && q.length < this.maxBatch && bytes < this.maxBatchSize)) return [];
    const out: Queued[] = [];
    let batchBytes = 0;
    while (q.length > 0 && out.length < this.maxBatch) {
      const next = q[0]!;
      if (out.length > 0 && batchBytes + next.bytes > this.maxBatchSize) break;
      out.push(q.shift()!);
      batchBytes += next.bytes;
    }
    return out;
  }

  private async post(events: WideEvent[]): Promise<DeliveryResult> {
    const body = events.map((ev) => JSON.stringify(ev)).join("\n") + "\n";
    const headers: Record<string, string> = { "Content-Type": "application/x-ndjson" };
    if (this.apiKey) headers.Authorization = `Bearer ${this.apiKey}`;
    try {
      const resp = await this.fetchImpl(this.url, { method: "POST", headers, body });
      if (resp.status >= 200 && resp.status < 300) return { success: true, retryable: false, retryAfterMs: 0 };
      if (resp.status === 429 || resp.status >= 500) {
        this.failures += events.length;
        return { success: false, retryable: true, retryAfterMs: retryAfterMs(resp.headers.get("Retry-After")) };
      }
      this.dropped += events.length;
      return { success: false, retryable: false, retryAfterMs: 0 };
    } catch {
      this.failures += events.length;
      return { success: false, retryable: true, retryAfterMs: 0 };
    }
  }

  private async retry(items: Queued[], priority: boolean, retryAfterMs: number): Promise<void> {
    const retry: Queued[] = [];
    for (const item of items) {
      item.attempts++;
      if (item.attempts > this.maxRetries) this.dropped++;
      else retry.push(item);
    }
    if (retry.length === 0) return;
    await new Promise((resolve) => setTimeout(resolve, retryAfterMs || backoffMs(retry)));
    this.requeueFront(retry, priority);
  }

  private requeueFront(items: Queued[], priority: boolean): void {
    const bytes = sumBytes(items);
    if (priority) {
      while (this.totalBytes() + bytes > this.inFlightCap && this.okQ.length > 0) this.dropOK();
      while (this.totalBytes() + bytes > this.inFlightCap && this.priorityQ.length > 0) this.dropPriorityTail();
      if (this.totalBytes() + bytes > this.inFlightCap) {
        this.dropped += items.length;
        return;
      }
      this.priorityQ.unshift(...items);
      this.priorityBytes += bytes;
      return;
    }
    const okCap = Math.floor((this.inFlightCap * this.okBudgetPct) / 100);
    while (this.okBytes + bytes > okCap && this.okQ.length > 0) this.dropOK();
    if (this.okBytes + bytes > okCap) {
      this.dropped += items.length;
      return;
    }
    this.okQ.unshift(...items);
    this.okBytes += bytes;
  }

  private async postJSON(ev: WideEvent): Promise<void> {
    const headers: Record<string, string> = { "Content-Type": "application/json" };
    if (this.apiKey) headers.Authorization = `Bearer ${this.apiKey}`;
    try {
      const resp = await this.fetchImpl(this.url, { method: "POST", headers, body: JSON.stringify(ev) });
      if (resp.status >= 200 && resp.status < 300) return;
      if (resp.status === 429 || resp.status >= 500) this.failures++;
      else this.dropped++;
    } catch {
      this.failures++;
    }
  }

  private totalBytes(): number {
    return this.okBytes + this.priorityBytes;
  }

  private dropOK(): void {
    const item = this.okQ.shift();
    if (!item) return;
    this.okBytes -= item.bytes;
    this.dropped++;
  }

  private dropPriority(): void {
    const item = this.priorityQ.shift();
    if (!item) return;
    this.priorityBytes -= item.bytes;
    this.dropped++;
  }

  private dropPriorityTail(): void {
    const item = this.priorityQ.pop();
    if (!item) return;
    this.priorityBytes -= item.bytes;
    this.dropped++;
  }
}

export function normalizeIngestUrl(raw: string): string {
  const url = new URL(raw);
  if (url.protocol !== "http:" && url.protocol !== "https:") throw new Error(`Transport: invalid ingestUrl ${raw}`);
  const path = url.pathname.replace(/\/+$/, "");
  url.pathname = path.endsWith("/v1/events") ? path : `${path}/v1/events`;
  return url.toString();
}

export function isPriorityStatus(status: Status): boolean {
  return status === "error" || status === "timeout" || status === "partial" || status === "aborted";
}

function estimateEventSize(ev: WideEvent): number {
  return JSON.stringify(ev).length + 1;
}

function sumBytes(items: Queued[]): number {
  return items.reduce((n, item) => n + item.bytes, 0);
}

function retryAfterMs(raw: string | null): number {
  if (!raw) return 0;
  const seconds = Number.parseInt(raw, 10);
  if (Number.isFinite(seconds)) return seconds * 1000;
  const t = Date.parse(raw);
  return Number.isFinite(t) ? Math.max(0, t - Date.now()) : 0;
}

function backoffMs(items: Queued[]): number {
  const attempt = Math.max(1, ...items.map((item) => item.attempts));
  return Math.min(5000, 100 * 2 ** (attempt - 1));
}

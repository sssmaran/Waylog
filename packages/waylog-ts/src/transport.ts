import type { WaylogConfig, WideEvent } from "./types.js";

// Transport batches WideEvents and POSTs them to /v1/events. Bounded queue,
// non-blocking enqueue (returns false on overflow), drop counter exposed so
// callers can surface loss.
export class Transport {
  private readonly endpoint: string;
  private readonly apiKey?: string;
  private readonly fetchImpl: typeof fetch;
  private readonly batchSize: number;
  private readonly flushIntervalMs: number;
  private readonly queueMax: number;

  private queue: WideEvent[] = [];
  private dropped = 0;
  private timer: ReturnType<typeof setTimeout> | null = null;
  private closed = false;

  constructor(config: WaylogConfig) {
    if (!config.endpoint) throw new Error("Transport: endpoint is required");
    this.endpoint = config.endpoint.replace(/\/+$/, "") + "/v1/events";
    this.apiKey = config.apiKey;
    this.fetchImpl = config.fetch ?? fetch;
    this.batchSize = config.batchSize ?? 32;
    this.flushIntervalMs = config.flushIntervalMs ?? 1000;
    this.queueMax = config.queueMax ?? 1024;
  }

  enqueue(ev: WideEvent): boolean {
    if (this.closed) {
      this.dropped++;
      return false;
    }
    if (this.queue.length >= this.queueMax) {
      this.dropped++;
      return false;
    }
    this.queue.push(ev);
    if (this.queue.length >= this.batchSize) {
      void this.flush();
    } else if (!this.timer) {
      this.timer = setTimeout(() => void this.flush(), this.flushIntervalMs);
    }
    return true;
  }

  droppedCount(): number {
    return this.dropped;
  }

  queueLength(): number {
    return this.queue.length;
  }

  async flush(): Promise<void> {
    if (this.timer) {
      clearTimeout(this.timer);
      this.timer = null;
    }
    // Drain one batch per POST so a burst doesn't block the event loop on a
    // single JSON.stringify of the whole queue. Network failures count as drops
    // (no retry — caller sees the loss via droppedCount()).
    while (this.queue.length > 0) {
      const batch = this.queue.splice(0, this.batchSize);
      const headers: Record<string, string> = { "Content-Type": "application/json" };
      if (this.apiKey) headers["Authorization"] = `Bearer ${this.apiKey}`;
      try {
        await this.fetchImpl(this.endpoint, {
          method: "POST",
          headers,
          body: JSON.stringify(batch.length === 1 ? batch[0] : batch),
        });
      } catch {
        this.dropped += batch.length;
      }
    }
  }

  async close(): Promise<void> {
    this.closed = true;
    await this.flush();
  }
}

export interface RefreshLoadOptions<V> {
  retryDelaysMs?: readonly number[];
  shouldRetry?(error: unknown): boolean;
  onError?(error: unknown): void;
  onSuccess?(value: V): void;
}

interface CachedValue<V> {
  generation: number;
  value: V;
}

interface ActiveRequest<V> {
  generation: number;
  promise: Promise<V>;
}

const superseded = Symbol("refresh superseded");

export class RefreshingCache<K, V> {
  private generation = 0;
  private readonly values = new Map<K, CachedValue<V>>();
  private readonly inFlight = new Map<K, ActiveRequest<V>>();

  invalidate(): void {
    this.generation += 1;
  }

  peek(key: K): V | undefined {
    return this.values.get(key)?.value;
  }

  load(key: K, loader: () => Promise<V>, options: RefreshLoadOptions<V> = {}): Promise<V> {
    const generation = this.generation;
    const cached = this.values.get(key);
    if (cached?.generation === generation) {
      return Promise.resolve(cached.value);
    }

    const active = this.inFlight.get(key);
    if (active?.generation === generation) {
      return active.promise;
    }

    let request: Promise<V>;
    request = this.loadWithRetry(loader, generation, options)
      .then((value) => {
        if (generation !== this.generation) {
          return this.load(key, loader, options);
        }
        this.values.set(key, { generation, value });
        options.onSuccess?.(value);
        return value;
      })
      .catch((error: unknown) => {
        if (generation !== this.generation || error === superseded) {
          return this.load(key, loader, options);
        }
        options.onError?.(error);
        const fallback = this.values.get(key);
        if (fallback) {
          return fallback.value;
        }
        throw error;
      })
      .finally(() => {
        if (this.inFlight.get(key)?.promise === request) {
          this.inFlight.delete(key);
        }
      });
    this.inFlight.set(key, { generation, promise: request });
    return request;
  }

  private async loadWithRetry(
    loader: () => Promise<V>,
    generation: number,
    options: RefreshLoadOptions<V>,
  ): Promise<V> {
    const retryDelays = options.retryDelaysMs ?? [];
    for (let attempt = 0; ; attempt += 1) {
      if (generation !== this.generation) {
        throw superseded;
      }
      if (attempt > 0) {
        await wait(retryDelays[attempt - 1]);
        if (generation !== this.generation) {
          throw superseded;
        }
      }
      try {
        return await loader();
      } catch (error) {
        const exhausted = attempt >= retryDelays.length;
        if (exhausted || options.shouldRetry?.(error) === false) {
          throw error;
        }
      }
    }
  }
}

function wait(milliseconds: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

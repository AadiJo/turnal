import assert from "node:assert/strict";
import test from "node:test";
import { RefreshingCache } from "../utils/refreshingCache";

test("a superseded request cannot replace a newer refresh", async () => {
  const cache = new RefreshingCache<string, string>();
  const first = deferred<string>();
  const second = deferred<string>();

  const oldRequest = cache.load("workspace", () => first.promise);
  cache.invalidate();
  const newRequest = cache.load("workspace", () => second.promise);

  second.resolve("new");
  assert.equal(await newRequest, "new");
  first.resolve("old");
  assert.equal(await oldRequest, "new");
  assert.equal(cache.peek("workspace"), "new");
});

test("a failed refresh retains the last successful value", async () => {
  const cache = new RefreshingCache<string, string>();
  assert.equal(await cache.load("workspace", async () => "history"), "history");
  cache.invalidate();

  let reported: unknown;
  const result = await cache.load(
    "workspace",
    async () => {
      throw new Error("temporary read failure");
    },
    { onError: (error) => (reported = error) },
  );

  assert.equal(result, "history");
  assert.match(String(reported), /temporary read failure/);
});

test("a transient failure is retried before using the fallback", async () => {
  const cache = new RefreshingCache<string, string>();
  let attempts = 0;
  const result = await cache.load(
    "workspace",
    async () => {
      attempts += 1;
      if (attempts === 1) {
        throw new Error("partial append");
      }
      return "updated history";
    },
    { retryDelaysMs: [0] },
  );

  assert.equal(result, "updated history");
  assert.equal(attempts, 2);
});

function deferred<T>(): { promise: Promise<T>; resolve(value: T): void } {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((complete) => {
    resolve = complete;
  });
  return { promise, resolve };
}

import type {
  Blame,
  DiffSummary,
  FilePatch,
  SessionSummary,
  SessionTurns,
  TurnDetail,
  ViewerError,
  Workspace,
} from "./types";

const launchPath = window.location.pathname.split("/").filter(Boolean)[0];
const apiRoot = `/${launchPath}/api/v1`;

export class APIError extends Error {
  code: string;
  status: number;

  constructor(code: string, message: string, status: number) {
    super(message);
    this.code = code;
    this.status = status;
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`${apiRoot}/${path}`, {
    ...init,
    credentials: "same-origin",
    headers: {
      "Content-Type": "application/json",
      "X-Turnal-Viewer": "1",
      ...init?.headers,
    },
  });
  if (!response.ok) {
    let payload: ViewerError | undefined;
    try {
      payload = (await response.json()) as ViewerError;
    } catch {
      payload = undefined;
    }
    throw new APIError(
      payload?.error.code ?? "request_failed",
      payload?.error.message ?? `Viewer request failed (${response.status})`,
      response.status,
    );
  }
  return (await response.json()) as T;
}

export async function bootstrap(): Promise<void> {
  const fragment = new URLSearchParams(window.location.hash.slice(1));
  const secret = fragment.get("token");
  if (!secret) return;
  await request<{ ok: true }>("auth/bootstrap", {
    method: "POST",
    body: JSON.stringify({ secret }),
  });
  history.replaceState({}, "", `${window.location.pathname}${window.location.search}`);
}

export const api = {
  workspace: () => request<Workspace>("workspace"),
  sessions: () => request<SessionSummary[]>("sessions"),
  sessionTurns: (key: string, signal?: AbortSignal) =>
    request<SessionTurns>(`sessions/${encodeURIComponent(key)}/turns`, { signal }),
  turn: (key: string, signal?: AbortSignal) =>
    request<TurnDetail>(`turns/${encodeURIComponent(key)}`, { signal }),
  diff: (key: string, signal?: AbortSignal) =>
    request<DiffSummary>(`diffs/${encodeURIComponent(key)}`, { signal }),
  patch: (key: string, path: string, signal?: AbortSignal) =>
    request<FilePatch>(
      `diffs/${encodeURIComponent(key)}/file?path=${encodeURIComponent(path)}`,
      { signal },
    ),
  blame: (key: string, path: string, signal?: AbortSignal) =>
    request<Blame>(
      `blame/${encodeURIComponent(key)}?path=${encodeURIComponent(path)}`,
      { signal },
    ),
};

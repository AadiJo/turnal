import type {
  ActivityItem,
  AddProjectRequest,
  AddProjectResult,
  Blame,
  DiffSummary,
  FilePatch,
  Project,
  SessionSummary,
  SessionTurns,
  TurnDetail,
  ViewerError,
  ViewerIndex,
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

// Held in memory only. Bootstrap hands this back once to same-origin script, and
// state-changing requests echo it in a header that cross-origin callers cannot
// read from the HttpOnly cookie.
let writeToken: string | null = null;

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`${apiRoot}/${path}`, {
    ...init,
    credentials: "same-origin",
    headers: {
      "Content-Type": "application/json",
      "X-Turnal-Viewer": "1",
      ...(writeToken ? { "X-Turnal-Write": writeToken } : {}),
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
  const result = await request<{ ok: true; write_token?: string }>("auth/bootstrap", {
    method: "POST",
    body: JSON.stringify({ secret }),
  });
  writeToken = result.write_token ?? null;
  history.replaceState({}, "", `${window.location.pathname}${window.location.search}`);
}

/** Whether this session may add or remove projects. False after a reload, since
 * the launch fragment is single use. */
export function canWrite(): boolean {
  return writeToken !== null;
}

const scoped = (storeID: string, path: string) =>
  `projects/${encodeURIComponent(storeID)}/${path}`;

export const api = {
  index: () => request<ViewerIndex>("index"),
  projects: () => request<Project[]>("projects"),
  activity: (limit = 40) => request<ActivityItem[]>(`activity?limit=${limit}`),
  refresh: () => request<ViewerIndex>("refresh"),
  /** Ask the host to show its native folder chooser. A browser file input only
   * reports a name, never a path, so the platform dialog is the only way to get
   * a directory the CLI can actually open. */
  pickDirectory: () =>
    request<{ cancelled: boolean; directory?: string }>("pick-directory", { method: "POST" }),
  addProject: (input: AddProjectRequest) =>
    request<AddProjectResult>("projects", { method: "POST", body: JSON.stringify(input) }),
  removeProject: (storeID: string) =>
    request<{ ok: true; history_kept: boolean }>(`projects/${encodeURIComponent(storeID)}`, {
      method: "DELETE",
    }),

  workspace: (storeID: string) => request<Workspace>(scoped(storeID, "workspace")),
  sessions: (storeID: string, signal?: AbortSignal) =>
    request<SessionSummary[]>(scoped(storeID, "sessions"), { signal }),
  sessionTurns: (storeID: string, key: string, signal?: AbortSignal) =>
    request<SessionTurns>(scoped(storeID, `sessions/${encodeURIComponent(key)}/turns`), { signal }),
  turn: (storeID: string, key: string, signal?: AbortSignal) =>
    request<TurnDetail>(scoped(storeID, `turns/${encodeURIComponent(key)}`), { signal }),
  diff: (storeID: string, key: string, signal?: AbortSignal) =>
    request<DiffSummary>(scoped(storeID, `diffs/${encodeURIComponent(key)}`), { signal }),
  patch: (storeID: string, key: string, path: string, signal?: AbortSignal) =>
    request<FilePatch>(
      scoped(storeID, `diffs/${encodeURIComponent(key)}/file?path=${encodeURIComponent(path)}`),
      { signal },
    ),
  blame: (storeID: string, key: string, path: string, signal?: AbortSignal) =>
    request<Blame>(
      scoped(storeID, `blame/${encodeURIComponent(key)}?path=${encodeURIComponent(path)}`),
      { signal },
    ),
};

import { useCallback, useEffect, useState } from "preact/hooks";
import { api, APIError, bootstrap } from "./api";
import { ProjectsView } from "./ProjectsView";
import { ProjectView } from "./ProjectView";
import type { ActivityItem, Project, ViewerIndex } from "./types";

/** Which screen is showing. The project index is the root; a selected project
 * pushes into its own view. State lives in the URL so reloads and history
 * navigation land in the same place. */
type Route = { name: "projects" } | { name: "project"; storeID: string; sessionKey?: string };

const launchBase = () => `/${window.location.pathname.split("/").filter(Boolean)[0]}/`;

function readRoute(): Route {
  const params = new URLSearchParams(window.location.search);
  const storeID = params.get("project");
  if (storeID) {
    return { name: "project", storeID, sessionKey: params.get("session") ?? undefined };
  }
  return { name: "projects" };
}

function writeRoute(route: Route) {
  const params = new URLSearchParams();
  if (route.name === "project") {
    params.set("project", route.storeID);
    if (route.sessionKey) params.set("session", route.sessionKey);
  }
  const query = params.toString();
  history.pushState({}, "", `${launchBase()}${query ? `?${query}` : ""}`);
}

export function App() {
  const [index, setIndex] = useState<ViewerIndex | null>(null);
  const [activity, setActivity] = useState<ActivityItem[]>([]);
  const [route, setRoute] = useState<Route>(readRoute);
  const [error, setError] = useState<Error | null>(null);
  const [loading, setLoading] = useState(true);

  const load = useCallback(async (refresh: boolean) => {
    try {
      const nextIndex = refresh ? await api.refresh() : await api.index();
      const nextActivity = await api.activity().catch(() => []);
      setIndex(nextIndex);
      setActivity(nextActivity);
      setError(null);
    } catch (nextError) {
      setError(nextError as Error);
    }
  }, []);

  useEffect(() => {
    (async () => {
      // A spent launch token is not fatal. The fragment is single use, so a
      // reloaded tab fails bootstrap while its session cookie is still valid.
      // Try to read anyway and only surface an error if that also fails.
      try {
        await bootstrap();
      } catch {
        // Fall through to the read below, which decides if we are really locked.
      }
      await load(false);
      setLoading(false);
    })();
  }, [load]);

  // Keep the view in sync with browser back and forward.
  useEffect(() => {
    const onPop = () => setRoute(readRoute());
    window.addEventListener("popstate", onPop);
    return () => window.removeEventListener("popstate", onPop);
  }, []);

  const navigate = (next: Route) => {
    writeRoute(next);
    setRoute(next);
  };

  if (loading) return <Loading />;
  if (error || !index) return <Fatal error={error} onRetry={() => load(false)} />;

  if (route.name === "project") {
    const project = index.projects.find((item) => item.store_id === route.storeID);
    if (!project) {
      return (
        <UnknownProject
          storeID={route.storeID}
          onBack={() => navigate({ name: "projects" })}
        />
      );
    }
    return (
      <ProjectView
        project={project}
        initialSessionKey={route.sessionKey}
        onBack={() => navigate({ name: "projects" })}
      />
    );
  }

  return (
    <ProjectsView
      index={index}
      activity={activity}
      onOpenProject={(project: Project) => navigate({ name: "project", storeID: project.store_id })}
      onReload={() => load(true)}
    />
  );
}

function Loading() {
  return (
    <div className="hub">
      <div className="hub-top">
        <h1>Opening the project index</h1>
      </div>
      <div className="lede">Reading the project index and verifying the viewer session.</div>
    </div>
  );
}

function Fatal({ error, onRetry }: { error: Error | null; onRetry: () => void }) {
  const locked = error instanceof APIError && error.code === "viewer_locked";
  return (
    <div className="hub">
      <div className="hub-top">
        <h1>{locked ? "Relaunch the viewer" : "The local history could not be read"}</h1>
      </div>
      <div className="lede">{error?.message || "The viewer did not return project data."}</div>
      <div className="empty">
        <p>
          {locked
            ? "This viewer session expired or its launch token was already used."
            : "Check that the store is readable and try again."}
        </p>
        <span className="row2">
          <button className="primary" onClick={onRetry}>
            Try again
          </button>
        </span>
        {locked && <code>turnal ui</code>}
      </div>
    </div>
  );
}

function UnknownProject({ storeID, onBack }: { storeID: string; onBack: () => void }) {
  return (
    <div className="hub">
      <div className="hub-top">
        <h1>That project is not indexed</h1>
      </div>
      <div className="lede">
        No registered store matches <code>{storeID}</code>. It may have been removed.
      </div>
      <div className="empty">
        <span className="row2">
          <button className="primary" onClick={onBack}>
            All projects
          </button>
        </span>
      </div>
    </div>
  );
}

import { useCallback, useEffect, useState } from "preact/hooks";
import { api, APIError, bootstrap } from "./api";
import { ProjectsView } from "./ProjectsView";
import { ProjectView } from "./ProjectView";
import type { ActivityItem, Project, ViewerIndex } from "./types";

/** Which screen is showing. The project index is the root; a selected project
 * pushes into its own view. State lives in the URL so reloads and history
 * navigation land in the same place. */
type ProjectRoute = {
  name: "project";
  storeID: string;
  sessionKey?: string;
  turnKey?: string;
  view?: "sessions" | "review" | "origins";
  path?: string;
  from?: number;
  to?: number;
};
type Route = { name: "projects" } | ProjectRoute;

const launchBase = () =>
  `/${window.location.pathname.split("/").filter(Boolean)[0]}/`;

function readRoute(): Route {
  const params = new URLSearchParams(window.location.search);
  const storeID = params.get("project");
  if (storeID) {
    const view = params.get("view");
    const from = Number(params.get("from"));
    const to = Number(params.get("to"));
    return {
      name: "project",
      storeID,
      sessionKey: params.get("session") ?? undefined,
      turnKey: params.get("turn") ?? undefined,
      view:
        view === "sessions" || view === "origins" || view === "review"
          ? view
          : undefined,
      path: params.get("path") ?? undefined,
      from: Number.isInteger(from) && from > 0 ? from : undefined,
      to: Number.isInteger(to) && to > 0 ? to : undefined,
    };
  }
  return { name: "projects" };
}

function writeRoute(route: Route) {
  const params = new URLSearchParams();
  if (route.name === "project") {
    params.set("project", route.storeID);
    if (route.sessionKey) params.set("session", route.sessionKey);
    if (route.turnKey) params.set("turn", route.turnKey);
    if (route.view) params.set("view", route.view);
    if (route.path) params.set("path", route.path);
    if (route.from) params.set("from", String(route.from));
    if (route.to) params.set("to", String(route.to));
  }
  const query = params.toString();
  history.pushState({}, "", `${launchBase()}${query ? `?${query}` : ""}`);
}

export function App() {
  const [index, setIndex] = useState<ViewerIndex | null>(null);
  const [activity, setActivity] = useState<ActivityItem[]>([]);
  const [activityTruncated, setActivityTruncated] = useState(false);
  const [activityError, setActivityError] = useState<string | null>(null);
  const [route, setRoute] = useState<Route>(readRoute);
  const [error, setError] = useState<Error | null>(null);
  const [loading, setLoading] = useState(true);

  const load = useCallback(async (refresh: boolean) => {
    try {
      const nextIndex = refresh ? await api.refresh() : await api.index();
      setIndex(nextIndex);
      setError(null);
      try {
        const nextActivity = await api.activity();
        setActivity(nextActivity.items);
        setActivityTruncated(nextActivity.truncated);
        setActivityError(null);
      } catch (nextError) {
        setActivity([]);
        setActivityTruncated(false);
        setActivityError(
          nextError instanceof Error
            ? nextError.message
            : "Recent activity could not be read.",
        );
      }
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
  if (error || !index)
    return <Fatal error={error} onRetry={() => load(false)} />;

  if (route.name === "project") {
    const project = index.projects.find(
      (item) => item.store_id === route.storeID,
    );
    if (!project) {
      return <UnknownProject onBack={() => navigate({ name: "projects" })} />;
    }
    return (
      <ProjectView
        project={project}
        initialSessionKey={route.sessionKey}
        initialTurnKey={route.turnKey}
        initialView={route.view}
        initialPath={route.path}
        initialSelection={
          route.from
            ? {
                from: route.from,
                to: Math.max(route.from, route.to ?? route.from),
              }
            : undefined
        }
        onBack={() => navigate({ name: "projects" })}
        onNavigate={(state) =>
          navigate({ name: "project", storeID: project.store_id, ...state })
        }
      />
    );
  }

  return (
    <ProjectsView
      index={index}
      activity={activity}
      activityTruncated={activityTruncated}
      activityError={activityError}
      onOpenProject={(project: Project, sessionKey?: string) =>
        navigate({ name: "project", storeID: project.store_id, sessionKey })
      }
      onReload={() => load(true)}
    />
  );
}

function Loading() {
  return (
    <div className="hub">
      <div className="hub-top">
        <h1>Opening Turnal</h1>
      </div>
      <div className="lede">
        Reading your projects and verifying the viewer session.
      </div>
    </div>
  );
}

function Fatal({
  error,
  onRetry,
}: {
  error: Error | null;
  onRetry: () => void;
}) {
  const locked = error instanceof APIError && error.code === "viewer_locked";
  return (
    <div className="hub">
      <div className="hub-top">
        <h1>
          {locked
            ? "Relaunch the viewer"
            : "The local history could not be read"}
        </h1>
      </div>
      <div className="lede">
        {error?.message || "The viewer did not return project data."}
      </div>
      <div className="empty">
        <p>
          {locked
            ? "This viewer session expired or its launch token was already used."
            : "Check that the project folder is readable and try again."}
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

function UnknownProject({ onBack }: { onBack: () => void }) {
  return (
    <div className="hub">
      <div className="hub-top">
        <h1>That project is no longer in the viewer</h1>
      </div>
      <div className="lede">
        It may have been removed. Add the project again to see it here.
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

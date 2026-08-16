import { useEffect, useMemo, useState } from "preact/hooks";
import { api } from "./api";
import { Chrome, Delta, Section, Tabs } from "./Chrome";
import {
  cleanAdapter,
  cx,
  displayTime,
  duration,
  isActive,
  shortAge,
  shortID,
} from "./format";
import type {
  Blame,
  BlameLine,
  DiffSummary,
  FileChange,
  FileContent,
  FilePatch,
  ManualSave,
  Project,
  SessionSummary,
  SessionTurns,
  Tree,
  TreeEntry,
  TurnDetail,
  TurnEvent,
} from "./types";

type Mode = "sessions" | "review" | "origins" | "files";
type ProjectLocation = {
  sessionKey?: string;
  turnKey?: string;
  view?: Mode;
  path?: string;
  from?: number;
  to?: number;
};
const PATCH_PAGE_SIZE = 20;
const EVENT_PAGE_SIZE = 20;

export function ProjectView({
  project,
  onBack,
  onNavigate,
  initialSessionKey,
  initialTurnKey,
  initialView,
  initialPath,
  initialSelection,
}: {
  project: Project;
  onBack: () => void;
  onNavigate: (location: ProjectLocation) => void;
  initialSessionKey?: string;
  initialTurnKey?: string;
  initialView?: Mode;
  initialPath?: string;
  initialSelection?: { from: number; to: number };
}) {
  const store = project.store_id;
  const [mode, setMode] = useState<Mode>(
    initialView ?? (initialSessionKey ? "review" : "sessions"),
  );
  const [sessions, setSessions] = useState<SessionSummary[]>([]);
  const [saves, setSaves] = useState<ManualSave[]>([]);
  const [sessionKey, setSessionKey] = useState<string | null>(
    initialSessionKey ?? null,
  );
  const [turns, setTurns] = useState<SessionTurns | null>(null);
  const [turnKey, setTurnKey] = useState<string | null>(initialTurnKey ?? null);
  const [detail, setDetail] = useState<TurnDetail | null>(null);
  const [diff, setDiff] = useState<DiffSummary | null>(null);
  const [patches, setPatches] = useState<Record<string, FilePatch>>({});
  const [patchErrors, setPatchErrors] = useState<Record<string, string>>({});
  const [blame, setBlame] = useState<Blame | null>(null);
  const [tree, setTree] = useState<Tree | null>(null);
  // Only a path that arrived with the files view means a browsed file. The other
  // views use the same query parameter for the file they have selected, so
  // adopting it unconditionally would open the Files tab inside a file the user
  // never picked.
  const [browsePath, setBrowsePath] = useState<string | null>(
    initialView === "files" ? (initialPath ?? null) : null,
  );
  const [browseFile, setBrowseFile] = useState<FileContent | null>(null);
  const [browseError, setBrowseError] = useState<string | null>(null);
  // Which directory the table is showing. Empty string is the repository root.
  const [browseDir, setBrowseDir] = useState("");
  const [selectedPath, setSelectedPath] = useState<string | null>(
    initialPath ?? null,
  );
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [annotations, setAnnotations] = useState(true);
  const [wordWrap, setWordWrap] = useState(true);
  const [visiblePatchCount, setVisiblePatchCount] = useState(PATCH_PAGE_SIZE);

  const navigateFromCurrent = (next: Partial<ProjectLocation>) =>
    onNavigate({
      sessionKey: sessionKey ?? undefined,
      turnKey: turnKey ?? undefined,
      view: mode,
      path: selectedPath ?? undefined,
      ...next,
    });

  useEffect(() => {
    // Browsing files is not scoped to a session, so it is restored on its own.
    // Everything below needs a session to mean anything.
    if (initialView === "files") {
      setMode("files");
      // Whether the restored path is a file or a directory is only knowable
      // once the tree loads, so the effect below resolves it.
      setBrowsePath(initialPath ?? null);
      if (!initialPath) setBrowseDir("");
      return;
    }
    if (!initialSessionKey) {
      setMode("sessions");
      return;
    }
    setSessionKey(initialSessionKey);
    setTurnKey(initialTurnKey ?? null);
    setMode(initialView ?? "review");
    setSelectedPath(initialPath ?? null);
  }, [initialSessionKey, initialTurnKey, initialView, initialPath]);

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    Promise.all([
      api.sessions(store, controller.signal),
      api.saves(store, controller.signal),
    ])
      .then(([nextSessions, nextSaves]) => {
        setSessions(nextSessions);
        setSaves(nextSaves);
        setSessionKey((current) => current ?? nextSessions[0]?.key ?? null);
        setError(null);
      })
      .catch((nextError: unknown) => {
        if (!isAbortError(nextError)) setError(errorMessage(nextError));
      })
      .finally(() => setLoading(false));
    return () => controller.abort();
  }, [store]);

  useEffect(() => {
    if (!sessionKey) return;
    const controller = new AbortController();
    setTurns(null);
    setTurnKey(null);
    setDetail(null);
    setDiff(null);
    setSelectedPath(initialPath ?? null);
    api
      .sessionTurns(store, sessionKey, controller.signal)
      .then((value) => {
        setTurns(value);
        const requestedTurn = value.turns.some(
          (turn) => turn.key === initialTurnKey,
        )
          ? initialTurnKey
          : undefined;
        setTurnKey(requestedTurn ?? value.turns[0]?.key ?? null);
      })
      .catch((nextError: unknown) => {
        if (!isAbortError(nextError)) setError(errorMessage(nextError));
      });
    return () => controller.abort();
  }, [store, sessionKey, initialTurnKey]);

  useEffect(() => {
    const turnBelongsToSession =
      turns?.turns.some((turn) => turn.key === turnKey) ?? false;
    if (!turnKey || !turnBelongsToSession) {
      setDetail(null);
      setDiff(null);
      return;
    }
    const controller = new AbortController();
    setError(null);
    setDetail(null);
    setDiff(null);
    setPatches({});
    setPatchErrors({});
    setVisiblePatchCount(PATCH_PAGE_SIZE);

    api
      .turn(store, turnKey, controller.signal)
      .then((nextDetail) => {
        setDetail(nextDetail);
        setSelectedPath(
          (current) => initialPath ?? current ?? nextDetail.files?.[0]?.path ?? null,
        );
      })
      .catch((nextError: unknown) => {
        if (!isAbortError(nextError)) setError(errorMessage(nextError));
      });

    api
      .diff(store, turnKey, controller.signal)
      .then((nextDiff) => {
        setDiff(nextDiff);
        setSelectedPath(initialPath ?? nextDiff.files[0]?.path ?? null);
      })
      .catch((nextError: unknown) => {
        if (!isAbortError(nextError)) {
          setError(
            "File changes are not available for this turn yet. Its recorded activity is shown below.",
          );
        }
      });
    return () => controller.abort();
  }, [store, turns, turnKey, initialPath]);

  // Load a bounded page at a time. The user can continue the same continuous
  // review explicitly without a large generated change freezing the browser.
  useEffect(() => {
    if (mode !== "review" || !turnKey || !diff) return;
    const controller = new AbortController();
    let cancelled = false;
    (async () => {
      const next: Record<string, FilePatch> = {};
      const failures: Record<string, string> = {};
      const batchSize = 4;
      // Include every visible file that has not reached a terminal state. If a
      // page change aborts an earlier request, the unfinished file is retried.
      const files = diff.files
        .slice(0, visiblePatchCount)
        .filter((file) => !patches[file.path] && !patchErrors[file.path]);
      for (let offset = 0; offset < files.length; offset += batchSize) {
        const batch = files.slice(offset, offset + batchSize);
        await Promise.all(
          batch.map(async (file) => {
            try {
              next[file.path] = await api.patch(
                store,
                turnKey,
                file.path,
                controller.signal,
              );
            } catch (nextError) {
              failures[file.path] =
                nextError instanceof Error
                  ? nextError.message
                  : "The patch could not be read.";
            }
          }),
        );
        if (!cancelled) {
          setPatches((current) => ({ ...current, ...next }));
          setPatchErrors((current) => ({ ...current, ...failures }));
        }
      }
    })();
    return () => {
      cancelled = true;
      controller.abort();
    };
  }, [mode, store, turnKey, diff, visiblePatchCount]);

  useEffect(() => {
    if (mode !== "origins" || !turnKey || !selectedPath) {
      setBlame(null);
      return;
    }
    const controller = new AbortController();
    setBlame(null);
    api
      .blame(store, turnKey, selectedPath, controller.signal)
      .then(setBlame)
      .catch((nextError: unknown) => {
        if (!isAbortError(nextError)) {
          setBlame(null);
          setError(errorMessage(nextError));
        }
      });
    return () => controller.abort();
  }, [mode, store, turnKey, selectedPath]);

  // The browsed tree is one checkpoint's file list, so it does not depend on the
  // selected session or turn. It is fetched once per project when the tab opens.
  useEffect(() => {
    if (mode !== "files" || tree) return;
    const controller = new AbortController();
    api
      .tree(store, controller.signal)
      .then((nextTree) => {
        setTree(nextTree);
        setBrowseError(null);
      })
      .catch((nextError: unknown) => {
        if (!isAbortError(nextError)) setBrowseError(errorMessage(nextError));
      });
    return () => controller.abort();
  }, [mode, store, tree]);

  // A restored path from the URL may name a directory rather than a file. The
  // tree is the only thing that can tell them apart, so reclassify once it
  // arrives and show that directory's table instead of a failed file read.
  useEffect(() => {
    if (mode !== "files" || !tree || !browsePath) return;
    if (tree.files.some((file) => file.path === browsePath)) return;
    const prefix = `${browsePath}/`;
    if (tree.files.some((file) => file.path.startsWith(prefix))) {
      setBrowseDir(browsePath);
      setBrowsePath(null);
    }
  }, [mode, tree, browsePath]);

  useEffect(() => {
    if (mode !== "files" || !browsePath) {
      setBrowseFile(null);
      return;
    }
    const controller = new AbortController();
    setBrowseFile(null);
    api
      .file(store, browsePath, controller.signal)
      .then((nextFile) => {
        setBrowseFile(nextFile);
        setBrowseError(null);
      })
      .catch((nextError: unknown) => {
        if (!isAbortError(nextError)) setBrowseError(errorMessage(nextError));
      });
    return () => controller.abort();
  }, [mode, store, browsePath]);

  const session =
    turns?.session ?? sessions.find((item) => item.key === sessionKey) ?? null;

  return (
    <>
      <Chrome
        crumbs={[
          { label: "All projects", onClick: onBack },
          { label: project.name },
          ...(project.branch ? [{ label: project.branch, mono: true }] : []),
        ]}
      />
      <Tabs
        tabs={[
          { id: "sessions", label: "Sessions", count: sessions.length },
          { id: "review", label: "Review" },
          { id: "origins", label: "Origins" },
          { id: "files", label: "Files" },
        ]}
        active={mode}
        onSelect={(id) => {
          setMode(id);
          // Review and Origins use `path` for the file they have selected. The
          // browser uses it for where you are in the tree, so entering or
          // leaving Files starts at that view's own root rather than inheriting
          // a path that means something different.
          const crossesBrowser = (id === "files") !== (mode === "files");
          navigateFromCurrent({
            view: id,
            ...(crossesBrowser ? { path: undefined } : {}),
          });
        }}
        meta={<span>{project.turn_count} turns</span>}
      />

      {error && (
        <div className="page" style={{ paddingBottom: 0 }}>
          <div className="note" role="alert">
            <span className="badge">!</span>
            <span>{error}</span>
          </div>
        </div>
      )}

      {mode === "sessions" && (
        <div className="page">
          <Section
            title="Sessions"
            note={loading ? "loading" : `${sessions.length}`}
          />
          {sessions.length === 0 && !loading ? (
            <div className="empty">
              <strong>No recorded sessions yet</strong>
              <p>
                Run your configured agent in this project to record a session.
              </p>
            </div>
          ) : (
            <div className="rows">
              {sessions.map((item) => (
                <a
                  key={item.key}
                  href="#"
                  className={cx(item.key === sessionKey && "on")}
                  onClick={(event) => {
                    event.preventDefault();
                    setSessionKey(item.key);
                    setMode("review");
                    onNavigate({ sessionKey: item.key, view: "review" });
                  }}
                >
                  <span
                    className={cx("status", isActive(item.status) && "active")}
                  >
                    {item.status === "complete"
                      ? "✓"
                      : item.status === "active"
                        ? "●"
                        : "!"}
                  </span>
                  <span className="row-main">
                    <strong>
                      {item.prompt_preview || `Session ${shortID(item.id, 12)}`}
                    </strong>
                    <span>
                      {cleanAdapter(item.adapter)}
                      {item.model && (
                        <>
                          {" "}
                          <i>·</i> {item.model}
                        </>
                      )}{" "}
                      <i>·</i> {item.turn_count} turn
                      {item.turn_count === 1 ? "" : "s"} <i>·</i>{" "}
                      {duration(item.started_at, item.finished_at)}
                    </span>
                  </span>
                  {item.branch && (
                    <span className="tag mono">{item.branch}</span>
                  )}
                  <Delta
                    additions={item.additions}
                    deletions={item.deletions}
                  />
                  <span className="when">
                    {shortAge(item.finished_at || item.started_at)}
                  </span>
                </a>
              ))}
            </div>
          )}
          <Section title="Saved snapshots" note={`${saves.length}`} />
          {saves.length === 0 ? (
            <div className="empty compact">
              <p>
                Save a folder snapshot from the terminal with{" "}
                <code>turnal save</code>.
              </p>
            </div>
          ) : (
            <div className="save-rows">
              {saves.map((save) => (
                <div className="save-row" key={save.id}>
                  <span className="status">✓</span>
                  <span className="row-main">
                    <strong>{save.message || "Saved folder snapshot"}</strong>
                    <span>
                      {save.warnings?.length
                        ? "Saved with a warning"
                        : "Folder snapshot"}
                    </span>
                  </span>
                  <span className="when">{shortAge(save.time)}</span>
                </div>
              ))}
            </div>
          )}
        </div>
      )}

      {mode === "review" && (
        <div className="session-shell">
          <aside className="turnlist">
            <Section title="Turns" note={`${turns?.turns.length ?? 0}`} />
            {turns?.turns.map((turn) => (
              <a
                key={turn.key}
                href="#"
                className={cx("turnrow", turn.key === turnKey && "on")}
                onClick={(event) => {
                  event.preventDefault();
                  setTurnKey(turn.key);
                  setSelectedPath(null);
                  navigateFromCurrent({
                    turnKey: turn.key,
                    view: "review",
                    path: undefined,
                    from: undefined,
                    to: undefined,
                  });
                }}
              >
                <span className="n">{turn.id}</span>
                <span className="body">
                  <strong>{turn.prompt || "Prompt not recorded"}</strong>
                  <span>
                    {displayTime(turn.finished_at)} <i>·</i>{" "}
                    {turn.files?.length ?? 0} file
                    {(turn.files?.length ?? 0) === 1 ? "" : "s"} <i>·</i>
                    <span className="p">+{turn.additions}</span>
                    <span className="m">-{turn.deletions}</span>
                  </span>
                </span>
              </a>
            ))}
          </aside>

          <div className="review">
            {session && (
              <div className="doc">
                <div className="doc-head">
                  <AgentAvatar adapter={session.adapter} />
                  <b>{cleanAdapter(session.adapter)}</b>
                  <span>
                    opened this session at {displayTime(session.started_at)}
                  </span>
                  <span className="sp" />
                  {detail?.post_commit && (
                    <span className="tag good">Snapshot verified</span>
                  )}
                </div>
                <div className="doc-body">
                  <p>
                    {detail?.prompt ||
                      session.prompt_preview ||
                      "No prompt text was stored."}
                  </p>
                </div>
              </div>
            )}

            {detail?.truncated && (
              <div className="note" role="status">
                <span className="badge">!</span>
                <span>
                  Only the first {detail.events.length} recorded events are
                  shown for this turn.
                </span>
              </div>
            )}

            {annotations && detail && <TurnAnnotation turn={detail} />}

            <Section
              title="Changes"
              note={
                diff
                  ? `${diff.files.length} files · before ${shortID(diff.pre_commit, 7)} → after ${shortID(diff.post_commit, 7)}`
                  : error
                    ? "unavailable"
                    : "loading"
              }
            >
              <button
                className={cx("ghost", annotations && "on")}
                onClick={() => setAnnotations(!annotations)}
                aria-pressed={annotations}
              >
                Annotations: {annotations ? "on" : "off"}
              </button>
              <button
                className={cx("ghost", wordWrap && "on")}
                onClick={() => setWordWrap(!wordWrap)}
                aria-pressed={wordWrap}
              >
                Word wrap: {wordWrap ? "on" : "off"}
              </button>
            </Section>

            <div className="surface">
              {diff?.files.slice(0, visiblePatchCount).map((file) => (
                <FileBox
                  key={file.path}
                  file={file}
                  patch={patches[file.path]}
                  patchError={patchErrors[file.path]}
                  turn={detail}
                  wordWrap={wordWrap}
                />
              ))}
              {diff && diff.files.length === 0 && (
                <div className="empty">
                  <strong>No file changes in this turn</strong>
                  <p>
                    No file changes were captured while this turn was recorded.
                  </p>
                </div>
              )}
              {diff && visiblePatchCount < diff.files.length && (
                <div className="load-more">
                  <button
                    className="ghost"
                    onClick={() =>
                      setVisiblePatchCount((current) =>
                        Math.min(current + PATCH_PAGE_SIZE, diff.files.length),
                      )
                    }
                  >
                    Load{" "}
                    {Math.min(
                      PATCH_PAGE_SIZE,
                      diff.files.length - visiblePatchCount,
                    )}{" "}
                    more files
                  </button>
                  <span>
                    {diff.files.length - visiblePatchCount} not loaded
                  </span>
                </div>
              )}
            </div>

          </div>
        </div>
      )}

      {mode === "origins" && (
        <div className="shell">
          <aside className="tree">
            <div className="tree-tabs">
              <button className="on">Changed files</button>
            </div>
            <div className="tree-list">
              {(diff?.files ?? []).map((file) => {
                const parts = file.path.split("/");
                const name = parts.pop();
                return (
                  <a
                    key={file.path}
                    href="#"
                    className={cx(selectedPath === file.path && "on")}
                    title={file.path}
                    onClick={(event) => {
                      event.preventDefault();
                      setSelectedPath(file.path);
                      navigateFromCurrent({
                        view: "origins",
                        path: file.path,
                        from: undefined,
                        to: undefined,
                      });
                    }}
                  >
                    <span className="nm">{name}</span>
                    <span className="sp" />
                    <span className="d">
                      <span className="p">+{file.additions}</span>
                      <span className="m">-{file.deletions}</span>
                    </span>
                  </a>
                );
              })}
            </div>
          </aside>
          <OriginsPane
            blame={blame?.path === selectedPath ? blame : null}
            path={selectedPath}
            sessionKey={sessionKey}
            turnKey={turnKey}
            initialSelection={initialSelection}
            onSelectionChange={(selection) =>
              navigateFromCurrent({
                view: "origins",
                from: selection?.from,
                to: selection?.to,
              })
            }
          />
        </div>
      )}

      {mode === "files" && (
        <div className="page">
          <Breadcrumb
            root={project.name}
            path={browsePath ?? browseDir}
            isFile={Boolean(browsePath)}
            onNavigate={(dir) => {
              setBrowsePath(null);
              setBrowseDir(dir);
              navigateFromCurrent({ view: "files", path: dir || undefined });
            }}
          />
          {browseError && !tree ? (
            <div className="note" role="alert">
              <span className="badge">!</span>
              <span>{browseError}</span>
            </div>
          ) : !tree ? (
            <div className="load-more">loading</div>
          ) : browsePath ? (
            <FilePane
              file={browseFile}
              error={browseError}
              wordWrap={wordWrap}
              onToggleWrap={() => setWordWrap((current) => !current)}
            />
          ) : (
            <>
              <FileTable
                tree={tree}
                dir={browseDir}
                onOpenDir={(dir) => {
                  setBrowseDir(dir);
                  navigateFromCurrent({ view: "files", path: dir });
                }}
                onOpenFile={(path) => {
                  setBrowsePath(path);
                  navigateFromCurrent({ view: "files", path });
                }}
              />
              <div className="note">
                <span className="badge">i</span>
                <span>
                  <b>This is a checkpoint, not your working tree.</b> Files
                  appear as they were captured in{" "}
                  <code>{shortID(tree.checkpoint_id)}</code>. Anything the
                  snapshot policy excludes, or that changed after this
                  checkpoint, is not shown.
                  {tree.truncated &&
                    ` Only the first ${tree.limit_entries} files are listed.`}
                </span>
              </div>
            </>
          )}
        </div>
      )}
    </>
  );
}

/** Path trail above the table. Every segment except the last is a link back up,
 * matching how GitHub and Forgejo move through a repository. */
function Breadcrumb({
  root,
  path,
  isFile,
  onNavigate,
}: {
  root: string;
  path: string;
  isFile: boolean;
  onNavigate: (dir: string) => void;
}) {
  const segments = path ? path.split("/") : [];
  return (
    <div className="pathbar">
      <a
        href="#"
        onClick={(event) => {
          event.preventDefault();
          onNavigate("");
        }}
      >
        {root}
      </a>
      {segments.map((segment, index) => {
        const last = index === segments.length - 1;
        const upto = segments.slice(0, index + 1).join("/");
        return (
          <>
            <i key={`sep-${upto}`}>/</i>
            {last ? (
              <b key={upto}>{segment}</b>
            ) : (
              <a
                key={upto}
                href="#"
                onClick={(event) => {
                  event.preventDefault();
                  onNavigate(upto);
                }}
              >
                {segment}
              </a>
            )}
          </>
        );
      })}
      {isFile && segments.length === 0 && <b>{path}</b>}
    </div>
  );
}

/** One directory level of the checkpoint tree. Directories sort before files,
 * as they do on GitHub and Forgejo. */
function FileTable({
  tree,
  dir,
  onOpenDir,
  onOpenFile,
}: {
  tree: Tree;
  dir: string;
  onOpenDir: (dir: string) => void;
  onOpenFile: (path: string) => void;
}) {
  const rows = useMemo(() => {
    const prefix = dir ? `${dir}/` : "";
    const dirs = new Map<string, TreeEntry>();
    const files: TreeEntry[] = [];
    for (const entry of tree.files) {
      if (!entry.path.startsWith(prefix)) continue;
      const rest = entry.path.slice(prefix.length);
      const cut = rest.indexOf("/");
      if (cut === -1) {
        files.push(entry);
        continue;
      }
      // Keep the newest turn seen anywhere under this directory, so the row
      // summarizes the directory rather than an arbitrary file inside it.
      const name = rest.slice(0, cut);
      const current = dirs.get(name);
      if (!current || (entry.changed_at ?? "") > (current.changed_at ?? "")) {
        dirs.set(name, entry);
      }
    }
    return {
      dirs: [...dirs.entries()].sort(([a], [b]) => a.localeCompare(b)),
      files: files.sort((a, b) => a.path.localeCompare(b.path)),
    };
  }, [tree, dir]);

  const head = rows.dirs[0]?.[1] ?? rows.files[0];

  return (
    <div className="filebox">
      <div className="filebox-head">
        {head?.prompt ? (
          <>
            <span className="who">{cleanAdapter(head.adapter) || "turnal"}</span>
            <span className="msg">{head.prompt}</span>
          </>
        ) : (
          <span className="msg">Captured in this checkpoint</span>
        )}
        <span className="sp" />
        <span className="hash">{shortID(tree.checkpoint_id)}</span>
        <span>{shortAge(tree.checkpoint_time)}</span>
      </div>
      <table className="filetable">
        <thead>
          <tr>
            <th>Name</th>
            <th>Last recorded change</th>
            <th className="r">Changed</th>
          </tr>
        </thead>
        <tbody>
          {dir && (
            <tr>
              <td className="name" colSpan={3}>
                <a
                  href="#"
                  onClick={(event) => {
                    event.preventDefault();
                    onOpenDir(dir.split("/").slice(0, -1).join(""));
                  }}
                >
                  <span className="ic">↑</span>
                  <span className="dir">..</span>
                </a>
              </td>
            </tr>
          )}
          {rows.dirs.map(([name, entry]) => (
            <tr key={`d-${name}`}>
              <td className="name">
                <a
                  href="#"
                  onClick={(event) => {
                    event.preventDefault();
                    onOpenDir(dir ? `${dir}/${name}` : name);
                  }}
                >
                  <span className="ic">▸</span>
                  <span className="dir">{name}</span>
                </a>
              </td>
              <td className="msg">{entry.prompt ?? ""}</td>
              <td className="when-col">
                {entry.changed_at ? shortAge(entry.changed_at) : ""}
              </td>
            </tr>
          ))}
          {rows.files.map((entry) => (
            <tr key={`f-${entry.path}`}>
              <td className="name">
                <a
                  href="#"
                  onClick={(event) => {
                    event.preventDefault();
                    onOpenFile(entry.path);
                  }}
                >
                  <span className="ic">·</span>
                  <span className="file">
                    {entry.path.slice(dir ? dir.length + 1 : 0)}
                  </span>
                </a>
              </td>
              <td className="msg">{entry.prompt ?? ""}</td>
              <td className="when-col">
                {entry.changed_at ? shortAge(entry.changed_at) : ""}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function FilePane({
  file,
  error,
  wordWrap,
  onToggleWrap,
}: {
  file: FileContent | null;
  error: string | null;
  wordWrap: boolean;
  onToggleWrap: () => void;
}) {
  const lines = useMemo(
    () => (file && !file.binary ? file.content.split("\n") : []),
    [file],
  );

  if (error && !file) {
    return (
      <div className="note" role="alert">
        <span className="badge">!</span>
        <span>{error}</span>
      </div>
    );
  }
  if (!file) return <div className="load-more">loading</div>;

  return (
    <div className="filebox">
      <div className="filebox-head">
        <span className="msg">
          {file.binary
            ? `${file.byte_count} B`
            : `${file.line_count} lines \u00b7 ${file.byte_count} B`}
        </span>
        <span className="sp" />
        <button className="ghost" onClick={onToggleWrap}>
          Word wrap: {wordWrap ? "on" : "off"}
        </button>
      </div>
      {file.binary ? (
        <div className="empty compact">
          <p>This file is not text, so it is not shown.</p>
        </div>
      ) : (
        <>
          <div className={cx("code", "plain", "stacked", wordWrap && "wrap")}>
            {lines.map((text, index) => (
              <div className="ln" key={index}>
                <span className="g" />
                <span className="g">{index + 1}</span>
                <span className="sign" />
                <code>{text || " "}</code>
              </div>
            ))}
          </div>
          {file.truncated && (
            <div className="load-more">
              <span>
                Truncated at {file.limit_lines} lines or {file.limit_bytes}{" "}
                bytes.
              </span>
            </div>
          )}
        </>
      )}
    </div>
  );
}


function isAbortError(error: unknown) {
  return error instanceof Error && error.name === "AbortError";
}

function errorMessage(error: unknown) {
  return error instanceof Error
    ? error.message
    : "Turnal could not read this view.";
}

function FileBox({
  file,
  patch,
  patchError,
  turn,
  wordWrap,
}: {
  file: FileChange;
  patch?: FilePatch;
  patchError?: string;
  turn: TurnDetail | null;
  wordWrap: boolean;
}) {
  const [open, setOpen] = useState(true);
  const parts = file.path.split("/");
  const name = parts.pop();
  const lines = useMemo(() => visiblePatchLines(patch?.patch), [patch]);
  const tools = turn?.tool_names?.slice(0, 3) ?? [];
  const unwrappedColumns = patchDisplayColumns(
    lines,
    file.path.length + tools.join(", ").length + 48,
  );

  return (
    <section className="file">
      <div className="fhead">
        <button
          className="caret"
          onClick={() => setOpen(!open)}
          aria-expanded={open}
        >
          {open ? "▾" : "▸"}
        </button>
        <span className="path">
          <i>{parts.length ? `${parts.join("/")}/` : ""}</i>
          {name}
        </span>
        <span className="sp" />
        <Delta additions={file.additions} deletions={file.deletions} />
        {turn && <span className="tag">Turn {turn.id}</span>}
      </div>
      {open && (
        <div
          className={cx("code", "bars", "stacked", wordWrap && "wrap")}
          style={{
            "--diff-row-width": `calc(var(--gutter) + ${unwrappedColumns}ch + 24px)`,
          }}
        >
          {turn && (
            <div className="hunk">
              <span className="expand" />
              <span className="range">
                {file.binary ? "binary file" : `@@ ${file.path}`}
              </span>
              <span className="hmeta">
                Turn <b>{turn.id}</b> · {displayTime(turn.finished_at)}
                {tools.length > 0 && ` · ${tools.join(", ")}`}
              </span>
            </div>
          )}
          {numberPatch(lines).map((row, index) => (
            <PatchLine key={`${index}-${row.value.slice(0, 10)}`} row={row} />
          ))}
          {!patch && (
            <div className="hunk">
              <span className="expand" />
              <span className="range">{patchError || "Loading patch…"}</span>
              <span className="hmeta" />
            </div>
          )}
          {patch?.truncated && (
            <div className="hunk">
              <span className="expand" />
              <span className="range">{patchLimitMessage(patch)}</span>
              <span className="hmeta" />
            </div>
          )}
        </div>
      )}
    </section>
  );
}

function patchLimitMessage(patch: FilePatch) {
  const limits: string[] = [];
  if (patch.byte_count > patch.limit_bytes) {
    limits.push(`${Math.round(patch.limit_bytes / 1024)} KB`);
  }
  if (patch.line_count > patch.limit_lines) {
    limits.push(`${patch.limit_lines.toLocaleString()} lines`);
  }
  if (limits.length === 0) return "Part of this patch could not be shown";
  return `Patch shortened at the ${limits.join(" and ")} viewing limit${limits.length > 1 ? "s" : ""}`;
}

/** Measures monospace patch rows in terminal columns so every unwrapped row can
 * share the width of the longest line. Tabs advance to the next eight-column
 * stop, matching the browser's default code rendering. */
function patchDisplayColumns(lines: VisiblePatchLine[], headerColumns: number) {
  let widest = headerColumns;
  for (const line of lines) {
    let columns = 0;
    for (const character of line.value) {
      columns += character === "\t" ? 8 - (columns % 8) : 1;
    }
    widest = Math.max(widest, columns);
  }
  return widest;
}

const hiddenPatchMetadataPrefixes = ["diff --git ", "index ", "--- ", "+++ "];

const visiblePatchMetadataPrefixes = [
  "new file mode ",
  "deleted file mode ",
  "old mode ",
  "new mode ",
  "similarity index ",
  "rename from ",
  "rename to ",
  "Binary files ",
];

type VisiblePatchLine = { value: string; metadata: boolean };

/** Removes transport-only headers but keeps file-mode, rename, and binary
 * information. Once a hunk starts, similarly prefixed source text is content. */
function visiblePatchLines(patch?: string): VisiblePatchLine[] {
  if (!patch) return [];
  const visible: VisiblePatchLine[] = [];
  let inPreamble = true;
  const lines = patch.split("\n");
  if (lines[lines.length - 1] === "") lines.pop();
  for (const line of lines) {
    if (inPreamble && line.startsWith("@@")) {
      inPreamble = false;
    }
    if (
      inPreamble &&
      hiddenPatchMetadataPrefixes.some((prefix) => line.startsWith(prefix))
    ) {
      continue;
    }
    if (
      inPreamble &&
      visiblePatchMetadataPrefixes.some((prefix) => line.startsWith(prefix))
    ) {
      visible.push({ value: line, metadata: true });
      continue;
    }
    inPreamble = false;
    visible.push({ value: line, metadata: false });
  }
  return visible;
}

type PatchRow = {
  kind: "add" | "del" | "ctx" | "hunk" | "meta";
  value: string;
  /** Line number in the pre-image, absent for additions. */
  old?: number;
  /** Line number in the post-image, absent for deletions. */
  next?: number;
};

/** Walk a unified patch and assign real old and new line numbers, tracked from
 * each @@ header. Numbering by array index would report the position in the
 * patch text, which is a different and less useful fact than the line number in
 * the file. */
function numberPatch(lines: VisiblePatchLine[]): PatchRow[] {
  const rows: PatchRow[] = [];
  let oldLine = 0;
  let newLine = 0;
  for (const line of lines) {
    const value = line.value;
    if (line.metadata) {
      rows.push({ kind: "meta", value });
      continue;
    }
    if (value.startsWith("@@")) {
      const match = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(value);
      if (match) {
        oldLine = Number(match[1]);
        newLine = Number(match[2]);
      }
      rows.push({ kind: "hunk", value });
      continue;
    }
    if (value.startsWith("+")) {
      rows.push({ kind: "add", value: value.slice(1), next: newLine++ });
      continue;
    }
    if (value.startsWith("-")) {
      rows.push({ kind: "del", value: value.slice(1), old: oldLine++ });
      continue;
    }
    if (value.startsWith("\\")) {
      // "\ No newline at end of file" annotates the previous line.
      rows.push({ kind: "ctx", value });
      continue;
    }
    rows.push({
      kind: "ctx",
      value: value.startsWith(" ") ? value.slice(1) : value,
      old: oldLine++,
      next: newLine++,
    });
  }
  return rows;
}

function PatchLine({ row }: { row: PatchRow }) {
  if (row.kind === "hunk" || row.kind === "meta") {
    return (
      <div className="hunk">
        <span className="expand">{row.kind === "hunk" ? "↕" : ""}</span>
        <span className="range">{row.value}</span>
        <span className="hmeta" />
      </div>
    );
  }
  return (
    <div
      className={cx(
        "ln",
        row.kind === "add" && "add",
        row.kind === "del" && "del",
      )}
    >
      <span className="g">{row.old ?? ""}</span>
      <span className="g">{row.next ?? ""}</span>
      <span className="sign">
        {row.kind === "add" ? "+" : row.kind === "del" ? "-" : ""}
      </span>
      <code>{row.value}</code>
    </div>
  );
}

function TurnAnnotation({ turn }: { turn: TurnDetail }) {
  const [open, setOpen] = useState(true);
  const [visibleEvents, setVisibleEvents] = useState(EVENT_PAGE_SIZE);

  useEffect(() => setVisibleEvents(EVENT_PAGE_SIZE), [turn.key]);

  const shownEvents = turn.events.slice(0, visibleEvents);
  return (
    <div className="anno">
      <div className="anno-in">
        <div className="anno-head">
          <span className="who">
            <AgentAvatar adapter={turn.adapter} />
            <span>Turn {turn.id}</span>
          </span>
          <span>{cleanAdapter(turn.adapter)}</span>
          <span>{displayTime(turn.finished_at)}</span>
          <span>{duration(turn.started_at, turn.finished_at)}</span>
          <span className="sp" />
          <button className="ghost" onClick={() => setOpen(!open)}>
            {open ? "Collapse" : "Expand"}
          </button>
        </div>
        {open && (
          <>
            <div className="event-list">
              {shownEvents.map((event: TurnEvent) => (
                <TurnEventRow event={event} key={event.sequence} />
              ))}
            </div>
            {visibleEvents < turn.events.length && (
              <div className="load-more event-more">
                <button
                  className="ghost"
                  onClick={() =>
                    setVisibleEvents((current) =>
                      Math.min(current + EVENT_PAGE_SIZE, turn.events.length),
                    )
                  }
                >
                  Show{" "}
                  {Math.min(
                    EVENT_PAGE_SIZE,
                    turn.events.length - visibleEvents,
                  )}{" "}
                  more events
                </button>
                <span>{turn.events.length - visibleEvents} not shown</span>
              </div>
            )}
            <div className="anno-refs">
              <span>before</span>
              <code>{shortID(turn.pre_commit, 10)}</code>
              <span>→</span>
              <span>after</span>
              <code>{shortID(turn.post_commit, 10)}</code>
              <span>·</span>
              <span>saved snapshots</span>
            </div>
          </>
        )}
      </div>
    </div>
  );
}

function TurnEventRow({ event }: { event: TurnEvent }) {
  const expanded =
    event.kind === "prompt" ||
    event.kind === "assistant" ||
    event.kind === "error";
  return (
    <details className={cx("event-row", `event-${event.kind}`)} open={expanded}>
      <summary>
        <span className="event-kind">{event.kind}</span>
        <b>{event.tool_name || event.title}</b>
        <span className="sp" />
        <span>{displayTime(event.time)}</span>
      </summary>
      {event.body && <pre>{event.body}</pre>}
    </details>
  );
}

function AgentAvatar({ adapter }: { adapter?: string }) {
  const label = cleanAdapter(adapter);
  const icon =
    label === "claude"
      ? "claude-ai.svg"
      : label === "codex"
        ? "openai.svg"
        : null;

  return (
    <span className={cx("avatar", icon && "agent-avatar")} aria-hidden="true">
      {icon ? <img src={icon} alt="" /> : label.slice(0, 2)}
    </span>
  );
}

function OriginsPane({
  blame,
  path,
  sessionKey,
  turnKey,
  initialSelection,
  onSelectionChange,
}: {
  blame: Blame | null;
  path: string | null;
  sessionKey: string | null;
  turnKey: string | null;
  initialSelection?: { from: number; to: number };
  onSelectionChange: (selection: { from: number; to: number } | null) => void;
}) {
  const [selection, setSelection] = useState<{
    from: number;
    to: number;
  } | null>(initialSelection ?? null);

  useEffect(() => {
    setSelection(initialSelection ?? null);
  }, [path, initialSelection?.from, initialSelection?.to]);

  if (!path) {
    return (
      <section className="pane">
        <div className="empty">
          <strong>Select a file</strong>
          <p>Recorded line origins will appear here.</p>
        </div>
      </section>
    );
  }

  // Merge consecutive lines only when their complete attribution identity
  // matches. Baseline and concurrent origins intentionally have no turn id.
  const blocks: Array<{ turn?: BlameLine; lines: BlameLine[] }> = [];
  for (const line of blame?.lines ?? []) {
    const last = blocks[blocks.length - 1];
    if (
      last &&
      last.turn?.kind === line.kind &&
      last.turn?.session_id === line.session_id &&
      last.turn?.turn_id === line.turn_id &&
      last.turn?.time === line.time
    )
      last.lines.push(line);
    else blocks.push({ turn: line, lines: [line] });
  }

  const selected = selection
    ? (blame?.lines ?? []).find((line) => line.line === selection.from)
    : undefined;

  return (
    <section className="pane">
      <div className="pane-head">
        <span className="path">{path}</span>
        <span className="sp" />
        {blame && (
          <span className="tag">{blame.complete_turns} turns replayed</span>
        )}
        {blame?.truncated && (
          <span className="tag">first {blame.lines.length} lines</span>
        )}
      </div>
      {!!blame?.warnings?.length && (
        <div className="note" role="status">
          <span className="badge">!</span>
          <span>{blame.warnings.join(" ")}</span>
        </div>
      )}
      <div className="origins">
        {blocks.map((block, index) => (
          <div
            key={index}
            className={cx("blk", block.turn?.kind === "baseline" && "base")}
          >
            <span className={cx("age", `a${Math.min(index + 1, 4)}`)} />
            <div className="attrib">
              <div className="top">
                <b>{originLabel(block.turn)}</b>
                {block.turn?.adapter && (
                  <span>{cleanAdapter(block.turn.adapter)}</span>
                )}
                {block.turn?.time && (
                  <span>{displayTime(block.turn.time)}</span>
                )}
              </div>
              <div className="why">
                {block.turn?.prompt || originExplanation(block.turn)}
              </div>
              {!!block.turn?.tool_names?.length && (
                <div className="foot">
                  {block.turn.tool_names.slice(0, 2).map((tool) => (
                    <span className="tag" key={tool}>
                      {tool}
                    </span>
                  ))}
                </div>
              )}
            </div>
            <div className="blines">
              {block.lines.map((line) => (
                <div
                  key={line.line}
                  className={cx(
                    "bl",
                    selection &&
                      line.line >= selection.from &&
                      line.line <= selection.to &&
                      "sel",
                  )}
                >
                  <button
                    type="button"
                    className="g"
                    aria-label={`Select line ${line.line}`}
                    aria-pressed={Boolean(
                      selection &&
                      line.line >= selection.from &&
                      line.line <= selection.to,
                    )}
                    onClick={(event) => {
                      const next =
                        event.shiftKey && selection
                          ? {
                              from: Math.min(selection.from, line.line),
                              to: Math.max(selection.from, line.line),
                            }
                          : { from: line.line, to: line.line };
                      setSelection(next);
                      onSelectionChange(next);
                    }}
                  >
                    {line.line}
                  </button>
                  <code>{line.text || " "}</code>
                </div>
              ))}
            </div>
          </div>
        ))}
      </div>
      {selection && (
        <div className="selbar">
          <span>Selected</span>
          <b>
            L{selection.from}
            {selection.to !== selection.from ? `-L${selection.to}` : ""}
          </b>
          <span>·</span>
          <span>
            {originLabel(selected).toLowerCase()}
            {selected?.adapter ? ` · ${cleanAdapter(selected.adapter)}` : ""}
          </span>
          <span className="sp" />
          <button
            className="ghost"
            onClick={() => {
              const url = new URL(window.location.href);
              url.hash = "";
              url.searchParams.set("view", "origins");
              url.searchParams.set("path", path);
              if (sessionKey) url.searchParams.set("session", sessionKey);
              if (turnKey) url.searchParams.set("turn", turnKey);
              url.searchParams.set("from", String(selection.from));
              url.searchParams.set("to", String(selection.to));
              navigator.clipboard?.writeText(url.toString());
            }}
          >
            Copy link
          </button>
        </div>
      )}
    </section>
  );
}

function originLabel(origin?: BlameLine) {
  if (origin?.kind === "concurrent") return "Concurrent changes";
  if (origin?.turn_id) return `Turn ${origin.turn_id}`;
  return "Baseline";
}

function originExplanation(origin?: BlameLine) {
  if (origin?.kind === "concurrent") {
    return "These lines changed while recorded agent turns overlapped, so they cannot be assigned safely to one turn.";
  }
  return "These lines predate the recorded turn history for this project.";
}

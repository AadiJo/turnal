import { useEffect, useMemo, useState } from "preact/hooks";
import { api } from "./api";
import { Chrome, Delta, Note, Section, Tabs } from "./Chrome";
import { cleanAdapter, cx, displayTime, duration, isActive, shortAge, shortID } from "./format";
import type {
  Blame,
  BlameLine,
  DiffSummary,
  FileChange,
  FilePatch,
  Project,
  SessionSummary,
  SessionTurns,
  TurnDetail,
  TurnEvent,
} from "./types";

type Mode = "sessions" | "review" | "origins";

export function ProjectView({
  project,
  onBack,
  initialSessionKey,
}: {
  project: Project;
  onBack: () => void;
  initialSessionKey?: string;
}) {
  const store = project.store_id;
  const [mode, setMode] = useState<Mode>("sessions");
  const [sessions, setSessions] = useState<SessionSummary[]>([]);
  const [sessionKey, setSessionKey] = useState<string | null>(initialSessionKey ?? null);
  const [turns, setTurns] = useState<SessionTurns | null>(null);
  const [turnKey, setTurnKey] = useState<string | null>(null);
  const [detail, setDetail] = useState<TurnDetail | null>(null);
  const [diff, setDiff] = useState<DiffSummary | null>(null);
  const [patches, setPatches] = useState<Record<string, FilePatch>>({});
  const [blame, setBlame] = useState<Blame | null>(null);
  const [selectedPath, setSelectedPath] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [annotations, setAnnotations] = useState(true);
  const [split, setSplit] = useState(false);

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    api
      .sessions(store, controller.signal)
      .then((value) => {
        setSessions(value);
        setSessionKey((current) => current ?? value[0]?.key ?? null);
        setError(null);
      })
      .catch((nextError: Error) => {
        if (nextError.name !== "AbortError") setError(nextError.message);
      })
      .finally(() => setLoading(false));
    return () => controller.abort();
  }, [store]);

  useEffect(() => {
    if (!sessionKey) return;
    const controller = new AbortController();
    api
      .sessionTurns(store, sessionKey, controller.signal)
      .then((value) => {
        setTurns(value);
        setTurnKey(value.turns[0]?.key ?? null);
      })
      .catch((nextError: Error) => {
        if (nextError.name !== "AbortError") setError(nextError.message);
      });
    return () => controller.abort();
  }, [store, sessionKey]);

  useEffect(() => {
    if (!turnKey) {
      setDetail(null);
      setDiff(null);
      return;
    }
    const controller = new AbortController();
    Promise.all([
      api.turn(store, turnKey, controller.signal),
      api.diff(store, turnKey, controller.signal).catch(() => null),
    ])
      .then(([nextDetail, nextDiff]) => {
        setDetail(nextDetail);
        setDiff(nextDiff);
        setSelectedPath(nextDiff?.files[0]?.path ?? nextDetail.files?.[0]?.path ?? null);
      })
      .catch((nextError: Error) => {
        if (nextError.name !== "AbortError") setError(nextError.message);
      });
    return () => controller.abort();
  }, [store, turnKey]);

  // Fetch every changed file's patch so the review surface is one continuous
  // diff rather than a file picker.
  useEffect(() => {
    if (mode !== "review" || !turnKey || !diff) return;
    const controller = new AbortController();
    let cancelled = false;
    (async () => {
      const next: Record<string, FilePatch> = {};
      for (const file of diff.files.slice(0, 20)) {
        try {
          next[file.path] = await api.patch(store, turnKey, file.path, controller.signal);
        } catch {
          // A single unreadable file must not blank the whole review.
        }
      }
      if (!cancelled) setPatches(next);
    })();
    return () => {
      cancelled = true;
      controller.abort();
    };
  }, [mode, store, turnKey, diff]);

  useEffect(() => {
    if (mode !== "origins" || !turnKey || !selectedPath) return;
    const controller = new AbortController();
    api
      .blame(store, turnKey, selectedPath, controller.signal)
      .then(setBlame)
      .catch((nextError: Error) => {
        if (nextError.name !== "AbortError") setError(nextError.message);
      });
    return () => controller.abort();
  }, [mode, store, turnKey, selectedPath]);

  const session = turns?.session ?? sessions.find((item) => item.key === sessionKey) ?? null;

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
        ]}
        active={mode}
        onSelect={(id) => setMode(id as Mode)}
        meta={
          <>
            <span>{project.turn_count} turns</span>
            <span className="tag mono">{project.root}</span>
          </>
        }
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
          <Section title="Sessions" note={loading ? "loading" : `${sessions.length}`} />
          {sessions.length === 0 && !loading ? (
            <div className="empty">
              <strong>No recorded sessions yet</strong>
              <p>
                Run your configured agent in this project, or create a manual checkpoint with{" "}
                <code>turnal checkpoint</code>.
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
                  }}
                >
                  <span className={cx("status", isActive(item.status) && "active")}>
                    {item.status === "complete" ? "✓" : item.status === "active" ? "●" : "!"}
                  </span>
                  <span className="row-main">
                    <strong>{item.prompt_preview || `Session ${shortID(item.id, 12)}`}</strong>
                    <span>
                      {cleanAdapter(item.adapter)}
                      {item.model && (
                        <>
                          {" "}
                          <i>·</i> {item.model}
                        </>
                      )}{" "}
                      <i>·</i> {item.turn_count} turn{item.turn_count === 1 ? "" : "s"} <i>·</i>{" "}
                      {duration(item.started_at, item.finished_at)}
                    </span>
                  </span>
                  {item.branch && <span className="tag mono">{item.branch}</span>}
                  <Delta additions={item.additions} deletions={item.deletions} />
                  <span className="when">{shortAge(item.finished_at || item.started_at)}</span>
                </a>
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
                }}
              >
                <span className="n">{turn.id}</span>
                <span className="body">
                  <strong>{turn.prompt || "Manual checkpoint"}</strong>
                  <span>
                    {displayTime(turn.finished_at)} <i>·</i> {turn.files?.length ?? 0} file
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
                  <span className="avatar">{cleanAdapter(session.adapter).slice(0, 2)}</span>
                  <b>{cleanAdapter(session.adapter)}</b>
                  <span>opened this session at {displayTime(session.started_at)}</span>
                  <span className="sp" />
                  {detail?.post_commit && <span className="tag good">Checkpoint-backed</span>}
                </div>
                <div className="doc-body">
                  <p>{detail?.prompt || session.prompt_preview || "No prompt text was stored."}</p>
                </div>
              </div>
            )}

            <Section
              title="Changes"
              note={
                diff
                  ? `${diff.files.length} files · pre ${shortID(diff.pre_commit, 7)} → post ${shortID(diff.post_commit, 7)}`
                  : "no diff"
              }
            >
              <button className="ghost" onClick={() => setSplit(!split)}>
                Layout: {split ? "split" : "stacked"}
              </button>
              <button
                className={cx("ghost", annotations && "on")}
                onClick={() => setAnnotations(!annotations)}
              >
                Annotations: {annotations ? "on" : "off"}
              </button>
            </Section>

            <div className="surface">
              {diff?.files.map((file) => (
                <FileBox
                  key={file.path}
                  file={file}
                  patch={patches[file.path]}
                  split={split}
                  turn={detail}
                  annotations={annotations}
                />
              ))}
              {!diff?.files.length && (
                <div className="empty">
                  <strong>No file changes in this turn</strong>
                  <p>The turn was recorded, but its checkpoint pair contains no differing files.</p>
                </div>
              )}
            </div>

            {annotations && (
              <Note>
                <b>Turn context sits at the hunk that produced it.</b> Prompt, tool activity, and the
                pre and post checkpoint pair are joined to recorded activity. This does not claim
                statement-level causality.
              </Note>
            )}
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
          <OriginsPane blame={blame} path={selectedPath} />
        </div>
      )}
    </>
  );
}

function FileBox({
  file,
  patch,
  split,
  turn,
  annotations,
}: {
  file: FileChange;
  patch?: FilePatch;
  split: boolean;
  turn: TurnDetail | null;
  annotations: boolean;
}) {
  const [open, setOpen] = useState(true);
  const parts = file.path.split("/");
  const name = parts.pop();
  // Drop the unified-diff preamble. "diff --git", "index", "---" and "+++"
  // restate the path already shown in the file header, and rendering them as
  // content makes every file open with four lines of noise.
  const lines = useMemo(
    () =>
      (patch?.patch.split("\n") ?? []).filter(
        (line) =>
          !line.startsWith("diff --git ") &&
          !line.startsWith("index ") &&
          !line.startsWith("--- ") &&
          !line.startsWith("+++ ") &&
          !line.startsWith("new file mode ") &&
          !line.startsWith("deleted file mode ") &&
          !line.startsWith("similarity index ") &&
          !line.startsWith("rename from ") &&
          !line.startsWith("rename to "),
      ),
    [patch],
  );
  const tools = turn?.tool_names?.slice(0, 3) ?? [];

  return (
    <section className="file">
      <div className="fhead">
        <button className="caret" onClick={() => setOpen(!open)} aria-expanded={open}>
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
        <div className={cx("code", "bars", split ? "split" : "stacked")}>
          {turn && (
            <div className="hunk">
              <span className="expand" />
              <span className="range">{file.binary ? "binary file" : `@@ ${file.path}`}</span>
              <span className="hmeta">
                Turn <b>{turn.id}</b> · {displayTime(turn.finished_at)}
                {tools.length > 0 && ` · ${tools.join(", ")}`}
              </span>
            </div>
          )}
          {numberPatch(lines).map((row, index) => (
            <PatchLine key={`${index}-${row.value.slice(0, 10)}`} row={row} />
          ))}
          {patch?.truncated && (
            <div className="hunk">
              <span className="expand" />
              <span className="range">
                Patch limited to {Math.round(patch.limit_bytes / 1024)} KB
              </span>
              <span className="hmeta" />
            </div>
          )}
          {annotations && turn && <TurnAnnotation turn={turn} />}
        </div>
      )}
    </section>
  );
}

type PatchRow = {
  kind: "add" | "del" | "ctx" | "hunk";
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
function numberPatch(lines: string[]): PatchRow[] {
  const rows: PatchRow[] = [];
  let oldLine = 0;
  let newLine = 0;
  for (const value of lines) {
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
  if (row.kind === "hunk") {
    return (
      <div className="hunk">
        <span className="expand">↕</span>
        <span className="range">{row.value}</span>
        <span className="hmeta" />
      </div>
    );
  }
  return (
    <div className={cx("ln", row.kind === "add" && "add", row.kind === "del" && "del")}>
      <span className="g">{row.old ?? ""}</span>
      <span className="g">{row.next ?? ""}</span>
      <span className="sign">{row.kind === "add" ? "+" : row.kind === "del" ? "-" : ""}</span>
      <code>{row.value}</code>
    </div>
  );
}

function TurnAnnotation({ turn }: { turn: TurnDetail }) {
  const [open, setOpen] = useState(true);
  const toolEvents = turn.events.filter((event) => event.kind === "tool").slice(0, 3);
  return (
    <div className="anno">
      <div className="anno-in">
        <div className="anno-head">
          <span className="who">
            <span className="avatar">{cleanAdapter(turn.adapter).slice(0, 2)}</span> Turn {turn.id}
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
            <div className="anno-body">{turn.prompt || "No prompt text was stored for this turn."}</div>
            {toolEvents.map((event: TurnEvent) => (
              <div className="anno-tool" key={event.sequence}>
                <b>{event.tool_name || event.title}</b> {event.body?.slice(0, 220) ?? ""}
              </div>
            ))}
            <div className="anno-refs">
              <span>pre</span>
              <code>{shortID(turn.pre_commit, 10)}</code>
              <span>→</span>
              <span>post</span>
              <code>{shortID(turn.post_commit, 10)}</code>
              <span>·</span>
              <span>private checkpoint git commits</span>
            </div>
          </>
        )}
      </div>
    </div>
  );
}

function OriginsPane({ blame, path }: { blame: Blame | null; path: string | null }) {
  const [selection, setSelection] = useState<{ from: number; to: number } | null>(null);

  if (!path) {
    return (
      <section className="pane">
        <div className="empty">
          <strong>Select a file</strong>
          <p>Checkpoint-derived line origins will appear here.</p>
        </div>
      </section>
    );
  }

  // Merge consecutive lines from the same turn so the prompt is paid for once
  // instead of stamped on every line.
  const blocks: Array<{ turn?: BlameLine; lines: BlameLine[] }> = [];
  for (const line of blame?.lines ?? []) {
    const last = blocks[blocks.length - 1];
    if (last && last.turn?.turn_id === line.turn_id) last.lines.push(line);
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
        {blame && <span className="tag">{blame.complete_turns} turns replayed</span>}
        {blame?.truncated && <span className="tag">first {blame.lines.length} lines</span>}
      </div>
      <div className="origins">
        {blocks.map((block, index) => (
          <div key={index} className={cx("blk", !block.turn?.turn_id && "base")}>
            <span className={cx("age", `a${Math.min(index + 1, 4)}`)} />
            <div className="attrib">
              <div className="top">
                <b>{block.turn?.turn_id ? `Turn ${block.turn.turn_id}` : "Baseline"}</b>
                {block.turn?.adapter && <span>{cleanAdapter(block.turn.adapter)}</span>}
                {block.turn?.time && <span>{displayTime(block.turn.time)}</span>}
              </div>
              <div className="why">
                {block.turn?.prompt ||
                  "These lines predate the scoped recorded turn history in this store."}
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
                    selection && line.line >= selection.from && line.line <= selection.to && "sel",
                  )}
                >
                  <span
                    className="g"
                    onClick={(event) =>
                      setSelection((current) =>
                        event.shiftKey && current
                          ? {
                              from: Math.min(current.from, line.line),
                              to: Math.max(current.from, line.line),
                            }
                          : { from: line.line, to: line.line },
                      )
                    }
                  >
                    {line.line}
                  </span>
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
            {selected?.turn_id ? `turn ${selected.turn_id}` : "baseline"}
            {selected?.adapter ? ` · ${cleanAdapter(selected.adapter)}` : ""}
          </span>
          <span className="sp" />
          <button
            className="ghost"
            onClick={() =>
              navigator.clipboard?.writeText(
                `${window.location.href.split("#")[0]}#L${selection.from}-L${selection.to}`,
              )
            }
          >
            Copy link
          </button>
        </div>
      )}
    </section>
  );
}

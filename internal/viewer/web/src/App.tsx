import { useEffect, useMemo, useState } from "preact/hooks";
import { AnimatePresence, MotionConfig, motion, useReducedMotion } from "motion/react";
import {
  Pulse as Activity,
  ArrowClockwise,
  ArrowLeft,
  ArrowRight,
  ArrowsClockwise,
  GitBranch as Branch,
  CaretDown,
  CaretRight,
  Check,
  CheckCircle,
  Clock,
  Code,
  Copy,
  FileCode,
  Files,
  GitCommit,
  GitDiff,
  GitMerge,
  HardDrives,
  Info,
  Keyboard,
  ListMagnifyingGlass,
  MagnifyingGlass,
  Moon,
  Robot,
  ShieldCheck,
  SidebarSimple,
  Sparkle,
  Sun,
  TerminalWindow,
  TreeStructure,
  WarningCircle,
  X,
} from "@phosphor-icons/react";
import { api, APIError, bootstrap } from "./api";
import type {
  Blame,
  BlameLine,
  DiffSummary,
  FileChange,
  FilePatch,
  SessionSummary,
  SessionTurns,
  TurnDetail,
  TurnEvent,
  TurnSummary,
  Workspace,
} from "./types";

type ViewMode = "timeline" | "diff" | "provenance";
type Theme = "dark" | "light";

const spring = { type: "spring", stiffness: 360, damping: 32 } as const;
const viewTransition = { type: "spring", stiffness: 260, damping: 28 } as const;

function cx(...values: Array<string | false | null | undefined>) {
  return values.filter(Boolean).join(" ");
}

function shortID(value?: string, length = 8) {
  if (!value) return "unavailable";
  return value.length <= length ? value : value.slice(0, length);
}

function displayTime(value?: string) {
  if (!value) return "No timestamp";
  const date = new Date(value);
  const today = new Date();
  const sameDay = date.toDateString() === today.toDateString();
  return new Intl.DateTimeFormat(undefined, {
    ...(sameDay ? {} : { month: "short", day: "numeric" }),
    hour: "numeric",
    minute: "2-digit",
  }).format(date);
}

function relativeTime(value?: string) {
  if (!value) return "No activity";
  const seconds = Math.round((new Date(value).getTime() - Date.now()) / 1000);
  const formatter = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });
  if (Math.abs(seconds) < 60) return formatter.format(seconds, "second");
  const minutes = Math.round(seconds / 60);
  if (Math.abs(minutes) < 60) return formatter.format(minutes, "minute");
  const hours = Math.round(minutes / 60);
  if (Math.abs(hours) < 24) return formatter.format(hours, "hour");
  return formatter.format(Math.round(hours / 24), "day");
}

function duration(start?: string, end?: string) {
  if (!start || !end) return "In progress";
  const seconds = Math.max(0, Math.round((new Date(end).getTime() - new Date(start).getTime()) / 1000));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  const remainder = seconds % 60;
  return remainder ? `${minutes}m ${remainder}s` : `${minutes}m`;
}

function cleanAdapter(value?: string) {
  if (!value) return "Agent";
  if (value.toLowerCase().includes("claude")) return "Claude Code";
  if (value.toLowerCase().includes("codex")) return "Codex";
  if (value.toLowerCase().includes("manual")) return "Manual capture";
  return value;
}

function initials(value?: string) {
  const label = cleanAdapter(value);
  return label
    .split(/\s+/)
    .slice(0, 2)
    .map((part) => part[0])
    .join("")
    .toUpperCase();
}

function useAppData() {
  const [workspace, setWorkspace] = useState<Workspace | null>(null);
  const [sessions, setSessions] = useState<SessionSummary[]>([]);
  const [error, setError] = useState<Error | null>(null);
  const [loading, setLoading] = useState(true);
  const [nonce, setNonce] = useState(0);

  useEffect(() => {
    let active = true;
    setLoading(true);
    (async () => {
      try {
        await bootstrap();
        const [nextWorkspace, nextSessions] = await Promise.all([api.workspace(), api.sessions()]);
        if (!active) return;
        setWorkspace(nextWorkspace);
        setSessions(nextSessions);
        setError(null);
      } catch (nextError) {
        if (active) setError(nextError as Error);
      } finally {
        if (active) setLoading(false);
      }
    })();
    return () => {
      active = false;
    };
  }, [nonce]);

  return { workspace, sessions, error, loading, refresh: () => setNonce((value) => value + 1) };
}

export function App() {
  const { workspace, sessions, error, loading, refresh } = useAppData();
  const reduceMotion = useReducedMotion();
  const [theme, setTheme] = useState<Theme>(() =>
    window.matchMedia?.("(prefers-color-scheme: light)").matches ? "light" : "dark",
  );
  const [sidebarOpen, setSidebarOpen] = useState(true);
  const [query, setQuery] = useState("");
  const [selectedSessionKey, setSelectedSessionKey] = useState<string | null>(null);
  const [sessionTurns, setSessionTurns] = useState<SessionTurns | null>(null);
  const [selectedTurnKey, setSelectedTurnKey] = useState<string | null>(null);
  const [turnDetail, setTurnDetail] = useState<TurnDetail | null>(null);
  const [diff, setDiff] = useState<DiffSummary | null>(null);
  const [patch, setPatch] = useState<FilePatch | null>(null);
  const [blame, setBlame] = useState<Blame | null>(null);
  const [selectedPath, setSelectedPath] = useState<string | null>(null);
  const [selectedLine, setSelectedLine] = useState<BlameLine | null>(null);
  const [mode, setMode] = useState<ViewMode>("timeline");
  const [detailLoading, setDetailLoading] = useState(false);
  const [detailError, setDetailError] = useState<Error | null>(null);
  const [copied, setCopied] = useState<string | null>(null);

  useEffect(() => {
    document.documentElement.dataset.theme = theme;
  }, [theme]);

  useEffect(() => {
    const handleShortcut = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        setSidebarOpen(true);
        window.setTimeout(() => document.querySelector<HTMLInputElement>(".session-search input")?.focus(), 0);
      }
    };
    window.addEventListener("keydown", handleShortcut);
    return () => window.removeEventListener("keydown", handleShortcut);
  }, []);

  const filteredSessions = useMemo(() => {
    const needle = query.trim().toLowerCase();
    if (!needle) return sessions;
    return sessions.filter((session) =>
      [session.id, session.adapter, session.model, session.branch, session.prompt_preview]
        .filter(Boolean)
        .some((value) => value!.toLowerCase().includes(needle)),
    );
  }, [sessions, query]);

  useEffect(() => {
    if (!sessions.length || selectedSessionKey) return;
    const requested = new URLSearchParams(window.location.search).get("session");
    const initial = sessions.find((session) => session.key === requested) ?? sessions[0];
    setSelectedSessionKey(initial.key);
  }, [sessions, selectedSessionKey]);

  useEffect(() => {
    if (!selectedSessionKey) return;
    const controller = new AbortController();
    setDetailLoading(true);
    api
      .sessionTurns(selectedSessionKey, controller.signal)
      .then((value) => {
        setSessionTurns(value);
        setSelectedTurnKey((current) =>
          current && value.turns.some((turn) => turn.key === current) ? current : value.turns[0]?.key ?? null,
        );
        setDetailError(null);
      })
      .catch((nextError) => {
        if ((nextError as Error).name !== "AbortError") setDetailError(nextError as Error);
      })
      .finally(() => setDetailLoading(false));
    return () => controller.abort();
  }, [selectedSessionKey]);

  useEffect(() => {
    if (!selectedTurnKey) {
      setTurnDetail(null);
      setDiff(null);
      return;
    }
    const controller = new AbortController();
    setDetailLoading(true);
    Promise.all([
      api.turn(selectedTurnKey, controller.signal),
      api.diff(selectedTurnKey, controller.signal).catch(() => null),
    ])
      .then(([nextDetail, nextDiff]) => {
        setTurnDetail(nextDetail);
        setDiff(nextDiff);
        const nextPath = nextDiff?.files[0]?.path ?? nextDetail.files?.[0]?.path ?? null;
        setSelectedPath((current) =>
          current && nextDiff?.files.some((file) => file.path === current) ? current : nextPath,
        );
        setSelectedLine(null);
        setDetailError(null);
        const root = window.location.pathname.split("/").filter(Boolean)[0];
        history.replaceState({}, "", `/${root}/turns/${encodeURIComponent(selectedTurnKey)}`);
      })
      .catch((nextError) => {
        if ((nextError as Error).name !== "AbortError") setDetailError(nextError as Error);
      })
      .finally(() => setDetailLoading(false));
    return () => controller.abort();
  }, [selectedTurnKey]);

  useEffect(() => {
    if (!selectedTurnKey || !selectedPath || mode === "timeline") return;
    const controller = new AbortController();
    setDetailLoading(true);
    const operation =
      mode === "diff"
        ? api.patch(selectedTurnKey, selectedPath, controller.signal).then((value) => {
            setPatch(value);
            setBlame(null);
          })
        : api.blame(selectedTurnKey, selectedPath, controller.signal).then((value) => {
            setBlame(value);
            setPatch(null);
            setSelectedLine((current) => current ?? value.lines.find((line) => line.turn_id) ?? value.lines[0] ?? null);
          });
    operation
      .then(() => setDetailError(null))
      .catch((nextError) => {
        if ((nextError as Error).name !== "AbortError") setDetailError(nextError as Error);
      })
      .finally(() => setDetailLoading(false));
    return () => controller.abort();
  }, [mode, selectedTurnKey, selectedPath]);

  const selectSession = (session: SessionSummary) => {
    setSelectedSessionKey(session.key);
    setSelectedTurnKey(null);
    setSessionTurns(null);
    setTurnDetail(null);
    setMode("timeline");
  };

  const copyText = async (label: string, value: string) => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(label);
      window.setTimeout(() => setCopied(null), 1400);
    } catch {
      setCopied(null);
    }
  };

  if (loading) return <LoadingScreen />;
  if (error || !workspace) return <FatalError error={error} onRetry={refresh} />;

  return (
    <MotionConfig reducedMotion="user">
    <div className="app-shell">
      <AppRail workspace={workspace} mode={mode} setMode={setMode} />
      <AnimatePresence initial={false}>
        {sidebarOpen && (
          <motion.aside
            className="session-sidebar"
            initial={reduceMotion ? false : { x: -22, opacity: 0 }}
            animate={{ x: 0, opacity: 1 }}
            exit={reduceMotion ? { opacity: 1 } : { x: -22, opacity: 0 }}
            transition={reduceMotion ? { duration: 0 } : spring}
          >
            <div className="sidebar-repo">
              <div className="repo-mark" aria-hidden="true">
                <span />
              </div>
              <div className="repo-copy">
                <strong>{workspace.name}</strong>
                <span><Branch size={13} weight="bold" /> {sessionTurns?.session.branch || "workspace"}</span>
              </div>
              <button className="icon-button quiet" title="Workspace menu" aria-label="Workspace menu">
                <CaretDown size={15} weight="bold" />
              </button>
            </div>

            <div className="sidebar-tabs" role="tablist" aria-label="History source">
              <button className="active" role="tab" aria-selected="true">Sessions</button>
              <button role="tab" aria-selected="false" disabled title="Available after single-workspace validation">Worktrees</button>
            </div>

            <label className="session-search">
              <MagnifyingGlass size={15} />
              <input
                value={query}
                onInput={(event) => setQuery(event.currentTarget.value)}
                placeholder="Find session or branch"
                aria-label="Find session or branch"
              />
              <kbd>⌘K</kbd>
            </label>

            <div className="session-list-header">
              <span>{filteredSessions.length} recorded sessions</span>
              <button className="icon-button quiet" onClick={refresh} aria-label="Refresh sessions" title="Refresh">
                <ArrowClockwise size={14} weight="bold" />
              </button>
            </div>

            <div className="session-list" role="listbox" aria-label="Recorded sessions">
              {filteredSessions.length ? (
                filteredSessions.map((session) => (
                  <SessionItem
                    key={session.key}
                    session={session}
                    selected={session.key === selectedSessionKey}
                    onSelect={() => selectSession(session)}
                  />
                ))
              ) : (
                <EmptySidebar query={query} />
              )}
            </div>

            <div className="sidebar-footer">
              <div className={cx("health-orb", workspace.history_state)}><ShieldCheck size={15} weight="fill" /></div>
              <div>
                <strong>{workspace.history_state === "ready" ? "History available" : "History needs attention"}</strong>
                <span>{workspace.turn_count} turns in this worktree</span>
              </div>
              <CaretRight size={14} />
            </div>
          </motion.aside>
        )}
      </AnimatePresence>

      <main className="workspace-stage">
        <TopBar
          workspace={workspace}
          sidebarOpen={sidebarOpen}
          setSidebarOpen={setSidebarOpen}
          theme={theme}
          setTheme={setTheme}
          onRefresh={refresh}
        />

        <div className="workspace-body">
          <SessionHeader
            session={sessionTurns?.session ?? null}
            mode={mode}
            setMode={setMode}
            copied={copied}
            copyText={copyText}
          />

          {detailError && <InlineError error={detailError} />}

          <div className="content-frame">
            <AnimatePresence mode="wait" initial={false}>
              {mode === "timeline" ? (
                <motion.div
                  key="timeline"
                  className="timeline-layout"
                  initial={reduceMotion ? false : { opacity: 0, y: 10 }}
                  animate={{ opacity: 1, y: 0 }}
                  exit={reduceMotion ? { opacity: 1 } : { opacity: 0, y: -6 }}
                  transition={reduceMotion ? { duration: 0 } : viewTransition}
                >
                  <Timeline
                    data={sessionTurns}
                    selectedTurnKey={selectedTurnKey}
                    onSelectTurn={setSelectedTurnKey}
                    loading={detailLoading && !sessionTurns}
                  />
                  <TurnInspector
                    detail={turnDetail}
                    loading={detailLoading && !turnDetail}
                    onOpenFile={(path, nextMode) => {
                      setSelectedPath(path);
                      setMode(nextMode);
                    }}
                    copied={copied}
                    copyText={copyText}
                  />
                </motion.div>
              ) : mode === "diff" ? (
                <motion.div
                  key="diff"
                  className="review-layout"
                  initial={reduceMotion ? false : { opacity: 0, x: 14 }}
                  animate={{ opacity: 1, x: 0 }}
                  exit={reduceMotion ? { opacity: 1 } : { opacity: 0, x: -10 }}
                  transition={reduceMotion ? { duration: 0 } : viewTransition}
                >
                  <FileNavigator files={diff?.files ?? []} selectedPath={selectedPath} onSelect={setSelectedPath} />
                  <PatchViewer patch={patch} diff={diff} loading={detailLoading} />
                  <ContextInspector detail={turnDetail} path={selectedPath} copyText={copyText} copied={copied} />
                </motion.div>
              ) : (
                <motion.div
                  key="provenance"
                  className="review-layout"
                  initial={reduceMotion ? false : { opacity: 0, x: 14 }}
                  animate={{ opacity: 1, x: 0 }}
                  exit={reduceMotion ? { opacity: 1 } : { opacity: 0, x: -10 }}
                  transition={reduceMotion ? { duration: 0 } : viewTransition}
                >
                  <FileNavigator files={diff?.files ?? []} selectedPath={selectedPath} onSelect={setSelectedPath} />
                  <BlameViewer blame={blame} selectedLine={selectedLine} onSelectLine={setSelectedLine} loading={detailLoading} />
                  <ProvenanceInspector line={selectedLine} blame={blame} onOpenTurn={setSelectedTurnKey} />
                </motion.div>
              )}
            </AnimatePresence>
          </div>
        </div>

        <StatusBar workspace={workspace} loading={detailLoading} />
      </main>
    </div>
    </MotionConfig>
  );
}

function AppRail({ workspace, mode, setMode }: { workspace: Workspace; mode: ViewMode; setMode: (mode: ViewMode) => void }) {
  const items: Array<{ mode?: ViewMode; label: string; icon: typeof Activity }> = [
    { mode: "timeline", label: "Timeline", icon: TreeStructure },
    { mode: "diff", label: "Changes", icon: GitDiff },
    { mode: "provenance", label: "Provenance", icon: ListMagnifyingGlass },
    { label: "Replay", icon: ArrowsClockwise },
  ];
  return (
    <nav className="app-rail" aria-label="Viewer sections">
      <div className="brand-glyph" title="Turnal Prism" aria-label="Turnal Prism">
        <span className="glyph-a" />
        <span className="glyph-b" />
      </div>
      <div className="rail-items">
        {items.map((item) => {
          const Icon = item.icon;
          const active = item.mode === mode;
          return (
            <button
              key={item.label}
              className={cx("rail-button", active && "active")}
              onClick={() => item.mode && setMode(item.mode)}
              disabled={!item.mode}
              aria-label={item.label}
              title={item.label}
            >
              {active && <motion.span className="rail-active" layoutId="rail-active" transition={spring} />}
              <Icon size={20} weight={active ? "fill" : "regular"} />
            </button>
          );
        })}
      </div>
      <div className="rail-bottom">
        <button className="rail-button" aria-label="Keyboard shortcuts" title="Keyboard shortcuts"><Keyboard size={20} /></button>
        <div className={cx("rail-health", workspace.history_state)} title={`History: ${workspace.history_state}`}>
          <ShieldCheck size={18} weight="fill" />
        </div>
      </div>
    </nav>
  );
}

function TopBar({
  workspace,
  sidebarOpen,
  setSidebarOpen,
  theme,
  setTheme,
  onRefresh,
}: {
  workspace: Workspace;
  sidebarOpen: boolean;
  setSidebarOpen: (open: boolean) => void;
  theme: Theme;
  setTheme: (theme: Theme) => void;
  onRefresh: () => void;
}) {
  return (
    <header className="topbar">
      <div className="topbar-left">
        <button className={cx("icon-button", sidebarOpen && "active")} onClick={() => setSidebarOpen(!sidebarOpen)} aria-label="Toggle session sidebar" title="Toggle sidebar">
          <SidebarSimple size={18} weight="bold" />
        </button>
        <span className="topbar-divider" />
        <button className="icon-button quiet" disabled aria-label="Back"><ArrowLeft size={17} /></button>
        <button className="icon-button quiet" disabled aria-label="Forward"><ArrowRight size={17} /></button>
        <div className="breadcrumbs">
          <span>{workspace.name}</span>
          <CaretRight size={12} />
          <strong>Local history</strong>
        </div>
      </div>
        <button className="command-search" type="button" onClick={() => {
          setSidebarOpen(true);
          window.setTimeout(() => document.querySelector<HTMLInputElement>(".session-search input")?.focus(), 0);
        }}>
          <MagnifyingGlass size={15} />
        <span>Search sessions and branches</span>
        <kbd>⌘ K</kbd>
      </button>
      <div className="topbar-actions">
        <button className="icon-button quiet" onClick={onRefresh} title="Refresh" aria-label="Refresh"><ArrowClockwise size={17} /></button>
        <button className="icon-button quiet" onClick={() => setTheme(theme === "dark" ? "light" : "dark")} title="Toggle theme" aria-label="Toggle theme">
          {theme === "dark" ? <Sun size={17} /> : <Moon size={17} />}
        </button>
        <div className="local-chip"><HardDrives size={14} weight="fill" /> Local only</div>
      </div>
    </header>
  );
}

function SessionItem({ session, selected, onSelect }: { session: SessionSummary; selected: boolean; onSelect: () => void }) {
  return (
    <motion.button
      layout
      className={cx("session-item", selected && "selected")}
      onClick={onSelect}
      role="option"
      aria-selected={selected}
      whileTap={{ scale: 0.985 }}
      transition={spring}
    >
      {selected && <motion.span className="session-selection" layoutId="session-selection" transition={spring} />}
      <div className={cx("agent-avatar", session.adapter?.toLowerCase().includes("codex") && "codex")}>{initials(session.adapter)}</div>
      <div className="session-copy">
        <div className="session-title-row">
          <strong>{session.prompt_preview || `Session ${shortID(session.id, 12)}`}</strong>
          <span>{relativeTime(session.finished_at)}</span>
        </div>
        <div className="session-meta-row">
          <span className={cx("status-mark", session.status)} />
          <span>{cleanAdapter(session.adapter)}</span>
          <span className="mini-separator" />
          <span>{session.turn_count} turn{session.turn_count === 1 ? "" : "s"}</span>
        </div>
        <div className="session-stats-row">
          <span><Branch size={12} /> {session.branch || "workspace"}</span>
          <span className="change-count plus">+{session.additions}</span>
          <span className="change-count minus">-{session.deletions}</span>
        </div>
      </div>
    </motion.button>
  );
}

function SessionHeader({
  session,
  mode,
  setMode,
  copied,
  copyText,
}: {
  session: SessionSummary | null;
  mode: ViewMode;
  setMode: (mode: ViewMode) => void;
  copied: string | null;
  copyText: (label: string, value: string) => void;
}) {
  const tabs: Array<{ id: ViewMode; label: string; icon: typeof Activity }> = [
    { id: "timeline", label: "Timeline", icon: TreeStructure },
    { id: "diff", label: "Changes", icon: GitDiff },
    { id: "provenance", label: "Line origins", icon: ListMagnifyingGlass },
  ];
  return (
    <section className="session-header">
      <div className="session-heading">
        <div className="heading-icon"><Robot size={20} weight="fill" /></div>
        <div>
          <div className="heading-title-row">
            <h1>{session?.prompt_preview || (session ? `Session ${shortID(session.id, 14)}` : "Select a session")}</h1>
            {session && <span className={cx("session-state", session.status)}>{session.status}</span>}
          </div>
          {session && (
            <p>
              <span>{cleanAdapter(session.adapter)}</span>
              <span>{session.model || "Recorded agent"}</span>
              <span>{displayTime(session.started_at)}</span>
              <span>{duration(session.started_at, session.finished_at)}</span>
            </p>
          )}
        </div>
      </div>
      <div className="session-actions">
        {session && (
          <button className="secondary-button" onClick={() => copyText("session", session.id)}>
            {copied === "session" ? <Check size={15} weight="bold" /> : <Copy size={15} />}
            {copied === "session" ? "Copied" : "Copy session ID"}
          </button>
        )}
        <button className="primary-button" onClick={() => session && copyText("command", `turnal show ${session.id}:latest --full`)} disabled={!session}>
          <TerminalWindow size={16} weight="fill" />
          {copied === "command" ? "Command copied" : "Copy inspect command"}
        </button>
      </div>
      <div className="view-tabs" role="tablist" aria-label="Session view">
        {tabs.map((tab) => {
          const Icon = tab.icon;
          return (
            <button key={tab.id} className={cx(mode === tab.id && "active")} onClick={() => setMode(tab.id)} role="tab" aria-selected={mode === tab.id}>
              <Icon size={15} weight={mode === tab.id ? "fill" : "regular"} />
              {tab.label}
              {mode === tab.id && <motion.span layoutId="view-tab" transition={spring} />}
            </button>
          );
        })}
      </div>
    </section>
  );
}

function Timeline({
  data,
  selectedTurnKey,
  onSelectTurn,
  loading,
}: {
  data: SessionTurns | null;
  selectedTurnKey: string | null;
  onSelectTurn: (key: string) => void;
  loading: boolean;
}) {
  if (loading) return <TimelineSkeleton />;
  if (!data?.turns.length) {
    return (
      <div className="empty-main">
        <div className="empty-illustration"><GitCommit size={28} weight="duotone" /></div>
        <h2>No recorded turns yet</h2>
        <p>Use your configured agent or run a manual turn to populate this session.</p>
        <code>turnal turn start --session &lt;id&gt;</code>
      </div>
    );
  }
  return (
    <section className="timeline-panel" aria-label="Turn topology">
      <div className="panel-toolbar">
        <div>
          <TreeStructure size={16} weight="fill" />
          <strong>Agent topology</strong>
          <span>{data.turns.length} turns</span>
        </div>
        <div className="toolbar-actions">
          <button className="compact-control"><GitMerge size={14} /> Main lane</button>
          <button className="icon-button quiet" title="Timeline options" aria-label="Timeline options"><CaretDown size={14} /></button>
        </div>
      </div>
      <div className="timeline-scroll">
        <div className="lane-labels">
          <span className="lane-chip primary"><span /> agent</span>
          <span className="lane-chip"><span /> checkpoints</span>
        </div>
        <div className="turn-graph">
          {data.turns.map((turn, index) => (
            <TurnNode
              key={turn.key}
              turn={turn}
              index={index}
              last={index === data.turns.length - 1}
              selected={turn.key === selectedTurnKey}
              onSelect={() => onSelectTurn(turn.key)}
            />
          ))}
        </div>
      </div>
    </section>
  );
}

function TurnNode({ turn, index, last, selected, onSelect }: { turn: TurnSummary; index: number; last: boolean; selected: boolean; onSelect: () => void }) {
  return (
    <motion.button
      className={cx("turn-node", selected && "selected")}
      onClick={onSelect}
      initial={{ opacity: 0, y: 10 }}
      animate={{ opacity: 1, y: 0 }}
      transition={{ ...spring, delay: Math.min(index * 0.035, 0.22) }}
      whileTap={{ scale: 0.992 }}
    >
      <div className="graph-column" aria-hidden="true">
        <span className={cx("graph-line", !last && "continues")} />
        <motion.span className={cx("graph-node-dot", turn.status)} animate={selected ? { scale: [1, 1.16, 1] } : { scale: 1 }} transition={{ duration: 0.34 }}>
          {turn.checkpointed && <Check size={10} weight="bold" />}
        </motion.span>
        <span className="checkpoint-tick pre" />
        <span className="checkpoint-tick post" />
      </div>
      <div className="turn-card">
        {selected && <motion.span className="turn-selection" layoutId="turn-selection" transition={spring} />}
        <div className="turn-main">
          <div className="turn-topline">
            <span className="turn-number">Turn {turn.id}</span>
            <span>{displayTime(turn.finished_at)}</span>
            <span>{duration(turn.started_at, turn.finished_at)}</span>
          </div>
          <strong className="turn-prompt">{turn.prompt || "Manual workspace checkpoint"}</strong>
          <div className="turn-tools">
            {(turn.tool_names?.length ? turn.tool_names : [cleanAdapter(turn.adapter)]).slice(0, 4).map((tool) => (
              <span key={tool}><Code size={12} /> {tool}</span>
            ))}
            {turn.tool_names && turn.tool_names.length > 4 && <span>+{turn.tool_names.length - 4}</span>}
          </div>
        </div>
        <div className="turn-change-summary">
          <span className="files"><Files size={14} /> {turn.files?.length ?? 0}</span>
          <span className="plus">+{turn.additions}</span>
          <span className="minus">-{turn.deletions}</span>
          <CaretRight size={15} />
        </div>
      </div>
    </motion.button>
  );
}

function TurnInspector({
  detail,
  loading,
  onOpenFile,
  copied,
  copyText,
}: {
  detail: TurnDetail | null;
  loading: boolean;
  onOpenFile: (path: string, mode: ViewMode) => void;
  copied: string | null;
  copyText: (label: string, value: string) => void;
}) {
  const [openEvents, setOpenEvents] = useState<Record<number, boolean>>({});
  if (loading) return <InspectorSkeleton />;
  if (!detail) return <div className="inspector-panel empty"><Info size={22} /><p>Select a turn to inspect its evidence.</p></div>;
  const promptEvent = detail.events.find((event) => event.kind === "prompt");
  const visibleEvents = detail.events.filter((event) => ["assistant", "tool", "result", "error"].includes(event.kind));
  return (
    <aside className="inspector-panel">
      <div className="inspector-heading">
        <div>
          <span>Turn {detail.id}</span>
          <strong>Evidence</strong>
        </div>
        <button className="icon-button quiet" onClick={() => copyText("turn", detail.key)} title="Copy canonical key" aria-label="Copy canonical key">
          {copied === "turn" ? <Check size={15} weight="bold" /> : <Copy size={15} />}
        </button>
      </div>
      <div className="truth-banner"><ShieldCheck size={16} weight="fill" /><span>Checkpoint-backed change</span></div>
      <InspectorSection title="Prompt" icon={Sparkle} defaultOpen>
        <p className="prompt-block">{promptEvent?.body || detail.prompt || "No prompt text was stored for this turn."}</p>
      </InspectorSection>
      <InspectorSection title={`Activity (${visibleEvents.length})`} icon={Activity} defaultOpen>
        <div className="event-stack">
          {visibleEvents.slice(0, 8).map((event) => (
            <EventRow
              key={event.sequence}
              event={event}
              open={!!openEvents[event.sequence]}
              onToggle={() => setOpenEvents((current) => ({ ...current, [event.sequence]: !current[event.sequence] }))}
            />
          ))}
        </div>
      </InspectorSection>
      <InspectorSection title={`Files changed (${detail.files?.length ?? 0})`} icon={FileCode} defaultOpen>
        <div className="inspector-files">
          {detail.files?.map((file) => (
            <div className="inspector-file" key={file.path}>
              <button onClick={() => onOpenFile(file.path, "diff")}><FileCode size={15} /> <span>{file.path}</span></button>
              <div><span className="plus">+{file.additions}</span><span className="minus">-{file.deletions}</span></div>
              <button className="origin-button" onClick={() => onOpenFile(file.path, "provenance")} title="View line origins"><ListMagnifyingGlass size={14} /></button>
            </div>
          ))}
        </div>
      </InspectorSection>
      <div className="inspector-identity">
        <div><span>pre</span><code>{shortID(detail.pre_commit, 10)}</code></div>
        <div><span>post</span><code>{shortID(detail.post_commit, 10)}</code></div>
      </div>
    </aside>
  );
}

function InspectorSection({ title, icon: Icon, defaultOpen = false, children }: { title: string; icon: typeof Activity; defaultOpen?: boolean; children: preact.ComponentChildren }) {
  const [open, setOpen] = useState(defaultOpen);
  return (
    <section className="inspector-section">
      <button onClick={() => setOpen(!open)} aria-expanded={open}>
        <span><Icon size={14} weight="fill" /> {title}</span>
        {open ? <CaretDown size={13} /> : <CaretRight size={13} />}
      </button>
      <AnimatePresence initial={false}>
        {open && (
          <motion.div className="inspector-section-content" initial={{ opacity: 0, height: 0 }} animate={{ opacity: 1, height: "auto" }} exit={{ opacity: 0, height: 0 }} transition={spring}>
            {children}
          </motion.div>
        )}
      </AnimatePresence>
    </section>
  );
}

function EventRow({ event, open, onToggle }: { event: TurnEvent; open: boolean; onToggle: () => void }) {
  const icon = event.kind === "tool" ? <TerminalWindow size={13} /> : event.kind === "error" ? <WarningCircle size={13} /> : <Activity size={13} />;
  return (
    <div className={cx("event-row", event.kind)}>
      <button onClick={onToggle} aria-expanded={open}>
        <span className="event-icon">{icon}</span>
        <span className="event-title"><strong>{event.title}</strong><small>{displayTime(event.time)}</small></span>
        {event.body && (open ? <CaretDown size={12} /> : <CaretRight size={12} />)}
      </button>
      {open && event.body && <pre>{event.body}</pre>}
    </div>
  );
}

function FileNavigator({ files, selectedPath, onSelect }: { files: FileChange[]; selectedPath: string | null; onSelect: (path: string) => void }) {
  return (
    <aside className="file-navigator">
      <div className="file-nav-heading">
        <div><Files size={15} weight="fill" /><strong>Changed files</strong></div>
        <span>{files.length}</span>
      </div>
      <div className="file-filter"><MagnifyingGlass size={13} /><span>Filter files</span></div>
      <div className="file-tree">
        {files.map((file) => {
          const parts = file.path.split("/");
          const name = parts.pop();
          return (
            <button key={file.path} className={cx(selectedPath === file.path && "selected")} onClick={() => onSelect(file.path)} title={file.path}>
              {selectedPath === file.path && <motion.span className="file-selection" layoutId="file-selection" transition={spring} />}
              <FileCode size={15} />
              <span className="file-label"><strong>{name}</strong><small>{parts.join("/") || "/"}</small></span>
              <span className="file-delta"><i>+{file.additions}</i><b>-{file.deletions}</b></span>
            </button>
          );
        })}
      </div>
    </aside>
  );
}

function PatchViewer({ patch, diff, loading }: { patch: FilePatch | null; diff: DiffSummary | null; loading: boolean }) {
  const lines = useMemo(() => patch?.patch.split("\n") ?? [], [patch]);
  if (loading && !patch) return <div className="code-panel"><CodeSkeleton /></div>;
  if (!patch) return <div className="code-panel empty"><GitDiff size={26} /><h2>Select a changed file</h2><p>The bounded unified patch will appear here.</p></div>;
  return (
    <section className="code-panel">
      <div className="code-toolbar">
        <div><FileCode size={16} weight="fill" /><strong>{patch.path}</strong></div>
        <div className="diff-totals"><span className="plus">+{diff?.additions ?? 0}</span><span className="minus">-{diff?.deletions ?? 0}</span><span>Unified</span></div>
      </div>
      <div className="commit-strip">
        <code>{shortID(diff?.pre_commit, 10)}</code><ArrowRight size={13} /><code>{shortID(diff?.post_commit, 10)}</code>
        <span>{diff?.truth_source}</span>
      </div>
      {patch.truncated && <div className="truncation-notice"><WarningCircle size={14} /> Patch limited to {Math.round(patch.limit_bytes / 1024)} KB.</div>}
      <div className="patch-scroll" role="region" aria-label={`Patch for ${patch.path}`} tabIndex={0}>
        <div className="patch-lines">
          {lines.map((line, index) => <PatchLine key={`${index}-${line.slice(0, 12)}`} value={line} index={index} />)}
        </div>
      </div>
    </section>
  );
}

function PatchLine({ value, index }: { value: string; index: number }) {
  const kind = value.startsWith("+") && !value.startsWith("+++") ? "addition" : value.startsWith("-") && !value.startsWith("---") ? "deletion" : value.startsWith("@@") ? "hunk" : value.startsWith("diff ") || value.startsWith("index ") || value.startsWith("---") || value.startsWith("+++") ? "meta" : "context";
  return (
    <div className={cx("patch-line", kind)}>
      <span className="patch-number">{index + 1}</span>
      <span className="patch-sign">{kind === "addition" ? "+" : kind === "deletion" ? "-" : ""}</span>
      <code>{kind === "addition" || kind === "deletion" ? value.slice(1) : value}</code>
    </div>
  );
}

function BlameViewer({ blame, selectedLine, onSelectLine, loading }: { blame: Blame | null; selectedLine: BlameLine | null; onSelectLine: (line: BlameLine) => void; loading: boolean }) {
  if (loading && !blame) return <div className="code-panel"><CodeSkeleton /></div>;
  if (!blame) return <div className="code-panel empty"><ListMagnifyingGlass size={26} /><h2>Select a changed file</h2><p>Checkpoint-derived line origins will appear here.</p></div>;
  const colors = ["lane-one", "lane-two", "lane-three", "lane-four"];
  return (
    <section className="code-panel">
      <div className="code-toolbar">
        <div><ListMagnifyingGlass size={16} weight="fill" /><strong>{blame.path}</strong></div>
        <div className="diff-totals"><span>{blame.complete_turns} turns replayed</span></div>
      </div>
      <div className="commit-strip"><ShieldCheck size={13} weight="fill" /><code>{shortID(blame.latest_commit, 10)}</code><span>{blame.truth_source}</span></div>
      {blame.truncated && <div className="truncation-notice"><WarningCircle size={14} /> Showing the first {blame.lines.length} lines.</div>}
      <div className="blame-scroll" role="region" aria-label={`Line origins for ${blame.path}`} tabIndex={0}>
        {blame.lines.map((line) => (
          <button key={line.line} className={cx("blame-line", selectedLine?.line === line.line && "selected")} onClick={() => onSelectLine(line)}>
            <span className={cx("origin-band", colors[(line.turn_id ?? 0) % colors.length])}>{line.turn_id ? `T${line.turn_id}` : "base"}</span>
            <span className="blame-number">{line.line}</span>
            <code>{line.text || " "}</code>
          </button>
        ))}
      </div>
    </section>
  );
}

function ContextInspector({ detail, path, copyText, copied }: { detail: TurnDetail | null; path: string | null; copyText: (label: string, value: string) => void; copied: string | null }) {
  const related = detail?.events.filter((event) => ["prompt", "tool", "assistant"].includes(event.kind)) ?? [];
  return (
    <aside className="context-inspector">
      <div className="context-heading"><span>Change context</span><strong>{path?.split("/").pop() || "File"}</strong></div>
      <div className="context-fact"><span>Origin turn</span><button onClick={() => detail && copyText("context-turn", detail.key)}>{copied === "context-turn" ? <Check size={13} /> : <Copy size={13} />} Turn {detail?.id ?? ""}</button></div>
      <div className="context-prompt"><span><Sparkle size={13} weight="fill" /> Recorded prompt</span><p>{detail?.prompt || "No prompt content was stored."}</p></div>
      <div className="context-events">
        <span>Related activity</span>
        {related.slice(0, 6).map((event) => <div key={event.sequence}><span className={cx("event-mini-icon", event.kind)}>{event.kind === "tool" ? <TerminalWindow size={12} /> : <Activity size={12} />}</span><div><strong>{event.title}</strong><small>{event.body?.slice(0, 90) || displayTime(event.time)}</small></div></div>)}
      </div>
      <div className="confidence-note"><Info size={14} /><p><strong>Evidence boundary</strong>This view joins recorded activity to the pre and post checkpoint pair. It does not claim statement-level causality.</p></div>
    </aside>
  );
}

function ProvenanceInspector({ line, blame, onOpenTurn }: { line: BlameLine | null; blame: Blame | null; onOpenTurn: (key: string) => void }) {
  return (
    <aside className="context-inspector provenance-inspector">
      <div className="context-heading"><span>Line origin</span><strong>{line ? `Line ${line.line}` : "Select a line"}</strong></div>
      {line ? (
        <>
          <div className="origin-summary">
            <div className="origin-avatar"><GitCommit size={18} weight="fill" /></div>
            <div><strong>{line.turn_id ? `Turn ${line.turn_id}` : "Baseline checkpoint"}</strong><span>{cleanAdapter(line.adapter)} {line.time ? `at ${displayTime(line.time)}` : ""}</span></div>
          </div>
          <div className="source-line"><code>{line.text || " "}</code></div>
          <div className="context-prompt"><span><Sparkle size={13} weight="fill" /> Prompt context</span><p>{line.prompt || "This line predates the scoped recorded turn history."}</p></div>
          {!!line.tool_names?.length && <div className="tool-chips">{line.tool_names.map((tool) => <span key={tool}><TerminalWindow size={12} />{tool}</span>)}</div>}
          {line.turn_key && <button className="primary-button full" onClick={() => onOpenTurn(line.turn_key!)}><ArrowRight size={15} /> Open origin turn</button>}
          <div className="confidence-note"><ShieldCheck size={14} weight="fill" /><p><strong>Checkpoint-derived</strong>{blame?.truth_source}. Associations are labeled separately from file-state facts.</p></div>
        </>
      ) : <div className="inspector-empty"><ListMagnifyingGlass size={22} /><p>Choose a line to see its originating turn and prompt.</p></div>}
    </aside>
  );
}

function StatusBar({ workspace, loading }: { workspace: Workspace; loading: boolean }) {
  return (
    <footer className="statusbar">
      <div><span className={cx("status-pulse", loading && "working")} />{loading ? "Reading durable history" : "Viewer ready"}</div>
      <div className="statusbar-center"><ShieldCheck size={12} weight="fill" /> Read-only <span /> <HardDrives size={12} weight="fill" /> No remote requests</div>
      <div><span>Index {workspace.index_state}</span><span className="statusbar-divider" /><span>{shortID(workspace.worktree_id, 9)}</span></div>
    </footer>
  );
}

function InlineError({ error }: { error: Error }) {
  return <div className="inline-error"><WarningCircle size={16} weight="fill" /><span><strong>Could not read this view.</strong> {error.message}</span><button aria-label="Dismiss"><X size={14} /></button></div>;
}

function FatalError({ error, onRetry }: { error: Error | null; onRetry: () => void }) {
  const locked = error instanceof APIError && error.code === "viewer_locked";
  return (
    <main className="fatal-state">
      <div className="fatal-mark"><ShieldCheck size={28} weight="duotone" /></div>
      <span>{locked ? "Viewer locked" : "Viewer unavailable"}</span>
      <h1>{locked ? "Relaunch from Turnal" : "The local history could not be read"}</h1>
      <p>{error?.message || "The viewer did not return workspace data."}</p>
      <button className="primary-button" onClick={onRetry}><ArrowClockwise size={16} /> Try again</button>
      {locked && <code>turnal ui</code>}
    </main>
  );
}

function LoadingScreen() {
  return (
    <div className="loading-screen">
      <motion.div className="loading-glyph" initial={{ scale: 0.88, opacity: 0 }} animate={{ scale: 1, opacity: 1 }} transition={spring}>
        <span className="glyph-a" /><span className="glyph-b" />
      </motion.div>
      <motion.div initial={{ opacity: 0, y: 8 }} animate={{ opacity: 1, y: 0 }} transition={{ ...spring, delay: 0.08 }}>
        <strong>Opening Turnal Prism</strong>
        <span>Verifying the viewer session and reading checkpoints</span>
      </motion.div>
      <div className="loading-track"><motion.span initial={{ scaleX: 0 }} animate={{ scaleX: 1 }} transition={{ duration: 0.9, ease: [0.16, 1, 0.3, 1] }} /></div>
    </div>
  );
}

function EmptySidebar({ query }: { query: string }) {
  return <div className="empty-sidebar"><MagnifyingGlass size={20} /><strong>No sessions match</strong><span>Try a different phrase than “{query}”.</span></div>;
}

function TimelineSkeleton() {
  return <div className="timeline-panel skeleton"><div className="panel-toolbar skeleton-bar" />{[1, 2, 3, 4].map((item) => <div className="skeleton-turn" key={item}><span /><div><i /><b /><em /></div></div>)}</div>;
}

function InspectorSkeleton() {
  return <div className="inspector-panel skeleton"><div className="skeleton-heading" /><div className="skeleton-block" /><div className="skeleton-block tall" /><div className="skeleton-block" /></div>;
}

function CodeSkeleton() {
  return <div className="code-skeleton">{Array.from({ length: 18 }, (_, index) => <span key={index} style={{ width: `${38 + ((index * 17) % 52)}%` }} />)}</div>;
}

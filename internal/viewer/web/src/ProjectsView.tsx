import { useState } from "preact/hooks";
import { api, APIError, canWrite } from "./api";
import { Chrome, Delta, Glyph, Note, Section, Tabs } from "./Chrome";
import { cleanAdapter, cx, displayTime, initials, isRealTime, shortAge } from "./format";
import type { ActivityItem, Project, ViewerIndex } from "./types";

const AGENTS = [
  { id: "auto", label: "Detect automatically", hint: "Configure whichever of Claude Code and Codex is discoverable." },
  { id: "claude", label: "Claude Code only", hint: "Install hooks for Claude Code and leave Codex untouched." },
  { id: "codex", label: "Codex only", hint: "Install hooks for Codex and leave Claude Code untouched." },
  { id: "none", label: "Manual checkpoints only", hint: "No hooks. Record with turnal checkpoint and turnal run." },
];

/** An hour, in ms. Projects touched inside this window are shown as working. */
const WORKING_WINDOW = 60 * 60 * 1000;

export function ProjectsView({
  index,
  activity,
  onOpenProject,
  onReload,
}: {
  index: ViewerIndex;
  activity: ActivityItem[];
  onOpenProject: (project: Project) => void;
  onReload: () => void;
}) {
  const [tab, setTab] = useState("projects");
  const [adding, setAdding] = useState(false);
  const [removing, setRemoving] = useState<Project | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const writable = canWrite();
  const working = index.projects.filter(isRecent);
  const rest = index.projects.filter((project) => !working.includes(project));

  const submitAdd = async (directory: string, agent: string, gitignore: boolean, gitSync: boolean) => {
    setBusy(true);
    setError(null);
    try {
      await api.addProject({ directory, agent, update_gitignore: gitignore, git_sync: gitSync });
      setAdding(false);
      onReload();
    } catch (nextError) {
      setError(nextError instanceof APIError ? nextError.message : String(nextError));
    } finally {
      setBusy(false);
    }
  };

  const submitRemove = async (project: Project) => {
    setBusy(true);
    setError(null);
    try {
      await api.removeProject(project.store_id);
      setRemoving(null);
      onReload();
    } catch (nextError) {
      setError(nextError instanceof APIError ? nextError.message : String(nextError));
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <Chrome
        crumbs={[{ label: "All projects" }]}
        onSearch={() => undefined}
        actions={
          writable ? (
            <button className="primary" onClick={() => setAdding(true)}>
              Add project
            </button>
          ) : undefined
        }
      />
      <Tabs
        tabs={[
          { id: "projects", label: "Projects", count: index.projects.length },
          { id: "activity", label: "Activity", count: activity.length },
        ]}
        active={tab}
        onSelect={setTab}
        meta={
          <>
            <span>{index.projects.length} indexed</span>
            <span>{index.db_path}</span>
          </>
        }
      />

      <div className="page">
        {error && (
          <div className="note" role="alert">
            <span className="badge">!</span>
            <span>{error}</span>
          </div>
        )}

        {tab === "projects" ? (
          <>
            {working.length > 0 && (
              <>
                <Section title="Working" note="agent active in the last hour" />
                <ProjectRows
                  projects={working}
                  writable={writable}
                  onOpen={onOpenProject}
                  onRemove={setRemoving}
                />
              </>
            )}

            <Section
              title={working.length > 0 ? "All projects" : "Projects"}
              note={`${rest.length}`}
            >
              <button className="ghost" onClick={onReload}>
                Refresh
              </button>
            </Section>

            {index.projects.length === 0 ? (
              <div className="empty">
                <Glyph size={34} />
                <strong>No projects recorded yet</strong>
                <p>
                  Turnal records what your agents did, per project. Add a directory to start recording,
                  or run <code>turnal init</code> in a terminal. Nothing is uploaded and no existing
                  repository is modified.
                </p>
                {writable && (
                  <span className="row2">
                    <button className="primary" onClick={() => setAdding(true)}>
                      Add project
                    </button>
                  </span>
                )}
              </div>
            ) : (
              <ProjectRows
                projects={rest}
                writable={writable}
                onOpen={onOpenProject}
                onRemove={setRemoving}
              />
            )}

            {index.projects.some((project) => !project.present) && (
              <Note>
                <b>Registry staleness is shown, not hidden.</b> A store whose worktree root no longer
                exists stays listed and dimmed. Its recorded history is still readable; only the working
                tree is gone.
              </Note>
            )}
          </>
        ) : (
          <>
            <Section title="Activity" note="all projects, newest first" />
            {activity.length === 0 ? (
              <div className="empty">
                <strong>No recorded sessions yet</strong>
                <p>Run an agent in a recorded project and its turns will appear here.</p>
              </div>
            ) : (
              <div className="rows">
                {activity.map((item) => (
                  <a
                    key={`${item.store_id}:${item.session_key}`}
                    href="#"
                    onClick={(event) => {
                      event.preventDefault();
                      const owner = index.projects.find((project) => project.store_id === item.store_id);
                      if (owner) onOpenProject(owner);
                    }}
                  >
                    <span className="avatar">{initials(item.project_name)}</span>
                    <span className="row-main">
                      <strong>{item.title || `Session ${item.session_id}`}</strong>
                      <span>
                        {item.project_name} <i>·</i> {cleanAdapter(item.adapter)} <i>·</i>{" "}
                        {item.turn_count} turn{item.turn_count === 1 ? "" : "s"}
                      </span>
                    </span>
                    <Delta additions={item.additions} deletions={item.deletions} />
                    <span className="when">{displayTime(item.finished_at || item.started_at)}</span>
                  </a>
                ))}
              </div>
            )}
          </>
        )}

        {!writable && (
          <Note>
            <b>This viewer session is read-only.</b> The launch token is single use, so a reloaded tab
            can still read history but cannot add or remove projects. Relaunch with{" "}
            <code>turnal ui</code> to manage projects.
          </Note>
        )}
      </div>

      {adding && (
        <AddProjectDialog busy={busy} onCancel={() => setAdding(false)} onSubmit={submitAdd} />
      )}
      {removing && (
        <RemoveProjectDialog
          project={removing}
          busy={busy}
          onCancel={() => setRemoving(null)}
          onConfirm={() => submitRemove(removing)}
        />
      )}
    </>
  );
}

function ProjectRows({
  projects,
  writable,
  onOpen,
  onRemove,
}: {
  projects: Project[];
  writable: boolean;
  onOpen: (project: Project) => void;
  onRemove: (project: Project) => void;
}) {
  return (
    <div className="rows">
      {projects.map((project) => (
        <a
          key={project.store_id}
          href="#"
          className={cx(!project.present && "gone")}
          onClick={(event) => {
            event.preventDefault();
            if (project.present) onOpen(project);
          }}
        >
          <span className={cx("status", isRecent(project) && "active")}>
            {project.present ? "●" : "○"}
          </span>
          <span className="row-main">
            <strong>{project.name}</strong>
            <span>
              {project.branch && <span className="tag mono">{project.branch}</span>}
              {project.branch && <i>·</i>}
              <em>{project.last_prompt || project.root}</em>
            </span>
          </span>
          <span className={cx("state", healthClass(project))}>{healthLabel(project)}</span>
          <span className="tag count-col">
            {project.session_count} session{project.session_count === 1 ? "" : "s"}
          </span>
          <span className="tag count-col">
            {project.turn_count} turn{project.turn_count === 1 ? "" : "s"}
          </span>
          <Delta additions={project.additions} deletions={project.deletions} />
          <span className="when">{shortAge(project.last_activity)}</span>
          {writable && (
            <button
              className="ghost"
              title="Remove from Turnal"
              onClick={(event) => {
                event.preventDefault();
                event.stopPropagation();
                onRemove(project);
              }}
            >
              Remove
            </button>
          )}
        </a>
      ))}
    </div>
  );
}

/** A project counts as working when an agent touched it inside the window. A
 * project with no recorded activity has a zero timestamp, which must not read as
 * ancient. */
function isRecent(project: Project) {
  if (!isRealTime(project.last_activity)) return false;
  return Date.now() - new Date(project.last_activity!).getTime() < WORKING_WINDOW;
}

function healthClass(project: Project) {
  if (!project.present) return "missing";
  if (project.index_state === "stale") return "stale";
  if (project.index_state === "missing" || project.history_state === "attention") return "missing";
  return "healthy";
}

function healthLabel(project: Project) {
  if (!project.present) return "worktree gone";
  if (project.index_state === "stale") return "stale index";
  if (project.index_state === "missing") return "index missing";
  if (project.history_state === "attention") return "needs attention";
  return "healthy";
}

function AddProjectDialog({
  busy,
  onCancel,
  onSubmit,
}: {
  busy: boolean;
  onCancel: () => void;
  onSubmit: (directory: string, agent: string, gitignore: boolean, gitSync: boolean) => void;
}) {
  const [directory, setDirectory] = useState("");
  const [agent, setAgent] = useState("auto");
  const [gitignore, setGitignore] = useState(true);
  const [gitSync, setGitSync] = useState(false);

  return (
    <div className="scrim" role="dialog" aria-modal="true" aria-label="Add project">
      <div className="dialog">
        <div className="dialog-head">
          <strong>Add project</strong>
          <span>
            Point Turnal at a directory to record. This runs the same steps as <code>turnal init</code>{" "}
            in that directory: it is the one flow in the viewer that writes to disk.
          </span>
        </div>
        <div className="dialog-body">
          <label className="field">
            <span>Directory</span>
            <span className="input">
              <input
                value={directory}
                placeholder="/home/you/projects/example"
                onInput={(event) => setDirectory(event.currentTarget.value)}
                autoFocus
              />
            </span>
          </label>

          <div className="field">
            <span>Agent capture</span>
            <div className="radios">
              {AGENTS.map((option) => (
                <label key={option.id} className={cx("radio", agent === option.id && "on")}>
                  <input
                    type="radio"
                    name="agent"
                    checked={agent === option.id}
                    onChange={() => setAgent(option.id)}
                  />
                  <span className="mk" aria-hidden="true" />
                  <span className="body">
                    <strong>{option.label}</strong>
                    <span>{option.hint}</span>
                  </span>
                </label>
              ))}
            </div>
          </div>

          <div className="field">
            <span>Options</span>
            <div className="checks">
              <label className={cx("check", gitignore && "on")}>
                <input
                  type="checkbox"
                  checked={gitignore}
                  onChange={(event) => setGitignore(event.currentTarget.checked)}
                />
                <span className="mk" aria-hidden="true">
                  ✓
                </span>
                <span className="body">
                  <strong>Add .turnal/ to .gitignore</strong>
                  <span>Keeps the store out of your existing repository history.</span>
                </span>
              </label>
              <label className={cx("check", gitSync && "on")}>
                <input
                  type="checkbox"
                  checked={gitSync}
                  onChange={(event) => setGitSync(event.currentTarget.checked)}
                />
                <span className="mk" aria-hidden="true">
                  ✓
                </span>
                <span className="body">
                  <strong>Enable workspace-Git rollback</strong>
                  <span>
                    Lets rollback restore a previously captured HEAD and index. Off by default: it is the
                    one mode that can modify your existing <code>.git/</code>.
                  </span>
                </span>
              </label>
            </div>
          </div>

          <div className="willdo">
            <span className="t">What this will do</span>
            <span className="ln2">
              <i>+</i> create {directory || "<directory>"}/.turnal/
            </span>
            {gitignore && (
              <span className="ln2">
                <i>+</i> append .turnal/ to .gitignore
              </span>
            )}
            {agent !== "none" && (
              <span className="ln2">
                <i>+</i> install hooks for the {agent === "auto" ? "detected" : agent} agent
              </span>
            )}
            <span className="ln2">
              <i>+</i> register the store in the project registry
            </span>
            <span className="ln2 no">
              <i>·</i> your existing .git/ is not modified
            </span>
          </div>
        </div>
        <div className="dialog-foot">
          <span className="hint">turnal init --agent {agent}</span>
          <span className="sp" />
          <button className="ghost" onClick={onCancel} disabled={busy}>
            Cancel
          </button>
          <button
            className="primary"
            disabled={busy || directory.trim() === ""}
            onClick={() => onSubmit(directory.trim(), agent, gitignore, gitSync)}
          >
            {busy ? "Adding…" : "Add project"}
          </button>
        </div>
      </div>
    </div>
  );
}

function RemoveProjectDialog({
  project,
  busy,
  onCancel,
  onConfirm,
}: {
  project: Project;
  busy: boolean;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  return (
    <div className="scrim" role="dialog" aria-modal="true" aria-label="Remove project">
      <div className="dialog">
        <div className="dialog-head">
          <strong>Remove {project.name} from Turnal</strong>
          <span>
            This deregisters the project so it leaves this list. Nothing is deleted.
          </span>
        </div>
        <div className="dialog-body">
          <div className="willdo">
            <span className="t">What this will do</span>
            <span className="ln2">
              <i>+</i> remove the registry entry for this store
            </span>
            <span className="ln2 no">
              <i>·</i> {project.store_path} is left on disk
            </span>
            <span className="ln2 no">
              <i>·</i> recorded history is kept and stays readable
            </span>
            <span className="ln2 no">
              <i>·</i> agent hooks stay installed
            </span>
          </div>
          <div className="note">
            <span className="badge">i</span>
            <span>
              Re-add this directory later, or run <code>turnal init</code> in it, and the existing
              history comes back. Use <code>turnal destroy</code> to actually delete recorded history.
            </span>
          </div>
        </div>
        <div className="dialog-foot">
          <span className="hint">registry entry only</span>
          <span className="sp" />
          <button className="ghost" onClick={onCancel} disabled={busy}>
            Cancel
          </button>
          <button className="primary" onClick={onConfirm} disabled={busy}>
            {busy ? "Removing…" : "Remove project"}
          </button>
        </div>
      </div>
    </div>
  );
}

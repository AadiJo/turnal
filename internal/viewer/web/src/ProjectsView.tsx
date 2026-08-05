import { useEffect, useRef, useState } from "preact/hooks";
import { api, APIError, canWrite } from "./api";
import { Chrome, Delta, Note, Section, Tabs } from "./Chrome";
import {
  cleanAdapter,
  cx,
  displayTime,
  initials,
  isRealTime,
  shortAge,
} from "./format";
import type {
  ActivityItem,
  AddProjectRequest,
  Project,
  ViewerIndex,
} from "./types";

const AGENTS = [
  {
    id: "auto",
    label: "Detect automatically",
    hint: "Configure whichever of Claude Code and Codex is discoverable.",
  },
  {
    id: "claude",
    label: "Claude Code only",
    hint: "Install hooks for Claude Code and leave Codex untouched.",
  },
  {
    id: "codex",
    label: "Codex only",
    hint: "Install hooks for Codex and leave Claude Code untouched.",
  },
  {
    id: "none",
    label: "Manual saves only",
    hint: "No hooks. Record agent runs with turnal run, or save folder snapshots with turnal save.",
  },
];

/** An hour, in ms. Projects touched inside this window are shown as working. */
const WORKING_WINDOW = 60 * 60 * 1000;

export function ProjectsView({
  index,
  activity,
  activityTruncated,
  activityError,
  onOpenProject,
  onReload,
}: {
  index: ViewerIndex;
  activity: ActivityItem[];
  activityTruncated: boolean;
  activityError: string | null;
  onOpenProject: (project: Project, sessionKey?: string) => void;
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

  const submitAdd = async (request: AddProjectRequest) => {
    setBusy(true);
    setError(null);
    try {
      const result = await api.addProject(request);
      setAdding(false);
      if (result.warning) setError(result.warning);
      onReload();
    } catch (nextError) {
      setError(
        nextError instanceof APIError ? nextError.message : String(nextError),
      );
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
      setError(
        nextError instanceof APIError ? nextError.message : String(nextError),
      );
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <Chrome
        crumbs={[{ label: "All projects" }]}
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
                <strong>No projects recorded yet</strong>
                <p>
                  Turnal records what your agents did, per project. Add a
                  directory to start recording, or run <code>turnal init</code>{" "}
                  in a terminal. Nothing is uploaded. Adding a project creates
                  Turnal files and may update agent settings; your existing Git
                  history is not changed.
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
                <b>Some project folders could not be found.</b> They stay listed
                and dimmed because their recorded history may still be
                available.
              </Note>
            )}
          </>
        ) : (
          <>
            <Section title="Activity" note="newest first" />
            {activityError ? (
              <div className="note" role="alert">
                <span className="badge">!</span>
                <span>{activityError}</span>
              </div>
            ) : activity.length === 0 ? (
              <div className="empty">
                <strong>No recorded sessions yet</strong>
                <p>
                  Run an agent in a recorded project and its turns will appear
                  here.
                </p>
              </div>
            ) : (
              <div className="rows">
                {activity.map((item) => {
                  const owner = index.projects.find(
                    (project) => project.store_id === item.store_id,
                  );
                  const content = (
                    <>
                      <span className="avatar">
                        {initials(item.project_name)}
                      </span>
                      <span className="row-main">
                        <strong>
                          {item.title || `Session ${item.session_id}`}
                        </strong>
                        <span>
                          {item.project_name} <i>·</i>{" "}
                          {cleanAdapter(item.adapter)} <i>·</i>{" "}
                          {item.turn_count} turn
                          {item.turn_count === 1 ? "" : "s"}
                          {owner && !owner.present && (
                            <>
                              <i>·</i> folder not found
                            </>
                          )}
                        </span>
                      </span>
                      <Delta
                        additions={item.additions}
                        deletions={item.deletions}
                      />
                      <span className="when">
                        {displayTime(item.finished_at || item.started_at)}
                      </span>
                    </>
                  );
                  return owner?.present ? (
                    <a
                      key={`${item.store_id}:${item.session_key}`}
                      href="#"
                      onClick={(event) => {
                        event.preventDefault();
                        onOpenProject(owner, item.session_key);
                      }}
                    >
                      {content}
                    </a>
                  ) : (
                    <div
                      className="activity-row gone"
                      key={`${item.store_id}:${item.session_key}`}
                      aria-disabled="true"
                    >
                      {content}
                    </div>
                  );
                })}
              </div>
            )}
            {activityTruncated && (
              <Note>
                <b>Showing the newest {activity.length} sessions.</b> Older
                activity is not shown.
              </Note>
            )}
          </>
        )}

        {!writable && (
          <Note>
            <b>This viewer session is read-only.</b> The launch token is single
            use, so a reloaded tab can still read history but cannot add or
            remove projects. Relaunch with <code>turnal ui</code> to manage
            projects.
          </Note>
        )}
      </div>

      {adding && (
        <AddProjectDialog
          busy={busy}
          onCancel={() => setAdding(false)}
          onSubmit={submitAdd}
        />
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
        <div className="project-row" key={project.store_id}>
          {project.present ? (
            <a
              href="#"
              className="project-link"
              onClick={(event) => {
                event.preventDefault();
                onOpen(project);
              }}
            >
              <ProjectRowContent project={project} />
            </a>
          ) : (
            <div className="project-link gone" aria-disabled="true">
              <ProjectRowContent project={project} />
            </div>
          )}
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
        </div>
      ))}
    </div>
  );
}

function ProjectRowContent({ project }: { project: Project }) {
  const health = projectHealth(project);
  return (
    <>
      <span className={cx("status", isRecent(project) && "active")}>
        {project.present ? "●" : "○"}
      </span>
      <span className="row-main">
        <strong>{project.name}</strong>
        <span>
          {project.branch && <span className="tag mono">{project.branch}</span>}
          {project.branch && <i>·</i>}
          <em>{project.last_prompt || "No recorded activity"}</em>
        </span>
      </span>
      <span
        className={cx("state", health.className, health.tooltip && "has-tooltip")}
        title={health.tooltip}
        aria-label={
          health.tooltip ? `${health.label}. ${health.tooltip}` : undefined
        }
      >
        {health.label}
        {health.tooltip && (
          <span className="state-tooltip" aria-hidden="true">
            Run <code>{health.command}</code> to rebuild the disposable cache used
            by search and indexed history commands.
          </span>
        )}
      </span>
      <span className="tag count-col">
        {project.session_count} session{project.session_count === 1 ? "" : "s"}
      </span>
      <span className="tag count-col">
        {project.turn_count} turn{project.turn_count === 1 ? "" : "s"}
      </span>
      <Delta additions={project.additions} deletions={project.deletions} />
      <span className="when">{shortAge(project.last_activity)}</span>
    </>
  );
}

/** A project counts as working when an agent touched it inside the window. A
 * project with no recorded activity has a zero timestamp, which must not read as
 * ancient. */
function isRecent(project: Project) {
  if (!isRealTime(project.last_activity)) return false;
  return (
    Date.now() - new Date(project.last_activity!).getTime() < WORKING_WINDOW
  );
}

function projectHealth(project: Project): {
  className: string;
  label: string;
  tooltip?: string;
  command?: string;
} {
  if (!project.present) {
    return { className: "missing", label: "folder not found" } as const;
  }
  if (project.index_state === "stale") {
    return { className: "stale", label: "search index may be stale" } as const;
  }
  if (project.index_state === "missing") {
    return {
      className: "missing",
      label: "search index missing",
      tooltip:
        "Run turnal reindex to rebuild the disposable cache used by search and indexed history commands.",
      command: "turnal reindex",
    } as const;
  }
  if (project.index_state === "unavailable") {
    return { className: "missing", label: "search index unavailable" } as const;
  }
  if (project.history_state === "attention") {
    return { className: "missing", label: "needs attention" } as const;
  }
  return { className: "healthy", label: "healthy" } as const;
}

function AddProjectDialog({
  busy,
  onCancel,
  onSubmit,
}: {
  busy: boolean;
  onCancel: () => void;
  onSubmit: (request: AddProjectRequest) => void;
}) {
  const [directory, setDirectory] = useState("");
  const [agent, setAgent] = useState("auto");
  const [gitignore, setGitignore] = useState(true);
  const [gitSync, setGitSync] = useState(false);
  const [picking, setPicking] = useState(false);
  const [pickError, setPickError] = useState<string | null>(null);
  const dialogRef = useDialogFocus(onCancel, busy);

  // The native dialog runs on the host. If the machine has none, say so and
  // leave the text field usable rather than dead-ending.
  const choose = async () => {
    setPicking(true);
    setPickError(null);
    try {
      const result = await api.pickDirectory();
      if (!result.cancelled && result.directory) setDirectory(result.directory);
    } catch (error) {
      setPickError(
        error instanceof APIError && error.code === "picker_unavailable"
          ? `${error.message}`
          : "Could not open a folder chooser. Type the path instead.",
      );
    } finally {
      setPicking(false);
    }
  };

  return (
    <div className="scrim">
      <div
        className="dialog"
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-label="Add project"
        tabIndex={-1}
      >
        <div className="dialog-head">
          <strong>Add project</strong>
          <span>
            Choose a project folder to start recording agent activity.
          </span>
        </div>
        <div className="dialog-body">
          <div className="field">
            <label htmlFor="project-directory">Project folder</label>
            <span className="input">
              <input
                id="project-directory"
                value={directory}
                placeholder="Choose a folder, or type a path"
                onInput={(event) => setDirectory(event.currentTarget.value)}
                autoFocus
              />
              <button
                className="browse"
                onClick={choose}
                disabled={picking || busy}
              >
                {picking ? "Choosing…" : "Browse…"}
              </button>
            </span>
            {pickError && <span className="field-note">{pickError}</span>}
          </div>

          <div className="field">
            <span>Agent capture</span>
            <div className="radios">
              {AGENTS.map((option) => (
                <label
                  key={option.id}
                  className={cx("radio", agent === option.id && "on")}
                >
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
                  onChange={(event) =>
                    setGitignore(event.currentTarget.checked)
                  }
                />
                <span className="mk" aria-hidden="true">
                  ✓
                </span>
                <span className="body">
                  <strong>Keep Turnal files out of Git</strong>
                  <span>
                    Prevents Turnal's recording files from appearing in project changes.
                  </span>
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
                  <strong>Enable full Git rollback</strong>
                  <span>
                    Lets rollback restore the captured branch and staged files.
                    Off by default because it can change your existing Git
                    history and staging area.
                  </span>
                </span>
              </label>
            </div>
          </div>

          <div className="willdo">
            <span className="t">What this will do</span>
            <span className="ln2">
              <i>+</i> prepare this folder for recording
            </span>
            {gitignore && (
              <span className="ln2">
                <i>+</i> keep Turnal files out of Git
              </span>
            )}
            {agent !== "none" && (
              <span className="ln2">
                <i>+</i> install hooks for the{" "}
                {agent === "auto" ? "detected" : agent} agent
              </span>
            )}
            <span className="ln2">
              <i>+</i> add this project to the viewer
            </span>
            <span className="ln2 no">
              <i>·</i> your existing Git history is not modified
            </span>
            <span className="t">Equivalent CLI, run in the selected folder</span>
            <code className="cli-command">
              {initCommand({
                directory: directory.trim(),
                agent,
                update_gitignore: gitignore,
                git_sync: gitSync,
              })}
            </code>
          </div>
        </div>
        <div className="dialog-foot">
          <span className="hint">
            The project folder will stay on your computer.
          </span>
          <span className="sp" />
          <button className="ghost" onClick={onCancel} disabled={busy}>
            Cancel
          </button>
          <button
            className="primary"
            disabled={busy || directory.trim() === ""}
            onClick={() =>
              onSubmit({
                directory: directory.trim(),
                agent,
                update_gitignore: gitignore,
                git_sync: gitSync,
              })
            }
          >
            {busy ? "Adding…" : "Add project"}
          </button>
        </div>
      </div>
    </div>
  );
}

function initCommand(request: AddProjectRequest) {
  const args = ["turnal", "init"];
  if (request.agent && request.agent !== "auto") {
    args.push("--agent", request.agent);
  }
  if (!request.update_gitignore) args.push("--update-gitignore=false");
  if (request.git_sync) args.push("--git-sync");
  return args.join(" ");
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
  const dialogRef = useDialogFocus(onCancel, busy);
  return (
    <div className="scrim">
      <div
        className="dialog"
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-label="Remove project"
        tabIndex={-1}
      >
        <div className="dialog-head">
          <strong>Remove {project.name} from Turnal</strong>
          <span>
            This removes the project from this viewer. The project folder and
            recorded history will not be deleted.
          </span>
        </div>
        <div className="dialog-body">
          <div className="willdo">
            <span className="t">What this will do</span>
            <span className="ln2">
              <i>+</i> remove this project from the viewer
            </span>
            <span className="ln2 no">
              <i>·</i> leave the project folder unchanged
            </span>
            <span className="ln2 no">
              <i>·</i> recorded history is kept and stays readable
            </span>
            <span className="ln2 no">
              <i>·</i> agent recording setup is left unchanged
            </span>
          </div>
          <div className="note">
            <span className="badge">i</span>
            <span>
              To see this history here again, add the project back to the
              viewer.
            </span>
          </div>
        </div>
        <div className="dialog-foot">
          <span className="hint">folder will not be deleted</span>
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

/** Keep keyboard focus inside a modal and restore it to the control that
 * opened the dialog when the modal closes. */
function useDialogFocus(onCancel: () => void, busy: boolean) {
  const dialogRef = useRef<HTMLDivElement>(null);
  const cancelRef = useRef(onCancel);
  const busyRef = useRef(busy);
  cancelRef.current = onCancel;
  busyRef.current = busy;

  useEffect(() => {
    const dialog = dialogRef.current;
    if (!dialog) return;
    const previous =
      document.activeElement instanceof HTMLElement
        ? document.activeElement
        : null;
    const focusable = () =>
      Array.from(
        dialog.querySelectorAll<HTMLElement>(
          "input:not(:disabled), button:not(:disabled), [href], [tabindex]:not([tabindex='-1'])",
        ),
      );
    (
      dialog.querySelector<HTMLElement>("[autofocus]") ??
      focusable()[0] ??
      dialog
    ).focus();

    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape" && !busyRef.current) {
        event.preventDefault();
        cancelRef.current();
        return;
      }
      if (event.key !== "Tab") return;
      const controls = focusable();
      if (controls.length === 0) {
        event.preventDefault();
        dialog.focus();
        return;
      }
      const first = controls[0];
      const last = controls[controls.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("keydown", onKeyDown);
      if (previous?.isConnected) previous.focus();
    };
  }, []);

  return dialogRef;
}

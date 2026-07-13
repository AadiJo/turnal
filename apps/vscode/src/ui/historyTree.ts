import * as vscode from "vscode";
import { TurnFileTarget, TurnTarget } from "../model";
import { TurnalCommandError } from "../turnal/cli";
import { DiffDocument, SessionSummary, SessionsResult, SessionTurn, turnsForSession } from "../turnal/types";
import { displayAgent, formatTimestamp, relativeTime, truncate, turnTitle } from "../utils/format";
import { recentTurns } from "../utils/recentTurns";
import { RefreshingCache } from "../utils/refreshingCache";
import { cliForFolder } from "../workspaces";

export type HistoryNode = WorkspaceNode | SessionNode | TurnNode | TurnFileNode;
export type HistoryLayout = "sessions" | "activity";

interface HistoryTreeOptions {
  extensionUri: vscode.Uri;
  getLayout(): HistoryLayout;
  onBackgroundError(error: unknown, folder: vscode.WorkspaceFolder): void;
  onCliAvailability(missing: boolean): void;
}

export class HistoryTreeProvider implements vscode.TreeDataProvider<HistoryNode>, vscode.Disposable {
  private readonly emitter = new vscode.EventEmitter<HistoryNode | undefined | null | void>();
  private readonly sessions = new RefreshingCache<string, SessionsResult>();
  private readonly diffCache = new Map<string, DiffDocument[]>();
  private readonly diffInFlight = new Map<string, Promise<DiffDocument[]>>();

  readonly onDidChangeTreeData = this.emitter.event;

  constructor(private readonly options: HistoryTreeOptions) {}

  getTreeItem(element: HistoryNode): vscode.TreeItem {
    return element;
  }

  async getChildren(element?: HistoryNode): Promise<HistoryNode[]> {
    if (element instanceof WorkspaceNode) {
      return this.folderNodes(element.folder);
    }
    if (element instanceof SessionNode) {
      return turnsForSession(element.session).map(
        (turn) => new TurnNode(element.folder, element.session, turn),
      );
    }
    if (element instanceof TurnNode) {
      return element.target.status === "complete" ? this.turnFileNodes(element) : [];
    }
    if (element instanceof TurnFileNode) {
      return [];
    }

    const folders = vscode.workspace.workspaceFolders ?? [];
    if (folders.length > 1) {
      return folders.map((folder) => new WorkspaceNode(folder));
    }
    return folders.length === 1 ? this.folderNodes(folders[0]) : [];
  }

  refresh(): void {
    this.sessions.invalidate();
    this.diffCache.clear();
    this.emitter.fire();
  }

  relayout(): void {
    this.emitter.fire();
  }

  async pickTurn(completeOnly: boolean, placeHolder: string): Promise<TurnTarget | undefined> {
    const folders = vscode.workspace.workspaceFolders ?? [];
    const loaded = await Promise.all(
      folders.map(async (folder) => {
        try {
          return { folder, sessions: await this.load(folder) };
        } catch {
          return undefined;
        }
      }),
    );
    const groups = loaded.filter((group): group is NonNullable<typeof group> => group !== undefined);
    const picks: Array<vscode.QuickPickItem & { target: TurnTarget }> = [];
    for (const { folder, sessions } of groups) {
      for (const session of sessions.sessions) {
        for (const turn of turnsForSession(session)) {
          if (completeOnly && turn.status !== "complete") {
            continue;
          }
          const target = turnTarget(folder, session, turn);
          picks.push({
            label: target.title,
            description: `#${target.turnId} · ${displayAgent(target.adapter)} · ${relativeTime(target.time)}`,
            detail: folders.length > 1 ? `${folder.name} · ${session.session_id}` : session.session_id,
            target,
          });
        }
      }
    }
    return (await vscode.window.showQuickPick(picks, { placeHolder, matchOnDescription: true, matchOnDetail: true }))?.target;
  }

  dispose(): void {
    this.emitter.dispose();
  }

  private async sessionNodes(folder: vscode.WorkspaceFolder): Promise<SessionNode[]> {
    try {
      const result = await this.load(folder);
      return result.sessions.map((session) => new SessionNode(folder, session, this.options.extensionUri));
    } catch {
      return [];
    }
  }

  private folderNodes(folder: vscode.WorkspaceFolder): Promise<SessionNode[] | TurnNode[]> {
    return this.options.getLayout() === "activity" ? this.activityNodes(folder) : this.sessionNodes(folder);
  }

  private async activityNodes(folder: vscode.WorkspaceFolder): Promise<TurnNode[]> {
    try {
      const result = await this.load(folder);
      return recentTurns(result.sessions).map(
        ({ session, turn }) => new TurnNode(folder, session, turn, this.options.extensionUri, true),
      );
    } catch {
      return [];
    }
  }

  private load(folder: vscode.WorkspaceFolder): Promise<SessionsResult> {
    const key = folder.uri.toString();
    return this.sessions.load(
      key,
      () => cliForFolder(folder).sessions(),
      {
        retryDelaysMs: [150, 450],
        shouldRetry: (error) => !isPermanentSessionLoadError(error),
        onSuccess: () => this.options.onCliAvailability(false),
        onError: (error) => {
          if (error instanceof TurnalCommandError && error.missingExecutable) {
            this.options.onCliAvailability(true);
          }
          this.options.onBackgroundError(error, folder);
        },
      },
    );
  }

  private async turnFileNodes(turn: TurnNode): Promise<TurnFileNode[]> {
    const key = `${turn.target.folderUri}\0${turn.target.sessionId}\0${turn.target.turnId}`;
    try {
      const cached = this.diffCache.get(key);
      if (cached) {
        return cached.map((file) => new TurnFileNode(turn.folder, turn.target, file));
      }
      let request = this.diffInFlight.get(key);
      if (!request) {
        request = cliForFolder(turn.folder)
          .diffDocuments(turn.target.sessionId, turn.target.turnId)
          .then((result) => {
            this.diffCache.set(key, result.files);
            return result.files;
          })
          .finally(() => this.diffInFlight.delete(key));
        this.diffInFlight.set(key, request);
      }
      return (await request).map((file) => new TurnFileNode(turn.folder, turn.target, file));
    } catch (error) {
      this.options.onBackgroundError(error, turn.folder);
      return [];
    }
  }
}

class WorkspaceNode extends vscode.TreeItem {
  constructor(readonly folder: vscode.WorkspaceFolder) {
    super(folder.name, vscode.TreeItemCollapsibleState.Expanded);
    this.id = `workspace:${folder.uri.toString()}`;
    this.description = "workspace";
    this.iconPath = new vscode.ThemeIcon("root-folder");
    this.contextValue = "turnalWorkspace";
  }
}

class SessionNode extends vscode.TreeItem {
  constructor(
    readonly folder: vscode.WorkspaceFolder,
    readonly session: SessionSummary,
    extensionUri: vscode.Uri,
  ) {
    super(session.model ? `${displayAgent(session.adapter)} · ${session.model}` : displayAgent(session.adapter), vscode.TreeItemCollapsibleState.Expanded);
    this.id = `session:${folder.uri.toString()}:${session.session_id}`;
    this.description = `${session.turn_count} ${session.turn_count === 1 ? "turn" : "turns"} · ${relativeTime(session.last_activity)}`;
    this.iconPath = providerIcon(session.adapter, extensionUri);
    this.contextValue = "turnalSession";
    this.tooltip = sessionTooltip(session);
  }
}

function providerIcon(adapter: string | undefined, extensionUri: vscode.Uri): vscode.TreeItem["iconPath"] {
  const normalized = adapter?.trim().toLowerCase() ?? "";
  if (normalized.includes("claude")) {
    return vscode.Uri.joinPath(extensionUri, "media", "providers", "claude.svg");
  }
  if (normalized.includes("codex") || normalized.includes("openai")) {
    return {
      light: vscode.Uri.joinPath(extensionUri, "media", "providers", "openai-light.svg"),
      dark: vscode.Uri.joinPath(extensionUri, "media", "providers", "openai-dark.svg"),
    };
  }
  return new vscode.ThemeIcon("robot");
}

function isPermanentSessionLoadError(error: unknown): boolean {
  if (error instanceof TurnalCommandError && error.missingExecutable) {
    return true;
  }
  const message = error instanceof Error ? error.message : String(error);
  return /turnal root|not initialized|not a turnal workspace/i.test(message);
}

export class TurnNode extends vscode.TreeItem {
  readonly target: TurnTarget;

  constructor(
    readonly folder: vscode.WorkspaceFolder,
    session: SessionSummary,
    turn: SessionTurn,
    extensionUri?: vscode.Uri,
    activityLayout = false,
  ) {
    const title = turnTitle(turn.prompt, turn.turn_id);
    super(
      title,
      turn.status === "complete" ? vscode.TreeItemCollapsibleState.Collapsed : vscode.TreeItemCollapsibleState.None,
    );
    this.target = turnTarget(folder, session, turn);
    this.id = `turn:${folder.uri.toString()}:${session.session_id}:${turn.turn_id}`;
    const activity = turn.last_activity ?? turn.first_activity ?? session.last_activity ?? session.first_activity;
    const activityStatus = turn.status === "complete" ? "" : `${turn.status} · `;
    this.description = activityLayout
      ? `${displayAgent(session.adapter)} · ${activityStatus}#${turn.turn_id} · ${relativeTime(activity)}`
      : `#${turn.turn_id} · ${relativeTime(activity)}`;
    this.iconPath = activityLayout && extensionUri
      ? providerIcon(session.adapter, extensionUri)
      : turnIcon(turn.status);
    this.contextValue = turn.status === "complete" ? "turnalTurnComplete" : "turnalTurn";
    this.tooltip = turnTooltip(session, turn);
    this.command = {
      command: turn.status === "complete" ? "turnal.openTurnDiff" : "turnal.showTurnDetails",
      title: turn.status === "complete" ? "View Turn Changes" : "Show Turn Details",
      arguments: [this.target],
    };
  }
}

export class TurnFileNode extends vscode.TreeItem {
  readonly target: TurnFileTarget;

  constructor(
    folder: vscode.WorkspaceFolder,
    turn: TurnTarget,
    readonly file: DiffDocument,
  ) {
    super(file.path, vscode.TreeItemCollapsibleState.None);
    this.target = { target: turn, path: file.path, oldPath: file.old_path };
    this.id = `file:${turn.folderUri}:${turn.sessionId}:${turn.turnId}:${file.path}`;
    this.description = fileDescription(file);
    this.resourceUri = vscode.Uri.joinPath(folder.uri, ...file.path.split("/"));
    this.iconPath = fileIcon(file.status);
    this.contextValue = "turnalTurnFile";
    this.tooltip = fileTooltip(file);
    this.command = {
      command: "turnal.openTurnDiff",
      title: "Open File Changes",
      arguments: [this.target],
    };
  }
}

function turnTarget(folder: vscode.WorkspaceFolder, session: SessionSummary, turn: SessionTurn): TurnTarget {
  return {
    folderUri: folder.uri.toString(),
    folderName: folder.name,
    sessionId: session.session_id,
    turnId: turn.turn_id,
    title: turnTitle(turn.prompt, turn.turn_id),
    status: turn.status,
    adapter: session.adapter,
    time: turn.last_activity ?? turn.first_activity,
  };
}

function turnIcon(status: string): vscode.ThemeIcon {
  switch (status) {
    case "complete":
      return new vscode.ThemeIcon("git-commit");
    case "active":
      return new vscode.ThemeIcon("sync~spin");
    case "events-only":
      return new vscode.ThemeIcon("comment-discussion");
    default:
      return new vscode.ThemeIcon("warning");
  }
}

function fileIcon(status: string): vscode.ThemeIcon {
  switch (status) {
    case "A":
      return new vscode.ThemeIcon("diff-added", new vscode.ThemeColor("gitDecoration.addedResourceForeground"));
    case "D":
      return new vscode.ThemeIcon("diff-removed", new vscode.ThemeColor("gitDecoration.deletedResourceForeground"));
    case "R":
      return new vscode.ThemeIcon("diff-renamed", new vscode.ThemeColor("gitDecoration.renamedResourceForeground"));
    default:
      return new vscode.ThemeIcon("diff-modified", new vscode.ThemeColor("gitDecoration.modifiedResourceForeground"));
  }
}

function fileDescription(file: DiffDocument): string {
  const status = file.status === "A" ? "added" : file.status === "D" ? "deleted" : file.status === "R" ? "renamed" : "modified";
  if (file.binary || file.truncated || (file.additions === 0 && file.deletions === 0)) {
    return status;
  }
  return `${status} · +${file.additions} −${file.deletions}`;
}

function fileTooltip(file: DiffDocument): vscode.MarkdownString {
  const markdown = new vscode.MarkdownString();
  markdown.appendMarkdown(`**${file.path}**\n\n`);
  if (file.old_path) {
    markdown.appendText(`Renamed from ${file.old_path}`);
    markdown.appendMarkdown("\n\n");
  }
  if (file.binary) {
    markdown.appendText("Binary file change");
  } else if (file.truncated) {
    markdown.appendText("File exceeds the 4 MiB editor preview limit");
  } else {
    markdown.appendMarkdown(`$(add) ${file.additions} additions · $(remove) ${file.deletions} deletions`);
  }
  return markdown;
}

function sessionTooltip(session: SessionSummary): vscode.MarkdownString {
  const markdown = new vscode.MarkdownString();
  markdown.appendMarkdown(`**${displayAgent(session.adapter)} session**\n\n`);
  markdown.appendMarkdown("Session: ");
  markdown.appendText(session.session_id);
  markdown.appendMarkdown(`\n\nStatus: ${session.status}\n\n`);
  if (session.model) {
    markdown.appendMarkdown("Model: ");
    markdown.appendText(session.model);
    markdown.appendMarkdown("\n\n");
  }
  markdown.appendMarkdown(`${session.turn_count} turns · ${session.event_count} events\n\n`);
  markdown.appendText(formatTimestamp(session.last_activity));
  return markdown;
}

function turnTooltip(session: SessionSummary, turn: SessionTurn): vscode.MarkdownString {
  const markdown = new vscode.MarkdownString();
  markdown.appendMarkdown(`**Turn ${turn.turn_id} · ${turn.status}**\n\n`);
  if (turn.prompt) {
    markdown.appendText(truncate(turn.prompt, 400));
    markdown.appendMarkdown("\n\n");
  }
  markdown.appendMarkdown(`${displayAgent(session.adapter)} · ${formatTimestamp(turn.last_activity ?? turn.first_activity)}\n\n`);
  if (turn.tool_names?.length) {
    markdown.appendMarkdown("Tools: ");
    markdown.appendText(turn.tool_names.join(", "));
  }
  return markdown;
}

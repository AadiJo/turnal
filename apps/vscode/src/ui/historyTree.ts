import * as vscode from "vscode";
import { TurnTarget } from "../model";
import { TurnalCommandError } from "../turnal/cli";
import { SessionSummary, SessionsResult, SessionTurn, turnsForSession } from "../turnal/types";
import { displayAgent, formatTimestamp, relativeTime, truncate, turnTitle } from "../utils/format";
import { cliForFolder } from "../workspaces";

export type HistoryNode = WorkspaceNode | SessionNode | TurnNode;

interface HistoryTreeOptions {
  onBackgroundError(error: unknown, folder: vscode.WorkspaceFolder): void;
  onCliAvailability(missing: boolean): void;
}

export class HistoryTreeProvider implements vscode.TreeDataProvider<HistoryNode>, vscode.Disposable {
  private readonly emitter = new vscode.EventEmitter<HistoryNode | undefined | null | void>();
  private readonly cache = new Map<string, SessionsResult>();
  private readonly inFlight = new Map<string, Promise<SessionsResult>>();

  readonly onDidChangeTreeData = this.emitter.event;

  constructor(private readonly options: HistoryTreeOptions) {}

  getTreeItem(element: HistoryNode): vscode.TreeItem {
    return element;
  }

  async getChildren(element?: HistoryNode): Promise<HistoryNode[]> {
    if (element instanceof WorkspaceNode) {
      return this.sessionNodes(element.folder);
    }
    if (element instanceof SessionNode) {
      return turnsForSession(element.session).map(
        (turn) => new TurnNode(element.folder, element.session, turn),
      );
    }
    if (element instanceof TurnNode) {
      return [];
    }

    const folders = vscode.workspace.workspaceFolders ?? [];
    if (folders.length > 1) {
      return folders.map((folder) => new WorkspaceNode(folder));
    }
    return folders.length === 1 ? this.sessionNodes(folders[0]) : [];
  }

  refresh(): void {
    this.cache.clear();
    this.emitter.fire();
  }

  async pickTurn(completeOnly: boolean, placeHolder: string): Promise<TurnTarget | undefined> {
    const folders = vscode.workspace.workspaceFolders ?? [];
    const groups = await Promise.all(folders.map(async (folder) => ({ folder, sessions: await this.load(folder) })));
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
      return result.sessions.map((session) => new SessionNode(folder, session));
    } catch (error) {
      this.options.onBackgroundError(error, folder);
      return [];
    }
  }

  private load(folder: vscode.WorkspaceFolder): Promise<SessionsResult> {
    const key = folder.uri.toString();
    const cached = this.cache.get(key);
    if (cached) {
      return Promise.resolve(cached);
    }
    const active = this.inFlight.get(key);
    if (active) {
      return active;
    }
    const request = cliForFolder(folder)
      .sessions()
      .then((result) => {
        this.cache.set(key, result);
        this.options.onCliAvailability(false);
        return result;
      })
      .catch((error: unknown) => {
        if (error instanceof TurnalCommandError && error.missingExecutable) {
          this.options.onCliAvailability(true);
        }
        throw error;
      })
      .finally(() => this.inFlight.delete(key));
    this.inFlight.set(key, request);
    return request;
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
  ) {
    super(`${displayAgent(session.adapter)} · ${compactSessionId(session.session_id)}`, vscode.TreeItemCollapsibleState.Expanded);
    this.id = `session:${folder.uri.toString()}:${session.session_id}`;
    this.description = `${session.turn_count} ${session.turn_count === 1 ? "turn" : "turns"} · ${relativeTime(session.last_activity)}`;
    this.iconPath = session.status === "active" ? new vscode.ThemeIcon("radio-tower") : new vscode.ThemeIcon("history");
    this.contextValue = "turnalSession";
    this.tooltip = sessionTooltip(session);
  }
}

export class TurnNode extends vscode.TreeItem {
  readonly target: TurnTarget;

  constructor(
    folder: vscode.WorkspaceFolder,
    session: SessionSummary,
    turn: SessionTurn,
  ) {
    const title = turnTitle(turn.prompt, turn.turn_id);
    super(title, vscode.TreeItemCollapsibleState.None);
    this.target = turnTarget(folder, session, turn);
    this.id = `turn:${folder.uri.toString()}:${session.session_id}:${turn.turn_id}`;
    this.description = `#${turn.turn_id} · ${relativeTime(turn.last_activity ?? turn.first_activity)}`;
    this.iconPath = turnIcon(turn.status);
    this.contextValue = turn.status === "complete" ? "turnalTurnComplete" : "turnalTurn";
    this.tooltip = turnTooltip(session, turn);
    this.command = {
      command: turn.status === "complete" ? "turnal.openTurnDiff" : "turnal.showTurnDetails",
      title: turn.status === "complete" ? "Open Turn Diff" : "Show Turn Details",
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
      return new vscode.ThemeIcon("circle-filled");
    case "active":
      return new vscode.ThemeIcon("sync~spin");
    case "events-only":
      return new vscode.ThemeIcon("comment-discussion");
    default:
      return new vscode.ThemeIcon("warning");
  }
}

function compactSessionId(value: string): string {
  if (value.length <= 24) {
    return value;
  }
  return `${value.slice(0, 12)}…${value.slice(-8)}`;
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

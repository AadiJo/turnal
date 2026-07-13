import * as path from "node:path";
import * as vscode from "vscode";
import { TurnTarget } from "../model";
import { BlameEntry, BlameResult } from "../turnal/types";
import { displayAgent, formatTimestamp, relativeTime, truncate, turnTitle } from "../utils/format";
import { recordedTextMatches } from "../utils/blame";
import { cliForFolder } from "../workspaces";

interface BlameControllerOptions {
  onBackgroundError(error: unknown, folder: vscode.WorkspaceFolder): void;
}

export class BlameController implements vscode.HoverProvider, vscode.Disposable {
  private readonly decoration = vscode.window.createTextEditorDecorationType({
    after: {
      color: new vscode.ThemeColor("editorCodeLens.foreground"),
      fontStyle: "italic",
      margin: "0 0 0 3em",
    },
  });
  private readonly cache = new Map<string, BlameResult>();
  private readonly inFlight = new Map<string, Promise<BlameResult | undefined>>();
  private readonly disposables: vscode.Disposable[] = [];
  private updateTimer: NodeJS.Timeout | undefined;

  constructor(private readonly options: BlameControllerOptions) {
    this.disposables.push(
      vscode.window.onDidChangeActiveTextEditor(() => this.schedule()),
      vscode.window.onDidChangeTextEditorSelection((event) => {
        if (event.textEditor === vscode.window.activeTextEditor) {
          this.schedule();
        }
      }),
      vscode.workspace.onDidChangeTextDocument((event) => {
        if (event.document === vscode.window.activeTextEditor?.document) {
          this.schedule();
        }
      }),
      vscode.workspace.onDidChangeConfiguration((event) => {
        if (event.affectsConfiguration("turnal.inlineBlame") || event.affectsConfiguration("turnal.cliPath")) {
          this.refresh();
        }
      }),
    );
    this.schedule();
  }

  async provideHover(
    document: vscode.TextDocument,
    position: vscode.Position,
    token: vscode.CancellationToken,
  ): Promise<vscode.Hover | undefined> {
    const context = this.documentContext(document);
    if (!context || token.isCancellationRequested) {
      return undefined;
    }
    const result = await this.blame(context.folder, context.relativePath);
    if (!result || token.isCancellationRequested || !matchesRecordedFile(document, result)) {
      return undefined;
    }
    const entry = result.entries[position.line];
    if (!entry || entry.line !== position.line + 1) {
      return undefined;
    }
    const markdown = blameHover(entry, context.folder);
    return markdown ? new vscode.Hover(markdown, document.lineAt(position.line).range) : undefined;
  }

  refresh(): void {
    this.cache.clear();
    this.schedule();
  }

  dispose(): void {
    if (this.updateTimer) {
      clearTimeout(this.updateTimer);
    }
    this.disposables.forEach((disposable) => disposable.dispose());
    this.decoration.dispose();
    this.cache.clear();
  }

  private schedule(): void {
    if (this.updateTimer) {
      clearTimeout(this.updateTimer);
    }
    this.updateTimer = setTimeout(() => void this.renderActiveLine(), 160);
  }

  private async renderActiveLine(): Promise<void> {
    const editor = vscode.window.activeTextEditor;
    for (const visible of vscode.window.visibleTextEditors) {
      if (visible !== editor) {
        visible.setDecorations(this.decoration, []);
      }
    }
    if (!editor || !inlineBlameEnabled(editor.document.uri)) {
      editor?.setDecorations(this.decoration, []);
      return;
    }
    const context = this.documentContext(editor.document);
    if (!context) {
      editor.setDecorations(this.decoration, []);
      return;
    }
    const result = await this.blame(context.folder, context.relativePath);
    if (!result || editor !== vscode.window.activeTextEditor || !matchesRecordedFile(editor.document, result)) {
      editor.setDecorations(this.decoration, []);
      return;
    }
    const line = editor.selection.active.line;
    const entry = result.entries[line];
    if (!entry || entry.line !== line + 1 || entry.origin.kind !== "turn") {
      editor.setDecorations(this.decoration, []);
      return;
    }
    const title = turnTitle(entry.origin.prompt, entry.origin.turn_id ?? 0, 52);
    const annotation = `Turnal · ${displayAgent(entry.origin.adapter)} · ${title} · ${relativeTime(entry.origin.time)}`;
    editor.setDecorations(this.decoration, [
      {
        range: editor.document.lineAt(line).range,
        renderOptions: { after: { contentText: annotation } },
      },
    ]);
  }

  private documentContext(
    document: vscode.TextDocument,
  ): { folder: vscode.WorkspaceFolder; relativePath: string } | undefined {
    if (document.uri.scheme !== "file") {
      return undefined;
    }
    const folder = vscode.workspace.getWorkspaceFolder(document.uri);
    if (!folder) {
      return undefined;
    }
    const relativePath = path.relative(folder.uri.fsPath, document.uri.fsPath);
    if (!relativePath || relativePath.startsWith("..") || path.isAbsolute(relativePath)) {
      return undefined;
    }
    return { folder, relativePath: relativePath.split(path.sep).join("/") };
  }

  private blame(folder: vscode.WorkspaceFolder, relativePath: string): Promise<BlameResult | undefined> {
    const key = `${folder.uri.toString()}\0${relativePath}`;
    const cached = this.cache.get(key);
    if (cached) {
      return Promise.resolve(cached);
    }
    const active = this.inFlight.get(key);
    if (active) {
      return active;
    }
    const request = cliForFolder(folder)
      .blame(relativePath)
      .then((result) => {
        this.cache.set(key, result);
        return result;
      })
      .catch((error: unknown) => {
        if (!isQuietBlameError(error)) {
          this.options.onBackgroundError(error, folder);
        }
        return undefined;
      })
      .finally(() => this.inFlight.delete(key));
    this.inFlight.set(key, request);
    return request;
  }
}

function inlineBlameEnabled(uri: vscode.Uri): boolean {
  return vscode.workspace.getConfiguration("turnal", uri).get<boolean>("inlineBlame.enabled", true);
}

export function matchesRecordedFile(document: vscode.TextDocument, result: BlameResult): boolean {
  return recordedTextMatches(document.getText(), result.entries);
}

function blameHover(entry: BlameEntry, folder: vscode.WorkspaceFolder): vscode.MarkdownString | undefined {
  const origin = entry.origin;
  if (origin.kind !== "turn" || !origin.session_id || !origin.turn_id) {
    return undefined;
  }
  const target: TurnTarget = {
    folderUri: folder.uri.toString(),
    folderName: folder.name,
    sessionId: origin.session_id,
    turnId: origin.turn_id,
    title: turnTitle(origin.prompt, origin.turn_id),
    status: "complete",
    adapter: origin.adapter,
    time: origin.time,
  };
  const markdown = new vscode.MarkdownString(undefined, true);
  markdown.isTrusted = {
    enabledCommands: ["turnal.openTurnDiff", "turnal.showTurnDetails", "turnal.rollbackBeforeTurn"],
  };
  markdown.appendMarkdown(`**Turnal — ${escapeMarkdown(target.title)}**\n\n`);
  markdown.appendMarkdown(`${displayAgent(origin.adapter)} · ${formatTimestamp(origin.time)} · ${relativeTime(origin.time)}\n\n`);
  markdown.appendMarkdown("Session: ");
  markdown.appendText(origin.session_id);
  markdown.appendMarkdown(` · Turn ${origin.turn_id}\n\n`);
  if (origin.prompt) {
    markdown.appendMarkdown("Prompt: ");
    markdown.appendText(truncate(origin.prompt, 800));
    markdown.appendMarkdown("\n\n");
  }
  if (origin.tool_names?.length) {
    markdown.appendMarkdown("Tools: ");
    markdown.appendText(origin.tool_names.join(", "));
    markdown.appendMarkdown("\n\n");
  }
  markdown.appendMarkdown(
    `[$(diff) Open turn diff](${commandUri("turnal.openTurnDiff", target)}) · ` +
      `[$(info) Details](${commandUri("turnal.showTurnDetails", target)}) · ` +
      `[$(discard) Roll back…](${commandUri("turnal.rollbackBeforeTurn", target)})`,
  );
  return markdown;
}

function commandUri(command: string, target: TurnTarget): string {
  return `command:${command}?${encodeURIComponent(JSON.stringify([target]))}`;
}

function escapeMarkdown(value: string): string {
  return value.replace(/[\\`*_{}\[\]()<>#+\-.!|]/g, "\\$&");
}

function isQuietBlameError(error: unknown): boolean {
  const message = error instanceof Error ? error.message : String(error);
  return /no completed turn history|file not found|binary file|line not found|turnal root|not initialized/i.test(message);
}

import * as vscode from "vscode";
import { TurnTarget } from "../model";
import { DiffDocument } from "../turnal/types";

interface NativeChangeResource {
  label: vscode.Uri;
  left?: vscode.Uri;
  right?: vscode.Uri;
  file: DiffDocument;
}

export class VirtualDocumentStore implements vscode.TextDocumentContentProvider, vscode.Disposable {
  private static readonly retainedDocumentSets = 4;
  private readonly contents = new Map<string, string>();
  private readonly documentSets: string[][] = [];
  private readonly emitter = new vscode.EventEmitter<vscode.Uri>();
  private readonly registration: vscode.Disposable;
  private currentDocumentSet: string[] = [];
  private sequence = 0;

  readonly onDidChange = this.emitter.event;

  constructor() {
    this.registration = vscode.workspace.registerTextDocumentContentProvider("turnal-content", this);
  }

  provideTextDocumentContent(uri: vscode.Uri): string {
    return this.contents.get(uri.toString()) ?? "Turnal content is no longer available. Refresh and try again.";
  }

  async openTurnDetails(target: TurnTarget, content: string): Promise<void> {
    this.beginDocumentSet();
    const uri = this.virtualUri(target, `${target.title} — turn ${target.turnId}.txt`, "details", content);
    const document = await vscode.workspace.openTextDocument(uri);
    await vscode.window.showTextDocument(document, { preview: true, preserveFocus: false });
  }

  async openTurnChanges(target: TurnTarget, folder: vscode.WorkspaceFolder, files: DiffDocument[], selectedPath?: string): Promise<void> {
    this.beginDocumentSet();
    const resources = files.map((file) => this.turnResource(target, folder, file));
    await this.openNativeChanges(`${target.title} · ${target.sessionId}`, resources, selectedPath);
  }

  async openRollbackChanges(target: TurnTarget, folder: vscode.WorkspaceFolder, files: DiffDocument[]): Promise<void> {
    this.beginDocumentSet();
    const resources = files.map((file) => this.rollbackResource(target, folder, file));
    await this.openNativeChanges(`Rollback preview · before ${target.title}`, resources);
  }

  dispose(): void {
    this.registration.dispose();
    this.emitter.dispose();
    this.contents.clear();
  }

  private turnResource(target: TurnTarget, folder: vscode.WorkspaceFolder, file: DiffDocument): NativeChangeResource {
    const beforePath = file.old_path ?? file.path;
    const binary = binaryPlaceholder(file);
    return {
      label: workspaceUri(folder, file.path),
      left: file.before_exists
        ? this.virtualUri(target, beforePath, "before", binary ? `${binary}\nBefore this turn.\n` : file.before_text ?? "")
        : undefined,
      right: file.after_exists
        ? this.virtualUri(target, file.path, "after", binary ? `${binary}\nAfter this turn.\n` : file.after_text ?? "")
        : undefined,
      file,
    };
  }

  private rollbackResource(target: TurnTarget, folder: vscode.WorkspaceFolder, file: DiffDocument): NativeChangeResource {
    const binary = binaryPlaceholder(file);
    return {
      label: workspaceUri(folder, file.path),
      left: file.before_exists
        ? binary
          ? this.virtualUri(target, file.path, "current", `${binary}\nCurrent workspace content.\n`)
          : workspaceUri(folder, file.path)
        : undefined,
      right: file.after_exists
        ? this.virtualUri(target, file.path, "rollback", binary ? `${binary}\nRollback target content.\n` : file.after_text ?? "")
        : undefined,
      file,
    };
  }

  private async openNativeChanges(title: string, resources: NativeChangeResource[], selectedPath?: string): Promise<void> {
    if (resources.length === 0) {
      void vscode.window.showInformationMessage("Turnal: This turn did not change any files.");
      return;
    }
    const selected = selectedPath ? resources.find((resource) => resource.file.path === selectedPath) : undefined;
    if (selected) {
      await this.openNativeDiff(title, selected);
      return;
    }
    if (resources.length === 1) {
      await this.openNativeDiff(title, resources[0]);
      return;
    }
    const resourceList: Array<[vscode.Uri, vscode.Uri | undefined, vscode.Uri | undefined]> = resources.map(
      (resource) => [resource.label, resource.left, resource.right],
    );
    await vscode.commands.executeCommand("vscode.changes", title, resourceList);
  }

  private async openNativeDiff(title: string, resource: NativeChangeResource): Promise<void> {
    const left = resource.left ?? this.emptyUri(resource.file.path, "before");
    const right = resource.right ?? this.emptyUri(resource.file.path, "after");
    await vscode.commands.executeCommand("vscode.diff", left, right, `${resource.file.path} · ${title}`, {
      preview: true,
      preserveFocus: false,
    });
  }

  private emptyUri(path: string, side: string): vscode.Uri {
    const synthetic: TurnTarget = {
      folderUri: "empty",
      folderName: "empty",
      sessionId: "empty",
      turnId: 0,
      title: "Empty",
      status: "complete",
    };
    return this.virtualUri(synthetic, path, side, "");
  }

  private virtualUri(target: TurnTarget, filePath: string, side: string, content: string): vscode.Uri {
    const path = safeVirtualPath(filePath);
    const query = new URLSearchParams({
      folder: target.folderUri,
      session: target.sessionId,
      turn: String(target.turnId),
      side,
      revision: String(++this.sequence),
    }).toString();
    const uri = vscode.Uri.from({ scheme: "turnal-content", path: `/${path}`, query });
    const key = uri.toString();
    this.contents.set(key, content);
    this.currentDocumentSet.push(key);
    return uri;
  }

  private beginDocumentSet(): void {
    this.currentDocumentSet = [];
    this.documentSets.push(this.currentDocumentSet);
    while (this.documentSets.length > VirtualDocumentStore.retainedDocumentSets) {
      for (const key of this.documentSets.shift() ?? []) {
        this.contents.delete(key);
      }
    }
  }
}

function workspaceUri(folder: vscode.WorkspaceFolder, path: string): vscode.Uri {
  return vscode.Uri.joinPath(folder.uri, ...path.split("/"));
}

function safeVirtualPath(value: string): string {
  const segments = value
    .split("/")
    .filter(Boolean)
    .map((segment) => segment.replace(/[\\:*?"<>|\r\n]/g, " ").trim() || "file");
  return segments.join("/") || "Turnal.txt";
}

function binaryPlaceholder(file: DiffDocument): string | undefined {
  if (file.truncated) {
    return "Turnal did not load this file because the checkpoint copy exceeds 4 MiB.";
  }
  if (file.binary) {
    return "Turnal recorded a binary file change; text comparison is unavailable.";
  }
  return undefined;
}

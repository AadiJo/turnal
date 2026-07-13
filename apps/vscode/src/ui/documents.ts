import * as vscode from "vscode";
import { TurnTarget } from "../model";

export type VirtualDocumentKind = "diff" | "turn" | "preview";

export class VirtualDocumentStore implements vscode.TextDocumentContentProvider, vscode.Disposable {
  private readonly contents = new Map<string, string>();
  private readonly emitter = new vscode.EventEmitter<vscode.Uri>();
  private readonly registrations: vscode.Disposable[];

  readonly onDidChange = this.emitter.event;

  constructor() {
    this.registrations = [
      vscode.workspace.registerTextDocumentContentProvider("turnal-diff", this),
      vscode.workspace.registerTextDocumentContentProvider("turnal-turn", this),
      vscode.workspace.registerTextDocumentContentProvider("turnal-preview", this),
    ];
  }

  provideTextDocumentContent(uri: vscode.Uri): string {
    return this.contents.get(uri.toString()) ?? "Turnal content is no longer available. Refresh and try again.";
  }

  async open(kind: VirtualDocumentKind, target: TurnTarget, content: string): Promise<void> {
    const uri = this.uri(kind, target);
    this.contents.set(uri.toString(), content);
    this.emitter.fire(uri);
    let document = await vscode.workspace.openTextDocument(uri);
    const language = kind === "diff" ? "diff" : "plaintext";
    if (document.languageId !== language) {
      document = await vscode.languages.setTextDocumentLanguage(document, language);
    }
    await vscode.window.showTextDocument(document, { preview: true, preserveFocus: false });
  }

  dispose(): void {
    this.registrations.forEach((registration) => registration.dispose());
    this.emitter.dispose();
    this.contents.clear();
  }

  private uri(kind: VirtualDocumentKind, target: TurnTarget): vscode.Uri {
    const extension = kind === "diff" ? "diff" : "txt";
    const label = safeFileName(kind === "preview" ? `Rollback preview — turn ${target.turnId}` : target.title);
    const query = new URLSearchParams({
      folder: target.folderUri,
      session: target.sessionId,
      turn: String(target.turnId),
    }).toString();
    return vscode.Uri.from({
      scheme: `turnal-${kind}`,
      path: `/${label} — turn ${target.turnId}.${extension}`,
      query,
    });
  }
}

function safeFileName(value: string): string {
  const sanitized = value.replace(/[\\/:*?"<>|\r\n]/g, " ").replace(/\s+/g, " ").trim();
  return (sanitized || "Turnal").slice(0, 80);
}

import * as vscode from "vscode";
import { isTurnFileTarget, isTurnTarget, TurnTarget } from "../model";
import { RollbackPreview } from "../turnal/types";
import { folderForTarget, cliForFolder } from "../workspaces";
import { VirtualDocumentStore } from "./documents";
import { HistoryTreeProvider } from "./historyTree";

interface TurnCommandsOptions {
  history: HistoryTreeProvider;
  documents: VirtualDocumentStore;
  output: vscode.OutputChannel;
  afterMutation(): void;
}

export class TurnCommands {
  constructor(private readonly options: TurnCommandsOptions) {}

  async openDiff(value?: unknown): Promise<void> {
    const argument = commandArgument(value);
    const fileTarget = isTurnFileTarget(argument) ? argument : undefined;
    const target = fileTarget?.target ?? (await this.resolveTarget(argument, true, "Choose a completed turn to review"));
    if (!target) {
      return;
    }
    await this.runCommand("Opening turn changes…", target, async () => {
      const folder = requiredFolder(target);
      const result = await cliForFolder(folder).diffDocuments(target.sessionId, target.turnId);
      await this.options.documents.openTurnChanges(target, folder, result.files, fileTarget?.path);
    });
  }

  async showDetails(value?: unknown): Promise<void> {
    const target = await this.resolveTarget(commandArgument(value), false, "Choose a turn to inspect");
    if (!target) {
      return;
    }
    await this.runCommand("Loading turn details…", target, async () => {
      const folder = requiredFolder(target);
      const content = await cliForFolder(folder).show(target.sessionId, target.turnId);
      await this.options.documents.openTurnDetails(target, content);
    });
  }

  async rollbackBefore(value?: unknown): Promise<void> {
    const target = await this.resolveTarget(commandArgument(value), true, "Choose a completed turn to roll back before");
    if (!target) {
      return;
    }
    try {
      const folder = requiredFolder(target);
      const cli = cliForFolder(folder);
      const rollbackDocuments = await vscode.window.withProgress(
        { location: vscode.ProgressLocation.Notification, title: "Turnal: Preparing native change review…" },
        () => cli.rollbackDocuments(target.sessionId, target.turnId),
      );
      const preview = previewFromDocuments(rollbackDocuments.files);

      if (preview.no_changes) {
        void vscode.window.showInformationMessage(`Turnal: ${folder.name} already matches the state before turn #${target.turnId}.`);
        return;
      }

      const dirty = affectedDirtyDocuments(folder, preview);
      if (dirty.length > 0) {
        const choice = await vscode.window.showWarningMessage(
          "Save affected files before rolling back?",
          {
            modal: true,
            detail: `Turnal won’t roll back over unsaved editor changes. Save these files, then preview again:\n\n${dirty
              .map((document) => vscode.workspace.asRelativePath(document.uri, false))
              .join("\n")}`,
          },
          "Save All and Preview Again",
        );
        if (choice === "Save All and Preview Again" && (await vscode.workspace.saveAll(false))) {
          await this.rollbackBefore(target);
        }
        return;
      }

      await this.options.documents.openRollbackChanges(target, folder, rollbackDocuments.files);

      const choice = await vscode.window.showWarningMessage(
        `Roll back to before “${target.title}”?`,
        {
          modal: true,
          detail: confirmationDetail(target, preview),
        },
        "Roll Back",
      );
      if (choice !== "Roll Back") {
        return;
      }

      const newlyDirty = affectedDirtyDocuments(folder, preview);
      if (newlyDirty.length > 0) {
        void vscode.window.showWarningMessage("Turnal: An affected editor changed after the preview. Save it and try again.");
        return;
      }

      if (!rollbackDocuments.workspace_tree) {
        throw new Error("The Turnal CLI did not return a rollback workspace fingerprint. Update the CLI and try again.");
      }

      const output = await vscode.window.withProgress(
        { location: vscode.ProgressLocation.Notification, title: "Turnal: Rolling back…" },
        () => cli.rollback(target.sessionId, target.turnId, rollbackDocuments.workspace_tree),
      );
      this.options.output.appendLine(output.trim());
      this.options.afterMutation();
      void vscode.window.showInformationMessage(
        `Turnal: Rolled back to before turn #${target.turnId}. A safety snapshot was saved.`,
      );
    } catch (error) {
      this.reportCommandError("Rollback failed", error, target);
    }
  }

  private async resolveTarget(
    value: unknown,
    completeOnly: boolean,
    placeHolder: string,
  ): Promise<TurnTarget | undefined> {
    if (isTurnTarget(value)) {
      if (!completeOnly || value.status === "complete") {
        return value;
      }
    }
    try {
      return await this.options.history.pickTurn(completeOnly, placeHolder);
    } catch (error) {
      this.reportCommandError("Couldn’t load turns", error);
      return undefined;
    }
  }

  private async runCommand(
    title: string,
    target: TurnTarget,
    task: () => Promise<void>,
  ): Promise<void> {
    try {
      await vscode.window.withProgress(
        { location: vscode.ProgressLocation.Window, title: `Turnal: ${title}` },
        task,
      );
    } catch (error) {
      this.reportCommandError(title.replace(/…$/, " failed"), error, target);
    }
  }

  private reportCommandError(title: string, error: unknown, target?: TurnTarget): void {
    const detail = error instanceof Error ? error.message : String(error);
    this.options.output.appendLine(`[${new Date().toISOString()}] ${title}${target ? ` (${target.sessionId}:${target.turnId})` : ""}`);
    this.options.output.appendLine(detail);
    void vscode.window.showErrorMessage(`Turnal: ${firstLine(detail)}`, "Show Output").then((choice) => {
      if (choice === "Show Output") {
        this.options.output.show(true);
      }
    });
  }
}

function requiredFolder(target: TurnTarget): vscode.WorkspaceFolder {
  const folder = folderForTarget(target);
  if (!folder) {
    throw new Error(`Workspace “${target.folderName}” is no longer open.`);
  }
  return folder;
}

function affectedDirtyDocuments(folder: vscode.WorkspaceFolder, preview: RollbackPreview): vscode.TextDocument[] {
  const affected = new Set(
    preview.changes.map((change) => normalizedUri(vscode.Uri.joinPath(folder.uri, ...change.path.split("/")))),
  );
  return vscode.workspace.textDocuments.filter((document) => document.isDirty && affected.has(normalizedUri(document.uri)));
}

function normalizedUri(uri: vscode.Uri): string {
  const value = uri.toString();
  return process.platform === "win32" ? value.toLowerCase() : value;
}

function confirmationDetail(target: TurnTarget, preview: RollbackPreview): string {
  const limit = 8;
  const files = preview.changes.slice(0, limit).map((change) => `${change.action} — ${change.path}`);
  if (preview.changes.length > limit) {
    files.push(`…and ${preview.changes.length - limit} more`);
  }
  return [
    `This restores the workspace to just before turn #${target.turnId} in ${target.sessionId}.`,
    "",
    "The full change set is open in VS Code’s Changes editor.",
    "",
    `Will change ${preview.changes.length} ${preview.changes.length === 1 ? "file" : "files"}:`,
    ...files,
    "",
    "Turnal will save the current workspace as a safety snapshot first. This can’t be undone with Editor Undo.",
  ].join("\n");
}

function previewFromDocuments(files: import("../turnal/types").DiffDocument[]): RollbackPreview {
  const actionForStatus: Record<string, RollbackPreview["changes"][number]["action"]> = {
    A: "added",
    M: "modified",
    D: "deleted",
    T: "mode-changed",
  };
  const changes = files.map((file) => ({
    action: actionForStatus[file.status] ?? "modified",
    path: file.path,
  }));
  return { raw: "", changes, no_changes: changes.length === 0 };
}

function firstLine(value: string): string {
  return value.trim().split(/\r?\n/, 1)[0] || "Command failed";
}

function commandArgument(value: unknown): unknown {
  if (isTurnTarget(value) || isTurnFileTarget(value)) {
    return value;
  }
  if (typeof value !== "object" || value === null || !("target" in value)) {
    return value;
  }
  return (value as { target: unknown }).target;
}

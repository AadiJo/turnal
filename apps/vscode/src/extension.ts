import * as vscode from "vscode";
import { TurnalCommandError } from "./turnal/cli";
import { BlameController } from "./ui/blame";
import { TurnCommands } from "./ui/commands";
import { VirtualDocumentStore } from "./ui/documents";
import { HistoryTreeProvider } from "./ui/historyTree";

export function activate(context: vscode.ExtensionContext): void {
  const output = vscode.window.createOutputChannel("Turnal", { log: true });
  let treeView: vscode.TreeView<unknown> | undefined;
  let cliMissing = false;
  const backgroundErrors = new Set<string>();

  const setCliAvailability = (missing: boolean): void => {
    cliMissing = missing;
    void vscode.commands.executeCommand("setContext", "turnal.cliMissing", missing);
  };
  setCliAvailability(false);

  const reportBackgroundError = (error: unknown, folder: vscode.WorkspaceFolder): void => {
    const detail = error instanceof Error ? error.message : String(error);
    const key = `${folder.uri.toString()}\0${detail}`;
    if (!backgroundErrors.has(key)) {
      backgroundErrors.add(key);
      output.error(`${folder.name}: ${detail}`);
    }
    if (error instanceof TurnalCommandError && error.missingExecutable) {
      setCliAvailability(true);
      if (treeView) {
        treeView.message = "Turnal CLI not found";
      }
      return;
    }
    if (!isMissingWorkspace(error) && treeView) {
      treeView.message = `Couldn’t load ${folder.name}; see Turnal output`;
    }
  };

  const history = new HistoryTreeProvider({
    extensionUri: context.extensionUri,
    onBackgroundError: reportBackgroundError,
    onCliAvailability: (missing) => {
      if (missing || cliMissing) {
        setCliAvailability(missing);
      }
      if (!missing && treeView) {
        treeView.message = undefined;
      }
    },
  });
  treeView = vscode.window.createTreeView("turnal.sessions", {
    treeDataProvider: history,
    showCollapseAll: true,
  });

  const documents = new VirtualDocumentStore();
  const blame = new BlameController({ onBackgroundError: reportBackgroundError });
  const refresh = (): void => {
    backgroundErrors.clear();
    history.refresh();
    blame.refresh();
  };
  const commands = new TurnCommands({ history, documents, output, afterMutation: refresh });

  context.subscriptions.push(
    output,
    history,
    treeView,
    documents,
    blame,
    vscode.languages.registerHoverProvider({ scheme: "file" }, blame),
    vscode.commands.registerCommand("turnal.refresh", refresh),
    vscode.commands.registerCommand("turnal.openTurnDiff", (target?: unknown) => commands.openDiff(target)),
    vscode.commands.registerCommand("turnal.showTurnDetails", (target?: unknown) => commands.showDetails(target)),
    vscode.commands.registerCommand("turnal.rollbackBeforeTurn", (target?: unknown) => commands.rollbackBefore(target)),
    vscode.commands.registerCommand("turnal.toggleInlineBlame", async () => {
      const resource = vscode.window.activeTextEditor?.document.uri;
      const configuration = vscode.workspace.getConfiguration("turnal", resource);
      const enabled = configuration.get<boolean>("inlineBlame.enabled", true);
      const target = vscode.workspace.workspaceFolders?.length
        ? vscode.ConfigurationTarget.Workspace
        : vscode.ConfigurationTarget.Global;
      await configuration.update("inlineBlame.enabled", !enabled, target);
      void vscode.window.showInformationMessage(`Turnal inline blame ${enabled ? "disabled" : "enabled"}.`);
    }),
  );

  const watchers: vscode.FileSystemWatcher[] = [];
  let watcherTimer: NodeJS.Timeout | undefined;
  const scheduleRefresh = (): void => {
    if (watcherTimer) {
      clearTimeout(watcherTimer);
    }
    watcherTimer = setTimeout(refresh, 300);
  };
  const resetWatchers = (): void => {
    watchers.splice(0).forEach((watcher) => watcher.dispose());
    for (const folder of vscode.workspace.workspaceFolders ?? []) {
      const watcher = vscode.workspace.createFileSystemWatcher(
        new vscode.RelativePattern(folder, ".turnal/{log,git/refs}/**"),
      );
      watcher.onDidCreate(scheduleRefresh);
      watcher.onDidChange(scheduleRefresh);
      watcher.onDidDelete(scheduleRefresh);
      watchers.push(watcher);
    }
  };
  resetWatchers();
  context.subscriptions.push(
    vscode.workspace.onDidChangeWorkspaceFolders(() => {
      resetWatchers();
      refresh();
    }),
    vscode.window.onDidChangeWindowState((state) => {
      if (state.focused) {
        refresh();
      }
    }),
    {
      dispose: () => {
        if (watcherTimer) {
          clearTimeout(watcherTimer);
        }
        watchers.forEach((watcher) => watcher.dispose());
      },
    },
  );
}

export function deactivate(): void {}

function isMissingWorkspace(error: unknown): boolean {
  const message = error instanceof Error ? error.message : String(error);
  return /turnal root|not initialized|\.turnal/i.test(message);
}

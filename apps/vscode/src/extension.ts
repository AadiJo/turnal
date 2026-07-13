import * as vscode from "vscode";
import { TurnalCommandError } from "./turnal/cli";
import { BlameController } from "./ui/blame";
import { TurnCommands } from "./ui/commands";
import { VirtualDocumentStore } from "./ui/documents";
import { HistoryLayout, HistoryNode, HistoryTreeProvider } from "./ui/historyTree";

export function activate(context: vscode.ExtensionContext): void {
  const output = vscode.window.createOutputChannel("Turnal", { log: true });
  let treeView: vscode.TreeView<HistoryNode> | undefined;
  let cliMissing = false;
  const backgroundErrors = new Set<string>();

  const getHistoryLayout = (): HistoryLayout =>
    vscode.workspace.getConfiguration("turnal").get<string>("history.layout", "sessions") === "activity"
      ? "activity"
      : "sessions";
  const getInlineBlameEnabled = (): boolean => {
    const resource = vscode.window.activeTextEditor?.document.uri;
    return vscode.workspace.getConfiguration("turnal", resource).get<boolean>("inlineBlame.enabled", true);
  };
  const syncInlineBlameContext = (): void => {
    void vscode.commands.executeCommand("setContext", "turnal.inlineBlameEnabled", getInlineBlameEnabled());
  };

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
    getLayout: getHistoryLayout,
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
  let displayedHistoryLayout: HistoryLayout | undefined;
  const syncHistoryLayout = (relayout = true): void => {
    const layout = getHistoryLayout();
    if (displayedHistoryLayout === layout) {
      return;
    }
    displayedHistoryLayout = layout;
    if (treeView) {
      treeView.title = layout === "activity" ? "Recent Activity" : "Sessions";
    }
    void vscode.commands.executeCommand("setContext", "turnal.historyLayout", layout);
    if (relayout) {
      history.relayout();
    }
  };
  const setHistoryLayout = async (layout: HistoryLayout): Promise<void> => {
    const configuration = vscode.workspace.getConfiguration("turnal");
    const target = vscode.workspace.workspaceFolders?.length
      ? vscode.ConfigurationTarget.Workspace
      : vscode.ConfigurationTarget.Global;
    try {
      await configuration.update("history.layout", layout, target);
      syncHistoryLayout();
    } catch (error) {
      const detail = error instanceof Error ? error.message : String(error);
      output.error(`Couldn’t save history layout: ${detail}`);
      void vscode.window.showErrorMessage(`Couldn’t save the Turnal history layout: ${detail}`);
    }
  };
  syncHistoryLayout(false);

  const setInlineBlameEnabled = async (enabled: boolean): Promise<void> => {
    const resource = vscode.window.activeTextEditor?.document.uri;
    const configuration = vscode.workspace.getConfiguration("turnal", resource);
    const inspection = configuration.inspect<boolean>("inlineBlame.enabled");
    const folder = resource ? vscode.workspace.getWorkspaceFolder(resource) : undefined;
    const target = folder && inspection?.workspaceFolderValue !== undefined
      ? vscode.ConfigurationTarget.WorkspaceFolder
      : vscode.workspace.workspaceFolders?.length
        ? vscode.ConfigurationTarget.Workspace
        : vscode.ConfigurationTarget.Global;
    try {
      await configuration.update("inlineBlame.enabled", enabled, target);
      if (getInlineBlameEnabled() !== enabled) {
        throw new Error("A more specific workspace setting is overriding this value.");
      }
      syncInlineBlameContext();
      void vscode.window.showInformationMessage(`Turnal inline blame ${enabled ? "enabled" : "disabled"}.`);
    } catch (error) {
      const detail = error instanceof Error ? error.message : String(error);
      output.error(`Couldn’t save inline blame setting: ${detail}`);
      void vscode.window.showErrorMessage(`Couldn’t save the Turnal inline blame setting: ${detail}`);
    }
  };
  syncInlineBlameContext();

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
    vscode.commands.registerCommand("turnal.showRecentActivity", () => setHistoryLayout("activity")),
    vscode.commands.registerCommand("turnal.groupBySession", () => setHistoryLayout("sessions")),
    vscode.commands.registerCommand("turnal.openTurnDiff", (target?: unknown) => commands.openDiff(target)),
    vscode.commands.registerCommand("turnal.showTurnDetails", (target?: unknown) => commands.showDetails(target)),
    vscode.commands.registerCommand("turnal.showRollbackDetails", (target?: unknown) => commands.showRollbackDetails(target)),
    vscode.commands.registerCommand("turnal.rollbackBeforeTurn", (target?: unknown) => commands.rollbackBefore(target)),
    vscode.commands.registerCommand("turnal.enableInlineBlame", () => setInlineBlameEnabled(true)),
    vscode.commands.registerCommand("turnal.disableInlineBlame", () => setInlineBlameEnabled(false)),
    vscode.commands.registerCommand("turnal.toggleInlineBlame", () => setInlineBlameEnabled(!getInlineBlameEnabled())),
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
    vscode.workspace.onDidChangeConfiguration((event) => {
      if (event.affectsConfiguration("turnal.history.layout")) {
        syncHistoryLayout();
      }
      if (event.affectsConfiguration("turnal.inlineBlame.enabled")) {
        syncInlineBlameContext();
      }
    }),
    vscode.window.onDidChangeActiveTextEditor(syncInlineBlameContext),
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

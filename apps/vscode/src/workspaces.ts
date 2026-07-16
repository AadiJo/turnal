import * as vscode from "vscode";
import { TurnTarget } from "./model";
import { TurnalCli } from "./turnal/cli";

export function cliForFolder(folder: vscode.WorkspaceFolder): TurnalCli {
  const executable = vscode.workspace
    .getConfiguration("turnal", folder.uri)
    .get<string>("cliPath", "turnal")
    .trim();
  return new TurnalCli(folder.uri.fsPath, executable || "turnal");
}

export function folderForTarget(target: TurnTarget): vscode.WorkspaceFolder | undefined {
  return vscode.workspace.workspaceFolders?.find((folder) => folder.uri.toString() === target.folderUri);
}

export function targetKey(target: TurnTarget): string {
  return `${target.folderUri}\0${target.sessionId}\0${target.turnId}`;
}

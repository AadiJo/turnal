import { execFile } from "node:child_process";
import { existsSync } from "node:fs";
import path from "node:path";
import {
  BlameResult,
  parseBlameResult,
  parseDiffDocumentsResult,
  parseRollbackPreview,
  parseSessionsResult,
  DiffDocumentsResult,
  RollbackPreview,
  SessionsResult,
} from "./types";

const MAX_OUTPUT_BYTES = 32 * 1024 * 1024;

export interface CommandOutput {
  stdout: string;
  stderr: string;
}

export type CommandExecutor = (
  executable: string,
  args: readonly string[],
  cwd: string,
) => Promise<CommandOutput>;

interface ExecutableResolutionOptions {
  platform?: NodeJS.Platform;
  arch?: string;
  env?: NodeJS.ProcessEnv;
  exists?: (candidate: string) => boolean;
}

export function resolveCliExecutable(
  executable: string,
  options: ExecutableResolutionOptions = {},
): string {
  const platform = options.platform ?? process.platform;
  if (platform !== "win32") {
    return executable;
  }

  const launcher = path.win32.basename(executable).toLowerCase();
  if (launcher !== "turnal" && launcher !== "turnal.cmd" && launcher !== "turnal.ps1") {
    return executable;
  }

  const env = options.env ?? process.env;
  const arch = options.arch ?? process.arch;
  const exists = options.exists ?? existsSync;
  const searchDirectories: string[] = [];
  const explicitDirectory = path.win32.dirname(executable);
  if (explicitDirectory !== ".") {
    searchDirectories.push(explicitDirectory);
  }
  searchDirectories.push(...(env.Path ?? env.PATH ?? "").split(path.win32.delimiter));
  if (env.APPDATA) {
    searchDirectories.push(path.win32.join(env.APPDATA, "npm"));
  }

  const visited = new Set<string>();
  for (const directory of searchDirectories) {
    const normalizedDirectory = directory.trim();
    if (!normalizedDirectory) {
      continue;
    }
    const key = normalizedDirectory.toLowerCase();
    if (visited.has(key)) {
      continue;
    }
    visited.add(key);
    const candidate = path.win32.join(
      normalizedDirectory,
      "node_modules",
      "@aadijo",
      "turnal",
      "npm",
      "bin",
      `win32-${arch}`,
      "turnal.exe",
    );
    if (exists(candidate)) {
      return candidate;
    }
  }

  return executable;
}

export class TurnalCommandError extends Error {
  constructor(
    public readonly executable: string,
    public readonly args: readonly string[],
    public readonly stdout: string,
    public readonly stderr: string,
    public readonly code?: string | number,
    cause?: unknown,
  ) {
    const detail = stderr.trim() || (cause instanceof Error ? cause.message : "command failed");
    super(detail, { cause });
    this.name = "TurnalCommandError";
  }

  get missingExecutable(): boolean {
    return this.code === "ENOENT";
  }
}

export class TurnalCli {
  public readonly executable: string;

  constructor(
    public readonly cwd: string,
    executable = "turnal",
    private readonly execute: CommandExecutor = executeCommand,
  ) {
    this.executable = resolveCliExecutable(executable);
  }

  async sessions(): Promise<SessionsResult> {
    return parseSessionsResult(await this.json(["sessions", "--json"]));
  }

  async blame(path: string): Promise<BlameResult> {
    return parseBlameResult(await this.json(["blame", path, "--json"]));
  }

  async diff(sessionId: string, turnId: number): Promise<string> {
    return (await this.run(["diff", `${sessionId}:${turnId}`])).stdout;
  }

  async diffDocuments(sessionId: string, turnId: number): Promise<DiffDocumentsResult> {
    return parseDiffDocumentsResult(await this.json(["diff", `${sessionId}:${turnId}`, "--json"]));
  }

  async rollbackDocuments(sessionId: string, turnId: number): Promise<DiffDocumentsResult> {
    return parseDiffDocumentsResult(
      await this.json(["diff", `${sessionId}:${turnId}`, "--json", "--rollback-preview"]),
    );
  }

  async show(sessionId: string, turnId: number): Promise<string> {
    return (await this.run(["show", `${sessionId}:${turnId}`])).stdout;
  }

  async previewRollback(sessionId: string, turnId: number): Promise<RollbackPreview> {
    const output = await this.run([
      "rollback",
      "--to",
      rollbackTarget(sessionId, turnId),
      "--workspace-git=false",
      "--dry-run",
    ]);
    return parseRollbackPreview(output.stdout);
  }

  async rollback(sessionId: string, turnId: number, expectedWorkspaceTree?: string): Promise<string> {
    const args = ["rollback", "--to", rollbackTarget(sessionId, turnId), "--workspace-git=false"];
    if (expectedWorkspaceTree) {
      args.push("--expect-workspace-tree", expectedWorkspaceTree);
    }
    return (
      await this.run(args)
    ).stdout;
  }

  async run(args: readonly string[]): Promise<CommandOutput> {
    return this.execute(this.executable, args, this.cwd);
  }

  private async json(args: readonly string[]): Promise<unknown> {
    const { stdout } = await this.run(args);
    try {
      return JSON.parse(stdout) as unknown;
    } catch (error) {
      throw new TurnalCommandError(
        this.executable,
        args,
        stdout,
        "Turnal returned invalid JSON. The CLI and extension may be incompatible.",
        undefined,
        error,
      );
    }
  }
}

export function rollbackTarget(sessionId: string, turnId: number): string {
  return `${sessionId}:turn:${turnId}:pre`;
}

export const executeCommand: CommandExecutor = (executable, args, cwd) =>
  new Promise((resolve, reject) => {
    execFile(
      executable,
      [...args],
      {
        cwd,
        encoding: "utf8",
        env: { ...process.env, NO_COLOR: "1", PAGER: "cat" },
        maxBuffer: MAX_OUTPUT_BYTES,
        windowsHide: true,
      },
      (error, stdout, stderr) => {
        if (error) {
          reject(
            new TurnalCommandError(
              executable,
              args,
              stdout,
              stderr,
              "code" in error && error.code !== null ? error.code : undefined,
              error,
            ),
          );
          return;
        }
        resolve({ stdout, stderr });
      },
    );
  });

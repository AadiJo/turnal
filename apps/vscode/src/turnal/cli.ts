import { execFile } from "node:child_process";
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
  constructor(
    public readonly cwd: string,
    public readonly executable = "turnal",
    private readonly execute: CommandExecutor = executeCommand,
  ) {}

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

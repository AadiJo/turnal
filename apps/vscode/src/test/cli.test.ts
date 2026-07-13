import assert from "node:assert/strict";
import test from "node:test";
import { CommandExecutor, rollbackTarget, TurnalCli } from "../turnal/cli";

test("uses the CLI JSON contracts without a shell", async () => {
  const calls: Array<{ executable: string; args: readonly string[]; cwd: string }> = [];
  const execute: CommandExecutor = async (executable, args, cwd) => {
    calls.push({ executable, args, cwd });
    return { stdout: '{"total_sessions":0,"sessions":[]}', stderr: "" };
  };
  const cli = new TurnalCli("/workspace", "/opt/turnal", execute);
  assert.deepEqual(await cli.sessions(), { total_sessions: 0, sessions: [] });
  assert.deepEqual(calls, [
    { executable: "/opt/turnal", args: ["sessions", "--json"], cwd: "/workspace" },
  ]);
});

test("builds an explicit pre-turn rollback target", () => {
  assert.equal(rollbackTarget("codex-session", 7), "codex-session:turn:7:pre");
});

test("forces the extension rollback onto checkpoint-only mode", async () => {
  const calls: string[][] = [];
  const execute: CommandExecutor = async (_executable, args) => {
    calls.push([...args]);
    return {
      stdout: args.includes("--dry-run")
        ? "Dry-run rollback\n  target: demo:turn:2:pre\nmodified:\n  app.ts\n"
        : "Rollback complete\n",
      stderr: "",
    };
  };
  const cli = new TurnalCli("/workspace", "turnal", execute);
  await cli.previewRollback("demo", 2);
  await cli.rollback("demo", 2);
  assert.deepEqual(calls, [
    ["rollback", "--to", "demo:turn:2:pre", "--workspace-git=false", "--dry-run"],
    ["rollback", "--to", "demo:turn:2:pre", "--workspace-git=false"],
  ]);
});

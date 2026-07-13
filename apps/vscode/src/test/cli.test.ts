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

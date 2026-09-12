// Managed by Turnal. Re-run turnal init --agent pi to update.
import { spawn } from "node:child_process";
import { closeSync, openSync, readSync } from "node:fs";
import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";

type Payload = Record<string, unknown>;
type CaptureResponse = { additional_context?: string };

const turnalCommand = "turnal";

const isRecord = (value: unknown): value is Payload =>
  typeof value === "object" && value !== null && !Array.isArray(value);

function messageText(message: unknown): string {
  if (!isRecord(message)) return "";
  if (typeof message.content === "string") return message.content;
  if (!Array.isArray(message.content)) return "";
  return message.content
    .filter(isRecord)
    .filter((part) => part.type === "text" && typeof part.text === "string")
    .map((part) => String(part.text))
    .join("\n");
}

function sessionIDFromFile(path: string | undefined): string | undefined {
  if (!path) return undefined;
  let descriptor: number | undefined;
  try {
    descriptor = openSync(path, "r");
    const buffer = Buffer.alloc(64 * 1024);
    const bytesRead = readSync(descriptor, buffer, 0, buffer.length, 0);
    const newline = buffer.subarray(0, bytesRead).indexOf(0x0a);
    const firstLine = buffer.subarray(0, newline < 0 ? bytesRead : newline).toString("utf8");
    const header: unknown = JSON.parse(firstLine);
    return isRecord(header) && typeof header.id === "string" ? header.id : undefined;
  } catch {
    return undefined;
  } finally {
    if (descriptor !== undefined) {
      try {
        closeSync(descriptor);
      } catch {
        // Parent topology is optional and must never block Pi.
      }
    }
  }
}

function sessionPayload(ctx: ExtensionContext, parentSessionID: string | undefined): Payload {
  return {
    session_id: ctx.sessionManager.getSessionId(),
    parent_session_id: parentSessionID,
    cwd: ctx.cwd,
    transcript_path: ctx.sessionManager.getSessionFile(),
    model: ctx.model?.id,
  };
}

function capture(hook: string, payload: Payload): Promise<CaptureResponse> {
  return new Promise((resolve) => {
    let settled = false;
    let timeout: ReturnType<typeof setTimeout> | undefined;
    let output = "";
    const finish = () => {
      if (settled) return;
      settled = true;
      if (timeout !== undefined) clearTimeout(timeout);
      try {
        const parsed: unknown = JSON.parse(output);
        resolve(isRecord(parsed) && typeof parsed.additional_context === "string" ? parsed : {});
      } catch {
        resolve({});
      }
    };

    let encoded: string;
    try {
      encoded = JSON.stringify(payload);
    } catch (error) {
      console.error(`turnal: pi adapter could not encode ${hook}: ${String(error)}`);
      finish();
      return;
    }

    const child = spawn(turnalCommand, ["adapter", "capture", "pi", hook], {
      stdio: ["pipe", "pipe", "inherit"],
    });
    child.once("error", (error) => {
      console.error(`turnal: pi adapter could not run ${hook}: ${error.message}`);
      finish();
    });
    child.once("close", finish);
    child.stdin.once("error", finish);
    child.stdout.on("data", (chunk: Buffer) => {
      if (output.length < 64 * 1024) output += chunk.toString("utf8");
    });
    child.stdin.end(encoded);
    timeout = setTimeout(() => {
      console.error(`turnal: pi adapter timed out while capturing ${hook}`);
      child.kill();
      finish();
    }, 10_000);
  });
}

export default function turnal(pi: ExtensionAPI) {
  let lastAssistantText = "";
  let parentSessionID: string | undefined;

  pi.on("session_start", async (event, ctx) => {
    lastAssistantText = "";
    parentSessionID = sessionIDFromFile(ctx.sessionManager.getHeader()?.parentSession);
    await capture("session_start", { ...sessionPayload(ctx, parentSessionID), reason: event.reason });
  });

  pi.on("before_agent_start", async (event, ctx) => {
    lastAssistantText = "";
    const response = await capture("before_agent_start", {
      ...sessionPayload(ctx, parentSessionID),
      prompt: event.prompt,
    });
    if (response.additional_context) {
      return { systemPrompt: `${event.systemPrompt}\n\n${response.additional_context}` };
    }
  });

  pi.on("tool_execution_start", async (event, ctx) => {
    await capture("tool_execution_start", {
      ...sessionPayload(ctx, parentSessionID),
      tool_call_id: event.toolCallId,
      tool_name: event.toolName,
      args: event.args,
    });
  });

  pi.on("tool_execution_end", async (event, ctx) => {
    await capture("tool_execution_end", {
      ...sessionPayload(ctx, parentSessionID),
      tool_call_id: event.toolCallId,
      tool_name: event.toolName,
      result: event.result,
      is_error: event.isError,
    });
  });

  pi.on("message_end", (event) => {
    if (isRecord(event.message) && event.message.role === "assistant") {
      lastAssistantText = messageText(event.message);
    }
  });

  pi.on("agent_settled", async (_event, ctx) => {
    await capture("agent_settled", { ...sessionPayload(ctx, parentSessionID), text: lastAssistantText });
  });
}

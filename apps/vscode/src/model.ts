import { SessionRollback } from "./turnal/types";

export interface TurnTarget {
  folderUri: string;
  folderName: string;
  sessionId: string;
  turnId: number;
  title: string;
  status: string;
  adapter?: string;
  time?: string;
}

export interface RollbackTarget {
  folderUri: string;
  folderName: string;
  sessionId: string;
  adapter?: string;
  rollback: SessionRollback;
}

export interface TurnFileTarget {
  target: TurnTarget;
  path: string;
  oldPath?: string;
}

export function isTurnTarget(value: unknown): value is TurnTarget {
  if (typeof value !== "object" || value === null) {
    return false;
  }
  const target = value as Partial<TurnTarget>;
  return (
    typeof target.folderUri === "string" &&
    typeof target.folderName === "string" &&
    typeof target.sessionId === "string" &&
    typeof target.turnId === "number" &&
    typeof target.title === "string" &&
    typeof target.status === "string"
  );
}

export function isTurnFileTarget(value: unknown): value is TurnFileTarget {
  if (typeof value !== "object" || value === null) {
    return false;
  }
  const candidate = value as Partial<TurnFileTarget>;
  return isTurnTarget(candidate.target) && typeof candidate.path === "string";
}

export function isRollbackTarget(value: unknown): value is RollbackTarget {
  if (typeof value !== "object" || value === null) {
    return false;
  }
  const candidate = value as Partial<RollbackTarget>;
  return (
    typeof candidate.folderUri === "string" &&
    typeof candidate.folderName === "string" &&
    typeof candidate.sessionId === "string" &&
    typeof candidate.rollback === "object" &&
    candidate.rollback !== null &&
    typeof candidate.rollback.sequence === "number" &&
    typeof candidate.rollback.target === "string"
  );
}

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

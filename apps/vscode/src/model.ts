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

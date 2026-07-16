import { SessionRollback, SessionSummary, SessionTurn, turnsForSession } from "../turnal/types";

export interface TurnActivity {
  kind: "turn";
  session: SessionSummary;
  turn: SessionTurn;
}

export interface RollbackActivity {
  kind: "rollback";
  session: SessionSummary;
  rollback: SessionRollback;
}

export type HistoryActivity = TurnActivity | RollbackActivity;

export function activitiesForSession(session: SessionSummary): HistoryActivity[] {
  return sortActivities([
    ...turnsForSession(session).map((turn): TurnActivity => ({ kind: "turn", session, turn })),
    ...session.rollbacks.map((rollback): RollbackActivity => ({ kind: "rollback", session, rollback })),
  ]);
}

export function recentActivities(sessions: readonly SessionSummary[]): HistoryActivity[] {
  return sortActivities(sessions.flatMap((session) => activitiesForSession(session)));
}

function sortActivities(activities: HistoryActivity[]): HistoryActivity[] {
  return activities.sort((left, right) => {
    const timeDifference = activityTime(right) - activityTime(left);
    if (timeDifference !== 0) {
      return timeDifference;
    }
    const sessionDifference = left.session.session_id.localeCompare(right.session.session_id);
    if (sessionDifference !== 0) {
      return sessionDifference;
    }
    if (left.kind !== right.kind) {
      return left.kind === "rollback" ? -1 : 1;
    }
    if (left.kind === "rollback" && right.kind === "rollback") {
      const streamDifference = (left.rollback.stream_id ?? "").localeCompare(right.rollback.stream_id ?? "");
      return streamDifference !== 0 ? streamDifference : right.rollback.sequence - left.rollback.sequence;
    }
    return left.kind === "turn" && right.kind === "turn"
      ? right.turn.turn_id - left.turn.turn_id
      : 0;
  });
}

function activityTime(item: HistoryActivity): number {
  const value = item.kind === "rollback"
    ? item.rollback.time
    : item.turn.last_activity ??
      item.turn.first_activity ??
      item.session.last_activity ??
      item.session.first_activity;
  const timestamp = value ? Date.parse(value) : Number.NaN;
  return Number.isFinite(timestamp) ? timestamp : 0;
}

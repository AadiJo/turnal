import { SessionSummary, SessionTurn, turnsForSession } from "../turnal/types";

export interface RecentTurn {
  session: SessionSummary;
  turn: SessionTurn;
}

export function recentTurns(sessions: readonly SessionSummary[]): RecentTurn[] {
  return sessions
    .flatMap((session) => turnsForSession(session).map((turn) => ({ session, turn })))
    .sort((left, right) => {
      const timeDifference = activityTime(right) - activityTime(left);
      if (timeDifference !== 0) {
        return timeDifference;
      }
      const sessionDifference = left.session.session_id.localeCompare(right.session.session_id);
      return sessionDifference !== 0 ? sessionDifference : right.turn.turn_id - left.turn.turn_id;
    });
}

function activityTime(item: RecentTurn): number {
  const value =
    item.turn.last_activity ??
    item.turn.first_activity ??
    item.session.last_activity ??
    item.session.first_activity;
  const timestamp = value ? Date.parse(value) : Number.NaN;
  return Number.isFinite(timestamp) ? timestamp : 0;
}

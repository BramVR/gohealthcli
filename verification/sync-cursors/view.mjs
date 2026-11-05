export const title = "Sync Cursor finalization";

export function state(v) {
  const backward = Object.keys(v.cursor).some(k => v.cursor[k] < v.highwater[k]);
  const dirty = Object.values(v.connectionDirty).some(Boolean);
  if (backward && dirty) return "Cursor moved backward; transaction still open";
  if (dirty) return "COMMIT rejected; transaction still open";
  if (backward) return "Older run completed; cursor moved backward";
  const completed = Object.values(v.status).filter(s => s === "sync_completed").length;
  if (completed === 2) return "Both runs completed; cursor at highwater";
  if (completed === 1) return "One run completed; cursor at highwater";
  if (Object.values(v.phase).every(p => p === "done")) return "Runs ended without cursor advance";
  if (Object.values(v.phase).some(p => p === "recovery")) return "Finalization failed; recovery pending";
  return "Two Sync Runs started";
}

export function step(action) {
  if (action.startsWith("Finalize")) return "Sync Run commits";
  if (action.startsWith("AbortFinalize")) return "Status or cursor write fails";
  if (action.startsWith("CommitRejected")) return "Reader lock rejects COMMIT; driver rolls back";
  if (action.startsWith("RecoveryExhausted")) return "Recovery does not persist";
  if (action.startsWith("Recover")) return "Recovery marks failed or canceled";
  return "No further writes";
}

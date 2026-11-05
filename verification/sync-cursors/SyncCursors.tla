--------------------------- MODULE SyncCursors ---------------------------
EXTENDS Integers, FiniteSets

CONSTANTS Runs, Keys
VARIABLES runKey, horizon, requestedOutcome, initialCursor,
          status, phase, cursor, highwater, connectionDirty

vars == <<runKey, horizon, requestedOutcome, initialCursor,
          status, phase, cursor, highwater, connectionDirty>>
Outcomes == {"sync_completed", "sync_failed", "sync_canceled"}
CanWrite == \A r \in Runs : ~connectionDirty[r]

Init ==
    /\ runKey \in [Runs -> Keys]
    /\ horizon \in [Runs -> 1..2]
    /\ requestedOutcome \in [Runs -> Outcomes]
    /\ initialCursor \in [Keys -> 0..1]
    /\ status = [r \in Runs |-> "sync_running"]
    /\ phase = [r \in Runs |-> "pending"]
    /\ cursor = initialCursor
    /\ highwater = initialCursor
    /\ connectionDirty = [r \in Runs |-> FALSE]

Finalize(r) ==
    /\ phase[r] = "pending"
    /\ CanWrite
    /\ status' = [status EXCEPT ![r] = requestedOutcome[r]]
    /\ phase' = [phase EXCEPT ![r] = "done"]
    /\ IF requestedOutcome[r] = "sync_completed"
       THEN /\ cursor' = [cursor EXCEPT ![runKey[r]] =
                  IF horizon[r] > @ THEN horizon[r] ELSE @]
            /\ highwater' = [highwater EXCEPT ![runKey[r]] =
                  IF horizon[r] > @ THEN horizon[r] ELSE @]
       ELSE UNCHANGED <<cursor, highwater>>
    /\ UNCHANGED <<runKey, horizon, requestedOutcome, initialCursor, connectionDirty>>

AbortFinalize(r) ==
    /\ phase[r] = "pending"
    /\ phase' = [phase EXCEPT ![r] = "recovery"]
    /\ UNCHANGED <<runKey, horizon, requestedOutcome, initialCursor,
                   status, cursor, highwater, connectionDirty>>

CommitRejected(r) ==
    /\ phase[r] = "pending"
    /\ CanWrite
    /\ phase' = [phase EXCEPT ![r] = "recovery"]
    /\ connectionDirty' = [connectionDirty EXCEPT ![r] = FALSE]
    /\ UNCHANGED <<runKey, horizon, requestedOutcome, initialCursor,
                   status, cursor, highwater>>

Recover(r) ==
    /\ phase[r] = "recovery"
    /\ CanWrite
    /\ status' = [status EXCEPT ![r] =
          IF requestedOutcome[r] = "sync_canceled"
          THEN "sync_canceled" ELSE "sync_failed"]
    /\ phase' = [phase EXCEPT ![r] = "done"]
    /\ UNCHANGED <<runKey, horizon, requestedOutcome, initialCursor,
                   cursor, highwater, connectionDirty>>

RecoveryExhausted(r) ==
    /\ phase[r] = "recovery"
    /\ phase' = [phase EXCEPT ![r] = "done"]
    /\ UNCHANGED <<runKey, horizon, requestedOutcome, initialCursor,
                   status, cursor, highwater, connectionDirty>>

Done == /\ \A r \in Runs : phase[r] = "done"
        /\ UNCHANGED vars

Next == (\E r \in Runs : Finalize(r))
     \/ (\E r \in Runs : AbortFinalize(r))
     \/ (\E r \in Runs : CommitRejected(r))
     \/ (\E r \in Runs : Recover(r))
     \/ (\E r \in Runs : RecoveryExhausted(r))
     \/ Done

TypeOK ==
    /\ status \in [Runs -> (Outcomes \cup {"sync_running"})]
    /\ phase \in [Runs -> {"pending", "recovery", "done"}]
    /\ cursor \in [Keys -> 0..2]
    /\ highwater \in [Keys -> 0..2]
    /\ connectionDirty \in [Runs -> BOOLEAN]

CompletedOnly == \A k \in Keys :
    cursor[k] = initialCursor[k] \/
    \E r \in Runs : /\ runKey[r] = k
                    /\ status[r] = "sync_completed"
                    /\ horizon[r] = cursor[k]

CursorHighwater == cursor = highwater
TransactionReleased == \A r \in Runs : ~connectionDirty[r]
=============================================================================

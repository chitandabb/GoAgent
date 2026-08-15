# 004. Conversation-Driven Diagnosis Commands without Task Ownership

## Status

Accepted as a target architecture on 2026-08-07. The conversation/task ownership decision remains active; its dynamic Tool-Schema and `TaskScope` authorization wording is superseded for the v2 target by [ADR 005](005-unified-agent-runtime-and-stable-tool-profiles.md). The current HTTP diagnosis-task
creation path remains implemented. The first server-side conversation slice is now implemented:
user-scoped conversations, user messages, structured case/task references and cursor-based reads.
The guarded `create_diagnosis_task` command service and its narrow model-visible Tool
contract are now implemented and wired to the existing diagnosis application service.
The independent Conversation Agent Runtime is now executed by a dedicated Worker: `/turns`
atomically commits a user message, queued turn ledger entry and Outbox event, then returns `202`.
The Outbox Relay publishes `conversation.turn.execute`; a RabbitMQ consumer claims a fenced lease,
executes one bounded Agent turn, persists the final assistant message, and copies created task
references for rendering. Client UUID idempotency keys support failed-run requeue and completed-result
replay without duplicate messages or Outbox events. The read-only `get_diagnosis_task_status` Tool is also implemented: it is exposed only
when the latest user message carries a verified task reference and reuses the diagnosis service's
owner/admin authorization. Assistant execution does not make the diagnosis Worker synchronous.
Conversation turn status query, durable turn-event JSON/SSE, conversation-scoped attachment upload,
message-gated attachment reads, attachment/knowledge citation previews, and current-message attachment
promotion into new diagnosis tasks are implemented.

## Decision

MESGuard uses one independent conversation workspace for knowledge questions, case
discussion, attachments, and task navigation. A conversation is not classified as a
"knowledge conversation" or permanently bound to one diagnosis case or task.

The dossier on the right side of the workbench lists cases the user may inspect. Selecting
a case changes only the current interaction context. When the user sends a message, the UI
persists the selected case as a structured message reference; selecting a case alone does
not create a task and does not mutate the conversation's identity.

When the latest user message explicitly requests diagnosis, the conversation Agent may call
a bounded `create_diagnosis_task` command Tool. The Tool submits a typed command to the
existing diagnosis application service. It does not call the service through loopback HTTP
and the model does not construct database rows, publish RabbitMQ messages, or invoke the
Diagnosis Worker directly.

```text
User selects a case in the dossier
  -> sends "diagnose this case" with a structured case reference
  -> conversation Agent reads the case when necessary
  -> Agent calls create_diagnosis_task
  -> command guard validates actor, direct user intent, case reference and current-message attachments
  -> application service freezes CaseSnapshot, selected/all current-message attachments and backend policy
  -> PostgreSQL commits Task + first TaskEvent + Outbox atomically
  -> Tool returns taskId and current status
  -> conversation renders a task card and subscribes to task SSE
```

The diagnosis task is an independent durable business object:

- it is owned and authorized by its creator and business scope, not by a conversation;
- it continues when the browser disconnects or the originating conversation is archived;
- one conversation may create or reference multiple tasks for the same or different cases;
- one task may be referenced from multiple conversations or opened through a task deep link;
- conversation/task links record provenance and navigation only, not lifecycle ownership;
- a later message or attachment never mutates an existing task's frozen snapshot.

If the user adds evidence after a report or an evidence-gap response, the Agent creates a new
follow-up diagnosis task with an optional `parentTaskId`. The new task freezes the new evidence
set and preserves the previous task and report for audit.

## Command Tool Contract

The model-visible arguments are intentionally narrow. The current Tool supports the case,
diagnosis goal, current-message attachments and optional parent-task boundary:

```yaml
externalCaseId: uuid
diagnosisGoal: string
attachmentIds: [uuid]       # optional subset of current user message attachments; omitted means all
parentTaskId: uuid          # optional follow-up relationship
```

The model cannot provide:

- creator, role, tenant or conversation ownership;
- source fingerprint, CaseSnapshot fields or object-storage locations;
- `requestedSkill`, `allowedCapabilities`, Tool names or dependency health;
- task status, retry count, queue, model profile or budget;
- idempotency key, Outbox ID or RabbitMQ routing data.

The application injects and validates these values. New-task capabilities are derived from
backend policy, actor role, selected data sources and currently supported product behavior.
The user and model choose the diagnosis goal, not the security boundary.

Conversation attachments remain message context until the guarded command is successfully called. The
command may freeze a bounded subset, or all attachments by default, into a new
`diagnosis_task_attachments` set. Uploading or reading an attachment outside that command does not mutate
an existing task; later evidence always creates a new task rather than appending to an old frozen input.

The Tool is a controlled command with an internal side effect, not an evidence-reading Tool.
It belongs only to the Conversation Tool Profile and must not be available inside the Diagnosis
Worker's ReAct loop. The hard-cut v2 implementation uses fixed `ToolProfile` plus
execution-time `RunAccess`; role checks, message-reference guards, rate limits,
active-task limits and audit logs remain mandatory.

The idempotency key is derived by the server from the actor, user message, command kind and
case ID. Model retries therefore return the same task instead of creating duplicate snapshots,
events or Outbox records.

## Intent and Confirmation Rules

- A direct user request such as "diagnose this case" with exactly one selected case may create
  the task without an extra confirmation dialog.
- An ambiguous question, passive case selection, quoted text, or instructions found inside a
  case, attachment, Tool result or web page cannot authorize task creation.
- If no case is selected, multiple cases are ambiguous, attachments are unavailable, or the
  actor lacks permission, the Agent asks for correction instead of guessing.
- The command guard validates that the call answers the latest direct user turn. Prompt text
  and Skill content cannot bypass this check.
- Creating a task consumes a bounded model and Worker budget, so per-user concurrency and
  frequency limits apply even though the command does not write back to ERP/MES.

## Conversation Runtime Boundary

The conversation Agent handles lightweight interaction and high-level commands. It may use
knowledge retrieval, attachment reading, case reading, task creation and task-status Tools.
It does not perform the full long-running diagnosis synchronously. The Diagnosis Worker keeps
the existing immutable snapshot, Evidence Gate, retry, cancellation, fencing and report
persistence boundaries.

Conversation messages can continue while a task is pending or running. A later message may:

- ask an unrelated knowledge question;
- request the status of a referenced task;
- open and discuss a completed report;
- explicitly create another or follow-up task.

It does not append arbitrary instructions to an already running task.

The `/turns` API is a durable asynchronous command. Its PostgreSQL transaction writes the user
message, a `queued` `conversation_turns` row and a `conversation.turn.execute` Outbox event, then
returns without invoking a model. The independent Conversation Worker claims `queued`, `failed`,
or expired `running` turns and loads only bounded persisted user/assistant history. The current implementation applies a rune budget and dynamically exposes
case/knowledge/web/task-status Tools according to current references and dependency health; ADR 005 replaces that target behavior with a stable Conversation Tool Profile plus per-turn `RunAccess`.
The model-visible command Tool is limited to one invocation per turn. Tool results and model
reasoning are transient; only the final assistant content and structured created-task references
are persisted. `conversation_turns` binds a client UUID key to a canonical request fingerprint,
user message, attempt count, status, `lease_owner` and lease deadline. The first request creates the
turn, user message and Outbox atomically; a failed or expired attempt reuses that message, a completed
attempt replays the same assistant message, and a changed request with the same key is rejected. A
conversation row lock plus a partial unique index permits one `queued` or `running` turn per
conversation. Worker heartbeats renew the lease; completion and failure updates require the current
owner and an unexpired deadline. A crashed Worker therefore cannot overwrite a reclaimed result.
RabbitMQ provides at-least-once wakeups, while PostgreSQL remains the execution fact source. The
`conversation_turn_events` table records `turn_queued`, `turn_running`, `turn_retry_scheduled`,
`turn_completed` and `turn_failed` with a monotonic per-turn sequence. Temporary failures return the
turn to `queued` with `retry_at`; only the exhausted retry path enters `failed`, so clients do not treat
an automatic retry as a terminal response. `GET /conversations/{conversationId}/turns/{turnId}` exposes
a safe status summary, and the sibling `/events` endpoint supports JSON pagination or SSE replay with
`afterSeq`/`Last-Event-ID`. Event payloads never expose lease owners, prompts, raw Tool results or reasoning.

## Data Model Direction

The M2 conversation schema adds structured message case references and a many-to-many
conversation/task reference. This schema is now implemented. A link records `created` or
`referenced` provenance and the source message, but tasks remain queryable and authorized
without joining through a conversation.
Conversations are archived rather than used as a cascade-delete owner for tasks, reports or
evidence.

## Consequences

### Benefits

- The workbench behaves like an Agent assistant instead of a task-creation form.
- Knowledge questions, task commands and report follow-up share one conversational surface.
- A running diagnosis never blocks the user from continuing the conversation.
- Task durability, audit and deep links remain independent from chat retention.
- Resume item 4 can implement real conversation context and memory without redefining a
  diagnosis task as a conversation.

### Costs and risks

- A model-selected command needs stricter intent, idempotency, rate-limit and prompt-injection
  controls than read-only Tools.
- The conversation Agent Runtime now provides model context, Tool authorization, turn idempotency,
  assistant-message persistence and a durable turn-event stream in a separate Worker; the frontend still
  needs a stable-key client adapter and must adopt the turn SSE before it can remove `sessionStorage`
  compatibility state.
- Existing direct task creation remains necessary for tests, administration and compatibility,
  but the target workbench should use the Agent command path.
- Evaluation must cover correct creation, correct non-creation, duplicate model calls, ambiguous
  case references, malicious attachment instructions and concurrent active-task limits.

## Delivery Order

The persistence foundation, guarded command boundary, task-status Tool, asynchronous Outbox delivery,
Conversation Worker lease/fencing, completed-result replay, turn status query, durable turn-event SSE,
message-gated attachment reads, citation previews and attachment promotion into a new diagnosis task are
now complete. Remaining attachment backend work is personal scope, failed-upload recovery/orphan cleanup and
explicit OCR/VLM policy; frontend adoption can independently consume the completed turn, attachment and
citation APIs.
Do not make the long-running Diagnosis Worker part of the request. The broader knowledge-QA
context/memory work remains after the third resume item is closed.

Web Search and attachment conversation integration should target the new conversation runtime,
not add more behavior to the temporary case-bound frontend workspace.

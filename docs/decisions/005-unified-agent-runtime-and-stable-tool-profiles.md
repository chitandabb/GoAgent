# 005. Unified Agent Runtime Access and Stable Tool Profiles

## Status

Accepted as the target architecture on 2026-08-14. Domain contracts, profile definitions, the legacy `TaskScope -> RunAccess` adapter and the execution-time Permission Guard are implemented. Production Tool Schema selection is now wired to fixed deployment-level `ToolProfile`s: `ToolCatalog.ResolveProfile(profileID)` is the only production entry, both Runners resolve fixed Profile IDs, and blocked/run-limit state never deletes Schema. `ToolsFor(TaskScope)` and `EvaluationBaselineToolsFor` remain evaluation-only APIs, and `TaskScope` remains only as the compatibility input for `RunAccess` derivation and legacy Tool-internal resource checks. In this wiring state the Conversation Profile deliberately excludes SQL Tools; Conversation Text-to-SQL lands in a later slice together with `turn_context` and SQL resource checks. The current Tool Selection evaluation validates the fixed-Profile assembly mechanism, the real Eino Skill Middleware and the controlled evaluation Tool contract; it does not claim to reproduce every production Knowledge/Web/Attachment Adapter, and the formal production-entry v2 retest is deferred until the Conversation SQL/`turn_context` slice lands.

## Decision

MESGuard keeps two execution modes, Conversation and Diagnosis, while treating SQL, knowledge, code, attachment, case and Web access as shared Tools rather than separate task types. The model decides which read-only Tool to call and in what order; application code owns authorization, resource ownership, QueryGuard, budgets, retries, degradation, evidence validation and state-changing command guards. No independent intent-classifier service and no Skill-to-Tool authorization binding are introduced.

`Capability` and the overloaded `TaskScope` are retired from the target vocabulary. A deployment-level `ToolProfile` determines the stable model-visible Tool Schema. A run-level `RunAccess` determines whether a visible Tool may execute and which resources it may touch. The Catalog keeps the existing guarded-wrapper seam: profile resolution selects Schema, the wrapper reads `RunAccess` from `context.Context` and checks coarse `Permission`, and — as the target end-state — the Tool implementation checks concrete `ResourceGrant` values before the existing infrastructure safety controls run. Currently only the Permission half of that seam is wired; the unified `ResourceGrant` projection and Tool-internal checks land in the `turn_context` + Conversation Text-to-SQL slice, and existing attachment/task Tools keep their `CommandContext`/owner checks until then.

Diagnosis task creation freezes an `InvestigationPolicy`. Every Diagnosis run derives `RunAccess` from the intersection of that policy and the current emergency-revocation/resource-disable ceiling. Therefore:

```text
RunAccess.Permissions is a subset of InvestigationPolicy.Permissions
RunAccess.ResourceGrants are subsets of InvestigationPolicy.ResourceGrants
```

Conversation has no persisted Investigation Policy. Each turn derives `RunAccess` from the authenticated actor, conversation ownership and current structured references. Neither the model, a Skill, Tool arguments nor a later deployment configuration may expand an old Diagnosis task.

The target Conversation Profile includes direct knowledge retrieval and safe Text-to-SQL Tools plus the guarded `create_diagnosis_task` command; the current wiring keeps SQL out of the Conversation Profile until the SQL resource-check slice lands. The Diagnosis Profile reuses read-only evidence Tools and Diagnosis Skills but never exposes task creation. Skill remains a diagnosis SOP only; it neither declares nor grants Tools.

`ToolProfile` is a startup assembly snapshot frozen within one process-start/deployment Epoch: the Profile content is decided by which Adapters complete construction successfully at startup. After the Runtime starts, message references, `TaskScope`, `RunAccess`, call counts and transient dependency execution failures never delete Tool Schema; temporary dependency failure returns a structured unavailable/degraded result. If the service restarts and an Adapter cannot complete construction, that is a new startup Profile/Epoch, which must produce a new Tool Schema fingerprint, and its evaluation data must not be mixed with data from the old Profile. This document deliberately avoids the absolute claim that "only a configuration change can change the Profile": the guarantee is scoped to one startup Epoch.

The current slice implements the execution-time Permission Guard only. The unified projection of `ResourceGrant` into Tool-internal resource checks is not yet complete and lands with the `turn_context` + Conversation Text-to-SQL slice; existing attachment/task Tools keep their `CommandContext`/owner checks until then.

Conversation `turn_context` (当前引用投影追加到 user 消息尾部) is target design for the next slice and is not wired yet; until then references keep entering the prompt through the existing message projection. Diagnosis `task_context` is likewise target design: it contains only task-level stable policy projections appended to the system instruction; current evidence gaps and attachments remain in user input. Existing canonical Tool Schema fingerprints and Prompt Epoch identity are reused. Evaluation records must bind `observationSchemaVersion`, `toolProfileId`, Tool names, Tool Schema fingerprint, system Prompt version, model Profile fingerprint and implementation revision; wide/filtered arms pair only when schema version, model Profile fingerprint and implementation revision/dirty state all match and neither arm is dirty.

## Consequences

- Main chat can perform direct knowledge Q&A and safe Text-to-SQL without first creating a Diagnosis task.
- Mixed questions remain Agentic and may use several read-only Tools in one turn without a single-label router.
- Tool Schema remains stable within one deployment, improving Prompt Cache comparability, while execution remains fail-closed per user and resource.
- The legacy `TaskTypeKnowledge`, `ToolCapability`, dependency-health-driven Schema filtering and caller-selected diagnosis capabilities become compatibility debt and must be removed only after production-entry evaluations pass.
- Existing Tool internals, QueryGuard, Evidence Gate, Worker state machines, RAG and context governance are preserved; this decision changes their orchestration contract, not their safety implementations.

# 005. Unified Agent Runtime Access and Stable Tool Profiles

## Status

Accepted as the target architecture on 2026-08-14. The first domain contracts and profile definitions are implemented, while production wiring still uses the legacy `TaskScope` path until the compatibility migration is complete.

## Decision

MESGuard keeps two execution modes, Conversation and Diagnosis, while treating SQL, knowledge, code, attachment, case and Web access as shared Tools rather than separate task types. The model decides which read-only Tool to call and in what order; application code owns authorization, resource ownership, QueryGuard, budgets, retries, degradation, evidence validation and state-changing command guards. No independent intent-classifier service and no Skill-to-Tool authorization binding are introduced.

`Capability` and the overloaded `TaskScope` are retired from the target vocabulary. A deployment-level `ToolProfile` determines the stable model-visible Tool Schema. A run-level `RunAccess` determines whether a visible Tool may execute and which resources it may touch. The Catalog keeps the existing guarded-wrapper seam: profile resolution selects Schema, the wrapper reads `RunAccess` from `context.Context` and checks coarse `Permission`, and the Tool implementation checks concrete `ResourceGrant` values before the existing infrastructure safety controls run.

Diagnosis task creation freezes an `InvestigationPolicy`. Every Diagnosis run derives `RunAccess` from the intersection of that policy and the current emergency-revocation/resource-disable ceiling. Therefore:

```text
RunAccess.Permissions is a subset of InvestigationPolicy.Permissions
RunAccess.ResourceGrants are subsets of InvestigationPolicy.ResourceGrants
```

Conversation has no persisted Investigation Policy. Each turn derives `RunAccess` from the authenticated actor, conversation ownership and current structured references. Neither the model, a Skill, Tool arguments nor a later deployment configuration may expand an old Diagnosis task.

The Conversation Profile includes direct knowledge retrieval and safe Text-to-SQL Tools plus the guarded `create_diagnosis_task` command. The Diagnosis Profile reuses read-only evidence Tools and Diagnosis Skills but never exposes task creation. Skill remains a diagnosis SOP only; it neither declares nor grants Tools. Temporary dependency failure returns a structured unavailable/degraded result and does not remove Tool Schema. Only a deployment configuration change may produce a different Profile.

Conversation `turn_context` is appended to the current user message so stable system, Tool Schema and history prefixes remain cacheable. Diagnosis `task_context` contains only task-level stable policy projections and is appended to the system instruction; current evidence gaps and attachments remain in user input. Existing canonical Tool Schema fingerprints and Prompt Epoch identity are reused. Evaluation records must bind `toolProfileId`, Tool names, Tool Schema fingerprint, system Prompt version, model Profile fingerprint and implementation revision; results from different fingerprints are not aggregated as one experiment arm.

## Consequences

- Main chat can perform direct knowledge Q&A and safe Text-to-SQL without first creating a Diagnosis task.
- Mixed questions remain Agentic and may use several read-only Tools in one turn without a single-label router.
- Tool Schema remains stable within one deployment, improving Prompt Cache comparability, while execution remains fail-closed per user and resource.
- The legacy `TaskTypeKnowledge`, `ToolCapability`, dependency-health-driven Schema filtering and caller-selected diagnosis capabilities become compatibility debt and must be removed only after production-entry evaluations pass.
- Existing Tool internals, QueryGuard, Evidence Gate, Worker state machines, RAG and context governance are preserved; this decision changes their orchestration contract, not their safety implementations.

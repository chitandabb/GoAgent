# MESGuard Documentation

This directory is the single location for project documentation.

| Document | Purpose |
| --- | --- |
| [Architecture](architecture.md) | Dependency direction, package boundaries, and repository layout |
| [Development](development.md) | Local configuration, Docker, tests, and volume operation |
| [Roadmap](roadmap.md) | Current delivery state and ordered next milestones |
| [ADR 001](decisions/001-eino-v0.9-for-mesguard.md) | Why Eino stays behind the project-owned Agent port |
| [ADR 002](decisions/002-modular-monolith-architecture.md) | Why MESGuard uses a modular monolith with explicit adapters |

Documentation rules:

- `README.md` at the repository root is only the project entry point.
- Operational instructions belong in `development.md`.
- Planned work belongs in `roadmap.md`.
- Long-lived design decisions belong in `decisions/`.
- Do not duplicate the same command, status, or design explanation in multiple
  documents.

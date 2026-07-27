# 002. Prefer Mature Open-Source Components over In-House Implementations

## Decision

For any capability that is not MESGuard domain logic, the default is to adopt a
mature open-source solution instead of building it in-house. This applies to
both backend and frontend work.

Before implementing any non-trivial capability by hand, first search for an
existing solution and evaluate it against the selection criteria below. If the
team still chooses to build in-house where a viable library exists, the commit
or pull request must record a one-line justification.

### Selection criteria for a dependency

- Actively maintained and widely adopted in its ecosystem.
- Permissive license (MIT, BSD, Apache-2.0) compatible with this repository.
- Reasonable transitive dependency footprint.
- Prefer small, focused libraries over kitchen-sink frameworks; a dependency
  must be adoptable without bending it against its own design to satisfy the
  contracts in `design/api.md` and `design/database.md`.

### Never build in-house

These are security- or correctness-critical and must always come from
established libraries:

- Cryptographic primitives, password hashing, and token generation
  (`golang.org/x/crypto`, standard library `crypto/*`).
- Parsers for complex formats (SQL, HTML, office documents, images).
- Protocol implementations (HTTP, AMQP, S3, database wire protocols).
- Database migration engines (goose), ORM/query layers (GORM), and validation
  engines (go-playground/validator).

### Hand-writing is acceptable

Thin glue code should be written in-house when all of the following hold:

- It encodes project-specific contracts (error codes, response envelope,
  session semantics, state-machine rules) that no external library can know.
- It stays thin — roughly a screenful, not a subsystem.
- Adopting a framework for it would cost more than the glue itself
  (integration, upgrade churn, supply-chain surface).

Examples: apperror-to-HTTP translation, the `TxManager` wrapper, repository
error mapping, role-check middleware.

### Frontend

The workbench uses shadcn/ui as a source-owned component foundation while
`design/DESIGN-apple.md` and `web/src/styles/tokens.css` continue to own the
visual language. The approved migration is defined in
`superpowers/specs/2026-07-27-shadcn-migration-design.md`:

- Existing interactive primitives migrate in one pass to shadcn/ui, Radix,
  TanStack Table, and sonner rather than leaving two component systems.
- Pure presentation components without an established equivalent may remain a
  thin project-owned layer.
- New complex widgets such as virtualized tables, date pickers, charts,
  rich-text editors, and drag-and-drop must use an established library.
- Data fetching, routing, and state stay on the already-adopted stack
  (TanStack Query, React Router); do not hand-roll parallel mechanisms.

## Rationale

Reimplementing solved problems costs implementation time twice: once to write
and once to debug edge cases the mature library already handles. The risk is
highest for security-sensitive code, where a subtle mistake is invisible until
exploited. Conversely, importing a heavy framework to avoid fifty lines of
glue creates lock-in and upgrade churn that outlives the convenience. The
decision therefore cuts both ways: buy the wheels, write the glue.

## Consequences

- Vertical slices start with a short survey of existing solutions, not with a
  blank file.
- In-house implementations of library-covered functionality require a recorded
  justification and are a valid review objection without one.
- Session management, rate limiting, and similar middle-ground capabilities
  are evaluated against existing libraries (for example `alexedwards/scs`,
  `ulule/limiter`) before any hand-written fallback.
- Dependency additions are reviewed against the selection criteria; "it saves
  typing" alone does not justify a large framework.
- The approved shadcn/ui migration replaces existing interactive primitives in
  one pass; design tokens remain the only visual styling source.

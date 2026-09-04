# Store immutable Revision content as versioned JSONB

Each Revision stores its complete structured content—Content blocks, Diagram, Provenance, and Source refs—as one schema-versioned JSONB payload, while identity, ownership, parentage, origin, and activation remain relational columns. This keeps a Revision atomic and aligned with structured model output; application validation and derived search indexes replace a normalized content graph, avoiding duplicated representations that could drift.

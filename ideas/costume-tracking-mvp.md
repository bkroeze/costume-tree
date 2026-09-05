# Costume Tracking App — MVP Idea

## Source

Shared conversation: <https://chatgpt.com/s/t_6a9b8a6a385c8191adcce0347dc2f0bf>

Reviewed 2026-09-04.

## One-sentence product idea

A fast, phone-friendly production workspace that tracks every physical costume piece by actor, category, status, progress, and next action, replacing an actor-by-actor manual list.

## Review verdict

Strong MVP direction. The concept is narrow, operational, and centered on the real unit of work: an individual physical piece. The proposed screens and acceptance criteria cover the essential workflow without pulling accounting, inventory, or project-management concerns into the first release.

The main implementation risk is not feature breadth; it is leaving a few derived-data rules ambiguous. Decide those rules before building so dashboard numbers remain trustworthy.

## Core workflow

1. Create or select the active production.
2. Add actors and optional roles/notes.
3. Add one record per physical costume piece.
4. Assign a reusable item type and optional description.
5. Track status, percentage, next action, blocker, and notes.
6. Work primarily from actor detail, with production-wide search/filter views for exceptions.
7. Use derived actor, production, and item-type summaries instead of manually maintained totals.

## MVP data model

- **Production**: `id`, `name`; owns actors, item types, and costume items.
- **Actor**: `id`, `production_id`, `name`, optional `role`, optional `notes`.
- **ItemType**: `id`, `production_id`, `name`; reusable category such as Pants or Jacket.
- **CostumeItem**: `id`, `production_id`, `actor_id`, human-readable immutable piece ID, item type, description, status, completion percentage, next action, blocker, notes, timestamps.

Keep the production relationship on `CostumeItem` even though actor already implies it. It makes scoping, querying, and future reassignment/validation explicit.

The physical-piece identifier should be unique within a production at minimum, never reused, searchable, and formatted like `C-0001`. If cross-production lookup is likely, use a globally unique internal key while keeping the displayed production sequence.

## Derived rules to settle

- **Completion rollup:** use the arithmetic mean of item percentages for an actor and production, unless the user explicitly needs weighted pieces. Empty actor/production sets should display “—”, not 0%.
- **Status/count precedence:** status is explicit; percentage is numeric. Define whether `Complete` forces 100%, whether 100% forces `Complete`, and whether `Blocked` can coexist with 100%.
- **Blocked definition:** a non-empty blocker field should make an item discoverable as blocked, or blocked should be status-only. Prefer status as the canonical filter and show a blocker warning when text exists without `Blocked`.
- **Incomplete definition:** anything below 100%, including `Blocked`; document whether items with missing progress are treated as 0% or unknown.
- **Item-type scope:** item types should belong to a production so two productions can use different vocabularies without collisions.
- **Deletion/ID behavior:** avoid hard deletion of physical-item records if IDs must never be reused. Archive or soft-delete instead, and exclude archived items from active rollups.

## Screen priorities

### Production dashboard / actor list

Show actor, role, item count, calculated completion, and blocked/incomplete count. Put production totals nearby: total pieces, complete, in progress, and blocked. Make an actor row open the working view.

### Actor detail — primary working screen

Show all pieces in a compact editable table with ID, type, description, status, percentage, and next action. Keep notes/blocker editing close to the row or in a lightweight detail expansion. Add an obvious “Add Costume Item” action and preserve unsaved edits safely.

### All costume items

Provide search by displayed ID plus filters for actor, item type, status, blocked state, and completion range. ID search is a core retrieval path, not a later enhancement.

### Summary

Group derived counts by item type. Counts should update from the same item query used by the rest of the app; do not maintain summary counters separately.

## Import / fast entry

Bulk entry is valuable because the starting data is actor-grouped. For MVP, support a deliberately simple parser for actor headings followed by item-type lines. Preserve the original order where practical and show a review step before creating records.

Do not make spreadsheet compatibility a prerequisite. The parser must define how it handles blank lines, duplicate item types, unknown actors, and lines that cannot be recognized as an actor or item.

## Recommended implementation order

1. Production and actor creation.
2. Costume-item creation with automatic IDs.
3. Actor detail editing for status, percentage, notes, next action, and blocker.
4. Derived actor/production rollups.
5. All-items search and filters, especially ID lookup and blocked work.
6. Item-type summary.
7. Simple bulk entry after the manual path is reliable.

## Deliberately deferred

Accounting, purchasing, vendors, authentication, photos, barcode/QR scanning, label printing, warehouse inventory, costume reuse across productions, and full task management. Keep fields and identifiers extensible enough that these can be added without changing the core item workflow, but do not pre-build them.

## Acceptance checklist

- [ ] Create a production.
- [ ] Add actors and optional role/notes.
- [ ] Add multiple physical items to an actor.
- [ ] Generate immutable, searchable item IDs.
- [ ] Categorize items independently from descriptions.
- [ ] Edit status, percentage, next action, blocker, and notes quickly.
- [ ] Calculate actor and production completion.
- [ ] Count items by type and status.
- [ ] Search by ID.
- [ ] Filter unfinished and blocked work.
- [ ] Work comfortably on laptop and phone.
- [ ] Validate simple bulk entry without making it the primary path.

## Open product decisions

1. Is the first release strictly single-production in the UI, or should production switching be visible from the beginning?
2. Is an actor allowed to have two otherwise identical pieces of the same type? The model should allow it; the UI should make duplicates intentional rather than merge them.
3. Should percentage be a free 0–100 value, fixed increments, or status-only shortcuts with optional fine-grained editing?
4. Should completed items remain editable and searchable indefinitely, or be archivable?
5. Should ID sequences reset per production or continue globally?

## Bottom line

Build the actor/item/status workflow first. Make IDs, derived rollups, and search reliable before adding import or reporting polish. The app succeeds when a costumer can answer “what is this piece, who needs it, what remains, and what is blocking it?” in seconds.

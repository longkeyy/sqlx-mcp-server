# Object listing

`list_objects` defaults to 1000 displayed entries, with `page_size` from 1 to 1000, `cursor`, `group_shards` (default true), and optional `group_id` expansion. Regex filtering precedes grouping. Groups require four tables in the same schema with equal suffix width: one/two decimal digits or two hexadecimal digits. Mixed hexadecimal series form one group. Labels describe suspected naming conventions, not database partition or schema guarantees. Disable grouping to list real objects; describe requires a real name.

Cursors bind to instance, filters, grouping, expansion, and the resulting metadata snapshot. Metadata changes invalidate a cursor rather than silently skipping entries. No row data or counts are read. Metadata is read afresh on each call; no unbounded cache is retained. Group expansion is paginated with the same options.

Integration: existing AppServer tool schemas and handlers carry these capabilities; preserve all optional arguments when forwarding unified routing calls. Browser metadata adapters should reuse the tool result rather than duplicate grouping rules.

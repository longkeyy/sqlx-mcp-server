# SQL MCP Server Design

## Goal

This server exposes a single configured SQL database as an MCP server. It is intended to let an LLM inspect database objects, read data, and execute database operations through a small tool surface.

## Current Tool Surface

The server registers four MCP tools:

- `query`: executes a SQL query and returns rows.
- `execute`: executes a write operation such as DML, DDL, or stored procedure calls.
- `list_objects`: lists database objects by type.
- `get_schema_object_definition`: returns the definition or schema details for a database object.

The supported object types are:

- `table`
- `view`
- `procedure`
- `function`

## Current Runtime Model

The process connects to one database at startup from a URL-style DSN. The active driver and connection pool are kept as package-level state. Tool handlers use that shared connection pool and switch on the active driver for database-specific metadata queries.

Supported database drivers:

- MySQL
- PostgreSQL
- SQLite
- SQL Server

Supported MCP transports:

- stdio
- SSE
- streamable HTTP

## Current Result Format

Query results are returned as plain text. The text contains a row count and a Go-formatted slice of row maps.

Object listings are returned as plain text lines grouped by object type. The response also appends an XML-like context attachment that tells the LLM which schema and dialect are active, lists the discovered objects, and asks the LLM to fetch definitions before using objects.

Object definitions are returned as plain text. Table descriptions are formatted as a simple pipe-delimited table, while views, procedures, and functions return the retrieved definition text.

## Current Safety Model

The `query` tool is described as read-only, but the current handler directly executes the supplied SQL through the database driver. The `execute` tool directly executes write statements. The strongest runtime safety boundary is therefore the configured database account and its permissions.

Object names used for metadata lookup are sanitized by escaping single quotes before being interpolated into driver-specific SQL strings.

## Known Limitations

- Query output is not a stable machine-readable format.
- Map iteration may produce unstable column order.
- `query` is not enforced as read-only by code.
- Metadata lookup relies on string interpolation for object names.
- PostgreSQL metadata lookup assumes the `public` schema in several paths.
- The context attachment mixes database facts with instructions to the LLM.
- Most MCP tool, metadata, and formatting logic is concentrated in one large source file.

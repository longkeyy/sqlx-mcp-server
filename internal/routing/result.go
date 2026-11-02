package routing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

// spliceInstanceField adds (or replaces) the top-level "instance" field of a
// JSON object without round-tripping the payload through map[string]any. Going
// through a map would reorder keys, lose integer precision for values above
// 2^53 and reformat the document; clients use the payload to inspect query
// results, so the router only splices in its own field.
//
// Text that is not a JSON object (arrays, scalars, the legacy execute
// confirmation message) is returned unchanged.
func spliceInstanceField(text, id string) string {
	if !json.Valid([]byte(text)) {
		return text
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	token, err := decoder.Token()
	if err != nil {
		return text
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return text
	}
	open := int(decoder.InputOffset()) - len(delim.String())

	for {
		token, err = decoder.Token()
		if err == io.EOF {
			// The object ended before a top-level "instance" key.
			closeAt := int(decoder.InputOffset()) - len("}")
			return insertInstanceField(text, open, closeAt, id)
		}
		if err != nil {
			return text
		}
		if closing, ok := token.(json.Delim); ok && closing == '}' {
			closeAt := int(decoder.InputOffset()) - len("}")
			return insertInstanceField(text, open, closeAt, id)
		}
		key, ok := token.(string)
		if !ok {
			return text
		}
		valueStart := int(decoder.InputOffset())
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return text
		}
		valueEnd := int(decoder.InputOffset())
		if key != "instance" {
			continue
		}
		replacement, err := json.Marshal(id)
		if err != nil {
			return text
		}
		spliced := text[:valueStart] + ":" + string(replacement) + text[valueEnd:]
		return reindent(spliced)
	}
}

// insertInstanceField inserts `"instance": "<id>"` directly after the opening
// brace at offset open. Existing members keep their order and formatting.
func insertInstanceField(text string, open, closeAt int, id string) string {
	encoded, err := json.Marshal(id)
	if err != nil {
		return text
	}
	member := `"instance":` + string(encoded)
	body := text[open+1 : closeAt]
	if strings.TrimSpace(body) == "" {
		return reindent(text[:open+1] + member + text[closeAt:])
	}
	return reindent(text[:open+1] + member + "," + body + text[closeAt:])
}

// reindent applies the same two-space layout the baseline tools use
// (json.MarshalIndent) after a splice changed the document's width.
func reindent(text string) string {
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, []byte(text), "", "  "); err != nil {
		return text
	}
	return formatted.String()
}

// tagResult returns a copy of result whose JSON text content and structured
// content carry the instance that produced the payload. Failed results are
// returned unchanged: an error belongs to the endpoint the client called, and
// tagging it would only be needed to debug routing.
func tagResult(result *mcp.CallToolResult, id string) *mcp.CallToolResult {
	if result == nil || result.IsError {
		return result
	}
	tagged := *result
	if len(result.Content) > 0 {
		content := make([]mcp.Content, len(result.Content))
		for i, item := range result.Content {
			text, ok := item.(mcp.TextContent)
			if !ok || text.Type != mcp.ContentTypeText {
				content[i] = item
				continue
			}
			text.Text = spliceInstanceField(text.Text, id)
			content[i] = text
		}
		tagged.Content = content
	}
	if structured, ok := result.StructuredContent.(map[string]any); ok {
		clone := make(map[string]any, len(structured)+1)
		for key, value := range structured {
			clone[key] = value
		}
		clone["instance"] = id
		tagged.StructuredContent = clone
	}
	return &tagged
}

// toolError builds a failed tool result. The message is the one the client
// sees, so routing failures name the tool or instance involved.
func toolError(format string, args ...any) *mcp.CallToolResult {
	return mcp.NewToolResultError(fmt.Sprintf(format, args...))
}

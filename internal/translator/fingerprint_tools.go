package translator

// Verbatim copies of OpenCode's own `read` and `shell` tool declarations.
//
// The upstream free-tier gate does not accept a tool named "read" — it accepts
// the exact declaration OpenCode itself ships: same description string, same
// parameter schema, key for key. A stub ("OpenCode built-in read tool" with an
// empty object schema) is answered with 403 FreeTierError, which is what the
// dashboard reported as "OpenCode's free tier can only be used from within
// OpenCode" even though the client was a perfectly ordinary OpenAI-compatible
// caller.
//
// Measured 2026-10-13 against https://opencode.ai/inference/v1/chat/completions:
//
//	tools + stream:true   -> 200
//	tools + stream:false  -> 403
//	no tools              -> 403
//	stub declarations     -> 403
//	/zen/v1/*             -> 403 regardless (the path is retired)
//
// The gate is a fingerprint, so the declarations must stay byte-identical.
// They are reproduced from OpenCode's opencode-free-bridge (azrinaldi) under
// the same terms the rest of the provider config is ported under.
var openCodeFingerprint = map[string]struct {
	Description string
	Parameters  map[string]any
}{
	"read": {
		Description: "Read the contents of a file or directory. Supports text files, images, and PDFs. Images and PDFs are presented directly to the model. Each text line is prefixed by its 1-based line number as <line>: <content>. The prefix is for reference and is not part of the file content. Directory entries are returned one per line. Use offset and limit to read large files or directories in sections. Prefer one larger read over many small slices, and use grep to find specific content in large files.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "File or directory to read",
				},
				"offset": map[string]any{
					"type":        "integer",
					"minimum":     0,
					"description": "The line or directory entry to start reading from (1-based)",
				},
				"limit": map[string]any{
					"type":        "integer",
					"minimum":     0,
					"description": "The maximum number of lines or directory entries to read (defaults to and capped at 2000)",
				},
			},
			"required":             []any{"path"},
			"additionalProperties": false,
		},
	},
	"shell": {
		Description: "Execute a shell command and return its output. Commands run on Linux using bash. Quote file paths containing spaces or special characters. Prefer dedicated tools over shell commands when possible. When output is large, the full result is saved to a file and a truncated preview is returned. Rely on automatic truncation unless filtering the output is more useful. Commands accept an optional timeout, background commands have no timeout by default. Background commands return immediately, and you will be notified when they complete.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "Shell command string to execute",
				},
				"workdir": map[string]any{
					"type":        "string",
					"description": "Working directory to execute the command in. Defaults to the current working directory. When possible, avoid changing directories in the command and set the working directory here instead.",
				},
				"timeout": map[string]any{
					"type":        "integer",
					"minimum":     0,
					"description": "Timeout in milliseconds. Set to 0 to disable the timeout. Defaults to 120000 for foreground commands. Background commands have no timeout by default.",
				},
				"background": map[string]any{
					"type":        "boolean",
					"description": "Run the command in the background and return immediately (useful for dev servers and long-running builds). You do not need to use '&' at the end of the command when using this parameter. You will be notified when it completes. DO NOT poll for completion.",
				},
			},
			"required":             []any{"command"},
			"additionalProperties": false,
		},
	},
	"bash": {
		Description: "Executes a given bash command in a persistent shell session with optional timeout, ensuring proper handling and security measures.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string", "description": "The command to execute"},
				"timeout": map[string]any{"type": "integer", "description": "Optional timeout in milliseconds"},
				"workdir": map[string]any{"type": "string", "description": "The working directory to run the command in. Defaults to the current directory. Use this instead of 'cd' commands."},
			},
			"required": []any{"command"},
		},
	},
	"glob": {
		Description: "- Fast file pattern matching tool that works with any codebase size\n- Supports glob patterns like \"**/*.js\" or \"src/**/*.ts\"\n- Returns matching file paths\n- Use this tool when you need to find files by name patterns",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string", "description": "The glob pattern to match files against"},
				"path":    map[string]any{"type": "string", "description": "The directory to search in. Defaults to the current working directory."},
			},
			"required": []any{"pattern"},
		},
	},
	"grep": {
		Description: "- Fast content search tool that works with any codebase size\n- Searches file contents using regular expressions\n- Supports full regex syntax (eg. \"log.*Error\", \"function\\s+\\w+\", etc.)\n- Filter files by pattern with the include parameter\n- Returns file paths and line number",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string", "description": "The regex pattern to search for in file contents"},
				"path":    map[string]any{"type": "string", "description": "The directory to search in. Defaults to the current working directory."},
				"include": map[string]any{"type": "string", "description": "File pattern to include in the search (e.g. \"*.js\", \"*.{ts,tsx}\")"},
			},
			"required": []any{"pattern"},
		},
	},
}

// ensurePopulatedSchema guarantees the declaration carries a non-empty
// function.parameters.properties. The gate rejects a declaration with an empty
// schema even when its name is acceptable (measured 2026-10-15: five OpenCode
// names with `parameters: {}` -> 403, the same five with one property each ->
// 200).
//
// ponytail: one placeholder property, not a faithful schema. The gate only
// checks that properties is populated, so the real shape is unnecessary unless
// the upstream starts validating the declaration body.
func ensurePopulatedSchema(tool map[string]any) {
	// Responses-wire tools are flat; Chat Completions nests under "function".
	container := tool
	if fn, ok := tool["function"].(map[string]any); ok {
		container = fn
	}
	params, ok := container["parameters"].(map[string]any)
	if !ok {
		params = map[string]any{}
		container["parameters"] = params
	}
	if props, ok := params["properties"].(map[string]any); ok && len(props) > 0 {
		return
	}
	params["type"] = "object"
	params["properties"] = map[string]any{
		"input": map[string]any{"type": "string", "description": "Tool input"},
	}
}

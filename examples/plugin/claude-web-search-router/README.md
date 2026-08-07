# Claude Code Web Search Router (ModelRouter example)

This plugin routes built-in `web_search` requests away from upstream models that do not support server-side search.

## What it detects

- Inbound `claude` / `anthropic` with `tools[].type` `web_search_20250305` or `web_search_20260209`
- Inbound `openai-response` with `tools[].type` `web_search` or `web_search_preview`; the plugin runs the client's own model and orchestrates search as a function tool (see below) instead of switching the whole turn to another provider
- Optional Claude Code heuristics: system text like “web search tool use”, or user text
  `Perform a web search for the query: …`

## Routes (`route` config)

| Value                    | Behavior                                                                                                                                                                                                                                                                                                  |
| ------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `fallback` (**default**) | Claude Messages requests use the plugin executor's **Tavily → Codex** fallback. OpenAI Responses requests run the client's own model and orchestrate search as a function tool. |
| `antigravity_google` / `codex_web_search` / `xai_web_search` / `tavily` | Same orchestration for that backend’s chain member(s): execution retry + penalty apply when multiple backends are eligible. |
| `default_provider`             | `default_provider` + optional `default_provider_model` via built-in AuthManager (not orchestrated).                                                                                                                                                                                                                          |
Routing for `fallback` requires at least one runnable backend (providers in `AvailableProviders` where needed, resolvable antigravity model, or `tavily_api_keys`).

### OpenAI Responses (Codex) web search orchestration

Codex attaches a hosted `web_search` tool to every normal reasoning turn, even when the model never searches. Rather than redirecting the whole turn to another provider (which discards the client's model), the plugin self-orchestrates:

1. Rewrites the hosted `web_search` tool into a plain `web_search` function tool the client model can call.
2. Runs the client's own model (whatever the request asked for, e.g. `claude-opus-4-8` or a `gpt-*` upstream) through the host with `EntryProtocol`/`ExitProtocol` = `openai-response`.
3. When the model emits a `web_search` function call, executes the search backend (Tavily) for just that query, appends `function_call` + `function_call_output` to `input`, and re-runs the model.
4. Repeats until the model produces a final answer or the round cap is reached, then forwards that turn verbatim as OpenAI Responses output.

The model reasons for the whole turn; only the actual search action hits the search backend. Turns that never call `web_search` pass through in a single round with no search. `only_models` still scopes interception to upstreams that lack native web search; natively search-capable upstreams are not listed there and pass through untouched. Requires `tavily_api_keys` to perform real searches; without them the model is told the search failed and degrades gracefully.

### xAI web search notes (aligned with upstream docs)

- **Model**: xAI documents `grok-4.3` for server-side `web_search`. This example sets `TargetModel` to **`grok-4.3`** when `xai_model` is empty (do not forward `claude-sonnet-4-6` to xAI).
- **Request shape**: Responses API `input` + `tools[]` with `"type": "web_search"`. Optional `filters.allowed_domains` / `filters.excluded_domains` (max 5 each, mutually exclusive).
- **Claude mapping today**: `internal/translator/codex/claude` copies Claude `allowed_domains` → `filters.allowed_domains`. Claude `blocked_domains` is **not** mapped to `excluded_domains` yet.
- **Executor**: `xai_executor` normalizes tools (drops unsupported `external_web_access` if present) and posts to `/responses`.
- **Response**: Citations / server tool metadata come back through OpenAI Responses SSE and are converted toward Claude `server_tool_use` / `web_search_tool_result` where the response translator supports it.

## Configuration

Plugin config lives under `plugins.configs.claude-web-search-router` (key must match the plugin name). Load the shared library via `plugins.path`.

### Recommended: fallback chain (default)

Claude Messages requests try **Tavily → Codex**. OpenAI Responses requests use Codex directly to preserve the native response format.

```yaml
plugins:
  path:
    - /absolute/path/to/examples/plugin/bin/claude-web-search-router-go.dylib
  configs:
    claude-web-search-router:
      enabled: true
      priority: 20
      route: fallback
      antigravity_model: "" # empty: registry lookup, then first supports_web_search
      codex_model: "gpt-5.4-mini"
      xai_model: "grok-4.3"
      tavily_api_keys:
        - "tvly-xxxxxxxx"
        # - "tvly-yyyyyyyy" # optional: round-robin
      require_web_search_only: true
```

Omit `route` to use the same default (`fallback`).

### Minimal fallback (Tavily as last resort only)

```yaml
plugins:
  configs:
    claude-web-search-router:
      enabled: true
      priority: 20
      route: fallback
      tavily_api_keys:
        - "tvly-xxxxxxxx"
      require_web_search_only: true
```

### Single backend (no fallback)

**Antigravity only:**

```yaml
plugins:
  configs:
    claude-web-search-router:
      enabled: true
      priority: 20
      route: antigravity_google
      antigravity_model: "gemini-3.1-flash-lite"
      require_web_search_only: true
```

**Codex only:**

```yaml
plugins:
  configs:
    claude-web-search-router:
      enabled: true
      priority: 20
      route: codex_web_search
      codex_model: "gpt-5.4-mini"
      require_web_search_only: true
```

**xAI only:**

```yaml
plugins:
  configs:
    claude-web-search-router:
      enabled: true
      priority: 20
      route: xai_web_search
      xai_model: "grok-4.3"
      require_web_search_only: true
```

**Tavily only (plugin executor):**

```yaml
plugins:
  configs:
    claude-web-search-router:
      enabled: true
      priority: 20
      route: tavily
      tavily_api_keys:
        - "tvly-xxxxxxxx"
      require_web_search_only: true
```

**Built-in provider via `default_provider`:**

```yaml
plugins:
  configs:
    claude-web-search-router:
      enabled: true
      priority: 20
      route: default_provider
      default_provider: claude
      default_provider_model: ""
      require_web_search_only: true
```

### Disable or relax detection

```yaml
plugins:
  configs:
    claude-web-search-router:
      enabled: false # plugin declines; host may use default Claude path

# Or keep enabled but allow mixed tool lists:
    claude-web-search-router:
      enabled: true
      route: fallback
      require_web_search_only: false
```

### Config field reference

| Field | Description |
| ----- | ----------- |
| `enabled` | `false` → `Handled: false` for all web_search matches |
| `priority` | Host plugin order for ModelRouter (higher runs earlier; see main repo plugins docs) |
| `route` | `fallback` (default), `antigravity_google`, `codex_web_search`, `xai_web_search`, `tavily`, `default_provider` |
| `antigravity_model` | Antigravity execution model; never the client Claude model name |
| `codex_model` | Codex model; empty → `gpt-5.4-mini` |
| `xai_model` | xAI model; empty → `grok-4.3` |
| `default_provider` / `default_provider_model` | Used when `route=default_provider` |
| `tavily_api_keys` | Required for `route=tavily` or fallback last step |
| `require_web_search_only` | `true` matches Claude Code–style exclusive `web_search` tools |
| `only_models` | Optional exact names or trailing-`*` prefixes matched against host-resolved upstream model names |

## Build

```bash
make -C examples/plugin bin/claude-web-search-router-go.dylib
```

Use `.so` on Linux and `.dll` on Windows. Point `plugins.path` at the built artifact.

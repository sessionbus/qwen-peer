# Qwen Sessionbus package

One Go binary supplies `qwen-peer` and its private sibling alias
`qwen-peer-mcp`. The native Qwen installation supplies its own Node runtime;
this package adds no Node adapter or npm dependencies. Use the archive so the
lane worker can resolve its exact private sibling executable.

```sh
scripts/package-product qwen ./dist
mkdir -p ./dist/qwen-install
tar -xzf ./dist/qwen-peer-linux-amd64.tar.gz -C ./dist/qwen-install
sh ./dist/qwen-install/install
```

Select the archive for the target platform. The literal installer replaces
`~/.local/libexec/sessionbus/qwen`, links `~/.local/bin/qwen-peer`, and uses
native `qwen extensions uninstall/install` for the owned `sessionbus`
extension. Reinstall replaces the owned plugin payload, including removed
skills. It preserves unrelated extensions and native history. The archive
contains one generic `skills/sessionbus/SKILL.md`, covering every product
through the same public tool; there are no separate product lane skills.

## Current lane interface

The daemon starts the token-selected worker with no model input. It owns one
native `qwen --acp` session and one shared Worker/Caller. Fresh Open adopts the
native `session/new` ID. Resume uses the retained native ID and rejects a
conflicting returned ID. The per-session MCP configuration uses the absolute
private sibling executable and a unique endpoint for that native session.
The helper forwards the single public tool to that existing Caller.

Call `mcp__sessionbus__sessionbus` with `{action, arguments}`. Managed lanes
and integrated interactive launches set `alwaysLoadTools:true` on their wrapper-owned
Sessionbus MCP entry, so the granted tool is declared directly. Native
`tool_search` may still occur; successful helper initialization alone does not
prove model selection or a completed MCP call. Every managed peer and lane
launch adds the exact native grant `--allowed-tools mcp__sessionbus__sessionbus`;
it does not approve another tool or change sandbox and approval policy.
Preserve an ambient native refusal and report it without replay rather than
broadening the grant.

Use `describe` with `product:"qwen-peer"` for supported Open fields, and
`spawn` with a product, child name and explicit `open` object. The current
fields are cwd, permission_mode, model, reasoning_effort and arguments.
Omitted/default permission uses native policy; bypassPermissions is an
explicit caller choice. Model and supported effort values are passed through
their native configuration paths. Unsupported values and arguments conflicting
with owned lane controls are rejected. Caller `--allowed-tools` entries remain
additive. If caller arguments set the native immutable
`--allowed-mcp-server-names` upper bound, it must include `sessionbus`; an
`--exclude-tools` rule that matches the managed public tool is rejected. Other
server bounds, tool exclusions, approval settings, and sandbox arguments remain
native-owned. Explicit bypass keeps both the narrow grant and Qwen's native
`--yolo` projection.

`start` returns session_id/run_id; `run`, `status` and `wait` read without
consuming. Receive a done result or report an unavailable reason before `ack`;
never acknowledge running. Interrupt acknowledgment is not a terminal result.
The one shared run remains owned until its native prompt and admitted delivery
and cancel operations settle. Closing or losing the worker invalidates its
unacknowledged results. See the [generic skill](skills/sessionbus/SKILL.md) for
the action examples, completion pointers and independent lifetime policies.

Every lane delivery returns NotRunning before local or native enqueue. The
daemon starts an idle delivery as a managed run or retains an active-turn
delivery in bounded memory for the automatic next run. A seeded run reports
`written` only after the full native prompt request write.
`queued_for_next_turn` is daemon scheduling, not native admission, durability,
or consumption. The native `craft/drainMidTurnQueue` response stays empty:
Qwen never invokes that drain while a tool call is executing, so it
cannot acknowledge mutually blocked Sessionbus sends safely.

Close and automatic close retire the lane and leave native Qwen history in
place. Forget removes the daemon's resume recipe, not native history. There is
no wrapper database, result journal or restart recovery. The shared result
cursor and daemon scheduling queue are bounded memory.

## Interactive launches and ordinary Qwen

The installed extension contains one generic skill and no MCP registration.
Ordinary native Qwen can discover the guidance but does not start this package's
MCP helper. `qwen-peer` adds its private sibling through one per-launch native
`--mcp-config`; dedicated ACP lanes use their per-session configuration instead.
Existing settings and other extensions retain native precedence.

Native initial selectors (including bare/title/ID resume, continue and fork)
remain native arguments. Native subcommands and help/version pass through. Repeated `-g`/`--group` comma lists and
`-n`/`--name`/`--peer-name` are wrapper options before `--`; tokens after `--`
remain untouched. No generated native ID or title resolver is used. Explicit
native permission arguments remain unchanged and omitted policy stays omitted.
`--yolo` coexists with the exact managed tool grant; it does not replace that
grant or change the wrapper's handling of another tool.
Headless input belongs in a lane. The wrapper reserves `--input-file`,
`--json-file` and `--json-fd`, and requires native chat recording for titles.

A single caller `--mcp-config` is composed in its original position. Existing
files use native file-only JSON comment handling; inline input is strict JSON.
Wrapped `mcpServers` and direct maps retain raw server values, numeric tokens and
unknown fields. Both caller input and combined output are capped at 65536 UTF-8
bytes. Duplicate flags, malformed input and an explicit caller `sessionbus`
server fail clearly. Caller files are never edited. No second config flag,
all-extension enablement, global toggle or broad permission grant is introduced.

After MCP initialization, the helper reserves an empty exclusive launch claim
after native env and live launch-ancestry validation, then asynchronously matches the first native
`session_start`, its native `QWEN_CODE_SESSION_ID`, and a live launch-owned
native process/registry record. Registry publication is source-bound ordering
after normal input-watcher construction; native caught watcher-init failures
mean it is not universal watcher-health proof. Linux checks native process-start
and PID-namespace tokens; macOS native records have null tokens, so live sysctl
process identity and a registry timestamp after that process start qualify the
weaker native record. Tools remain cancelable while binding is pending.

Native camel-case aliases of the managed input/output, recording and MCP-config
options have the same reserved/composition rules as their dashed spellings.
Recording-disable aliases are rejected; caller configuration is still composed
into one argument at its original position.

Initial `-n` uses literal `/rename -- <name>` so flag-like names cannot select
auto-rename. Names use native whitespace normalization (runs become one space)
and the native 200 UTF-16-code-unit limit; empty, multiline, invalid UTF-8 and
NUL-containing names are rejected before launch. The rename is appended once
after that gate, then published only after matching
native `custom_title` confirmation. Existing historical title text cannot confirm
this new rename. Later native title changes, including clearing a name, update
the same bus connection. An empty per-launch exclusive claim prevents restarted
or overlapping helpers from creating another owner or repeating the rename;
a failed owner requires a fresh launch. This includes a native updater that
restarts Qwen within the same launcher: exit that replacement and start a new
`qwen-peer` process. Integration is not transferred to the replacement helper.
The claim contains no session metadata.

Initial daemon absence and later daemon connection loss keep the same helper
and native session alive. The helper retries the launch's socket and publishes
the current session name after reconnecting. Calls during an outage return
`not_connected`; old calls and deliveries are never replayed. A rename on an
already admitted connection keeps delivery admission while its updated hello
is pending. Supersession, a refused hello, and native/helper exit remain terminal;
they do not start another owner. Lane Workers retain their single connection
lifetime.

The launcher owns its native child, unique temporary input file and native event
FIFO until native exit. One claimed helper reads the initial native event and
then drains/discards output; it does not store an answer transcript. Native may
open before or after the reader. The reader closes and joins on cancellation.
The input file remains append-only during the live launch, with an 8 MiB
per-record wrapper limit and no total-byte ceiling; it is never truncated or
rotated because native consumed-offset guarantees are absent. Native event and
history observation records are bounded at 8 MiB. Directory/file watches are
bounded at eight, and incoming bus handler work at 32. This is temporary native
transport, not a wrapper history/recovery store.

Terminal-group SIGINT is left to native Qwen; the launcher does not convert it
into TERM or send a duplicate interrupt. Normal exit/TERM joins the child and
removes those resources. Helper
EOF, native-parent loss or launcher loss cancels its sole Caller and observers.
If the launcher is killed abruptly, the integration retires but native processes
and unique files may remain; there is no claim of reaping/cleanup by a dead
launcher and no recovery from these files.

## Session switching limitation

The owner has accepted the released native interfaces' session-switch limit:
Sessionbus binds to the launch's initial native session. After an in-process
/new, /clear, /resume or other switch, outbound MCP calls can retain that old
identity while input-file deliveries reach the displayed session. Presence,
delivery and completion-pointer attribution across that switch are outside
supported guarantees. Exit and start a fresh `qwen-peer` using the native
selector to use Sessionbus with another session. This is guidance, not an
enforced switch ban or a race-free withdrawal mechanism. ACP lanes each own a
dedicated session and are unaffected. No native patch is a prerequisite.

The lane runtime was exercised on unmodified Qwen 0.23.0: new/resume startup,
native ToolSearch plus public list, historical staged delivery, seeded wake, active pull,
interrupt, repeated collection/acknowledgment and healthy following runs.
Receipt limits and later process-absence evidence are retained separately;
these observations are not an interactive or all-policy acceptance claim.
The same permanent installation now has native interactive identity/name/groups,
public ToolSearch/list, parent/child completion-pointer collection, inbound
observation, fresh-process title resume and ordinary skill-only checks. Native
0.23.0 executed the model rows; native auto-update moved later zero-model
resume/ordinary/lifetime checks to 0.23.2. See the
[acceptance ledger](../docs/designs/qwen-0.5.0/ACCEPTANCE.md) for exact evidence
and limits, including updater relaunch and abrupt cleanup.

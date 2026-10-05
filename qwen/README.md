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
contains `qwen-extension.json` and static `SESSIONBUS.md` guidance, with no
invokable Skill or global MCP registration.

## Current lane interface

The daemon starts the token-selected worker with no model input. It owns one
native `qwen --acp` session and one shared Worker/Caller. Fresh Open adopts the
native `session/new` ID. Resume uses the retained native ID and rejects a
conflicting returned ID. The per-session MCP configuration uses the absolute
private sibling executable and a unique endpoint for that native session.
The helper forwards the single public tool to that existing Caller.
The lane keeps the user's effective native system-defaults setting; it does
not create a Skill-hiding defaults file or override that setting.

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
unacknowledged results. See [SESSIONBUS.md](SESSIONBUS.md) for concise tool and receipt guidance.

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

## Integrated interactive launches and ordinary Qwen

The installed extension contains `qwen-extension.json` and short static
`SESSIONBUS.md` context. It registers no global MCP server and provides no
invokable Skill. Ordinary `qwen` does not start this package's MCP helper.
`qwen-peer` composes one launch-scoped `--mcp-config` for its private sibling
helper, and grants only `mcp__sessionbus__sessionbus`. Existing caller MCP
configuration remains in its original argv position; its files are not edited.

The outer wrapper resolves native `qwen`, creates a private launch directory
(mode 0700) with an empty input file (mode 0600) under `$XDG_RUNTIME_DIR`, or
the system temporary directory when that is unset, composes the helper config,
passes `--input-file` to native, then replaces itself with native Qwen using
`exec`. It creates no event FIFO, scoped defaults file, owner claim or resident
parent, and needs no native controller grant.
The helper binds the exact native `QWEN_CODE_SESSION_ID` to the live parent PID
registry row, process generation, namespace where available, and CWD; the row's
optional peer-inbox address is not used. It delivers each Sessionbus message by
appending one `{"type":"submit","text":...}` record to the input file, which
native watches: an idle session takes it as new input, and an active task takes
it at its next eligible boundary. A Sessionbus `written` receipt means the
complete record was appended, not that native queued it or ran a model turn.
Uncertain appends are not replayed, and a partially written record cannot
corrupt a later one. Registry and native-parent loss withdraw the helper.
Daemon loss reconnects the same helper and identity; supersession is terminal.

Launching through `qwen-peer` is the user's permission for Sessionbus input in
that session. Input-file messages do not pass through Qwen's peer-messaging
policy: if someone has disabled Qwen's peer feature, or set hold or refuse, and
then launches `qwen-peer`, Sessionbus messages still arrive. A Sessionbus
message still queued when the user cancels a running turn is returned to the
composer unsent, as Qwen does with its own typed queue; a message already taken
into the turn follows Qwen's handling of that turn.

The helper exits as soon as native closes its connection; native waits for that
during quit. Each helper records the native TUI it is bound to in the launch
directory. A later managed launch removes a launch directory once that recorded
TUI has ended. A directory whose helper never bound, or whose recorded identity
cannot be read, is left in place. Until it is removed, the input file keeps the
delivered message text; whether the runtime directory is cleared at logout
depends on the host.

`-g`/`--group` and `-n`/`--name`/`--peer-name` are wrapper options before `--`.
For `-n`, the wrapper supplies a single native interactive `/rename -- NAME`
startup command. It refuses a caller startup `-i`/`-p` or arguments after `--`
that would compete for that input; it never overwrites a caller prompt.
The validated `-n` value is also the initial Sessionbus display name. Qwen's
native `custom_title` controls the TUI title, while the native registry name
shown by `qwen sessions ps` may retain its launch default. Later native
`/rename` changes the TUI title; it is not promised to mirror to Sessionbus.
Without `-n`, caller native arguments and the `--` boundary are preserved, and
the Sessionbus display follows the observed native registry name.
Native selectors, permission choices and chat-recording choices remain native.
Integrated `--no-chat-recording` is supported. Native subcommands and
help/version pass through without integration.
Integrated launches refuse native `--bare` mode and a truthy `QWEN_CODE_SIMPLE`,
which are not supported integrated configurations; the experimental OpenTUI
renderer (`QWEN_TUI_RENDERER=opentui`), which does not read the input file; and
a caller `--input-file`, which the wrapper owns. Ordinary native subcommand
passthrough retains its own bare-mode behavior.

Native `/new`, `/clear` and `/resume` can change the displayed session while
the helper remains bound to the initial ID. Exit and start a new `qwen-peer`
process for a different session. ACP lanes keep their separate per-session
binding. Native Qwen 0.24.6's `--input-file` watcher is the source-supported
idle-wake and active-task delivery mechanism; real installed model
observations remain a separate acceptance check.

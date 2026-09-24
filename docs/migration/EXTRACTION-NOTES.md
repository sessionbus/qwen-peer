# Qwen extraction record

Baseline: `710e5d33369cba4fb9468cd24fea0fe844a0219d` from the complete
organisation copy. The original history and sibling repositories retain removed
product paths. The Qwen Go wrapper, native extension manifest, generic skill,
private `qwen-peer-mcp` alias, managed tool grant, native argument and permission
handling, install layout and archive recipe remain.

The frozen inventory accounts for 91 protected files and 146 original Go test
functions: 57 Qwen-owned files, the shared archive installer, one held Qwen
lane reference and 32 shared support files now resolved from peer-common. The
frozen record's phrase “held Grok lane reference” was a wording error: its
Qwen path and hash were correct. `PRESERVED-FILES.json` corrects only that
phrase and maps each file to its current location. Product runtime has no
intended behavior change.

Shared host/MCP/version/socket support and the legacy cleanup utility resolve to
`github.com/sessionbus/peer-common` at the reviewed immutable version
`v0.0.0-20260922143100-eb655f686e44` (commit
`eb655f686e4456a4c1121054763318e3d27e89b0`), without a filesystem
replacement or workspace. Linker flags stamp the Qwen release/revision into
the shared version helper. The legacy cleanup document now points to that
pinned source.

The root boundary, packaging, version and download tests and workflows are
scoped to Qwen. There is no ordinary `qwen/.mcp.json` or `qwen/mcp.json`:
ordinary native Qwen receives the skill only. Managed launches use the exact
Sessionbus tool grant and a per-session private alias. `qwen/plugin.json` keeps
its independent `0.4.0` extension identity. The `scripts/package-product qwen
DIR` interface and archive members, including the alias symlink, remain. The
archive installer retains the Qwen extension uninstall/install sequence and
native consent/user scope. The bootstrap retains checksum and role checks but
uses the canonical `sessionbus/qwen-peer` release URL. The packaged archive
needs no extra Node/npm runtime; the native Qwen application owns its runtime.

The Qwen design, acceptance record, facts, 0.23.0 native help and generic skill
remain. The held lane reference formerly under Claude design notes is copied
byte-for-byte to `docs/designs/qwen-0.5.0/legacy-lane-skill/SKILL.md`; it is
not packaged. Three deletion-induced Pi/OMP release-note links now point to
immutable original source in the Pi/OMP repository. The v0.5.0 README install
anchor follows this repository's README.

## Explicit exclusions and holds

The only unique unmerged change, signed commit
`5c1126e334349ab75072857da65a83d98249fcde`, adds a managed Skill grant
beside the directly granted MCP tool. It is deliberately **excluded**. Its
bundle, patch and signature evidence are preserved in
`/home/antst/sessionbus-evidence/qwen-preservation-inventory-opus-20260923/archive-5c1126e`;
the SHA256 of that archive's `SHA256SUMS` is
`c0d06ea8bf6b75bc68b2ca16ced679b14a70855bfbd0c8a83f33297e661f343d`.
The permanent `acd7c4c` installation replaced that testing-only build;
install and idempotent reinstall on the real home passed independent archive,
extension and alias review. QWK922F denied the model-selected Skill under AUTO
before MCP send, reply or final, so the excluded commit was not a validated fix.

Direct MCP communication historically worked. QWK922E/F show that a
model-selected Skill-first wake may be denied under AUTO; the Skill is guidance,
not a prerequisite. Both are incomplete wake evidence, not product-wide
acceptance or proof that a direct MCP selection would fail. Any future Skill
grant change needs separate review and fresh installed evidence.

## Source, installation and fresh behavior

The reviewed `acd7c4c` extraction accounts for all 91 protected files and 146
original test functions. PR #1's Linux, macOS, scan and workflow-guard checks
pass. The permanent archive installation and idempotent reinstall are bound by
`qwen-extraction-installed-dev1-20260923/BINDING.json` (packet seal
`17000f41287b8b1246bfe79793189d965c91ba5c89e30ad133b13eee81ef92da`).
It replaced testing-only `5c1126e`/binary `9fe201b9`; native Qwen 0.24.3 is
provenance, not a compatibility restriction. This proves installed bytes and
layout, not wake acceptance.

The permanent option F installation then replaced `acd7c4c` with reviewed
`bf6d0ea`/binary `6370f92e`. Its two real-home installs were idempotent, with
the installed archive, extension, alias and private binary bound by
`qwen-option-f-installed-bf6-dev2-20260923/BINDING.json` (binding `32dc5cb2`,
packet seal `239621ab`). Native Qwen 0.24.3 remains provenance only. Host
system-defaults were observed absent at installation; they were not
re-observed at QWK923C cell time.

Fresh default managed-idle QWK923A and QWK923B each completed setup and had
one written inbound admitted as an ordinary native user message. Neither
launch supplied a bypass or approval-mode override. The model
selected the non-granted `skill` tool, and native Qwen 0.24.3 AUTO denied the
cross-session action before any Sessionbus MCP call, reply or requested final.
A's denial also cited its standing setup prohibition. B used a reviewed
setup-only restraint and its denial did not cite that prohibition, so the
fixture confounder was removed without completing the wake. Both drivers
later exited on local launcher stdin EOF, after the native AUTO denial
and the model's next response; that EOF was a harness exit condition,
not the Qwen outcome. The original A and B failure packets remain preserved at
`qwen-wake-acceptance-live-dev1-20260923/cells-qwk923a` and `cells-qwk923b`
(seals `1f77f999` and `6e4318b6`). Those denials did not establish that the
already-granted MCP tool would be denied. QWK923C, on installed option F with
default AUTO, is a **clean original managed-idle pass only**. Its setup turn
was tool-free, and its final native history contains exactly two ordinary
inputs and no injected rows under an empty environment profile. The wake made
one granted `mcp__sessionbus__sessionbus` call with an explicitly successful
native result, no Skill, AUTO denial or `tool_search` call, and the exact final.
That result's message ID joins to the operator-attested direct reply; the
reply is not a cryptographic receipt. The lane-private defaults included
`sessionbus:sessionbus` in `skills.disabled`, and the `--mcp-config` set
`alwaysLoadTools: true`. Both were
observed during the run and removed after Close; owned cleanup passed. The
original cell packet is
`qwen-option-f-live-dev2-20260924/cells-qwk923c/qwen-lane-idle-qwk923c`
(seal `53b93311`); independent review is retained in
`qwen-f-review-records-opus-20260924` (seal `39448482`). The review record
separately corrects the original outcome's process-scan sentence; the cell
packet remains unchanged. At that `bf6d0ea` checkpoint, interactive idle and
active remained untested. QWQ924C later supplied an independently reviewed
clean original managed-active PASS on the same build. No bypass cell was run.

The reviewed interactive-visibility source at `2ba3e12` is complete, separate
from installed behavior. Its permanent real-home installation replaced
`bf6d0ea` with binary `0eb401e0`; post-install observations after two installs
were byte-identical, and packet
`qwen-interactive-option-f-installed-2ba3-dev2-20260924` (seal `6dfc48fd`)
binds the archive, wrapper, extension and aliases in BINDING `c145d755` and
observation `1909c885`. Native Qwen 0.24.3 is observed provenance, not an
allowlist. The integrated interactive wrapper now supplies its native child
the reviewed private defaults merge and wrapper-owned Sessionbus MCP entry
with `alwaysLoadTools:true`. The child's descendants inherit the defaults
path; plain `qwen` and the wrapper's own environment are unchanged. Other
extensions, accepted args, grants and native approval modes remain in force.
This is a conditional skill-visibility mechanism: native `.env` files or
settings `env` entries can enable bare mode after the wrapper's inherited-env
check, so the wrapper does not claim to cover every configuration source.
Integrated interactive launches reject an explicit `-e sessionbus` combined
with `--bare` or `--bare=true` before `--`, an exact `--bare` argv element
after `--`, or truthy inherited `QWEN_CODE_SIMPLE`. After `--`, a single
element merely containing `--bare` remains accepted; `--bare=x` and
`--bare=TRUE` pass through unchanged. They also fail closed on unmergeable
host defaults. These are new
installed compatibility restrictions. A panic, SIGKILL or SIGHUP can leave the
private interactive launch directory; handling SIGHUP as owned termination
and testing descendant behavior remain a bounded lifecycle follow-up.

On that installed build, managed idle QWK924R (cell `c153b1e0`, independent
review `edc29e93`) and managed active QWQ924R (`a890ca68`, review `c2d90c03`)
independently re-PASSed. Interactive idle QWI924E (`88d135f2`, review
`0e53b88a`) is a clean original PASS under normal policy with one direct,
explicitly successful MCP call, exact final, message-ID-joined
operator-attested reply and owned cleanup. Its native-child capture records
`skills.disabled:["sessionbus:sessionbus"]`, `alwaysLoadTools:true`, and bare
mode false. The conclusion that the Skill was hidden is **inferred from the
captured effective defaults (source-backed)**; no native Skill listing was
directly recoverable. Host defaults were observed absent at install and in
QWQ924R, QWI924E and QWI925A/B preflights; QWK924R did not re-observe absence
at cell time, although its private defaults matched the fallback bytes. These
observations do not test every merge case. QWI924A–D ran on the earlier
`bf6d0ea` build (observation `ca33703d`) and retain their original FAILs.
Root's later criterion correction accepts the pinned native `tool_call` bridge
as granted MCP success for future interactive cells only; it did not relabel
QWI924C. Interactive active QWI925A is an original FAIL caused by a harness
projector timestamp defect, with no native failure in the raw history
(`QWI925A-INDEPENDENT-CLASSIFICATION-opus.md`, SHA `26f4fed5`). QWI925B is
an independently reviewed clean original PASS under the active gate (packet
`6990b392`, review `bc41aa65`), so interactive active is accepted for the
tested steady-state lifecycle. That named-launch fixture covers this lifecycle
only: it launched without `-i`, waited for title confirmation and accepted
`session.hello`, then started one ordinary turn with a single harness-authored
`input.jsonl` submit and zero PTY writes. For a named launch, source-based
reachability analysis predicts that an inbound during an initial `-i` turn
before publication is rejected as `unknown_session`; no live cell tested that
timing. The
`written` receipt and queued PTY preview prove admission, not same-turn
consumption. The gate accepts either a mid-turn steer or next-turn delivery;
QWI925B's native history shows one `mid_turn_user_message` steer. Two
successful informational `tool_search` calls selected nothing: the model
searched an invented `mcp__plugin_qwen-code-dnd_…` name, then `sessionbus`,
before using the directly loaded granted MCP tool. Each direct reply is
operator-attested and joined by message ID, not a cryptographic receipt.
QWI925B did not re-observe the earlier QWI925A watcher residue; its sealed
packet's untouched-residue statement follows from path confinement. The
0.24.4 update notice did not change the 0.24.3 post-cell observation
`1909c885`; a later update is an identity event requiring a reviewed re-pin,
not a product failure by itself.

The old `develop` branch has been removed. The binary-release and package
preview workflows remain manually disabled; PR #1's hosted checks ran without
enabling either publishing path. The release and native extension versions are
unchanged, and publication is held. The original Codex repository's
`install-qwen.sh`, pinned to final combined v0.5.3 assets, remains the working
public compatibility installer
until an independent Qwen release exists. The new bootstrap fails closed before
that release.

`measure.py` and `SIZE.json` are retained historical artifacts. Their former
local `wrappers/mcp` ownership accounting is not reproducible after the common
module split. Preserve the recorded values as history, not a fresh measurement.
Native Qwen versions are not pinned; exact versions/hashes identify observations,
not a compatibility allowlist.

Source and archive verification, permanent real-home installation, and fresh
native wake behavior are separate evidence. Source and installation are
complete. Managed idle and active re-PASSed, interactive idle PASSed and
interactive active PASSed once for the tested steady-state lifecycle under
the new installed build. All four surfaces now have an accepted cell. No
extraction tag, release or version change has been made.

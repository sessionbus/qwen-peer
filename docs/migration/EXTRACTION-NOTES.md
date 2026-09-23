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
The permanent `acd7c4c` installation has replaced that testing-only build;
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
It replaces testing-only `5c1126e`/binary `9fe201b9`; native Qwen 0.24.3 is
provenance, not a compatibility restriction. This proves installed bytes and
layout, not wake acceptance.

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
not the Qwen outcome. The original A
and B failure packets remain preserved at
`qwen-wake-acceptance-live-dev1-20260923/cells-qwk923a` and `cells-qwk923b`
(seals `1f77f999` and `6e4318b6`). The native direct-MCP selection path on
0.24.x has not been tested, and these denials do not establish that the
already-granted MCP tool would be denied. Interactive idle/active and managed
active are untested and held, not failed. No bypass cell was run; bypass would
not substitute for default-mode evidence.

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
native wake behavior are separate evidence. The first two are complete; the
default managed-idle wake remains incomplete. No extraction tag or release has
been made.

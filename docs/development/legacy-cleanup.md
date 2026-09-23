# Legacy installation cleanup

The unchanged maintenance utility and all its regression tests, including the
Qwen extension inventory case, now live in
[peer-common at the pinned revision](https://github.com/sessionbus/peer-common/blob/eb655f686e4456a4c1121054763318e3d27e89b0/docs/development/legacy-cleanup.md).
It remains an explicitly invoked migration utility, never part of normal
Qwen installation or startup. Follow that document's inspect-before-apply
instructions; the extraction does not authorize running cleanup automatically.

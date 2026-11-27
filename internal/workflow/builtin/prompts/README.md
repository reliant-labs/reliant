# Builtin prompts

Prompt files the engine ships, named from a workflow as
`builtin://<group>/<name>.md` (a review node's `prompt:`, for one). The group
directory is required: `builtin://pipeline/review.md` is
`prompts/pipeline/review.md` here. This README is not a prompt and cannot be
named.

A prompt file is static text, never a template: a review node hands the
reviewer its data (`with:`) as JSON after the prompt.

A prompt is never read from the repository under review: the instructions a
reviewer follows must not come from the change it is judging.

# .reliant Directory

This directory contains metadata and configuration for your Forge project.

## Files

### `project.json`

Core project metadata including:

- `name`: Project name
- `module_path`: Go module path (e.g., `github.com/example/my-project`)
- `created_at`: Project creation timestamp
- `version`: Project version
- `generator`: Tool that generated the project

This metadata is used by Forge commands to understand your project structure and generate code with correct import paths.

**Do not delete this directory** - it's required for Forge CLI commands to work correctly.

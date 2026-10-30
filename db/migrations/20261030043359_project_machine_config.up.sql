-- forge:backward-compatible — adds nullable columns and a defaulted flag to project_machine_catalogs; the previous release reads and writes only the columns it knows, and keeps writing project_configs whole, which this release still maintains.

-- The rest of a machine's synced project config, beside its workflows and
-- presets: skills, repo and project memories, MCP servers, config YAML and
-- scenarios. A run reads its OWN machine's (a skill or an MCP server lives
-- on the machine that runs it), and project_configs keeps the union for
-- reads with no machine: skills, repo memories and scenarios per file across
-- every machine (newest push wins), the single-valued parts from the last
-- pusher. So a machine that syncs without a nested checkout no longer erases
-- that checkout's skills for everyone.
ALTER TABLE project_machine_catalogs
    ADD COLUMN user_config_yaml text,
    ADD COLUMN project_config_yaml text,
    ADD COLUMN local_config_yaml text,
    ADD COLUMN global_memory_md text,
    ADD COLUMN project_memory_md text,
    ADD COLUMN mcp_configs text,
    ADD COLUMN scenarios_json text,
    ADD COLUMN skills_json text,
    ADD COLUMN repo_memories_json text,
    ADD COLUMN runtime_type text,
    -- True once the row carries the whole snapshot above. A row written
    -- before this migration holds only workflows and presets; its machine's
    -- config reads fall back to the union until its next push (every
    -- connect pushes a full snapshot).
    ADD COLUMN config_synced boolean NOT NULL DEFAULT false;

-- The last pusher's row already is its whole snapshot: project_configs holds
-- exactly what it pushed (the union columns aside, which equal its own when
-- it is the only machine and are recomputed on the next push otherwise).
UPDATE project_machine_catalogs m
SET user_config_yaml = pc.user_config_yaml,
    project_config_yaml = pc.project_config_yaml,
    local_config_yaml = pc.local_config_yaml,
    global_memory_md = pc.global_memory_md,
    project_memory_md = pc.project_memory_md,
    mcp_configs = pc.mcp_configs,
    scenarios_json = pc.project_scenarios_json,
    skills_json = pc.project_skills_json,
    repo_memories_json = pc.repo_memories_json,
    runtime_type = pc.runtime_type,
    config_synced = true
FROM project_configs pc
WHERE pc.project_id = m.project_id AND pc.daemon_id = m.daemon_id;

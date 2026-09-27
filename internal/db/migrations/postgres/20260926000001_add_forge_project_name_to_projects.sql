-- +goose Up
--
-- forge_project_name is the `name` in the project's forge.yaml — the key the
-- control plane files a forge project's environments under (forge sends it as
-- a deploy environment's `project`). The web joins a Reliant project to its
-- control-plane environments on it, so it must be known WITHOUT the daemon:
-- a hosted environment has to stay on screen while the laptop that runs forge
-- is asleep.
--
-- NULL means "never read": not a forge project, a forge.yaml with no name,
-- or a project created before this column whose daemon has not reported
-- since. It is written at project create (repo.discover reads forge.yaml)
-- and refreshed whenever ForgeService.GetTopology hears forge report it.

ALTER TABLE projects ADD COLUMN forge_project_name TEXT;

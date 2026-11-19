// Fixture: an adopted frontend whose stores live in src/store/ (singular),
// holding server data fetched through a generated client. The scan used to
// read only src/stores/ under frontends/<name>/, so this file was invisible.

import { create } from "zustand";
import {
  ProjectService,
  type Project,
} from "../gen/reliant/v1/project_pb";

type ProjectState = {
  projects: Project[];
};

export const useProjectStore = create<ProjectState>(() => ({
  projects: [],
}));

export const _service = ProjectService;

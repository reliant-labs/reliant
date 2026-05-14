import { registerStepComponents } from "../stepConfig";
import { ComputeStep } from "./ComputeStep";
import { ModelStep } from "./ModelStep";
import { ProjectChoiceStep } from "./ProjectChoiceStep";
import { GitHubConnectStep } from "./GitHubConnectStep";
import { ProjectPickerStep } from "./ProjectPickerStep";

registerStepComponents({
  'compute': ComputeStep,
  'model': ModelStep,
  'project-choice': ProjectChoiceStep,
  'github-connect': GitHubConnectStep,
  'project-picker': ProjectPickerStep,
});

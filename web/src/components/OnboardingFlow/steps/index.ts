import { registerStepComponents } from "../stepConfig";
import { ComputeStep } from "./ComputeStep";
import { DaemonConnectStep } from "./DaemonConnectStep";
import { ModelStep } from "./ModelStep";
import { ProjectChoiceStep } from "./ProjectChoiceStep";
import { GitHubConnectStep } from "./GitHubConnectStep";
import { ProjectPickerStep } from "./ProjectPickerStep";

registerStepComponents({
  'compute': ComputeStep,
  'daemon-connect': DaemonConnectStep,
  'model': ModelStep,
  'project-choice': ProjectChoiceStep,
  'github-connect': GitHubConnectStep,
  'project-picker': ProjectPickerStep,
});

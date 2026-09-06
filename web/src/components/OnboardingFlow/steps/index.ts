import { registerStepComponents } from "../stepConfig";
import { ComputeStep } from "./ComputeStep";
import { ModelStep } from "./ModelStep";
import { CheckoutStep } from "./CheckoutStep";
import { ProjectChoiceStep } from "./ProjectChoiceStep";
import { GitHubConnectStep } from "./GitHubConnectStep";
import { ProjectPickerStep } from "./ProjectPickerStep";

registerStepComponents({
  'compute': ComputeStep,
  'model': ModelStep,
  'checkout': CheckoutStep,
  'project-choice': ProjectChoiceStep,
  'github-connect': GitHubConnectStep,
  'project-picker': ProjectPickerStep,
});

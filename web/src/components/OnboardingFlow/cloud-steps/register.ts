import { stepRegistry } from '../StepRegistry';
import { ComputeStep } from './steps/ComputeStep';
import { DaemonConnectStep } from './steps/DaemonConnectStep';
import { GitHubConnectStep } from './steps/GitHubConnectStep';
import { LLMKeyStep } from './steps/LLMKeyStep';

// Register cloud completion handler (calls CompleteOnboarding + CreateDaemon RPCs)
import './completeHandler';

stepRegistry.registerMany([
  {
    id: 'compute',
    category: 'compute',
    order: 1,
    component: ComputeStep,
    shouldShow: () => true,
  },
  {
    id: 'daemon-connect',
    category: 'compute',
    order: 2,
    component: DaemonConnectStep,
    shouldShow: (plan) => plan.compute === 'local_daemon',
  },
  {
    id: 'github-connect',
    category: 'workspace',
    order: 3,
    component: GitHubConnectStep,
    shouldShow: (plan) =>
      plan.codeSource === 'github_repo' && plan.compute === 'cloud_free_trial',
  },
  {
    id: 'llm-key',
    category: 'compute',
    order: 3,
    component: LLMKeyStep,
    shouldShow: () => true,
  },
]);
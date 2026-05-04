import { logger } from "../../lib/logger";
import { isDev } from "../../lib/constants";
import {
  memo,
  useRef,
  useState,
  useEffect,
  useCallback,
  useMemo,
  forwardRef,
  useImperativeHandle,
} from "react";
import { Settings2, RefreshCw, X, ChevronDown } from "lucide-react";
import { useAttachmentStore } from "../../store/attachmentStore";
import { useChatParamsStore } from "../../store/chatParamsStore";
import { useActiveChatId } from "../../store/chatStoreHooks";
import {
  usePresetsForWorkflow,
  useModels,
  type Preset,
} from "../../store/globalDataStore";
import { useChatNavigationStore } from "../../store/chatNavigationStore";
import { PromptsSelector } from "./PromptsSelector";
import { AttachmentPreview } from "./AttachmentPreview";
import { WorkflowSelector } from "./WorkflowSelector";
import { ChatTextArea } from "./ChatTextArea";
import { ChatActionButtons } from "./ChatActionButtons";
import { useChatInputState } from "./useChatInputState";
import { useDragAndDrop } from "../../hooks/useDragAndDrop";
import { DragDropOverlay } from "./DragDropOverlay";
import { api } from "../../api/client";
import type { ConnectionStatus } from "../../types/streaming";
import { cn } from "../../lib/utils";
import { Tooltip } from "../ui/Tooltip";
import { getAcceptedMimeTypes } from "../../lib/filetypes";
import { type WorkflowInputs } from "../workflow/WorkflowParamsPanel";
import { ChatSettingsPopover } from "./settings";
import { InlineParamInput } from "../workflow/InlineParamInput";
import { InlinePresetPicker } from "../workflow/InlinePresetPicker";
import type { InputDef } from "../../lib/inputHelpers";
import { workflowGrpc } from "../../api/workflow-grpc";
import { presetGrpc } from "../../api/preset-grpc";
import { chatGrpc } from "../../api/chat-grpc";
import { useProjectStore } from "../../store/projectStore";
import { usePreferencesStore, DEFAULT_WORKFLOW } from "../../store/preferencesStore";
import { useChatStore } from "../../store/chatStore";
import { useChat, usePendingQuestion } from "../../store/chatStoreHooks";
import { ChatWorkflowStatus } from "../../gen/reliant/v1/chat_pb";
import type { WorkflowExecution } from "./ExecutionSidebar/types";
import { getThreadColor, formatNodeId, resolveThreadNameFromActiveThreads } from "./thread-views/threadUtils";
import { useActiveThreads } from "../../store/threadActivityStore";
import { getInputDefault, getInputNestedInputs, getInputPresetConfig, getInputUI } from "../../lib/inputHelpers";
import { parseAskUserMetadata } from "./askUserUtils";
import { QuestionPrompt } from "./QuestionPrompt";
import { loadTagModelConfigs } from "../Settings/ModelPreferences";

/** Extract WorkflowInputs schema from a proto Workflow's inputs.
 *  Returns { inputs, groupTags, groupUIs } for use with WorkflowParamsPanel. */
export function buildWorkflowInputsFromProto(
  inputsMap: Record<string, any> | undefined
): {
  inputs: Record<string, InputDef>;
  groupTags: Record<string, string>;
  groupUIs: Record<string, string | undefined>;
} {
  const inputs: Record<string, InputDef> = {};
  const groupTags: Record<string, string> = {};
  const groupUIs: Record<string, string | undefined> = {};

  if (!inputsMap) return { inputs, groupTags, groupUIs };

  for (const [name, rawInput] of Object.entries(inputsMap)) {
    if (rawInput?.type === "group") {
      const nestedInputs = getInputNestedInputs(rawInput);
      const presetConfig = getInputPresetConfig(rawInput);
      const ui = getInputUI(rawInput);

      if (presetConfig?.tag) {
        groupTags[name] = presetConfig.tag;
      }
      groupUIs[name] = ui;

      if (nestedInputs) {
        for (const [paramName, nestedRaw] of Object.entries(nestedInputs)) {
          inputs[`${name}.${paramName}`] = nestedRaw;
        }
      }
    } else {
      inputs[name] = rawInput;
    }
  }

  return { inputs, groupTags, groupUIs };
}

interface ChatInputProps {
  onSend: (
    message: string,
    attachmentIds?: string[],
    workflow?: string | null,
    workflowParams?: Record<string, unknown>,
    targetThread?: string | null,
    selectedPresets?: Record<string, string | null>
  ) => void;
  onStop?: () => void;
  disabled?: boolean;
  isStreaming?: boolean;
  projectId?: string;
  worktreeId?: string;
  chatId?: string;
  connectionStatus?: ConnectionStatus;
  isChatBusy?: boolean;
  onToggleRecentChanges?: () => void;
  isRecentChangesOpen?: boolean;
  currentActivity?: string;
  messageCount?: number;
  // Command center mode
  paneId?: string;
  // Thread-specific params
  selectedThreadId?: string | null;  // Currently selected thread (null = main/all)
  workflowExecution?: WorkflowExecution;  // Root workflow execution tree
  // Discuss mode
  isDiscussMode?: boolean;
  onToggleDiscuss?: () => void;
}

const ChatInputComponent = forwardRef<HTMLDivElement, ChatInputProps>(
  function ChatInputComponent(
    {
      onSend,
      onStop,
      disabled = false,
      isStreaming = false,
      chatId,
      projectId,
      worktreeId,
      onToggleRecentChanges,
      isRecentChangesOpen = false,
      currentActivity,
      messageCount = 0,
      isChatBusy = false,
      paneId,
      selectedThreadId,
      workflowExecution,
      isDiscussMode,
      onToggleDiscuss,
    },
    ref
  ) {
    const fileInputRef = useRef<HTMLInputElement>(null);
    const containerRef = useRef<HTMLDivElement>(null);
    const textareaRef = useRef<HTMLDivElement>(null);
    const [isCompact, setIsCompact] = useState(false);

    // Models list for resolving tags to display names in the toolbar pill
    const { models: availableModels } = useModels();

    // Forward the internal ref to the parent
    useImperativeHandle(ref, () => textareaRef.current!, []);

    // Transfer workflow params from temp to real chatId when chat is created
    const previousChatId = useRef<string | undefined>(chatId);
    useEffect(() => {
      if (previousChatId.current === undefined && chatId !== undefined) {
        // Chat just created - transfer params from temp to real chatId
        useChatParamsStore.getState().transferTempToChat(chatId);
        logger.info("[ChatInput] Transferred temp params to chat", {
          chatId: chatId.slice(0, 8),
        });
      }
      previousChatId.current = chatId;
    }, [chatId]);

    // Get current tab ID for auto-draft functionality
    const activeChatId = useActiveChatId();
    // In command center mode, use paneId+chatId as unique identifier; otherwise use activeChatId
    const tabId = paneId && chatId ? `pane-${paneId}-${chatId}` : activeChatId;

    // Handle click on chat input container to activate chat tab and focus textarea
    const handleInputContainerClick = useCallback(
      (e: React.MouseEvent) => {
        // Don't focus if clicking on interactive elements (buttons, selectors, inputs, textareas, etc.)
        const target = e.target as HTMLElement;
        if (
          target.closest(
            'button, select, input, textarea, [role="button"], [role="combobox"]'
          )
        ) {
          return;
        }

        // Focus the textarea when clicking on padding/container area
        textareaRef.current?.focus();

        // Don't clear viewer - just ensure chat is active
        // Only activate in normal mode (not command center)
        if (activeChatId && !paneId) {
          useChatNavigationStore.getState().navigateToChat(activeChatId);
        }
      },
      [activeChatId, paneId]
    );

    useEffect(() => {
      const container = containerRef.current;
      if (!container) return;

      const checkCompact = () => {
        const containerWidth = container.offsetWidth;
        // Use container width to determine compact mode
        // Trigger compact mode when container width < 500px
        // The selector buttons are small (~80-100px each) so they can
        // comfortably fit at much larger widths before needing to collapse
        setIsCompact(containerWidth < 500);
      };

      // Initial check
      checkCompact();

      // Use ResizeObserver to watch container size changes
      const resizeObserver = new ResizeObserver(() => {
        checkCompact();
      });

      resizeObserver.observe(container);

      return () => {
        resizeObserver.disconnect();
      };
    }, []);

    // Use our custom hook for all state management
    const {
      input,
      setInput,
      selectedPrompts,
      setSelectedPrompts,
      forceStreaming,
      setForceStreaming,
      currentProject,
      selectedWorkflow,
      setSelectedWorkflow,
      isPendingChat,
      handleClearInput,
    } = useChatInputState({
      chatId,
      tabId: tabId ?? undefined,
    });

    // Question (ask_user) state
    const pendingQuestion = usePendingQuestion(chatId || "");
    const hasPendingQuestion = !!pendingQuestion;
    const askUserQuestion = useMemo(
      () => parseAskUserMetadata(pendingQuestion?.metadata),
      [pendingQuestion?.metadata]
    );
    const showAskOnly = hasPendingQuestion && !!askUserQuestion;

    // Workflow params panel state
    const [workflowInputs, setWorkflowInputs] = useState<WorkflowInputs | null>(
      null
    );
    // Workflow tag metadata for preset matching
    const [workflowTagInfo, setWorkflowTagInfo] = useState<{
      workflowTag?: string;
      groupTags: Record<string, string>; // groupName -> tag
      groupUIs: Record<string, string | undefined>; // groupName -> ui
    }>({ groupTags: {}, groupUIs: {} });
    // Initialize workflowParams from persisted storage for existing chats
    // For new chats (no chatId), start empty so workflow defaults are used
    const [workflowParams, setWorkflowParams] = useState<
      Record<string, unknown>
    >(() => {
      if (chatId) {
        return { ...useChatParamsStore.getState().getChatParams(chatId) };
      }
      // New chats start fresh - workflow defaults will be applied via preset loading
      return {};
    });
    const [syncedParams, setSyncedParams] = useState<Record<string, unknown>>(
      {}
    ); // Last params synced to server
    const [settingsPage, setSettingsPage] = useState<'main' | 'model' | 'preset' | 'params' | null>(null);
    useEffect(() => {
      if (settingsPage !== null) {
        window.dispatchEvent(new CustomEvent("contextual-tip-params-opened"));
      }
    }, [settingsPage]);
    const [isSyncing, setIsSyncing] = useState(false);

    // Get user's default workflow preference
    const userDefaultWorkflow = usePreferencesStore(
      (state) => state.preferences?.defaultWorkflow
    ) || DEFAULT_WORKFLOW;

    // Compute the effective workflow name
    // selectedWorkflow is the user's selection (null = use user's default)
    const workflowName = selectedWorkflow || userDefaultWorkflow;

    // Track the workflow that user explicitly changed to - skip loading default presets for it
    // We store the workflow name (not just a boolean) because useEffect runs multiple times
    // (once for workflowName change, again when presets load)
    const skipDefaultPresetsForWorkflowRef = useRef<string | null>(null);
    // Track previous workflow name so we only clear temp params on actual workflow changes,
    // not on re-renders or effect re-runs from other dependency changes.
    const prevWorkflowNameRef = useRef<string | undefined>(workflowName);

    // Thread-specific params state: when viewing a sub-agent thread's params
    const [threadParamsOverride, setThreadParamsOverride] = useState<{
      workflowName: string;
      inputs: WorkflowInputs;  // Schema for the thread's workflow
      values: Record<string, unknown>;  // Flattened param values from Temporal
      isRunning: boolean;
    } | null>(null);
    // Current thread param values (full values map, updated by WorkflowParamsPanel.onChange)
    const [threadParamValues, setThreadParamValues] = useState<Record<string, unknown> | null>(null);
    // Last thread params synced to server (to detect unsaved changes)
    const [threadSyncedParams, setThreadSyncedParams] = useState<Record<string, unknown>>({});

    // Whether we're currently showing a non-main thread's params
    const isViewingThreadParams = selectedThreadId != null && selectedThreadId !== chatId && threadParamsOverride != null;

    const currentProjectFromStore = useProjectStore(
      (state) => state.currentProject
    );

    // Get hidden preset check function
    const isPresetHidden = usePreferencesStore((state) => state.isPresetHidden);

    // Fetch compatible presets for the selected workflow
    const { presets, loading: presetsLoading } = usePresetsForWorkflow(
      workflowName
    );
    // Track selected preset per group (group name -> preset name)
    // Initialize from backend data for existing chats, empty for new chats
    const [selectedPresets, setSelectedPresets] = useState<
      Record<string, string | null>
    >(() => {
      // For existing chats, prefer backend data over localStorage
      if (chatId) {
        const chatObj = useChatStore.getState().chats.get(chatId);
        if (chatObj?.selectedPresets && Object.keys(chatObj.selectedPresets).length > 0) {
          return chatObj.selectedPresets as Record<string, string | null>;
        }
        // Fall back to chatParamsStore for existing chats
        const persistedParams = useChatParamsStore.getState().getChatParams(chatId);
        return (persistedParams.__selectedPresets as Record<string, string | null>) || {};
      }
      // New chats start fresh - default preset will be loaded via loadDefaultPreset
      return {};
    });

    // Load workflow inputs when selection changes
    useEffect(() => {
      const loadInputs = async () => {
        if (!currentProjectFromStore?.id) {
          setWorkflowInputs(null);
          setWorkflowTagInfo({ groupTags: {}, groupUIs: {} });
          return;
        }

        const targetWorkflow = workflowName;

        try {
          const result = await workflowGrpc.getWorkflow(
            currentProjectFromStore.id,
            { name: targetWorkflow }
          );
          const workflow = result.workflow;
          const { inputs, groupTags, groupUIs } = buildWorkflowInputsFromProto(
            workflow?.inputs as Record<string, any> | undefined
          );

          setWorkflowInputs(inputs);
          setWorkflowTagInfo({
            workflowTag: workflow?.presets?.tag,
            groupTags,
            groupUIs,
          });
        } catch (error) {
          logger.warn("[ChatInput] Failed to load workflow inputs", {
            error,
            targetWorkflow,
          });
          setWorkflowInputs(null);
          setWorkflowTagInfo({ groupTags: {}, groupUIs: {} });
        }
      };

      loadInputs();
    }, [currentProjectFromStore?.id, workflowName]);

    // Fetch thread-specific workflow inputs when a non-main thread is selected
    useEffect(() => {
      // Clear override when switching to main thread or "All"
      if (!selectedThreadId || selectedThreadId === chatId) {
        setThreadParamsOverride(null);
        setThreadParamValues(null);
        setThreadSyncedParams({});
        return;
      }

      if (!chatId || !currentProjectFromStore?.id) {
        setThreadParamsOverride(null);
        setThreadParamValues(null);
        setThreadSyncedParams({});
        return;
      }

      // Clear stale state from previous thread immediately on switch
      setThreadParamValues(null);
      setThreadSyncedParams({});

      let cancelled = false;

      const fetchThreadParams = async () => {
        try {
          // Fetch the thread's workflow inputs from the backend
          const result = await chatGrpc.getThreadWorkflowInputs(chatId, selectedThreadId);

          if (cancelled) return;

          // Load the workflow schema for the thread's workflow name
          const schemaResult = await workflowGrpc.getWorkflow(
            currentProjectFromStore!.id,
            { name: result.workflowName }
          );

          if (cancelled) return;

          const workflow = schemaResult.workflow;
          const { inputs } = buildWorkflowInputsFromProto(
            workflow?.inputs as Record<string, any> | undefined
          );

          // Flatten the nested inputs from Temporal into dot-notation values
          // The backend returns nested structure like { agent: { model: {...} } }
          // But the params panel expects flat keys like "agent.model"
          const flatValues: Record<string, unknown> = {};
          for (const [key, value] of Object.entries(result.inputs)) {
            if (typeof value === "object" && value !== null && !Array.isArray(value)) {
              // Nested group — flatten it
              for (const [nestedKey, nestedValue] of Object.entries(value as Record<string, unknown>)) {
                flatValues[`${key}.${nestedKey}`] = nestedValue;
              }
            } else {
              flatValues[key] = value;
            }
          }

          setThreadParamsOverride({
            workflowName: result.workflowName,
            inputs,
            values: flatValues,
            isRunning: result.isRunning,
          });
        } catch (error) {
          if (!cancelled) {
            logger.warn("[ChatInput] Failed to fetch thread workflow inputs", { error, threadId: selectedThreadId });
            setThreadParamsOverride(null);
          }
        }
      };

      fetchThreadParams();

      return () => {
        cancelled = true;
      };
    }, [selectedThreadId, chatId, currentProjectFromStore]);

    // Reset params and preset when workflow changes
    // For existing chats, restore from persisted storage
    // For new chats, start fresh so workflow defaults are applied
    useEffect(() => {
      let persistedParams: Record<string, unknown> = {};
      let persistedPresets: Record<string, string | null> = {};

      if (chatId) {
        // Existing chat - restore from storage
        persistedParams = useChatParamsStore.getState().getChatParams(chatId);
        const chatObj = useChatStore.getState().chats.get(chatId);

        // Only load persisted presets if the workflow matches
        // Presets are workflow-specific, so if user switched workflows, don't apply old presets
        const storedWorkflow = chatObj?.workflowName;
        const workflowMatches = storedWorkflow === workflowName;

        if (workflowMatches) {
          if (chatObj?.selectedPresets && Object.keys(chatObj.selectedPresets).length > 0) {
            persistedPresets = chatObj.selectedPresets as Record<string, string | null>;
          } else {
            persistedPresets = (persistedParams.__selectedPresets as Record<string, string | null>) || {};
          }
        }
        // If workflow doesn't match, persistedPresets stays empty - fresh start for new workflow
      } else {
        // New chat (no chatId): only clear temp params when the workflow actually changes.
        // This preserves user-set params (e.g. model) across re-renders and effect re-runs
        // while still clearing stale params when switching workflows.
        const workflowActuallyChanged = prevWorkflowNameRef.current !== undefined && prevWorkflowNameRef.current !== workflowName;
        if (workflowActuallyChanged) {
          logger.debug("[ChatInput] Workflow changed for new chat, clearing temp params", {
            from: prevWorkflowNameRef.current,
            to: workflowName,
          });
          useChatParamsStore.getState().clearTempNewChatParams();
        } else {
          // Same workflow (or initial mount) — restore any existing temp params
          // so user selections (model, etc.) survive re-renders
          const tempParams = useChatParamsStore.getState().tempNewChatParams;
          if (Object.keys(tempParams).length > 0) {
            persistedParams = { ...tempParams };
            persistedPresets = (tempParams.__selectedPresets as Record<string, string | null>) || {};
            logger.debug("[ChatInput] Restoring temp params for new chat", {
              keys: Object.keys(tempParams),
            });
          }
        }
      }
      prevWorkflowNameRef.current = workflowName;

      setWorkflowParams({ ...persistedParams });
      setSyncedParams({});
      setSelectedPresets(persistedPresets);

      // Load default preset for this workflow (only if no preset is already selected)
      const loadDefaultPresets = async () => {
        if (!currentProjectFromStore?.id || !workflowName) return;

        // Skip if user just explicitly changed to this workflow
        // (we want to start fresh, not load defaults from the new workflow)
        if (skipDefaultPresetsForWorkflowRef.current === workflowName) {
          return;
        }

        // Skip if user already has presets selected for this workflow
        if (Object.keys(persistedPresets).length > 0) {
          logger.debug("[ChatInput] Skipping default presets - user has presets selected", {
            persistedPresets
          });
          return;
        }

        // Skip if user has explicitly set any workflow params (including model)
        // This prevents overwriting user's manual selections
        if (Object.keys(persistedParams).length > 0) {
          // Check if any of the persisted params are user-set (not just defaults)
          // If user has set a model or other params, don't load default presets
          const hasUserSetParams = Object.keys(persistedParams).some(key => {
            // Skip metadata keys
            if (key === "__selectedPresets") return false;
            // If param exists in persistedParams, user has set it
            return true;
          });

          if (hasUserSetParams) {
            logger.debug("[ChatInput] Skipping default presets - user has manually set params", {
              persistedParams: Object.keys(persistedParams)
            });
            return;
          }
        }

        // Get all default presets for this workflow (one RPC)
        const defaults = await presetGrpc.getDefaultPresets(
          currentProjectFromStore.id,
          workflowName
        );

        if (Object.keys(defaults).length === 0 || presets.length === 0) {
          return;
        }

        logger.info("[ChatInput] Auto-loading default presets", {
          workflowName,
          defaults
        });

        const newSelectedPresets: Record<string, string> = {};
        const newValues = { ...persistedParams };

        // Apply each group's default preset
        for (const [groupName, presetName] of Object.entries(defaults)) {
          const preset = presets.find(p => p.name === presetName);
          if (!preset) continue;

          newSelectedPresets[groupName] = presetName;

          // Apply preset params with proper prefixing
          for (const [key, value] of Object.entries(preset.params)) {
            const fullKey = groupName ? `${groupName}.${key}` : key;
            newValues[fullKey] = value;
          }
        }

        if (Object.keys(newSelectedPresets).length === 0) {
          return;
        }

        newValues.__selectedPresets = newSelectedPresets;
        setSelectedPresets(newSelectedPresets);
        setWorkflowParams(newValues);

        // Persist to chatParamsStore
        if (chatId) {
          useChatParamsStore.getState().setChatParams(chatId, newValues);
        } else {
          useChatParamsStore.getState().setTempNewChatParams(newValues);
        }
      };

      loadDefaultPresets();
    }, [workflowName, chatId, currentProjectFromStore?.id, presets, workflowTagInfo]);

    // Apply per-tag model preferences (model_id, thinking_level, temperature, compaction)
    // to new chats that haven't had those values explicitly set yet.
    const tagPrefsApplied = useRef(false);
    useEffect(() => {
      if (!workflowInputs || tagPrefsApplied.current) return;
      if (chatId && !isPendingChat) return;

      // Find the model key
      let modelKey: string | null = null;
      for (const [key, def] of Object.entries(workflowInputs)) {
        if (def?.type === "model") { modelKey = key; break; }
      }
      if (!modelKey) return;

      const applyTagPrefs = async () => {
        const tagConfigs = await loadTagModelConfigs();
        if (Object.keys(tagConfigs).length === 0) return;

        const currentParams = chatId
          ? useChatParamsStore.getState().getChatParams(chatId)
          : useChatParamsStore.getState().tempNewChatParams;
        const currentModel = (currentParams[modelKey!] ?? {}) as Record<string, unknown>;

        // Find which tag the current model uses
        const currentTags = currentModel.tags as string[] | undefined;
        const currentTag = currentTags?.[0];
        if (!currentTag) return;

        const tagConfig = tagConfigs[currentTag];
        if (!tagConfig) return;

        // Merge tag preferences into model value (don't overwrite existing user/preset values)
        const merged = { ...currentModel };
        let changed = false;
        if (tagConfig.model_id && !merged.id) {
          merged.id = tagConfig.model_id;
          changed = true;
        }
        if (tagConfig.thinking_level && !merged.thinking_level) {
          merged.thinking_level = tagConfig.thinking_level;
          changed = true;
        }
        if (tagConfig.temperature !== undefined && merged.temperature === undefined) {
          merged.temperature = tagConfig.temperature;
          changed = true;
        }
        if (tagConfig.compaction_threshold !== undefined && merged.compaction_threshold === undefined) {
          merged.compaction_threshold = tagConfig.compaction_threshold;
          changed = true;
        }
        if (!changed) return;

        tagPrefsApplied.current = true;
        const newParams = { ...currentParams, [modelKey!]: merged };
        setWorkflowParams(newParams);
        if (chatId) {
          useChatParamsStore.getState().setChatParams(chatId, newParams);
        } else {
          useChatParamsStore.getState().setTempNewChatParams(newParams);
        }
        logger.info("[ChatInput] Applied tag model preferences", { tag: currentTag, config: tagConfig });
      };

      applyTagPrefs();
    }, [workflowInputs, chatId, isPendingChat]);

    // Reset tag prefs flag when workflow changes
    useEffect(() => {
      tagPrefsApplied.current = false;
    }, [workflowName]);

    // Handle preset selection for a specific target (workflow or group)
    // targetName is "" for workflow-level, or group name like "Agent A"
    const handlePresetChange = useCallback(
      (targetName: string, preset: Preset | null) => {
        const newSelectedPresets = {
          ...selectedPresets,
          [targetName]: preset?.name ?? null,
        };
        setSelectedPresets(newSelectedPresets);

        // Build new values including preset params and the preset selection itself
        const newValues: Record<string, unknown> = { ...workflowParams, __selectedPresets: newSelectedPresets };

        if (preset) {
          // Merge preset params with current values (preset overrides)
          for (const [key, value] of Object.entries(preset.params)) {
            // For groups, prefix with group name; for workflow, use as-is
            const fullKey = targetName ? `${targetName}.${key}` : key;
            // Normalize model values: if a preset has a bare string model, convert to {id: string}
            if (key === "model" && typeof value === "string" && value !== "") {
              newValues[fullKey] = { id: value };
            } else {
              newValues[fullKey] = value;
            }
          }
        }

        setWorkflowParams(newValues);

        // Persist to chatParamsStore (includes __selectedPresets)
        if (chatId) {
          useChatParamsStore.getState().setChatParams(chatId, newValues);
        } else {
          useChatParamsStore.getState().setTempNewChatParams(newValues);
        }
      },
      [workflowParams, chatId, selectedPresets]
    );

    // Check if there are configurable params (non-hidden, non-message)
    const hasConfigurableParams = useMemo(() => {
      if (!workflowInputs) return false;
      return Object.values(workflowInputs).some(
        (schema) =>
          getInputUI(schema) !== "hidden" &&
          schema.type !== "message" &&
          schema.type !== "attachments"
      );
    }, [workflowInputs]);

    // Get params explicitly marked for toolbar display
    const toolbarParams = useMemo(() => {
      if (!workflowInputs) return [];
      const result: [string, InputDef][] = [];
      for (const [name, schema] of Object.entries(workflowInputs)) {
        if (getInputUI(schema) === "toolbar") {
          result.push([name, schema]);
        }
      }
      return result;
    }, [workflowInputs]);

    // Group presets by their tag, filtering out hidden presets
    // Presets with the same tag show up in pickers for targets with that tag
    const presetsByTag = useMemo(() => {
      const map = new Map<string, Preset[]>();
      for (const preset of presets) {
        const tag = preset.tag || ""; // Empty string = no tag
        if (tag === "") continue; // Skip presets without tags
        if (isPresetHidden(preset.name)) continue; // Skip hidden presets
        if (!map.has(tag)) {
          map.set(tag, []);
        }
        map.get(tag)!.push(preset);
      }
      return map;
    }, [presets, isPresetHidden]);

    // Identify preset targets - workflow + groups that have tags
    // Each target gets its own preset picker
    // Returns array of { name, tag, isWorkflow } where name is "" for workflow or group name
    const presetTargets = useMemo(() => {
      const targets: Array<{ name: string; tag: string; isWorkflow: boolean }> = [];

      // Use actual tag metadata from workflow definition
      const { workflowTag, groupTags, groupUIs } = workflowTagInfo;

      // Add workflow-level target if it has a tag (presets may not exist yet — user can create them)
      if (workflowTag) {
        targets.push({ name: "", tag: workflowTag, isWorkflow: true });
      }

      // Add group targets if the group has ui: "toolbar" AND a tag
      for (const [groupName, groupTag] of Object.entries(groupTags)) {
        if (groupTag && groupUIs[groupName] === "toolbar") {
          targets.push({ name: groupName, tag: groupTag, isWorkflow: false });
        }
      }

      // Sort: workflow first, then groups alphabetically
      return targets.sort((a, b) => {
        if (a.isWorkflow) return -1;
        if (b.isWorkflow) return 1;
        return a.name.localeCompare(b.name);
      });
    }, [workflowTagInfo]);

    // Compute which params are covered by the CURRENTLY SELECTED presets
    // (not all presets, just the ones the user has selected)
    // Keys are full param names ("mode" for workflow, "Agent A.model" for groups)
    const paramsFromSelectedPresets = useMemo(() => {
      const covered = new Set<string>();
      for (const [targetName, presetName] of Object.entries(selectedPresets)) {
        if (!presetName) continue;
        // Find the target to get its tag
        const target = presetTargets.find(t => t.name === targetName);
        if (!target) continue;

        const tagPresets = presetsByTag.get(target.tag) || [];
        const selectedPreset = tagPresets.find(p => p.name === presetName);
        if (selectedPreset) {
          for (const paramName of Object.keys(selectedPreset.params)) {
            // For groups, prefix with group name; for workflow, use as-is
            const fullKey = targetName ? `${targetName}.${paramName}` : paramName;
            covered.add(fullKey);
          }
        }
      }
      return covered;
    }, [selectedPresets, presetTargets, presetsByTag]);

    // Check if any non-hidden, non-message, non-attachments param is unset
    // (no value in workflowParams, no meaningful default in schema, and not covered by a selected preset)
    const hasUnsetParams = useMemo(() => {
      if (!workflowInputs) return false;
      for (const [name, schema] of Object.entries(workflowInputs)) {
        if (getInputUI(schema) === "hidden") continue;
        if (schema.type === "message" || schema.type === "attachments") continue;
        if (schema.type === "tools" || schema.type === "preset" || schema.type === "thread") continue;

        // Model params need valid id or tags - check for empty/invalid model values
        const isEmptyModelValue = (val: unknown): boolean => {
          if (val === undefined || val === null || val === "") return true;
          if (typeof val === "object" && val !== null) {
            const obj = val as Record<string, unknown>;
            // Model selector needs 'id' or 'tags' to be valid
            const hasId = typeof obj.id === "string" && obj.id.length > 0;
            const hasTags = Array.isArray(obj.tags) && obj.tags.length > 0;
            return !hasId && !hasTags;
          }
          return false;
        };

        const schemaDefault = getInputDefault(schema);
        const hasDefault = schemaDefault !== undefined && schemaDefault !== null &&
          !(schema.type === "model" && isEmptyModelValue(schemaDefault));

        if (name.includes(".")) {
          const hasValue = workflowParams[name] !== undefined &&
            !(schema.type === "model" && isEmptyModelValue(workflowParams[name]));
          const coveredByPreset = paramsFromSelectedPresets.has(name);
          if (!hasValue && !hasDefault && !coveredByPreset) return true;
          continue;
        }
        const hasValue = workflowParams[name] !== undefined &&
          !(schema.type === "model" && isEmptyModelValue(workflowParams[name]));
        const coveredByPreset = paramsFromSelectedPresets.has(name);
        if (!hasValue && !hasDefault && !coveredByPreset) return true;
      }
      return false;
    }, [workflowInputs, workflowParams, paramsFromSelectedPresets]);

    // Handle workflow param changes - update local state and persist all params to chatParamsStore
    const handleWorkflowParamsChange = useCallback(
      (newParams: Record<string, unknown>) => {
        setWorkflowParams(newParams);

        // Persist all params to chatParamsStore
        if (chatId) {
          useChatParamsStore.getState().setChatParams(chatId, newParams);
        } else {
          useChatParamsStore.getState().setTempNewChatParams(newParams);
        }
      },
      [chatId]
    );

    // Check if there are unsaved param changes
    // Only show sync button when chat is active (has a running workflow)
    const hasUnsyncedChanges = useMemo(() => {
      if (!chatId) return false; // No chat = nothing to sync
      if (!isChatBusy) return false; // No active workflow = can't sync (params applied on next send)

      // Compare current params to synced params (excluding UI-only metadata)
      for (const [key, value] of Object.entries(workflowParams)) {
        if (key === "__selectedPresets") continue; // Skip UI-only metadata
        if (syncedParams[key] !== value) {
          return true;
        }
      }
      return false;
    }, [chatId, isChatBusy, workflowParams, syncedParams]);

    // Sync workflow params to server
    const handleSyncParams = useCallback(async () => {
      if (!chatId || isSyncing) return;

      // Get all changed params (excluding __selectedPresets which is UI-only)
      const changedParams: Record<string, unknown> = {};
      for (const [key, value] of Object.entries(workflowParams)) {
        if (key === "__selectedPresets") continue; // Skip UI-only metadata
        if (syncedParams[key] !== value) {
          changedParams[key] = value;
        }
      }

      if (Object.keys(changedParams).length === 0) return;

      setIsSyncing(true);
      try {
        logger.info("[ChatInput] Syncing workflow params", {
          chatId,
          changedParams,
        });
        await api.chatsV2.updateWorkflowParams(chatId, changedParams);
        // Update synced state to match current
        setSyncedParams({ ...syncedParams, ...changedParams });
        logger.info("[ChatInput] Params synced successfully");
      } catch (error) {
        logger.error("[ChatInput] Failed to sync params", { error, chatId });
      } finally {
        setIsSyncing(false);
      }
    }, [chatId, workflowParams, syncedParams, isSyncing]);

    // Thread-specific param change handler (receives full values map from WorkflowParamsPanel)
    const handleThreadParamsChange = useCallback(
      (newParams: Record<string, unknown>) => {
        setThreadParamValues(newParams);
      },
      []
    );

    // Current thread param values: local edits if any, otherwise fetched from Temporal
    const currentThreadParams = threadParamValues ?? threadParamsOverride?.values ?? {};

    // Check if thread params have unsaved changes (compare against fetched baseline)
    const hasUnsyncedThreadChanges = useMemo(() => {
      if (!isViewingThreadParams || !threadParamsOverride?.isRunning) return false;
      if (!chatId || !threadParamValues) return false;  // No local edits yet

      const baseline = threadParamsOverride.values;
      for (const [key, value] of Object.entries(threadParamValues)) {
        if (key === "__selectedPresets") continue;
        // Compare against last-synced value if available, otherwise against fetched baseline
        const reference = key in threadSyncedParams ? threadSyncedParams[key] : baseline[key];
        if (reference !== value) return true;
      }
      return false;
    }, [isViewingThreadParams, threadParamsOverride, chatId, threadParamValues, threadSyncedParams]);

    // Sync thread params to server
    const handleSyncThreadParams = useCallback(async () => {
      if (!chatId || !selectedThreadId || !threadParamValues || isSyncing) return;

      const baseline = threadParamsOverride?.values ?? {};
      const changedParams: Record<string, unknown> = {};
      for (const [key, value] of Object.entries(threadParamValues)) {
        if (key === "__selectedPresets") continue;
        const reference = key in threadSyncedParams ? threadSyncedParams[key] : baseline[key];
        if (reference !== value) {
          changedParams[key] = value;
        }
      }

      if (Object.keys(changedParams).length === 0) return;

      setIsSyncing(true);
      try {
        logger.info("[ChatInput] Syncing thread workflow params", {
          chatId,
          threadId: selectedThreadId,
          changedParams,
        });
        await api.chatsV2.updateWorkflowParams(chatId, changedParams, selectedThreadId);
        // Update the baseline to include synced values so the sync button clears.
        // Also clear threadParamValues so UI falls back to the updated baseline.
        if (threadParamsOverride) {
          setThreadParamsOverride({
            ...threadParamsOverride,
            values: { ...threadParamsOverride.values, ...changedParams },
          });
        }
        setThreadParamValues(null);
        setThreadSyncedParams({});
        logger.info("[ChatInput] Thread params synced successfully");
      } catch (error) {
        logger.error("[ChatInput] Failed to sync thread params", { error, chatId, threadId: selectedThreadId });
      } finally {
        setIsSyncing(false);
      }
    }, [chatId, selectedThreadId, threadParamValues, threadParamsOverride, threadSyncedParams, isSyncing]);

    // Attachment management
    const attachFile = useAttachmentStore((state) => state.attachFile);
    const removeAttachment = useAttachmentStore(
      (state) => state.removeAttachment
    );
    const clearAttachments = useAttachmentStore(
      (state) => state.clearAttachments
    );
    const uploading = useAttachmentStore((state) => state.uploading);

    // Use tabId to isolate attachments per tab when no chatId exists
    // Subscribe directly to attachments Map for reactivity
    const attachmentSessionId = chatId || tabId || "temp";
    const attachmentsMap = useAttachmentStore((state) => state.attachments);
    const attachments = useMemo(
      () => attachmentsMap.get(attachmentSessionId) || [],
      [attachmentsMap, attachmentSessionId]
    );

    // Check if workflow is paused (for discuss mode button)
    const chatForStatus = useChat(chatId || "");
    const isPaused = chatForStatus?.workflowStatus === ChatWorkflowStatus.PAUSED;

    // Thread color for border - non-main threads get their color
    const threadBorderColor = useMemo(() => {
      if (!selectedThreadId || !chatId || selectedThreadId === chatId) return undefined;
      return getThreadColor(selectedThreadId, false);
    }, [selectedThreadId, chatId]);

    // Resolve thread display name from workflow execution tree + activeThreads fallback
    const activeThreadsForName = useActiveThreads(chatId || "");
    const threadDisplayName = useMemo(() => {
      if (!selectedThreadId || !workflowExecution) return 'Thread';
      const findThread = (wf: WorkflowExecution): WorkflowExecution | undefined => {
        if (wf.thread === selectedThreadId) return wf;
        for (const child of wf.children) {
          const found = findThread(child);
          if (found) return found;
        }
        return undefined;
      };
      const match = findThread(workflowExecution);
      if (match) {
        if (match.threadTitle) return formatNodeId(match.threadTitle);
        if (match.spawnedByNodeId) return formatNodeId(match.spawnedByNodeId);
      }
      // Fallback: resolve from activeThreads streaming data
      return resolveThreadNameFromActiveThreads(selectedThreadId, activeThreadsForName) || 'Thread';
    }, [selectedThreadId, workflowExecution, activeThreadsForName]);

    // In discuss mode or when a question is pending, treat as not-streaming so the input stays enabled
    const effectiveStreaming = isDiscussMode ? false : (hasPendingQuestion ? false : (forceStreaming || isStreaming));

    // Allow messaging at all times - users can type while workflow is running
    const isMessagingAllowed = true;


    // Debug: Log streaming state changes
    useEffect(() => {
      logger.info("[ChatInput] Streaming state:", {
        isStreaming,
        forceStreaming,
        effectiveStreaming,
        isChatBusy,
        chatId: chatId?.slice(0, 8),
      });
    }, [isStreaming, forceStreaming, effectiveStreaming, isChatBusy, chatId]);

    // Drag and drop functionality
    const handleFileDrop = async (files: File[]) => {
      for (const file of files) {
        try {
          await attachFile(attachmentSessionId, file);
        } catch (error) {
          console.error("Upload failed:", error);
          // Error is already shown as toast by attachFile
        }
      }
    };

    const { isDragging, isValidDrag, handlers, handlePaste } = useDragAndDrop({
      onDrop: handleFileDrop,
      // Use broad MIME type filtering for drag-drop UI feedback
      // Actual validation happens in attachmentStore using filename-based detection
      allowedMimeTypes: [
        "image/", // All image types
        "text/", // All text types
        "application/json",
        "application/xml",
        "application/x-yaml",
        "application/pdf",
        "application/msword",
        "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
        "application/octet-stream", // Allow unknown types, will be validated by filename
      ],
      maxFileSize: 50 * 1024 * 1024, // 50MB
      maxFiles: 10,
      disabled: disabled || uploading,
    });

    // Add paste listener for file pasting
    useEffect(() => {
      const handlePasteEvent = (e: ClipboardEvent) => {
        // Only handle paste if the chat input or a child element is focused
        const activeElement = document.activeElement;
        const chatInputContainer = document.querySelector(
          ".chat-input-container"
        );
        if (chatInputContainer && chatInputContainer.contains(activeElement)) {
          handlePaste(e);
        }
      };

      document.addEventListener("paste", handlePasteEvent);
      return () => document.removeEventListener("paste", handlePasteEvent);
    }, [handlePaste]);

    const handleQuestionSubmit = useCallback(async (answer: { answers: Array<{ question: string; selected: string[]; freetext?: string }> }) => {
      if (!chatId || !pendingQuestion) return;
      await useChatStore.getState().resolveQuestion(
        chatId,
        pendingQuestion.question_id,
        "reply",
        JSON.stringify(answer)
      );
    }, [chatId, pendingQuestion]);

    const handleSend = async () => {
      const hasContent = input.trim().length > 0 || selectedPrompts.length > 0;
      const hasAttachments = attachments.length > 0;
      
      // Parse markers from input text
      // Format: [[filePath:startLine-endLine]] where filePath can be full path or just filename
      const markerPattern = /\[\[([^\]]+):(\d+)-(\d+)\]\]/g;
      const markers: Array<{ filePath: string; fileName: string; startLine: number; endLine: number }> = [];
      let match;
      while ((match = markerPattern.exec(input)) !== null) {
        const filePath = match[1];
        const fileName = filePath.split('/').pop() || filePath;
        markers.push({
          filePath,
          fileName,
          startLine: parseInt(match[2], 10),
          endLine: parseInt(match[3], 10),
        });
      }
      const hasCodeContexts = markers.length > 0;

      if ((hasContent || hasAttachments || hasCodeContexts) && !effectiveStreaming && isMessagingAllowed) {
        // Keep markers in the message text so it displays as the user typed it
        const messageText = input.trim();
        const fullMessage = selectedPrompts
          ? `${messageText}\n\n${selectedPrompts}`
          : messageText;
        const attachmentIds = attachments.map((a) => a.id);
        
        // Format code contexts as part of the message
        // Read file content and include it directly in the message text
        let messageWithContexts = fullMessage;
        
        if (markers.length > 0) {
          // Read file content for each marker and format it in the message
          try {
            const { getFileContent, getFilePreviewInfo } = await import('../../api/fileSystem');
            const { useWorktreeStore } = await import('../../store/worktreeStore');
            const worktreeStore = useWorktreeStore.getState();
            const currentWorktree = worktreeStore.currentWorktree;
            
            const contextSections: string[] = [];
            
            for (const marker of markers) {
              try {
                const previewInfo = await getFilePreviewInfo(marker.filePath, currentWorktree?.id);
                if (previewInfo.viewerKind !== 'text') {
                  contextSections.push(
                    `[Cannot include line-based context from ${marker.fileName}:${marker.startLine}-${marker.endLine} — ${previewInfo.viewerKind} files are preview-only (${previewInfo.mimeType})]`
                  );
                  continue;
                }

                // Use the full file path to read the file
                const fileContent = await getFileContent(marker.filePath, currentWorktree?.id);
                const lines = fileContent.split('\n');
                
                // Extract the selected lines (1-indexed to 0-indexed)
                const selectedLines = lines.slice(marker.startLine - 1, marker.endLine);
                const selectedContent = selectedLines.join('\n');
                
                // Format as code block with file path and line numbers
                const language = marker.fileName.split('.').pop() || '';
                const contextSection = `${marker.fileName}:${marker.startLine}-${marker.endLine}\n\`\`\`${language}\n${selectedContent}\n\`\`\``;
                contextSections.push(contextSection);
              } catch (error) {
                console.warn(`Failed to read file content for ${marker.fileName}:`, error);
                // Add a note if file couldn't be read
                contextSections.push(`[Could not read ${marker.fileName}:${marker.startLine}-${marker.endLine}]`);
              }
            }
            
            // Append context sections to the message
            if (contextSections.length > 0) {
              const contextText = '\n\n' + contextSections.join('\n\n');
              messageWithContexts = fullMessage + contextText;
            }
          } catch (error) {
            console.warn('Failed to import fileSystem:', error);
            // Fallback: just use the original message
            messageWithContexts = fullMessage;
          }
        }
        
        // Only use regular attachments (no file reference attachments)
        const allAttachmentIds = [...attachmentIds];

        // For first message in a new chat, we MUST send the effective workflow.
        // Relying on backend defaults here is brittle (and has caused fallbacks to Agent).
        const workflowToSend = selectedWorkflow ?? workflowName;

        // Get workflow params (only include non-empty values)
        // Filter out __selectedPresets which is only used for UI state persistence
        const { __selectedPresets: _, ...paramsWithoutMeta } = workflowParams;

        const paramsToSend =
          Object.keys(paramsWithoutMeta).length > 0 ? paramsWithoutMeta : undefined;

        // Filter selectedPresets to only include valid targets for current workflow
        // This prevents stale presets from a different workflow from being sent
        const validTargetNames = new Set(presetTargets.map(t => t.name));
        const presetsToSend = Object.entries(selectedPresets).reduce(
          (acc, [k, v]) => (v && validTargetNames.has(k) ? { ...acc, [k]: v } : acc),
          {} as Record<string, string>
        );

        onSend(
          messageWithContexts,
          allAttachmentIds.length > 0 ? allAttachmentIds : undefined,
          workflowToSend,
          paramsToSend,
          undefined, // targetThread
          Object.keys(presetsToSend).length > 0 ? presetsToSend : undefined
        );
        handleClearInput(); // This will clear both input and auto-draft
        clearAttachments(attachmentSessionId);

        // Don't restore params from chatParamsStore after sending - keep current state
        // The params were already sent and saved. Restoring from store would overwrite
        // any changes the user made right before sending (like switching models).
        // The current workflowParams state is the source of truth.

        // Update syncedParams to match what was sent - this clears the sync button
        // The params we just sent are now "synced" with the backend
        if (paramsToSend && chatId) {
          setSyncedParams({ ...paramsToSend });
        }
      }
    };

    const handleAttachClick = () => {
      fileInputRef.current?.click();
    };

    const handleFileSelect = async (
      event: React.ChangeEvent<HTMLInputElement>
    ) => {
      const files = event.target.files;
      if (!files) return;

      for (const file of Array.from(files)) {
        try {
          await attachFile(attachmentSessionId, file);
        } catch (error) {
          console.error("Upload failed:", error);
        }
      }
      event.target.value = "";
    };

    const handleRemoveAttachment = (attachmentId: string) => {
      removeAttachment(attachmentSessionId, attachmentId);
    };

    const handleStop = useCallback(() => {
      logger.info("🔴 Stop button handleStop called", {
        onStop: !!onStop,
        isStreaming,
      });
      if (onStop) {
        onStop();
      } else {
        console.warn("⚠️ No onStop handler provided");
      }
    }, [onStop, isStreaming]);


    // Handle compaction
    const [isCompacting, setIsCompacting] = useState(false);
    const canCompact =
      chatId && messageCount > 0 && !isCompacting && !effectiveStreaming;

    const handleCompact = useCallback(async () => {
      if (!canCompact) {
        logger.warn("⚠️ Cannot compact", {
          chatId,
          messageCount,
          isCompacting,
          effectiveStreaming,
        });
        return;
      }

      // Get the thread ID from chat data (workflow_id is the thread)
      const chatObj = useChatStore.getState().chats.get(chatId);
      const threadId = chatObj?.workflowId;
      if (!threadId) {
        logger.error("❌ Cannot compact: no thread ID found", { chatId });
        alert("Cannot compact: no active thread found");
        return;
      }

      try {
        setIsCompacting(true);
        logger.info("🗜️ Starting context compaction", { chatId, threadId, messageCount });
        const result = await api.chatsV2.compact(chatId, threadId);
        logger.info("✅ Context compaction initiated", { chatId, threadId, result });
      } catch (error: any) {
        logger.error("[ChatInput] Failed to compact context", { chatId, threadId, error });
        // Show error to user
        alert(`Failed to start compaction: ${error?.message || error}`);
        setIsCompacting(false);
      } finally {
        // Keep the compacting state while the activity is running
        // It will be cleared when currentActivity changes
      }
    }, [chatId, canCompact, messageCount, isCompacting, effectiveStreaming]);

    // Track compaction activity state
    useEffect(() => {
      if (currentActivity === "Compact") {
        setIsCompacting(true);
      } else if (isCompacting) {
        // Clear compacting state when activity changes
        setIsCompacting(false);
      }
    // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [currentActivity]);

    // Note: Global ESC key handling is done in ModernApp.tsx with proper priority checks
    // (checks for open modals, workflow builder, etc. before pausing chat)

    // Local escape hatch: allow closing expanded settings with Escape when focus is in chat input area.
    useEffect(() => {
      if (settingsPage === null) return;

      const handleEscapeToCloseSettings = (event: KeyboardEvent) => {
        if (event.key !== "Escape") return;

        const container = containerRef.current;
        if (!container) return;

        const activeElement = document.activeElement as HTMLElement | null;
        if (!activeElement) return;

        // Only handle ESC when focus is inside this chat input container.
        if (!container.contains(activeElement)) return;

        event.preventDefault();
        event.stopPropagation();
        setSettingsPage(null);
      };

      // Use capture to run before document-level handlers that may use ESC for pause/stop.
      window.addEventListener("keydown", handleEscapeToCloseSettings, true);
      return () => {
        window.removeEventListener("keydown", handleEscapeToCloseSettings, true);
      };
    }, [settingsPage]);

    // Compute effective popover props once (used by whichever trigger renders it)
    const popoverInputs = isViewingThreadParams && threadParamsOverride
      ? threadParamsOverride.inputs
      : workflowInputs;
    const popoverValues = isViewingThreadParams && threadParamsOverride
      ? currentThreadParams
      : workflowParams;
    const popoverOnChange = isViewingThreadParams && threadParamsOverride
      ? handleThreadParamsChange
      : handleWorkflowParamsChange;

    const renderSettingsPopover = (initialPage: 'main' | 'model' | 'preset' | 'params') => {
      if (!popoverInputs) return null;
      return (
        <ChatSettingsPopover
          isOpen={true}
          onClose={() => setSettingsPage(null)}
          initialPage={initialPage}
          inputs={popoverInputs}
          values={popoverValues}
          onChange={popoverOnChange}
          presets={presets}
          selectedPreset={selectedPresets?.["default"] ?? null}
          onPresetChange={(preset) => handlePresetChange("default", preset)}
          workflowTag={workflowTagInfo?.workflowTag}
          groupTags={workflowTagInfo?.groupTags}
        />
      );
    };

    return (
      <div
        className={`layout-stable ${
          effectiveStreaming ? "chat-streaming" : ""
        }`}
        style={{ minHeight: 0, position: "relative", zIndex: 20 }}
      >
        <div className="px-4 sm:px-6 lg:px-8">
          <div className="max-w-[1200px] mx-auto">
            <div ref={containerRef} className="my-2">
              <div
                className={`relative rounded-lg chat-input-container border-2 transition-all duration-200 cursor-text ${isDiscussMode ? "border-blue-500/70" : hasPendingQuestion ? "border-yellow-500/70" : threadBorderColor ? "" : "border-border/70"}`}
                data-onboarding="chat-input"
                onClick={handleInputContainerClick}
                style={{
                  padding: "4px 8px",
                  ...(threadBorderColor && !isDiscussMode ? { borderColor: threadBorderColor } : {}),
                  backgroundColor: isDragging
                    ? "var(--chat-drag-bg, var(--transparent-button-hover))"
                    : effectiveStreaming
                    ? "hsl(var(--primary) / 0.05)"
                    : "var(--chat-input-bg)",
                  transform: isDragging ? "scale(1.002)" : "none",
                  transition: isDragging ? "transform 0.1s" : "all 0.2s",
                }}
                {...handlers}
              >
                {/* Drag overlay indicator */}
                <DragDropOverlay
                  isDragging={isDragging}
                  isValid={isValidDrag}
                />
                <div className="flex flex-col gap-1 relative z-[1001]">
                  {/* Question prompt - replaces normal input when ask_user question is pending */}
                  {hasPendingQuestion && askUserQuestion && (
                    <QuestionPrompt
                      questions={askUserQuestion.questions.map((q: any) => ({
                        question: q.question,
                        options: q.options || [],
                        allowMultiple: q.allow_multiple ?? false,
                      }))}
                      onSubmit={handleQuestionSubmit}
                    />
                  )}

                  {/* Normal input UI - hidden when question prompt is shown */}
                  {!showAskOnly && (<>
                  {/* Attachments */}
                    <div className="pt-3 px-2">
                      <AttachmentPreview
                        attachments={attachments}
                        onRemove={handleRemoveAttachment}
                        className={attachments.length > 0 ? "" : "hidden"}
                      />
                    </div>

                    <div className="px-2">
                      <ChatTextArea
                        ref={textareaRef}
                        value={input}
                        onChange={setInput}
                        onSend={handleSend}
                        onStop={handleStop}
                        disabled={disabled || !isMessagingAllowed}
                        isStreaming={effectiveStreaming}
                        chatId={chatId}
                      />
                    </div>

                  {/* Thread indicator - shown when typing into a sub-thread */}
                  {threadBorderColor && (
                    <div className="px-3 py-0.5">
                      <span
                        className="text-[10px] font-medium"
                        style={{ color: threadBorderColor }}
                      >
                        {threadDisplayName}
                      </span>
                    </div>
                  )}

                  {/* Discuss mode hint */}
                  {isDiscussMode && (
                    <div className="px-3 py-1">
                      <span className="text-[11px] text-blue-600 dark:text-blue-400">
                        Discussion mode — messages won't resume the workflow
                      </span>
                    </div>
                  )}

                  {/* Question prompt - shown when agent is waiting for user input */}
                  {hasPendingQuestion && askUserQuestion && (
                      <QuestionPrompt
                        questions={askUserQuestion.questions.map((q: any) => ({
                          question: q.question,
                          options: q.options || [],
                          allowMultiple: q.allow_multiple ?? false,
                        }))}
                        onSubmit={handleQuestionSubmit}
                      />
                  )}

                  {/* Single Bottom Row: All Controls */}
                  {<div className="flex items-center justify-between pt-2 mt-2 border-t border-border/50">
                    {/* Left side: Workflow selector + inline required params + expand button */}
                    <div className="flex items-center gap-1 flex-wrap" data-onboarding="chat-controls">
                      {/* Workflow Selector - disabled once chat has started (not pending) */}
                      <div>
                        <WorkflowSelector
                          value={selectedWorkflow}
                          onChange={(wf) => {
                            // Mark the workflow user explicitly changed to - skip loading defaults for it
                            // We need to compute the effective workflow name (wf or userDefaultWorkflow)
                            const effectiveWf = wf || userDefaultWorkflow;
                            skipDefaultPresetsForWorkflowRef.current = effectiveWf;
                            setSelectedWorkflow(wf);
                            // Close settings popover when workflow changes
                            setSettingsPage(null);
                            // Clear workflow params when switching workflows
                            // This prevents stale params from a different workflow
                            // being sent to the new workflow (which would cause validation errors)
                            setWorkflowParams({});
                            setSelectedPresets({});
                            // Also clear persisted params for this chat
                            if (chatId) {
                              useChatParamsStore.getState().setChatParams(chatId, {});
                              // Clear selected_presets in chatStore so the useEffect doesn't reload them
                              useChatStore.setState((state) => {
                                const existing = state.chats.get(chatId);
                                if (!existing) return state;
                                const newChats = new Map(state.chats);
                                newChats.set(chatId, { ...existing, selectedPresets: {} });
                                return { chats: newChats };
                              });
                            }
                          }}
                          isStreaming={effectiveStreaming}
                          disabled={
                            disabled ||
                            !isPendingChat
                          }
                          compact={isCompact}
                        />
                      </div>

                      {/* Inline Preset Pickers - one per target (workflow or group with tag) */}
                      {/* Note: Presets and params are ALWAYS editable, even while streaming */}
                      {presetTargets.length > 0 && (
                        <div className="flex items-center gap-1">
                          {presetTargets.map((target) => (
                            <InlinePresetPicker
                              key={`preset-${target.name || "workflow"}`}
                              presets={presetsByTag.get(target.tag) || []}
                              value={selectedPresets[target.name]}
                              onChange={(preset) =>
                                handlePresetChange(target.name, preset)
                              }
                              groupLabel={target.name || undefined} // Empty string = workflow, show "Preset"
                              isLoading={presetsLoading}
                              disabled={disabled || !isMessagingAllowed}
                              isStreaming={false}
                            />
                          ))}
                        </div>
                      )}

                      {/* Toolbar Params - explicitly marked with ui: "toolbar" */}
                      {toolbarParams.length > 0 && (
                        <div className="flex items-center gap-1">
                          {toolbarParams.map(([paramName, schema]) => (
                            <InlineParamInput
                              key={paramName}
                              name={paramName}
                              schema={schema}
                              value={workflowParams[paramName] ?? getInputDefault(schema)}
                              onChange={(value) =>
                                handleWorkflowParamsChange({
                                  ...workflowParams,
                                  [paramName]: value,
                                })
                              }
                              disabled={disabled || !isMessagingAllowed}
                              isStreaming={false}
                            />
                          ))}
                        </div>
                      )}

                      {/* Model pill - opens settings popover directly to model page */}
                      {hasConfigurableParams && (() => {
                        const modelValue = workflowParams?.model as Record<string, unknown> | undefined
                          ?? Object.entries(workflowParams).find(([k]) => k.endsWith('.model'))?.[1] as Record<string, unknown> | undefined;
                        const currentModelTag = (modelValue?.tags as string[])?.[0];
                        const currentModelId = modelValue?.id as string | undefined;
                        const resolvedModel = currentModelTag
                          ? availableModels.find(m => m.tags?.includes(currentModelTag))
                          : currentModelId
                            ? availableModels.find(m => m.id === currentModelId || m.id.split('@')[0] === currentModelId)
                            : null;
                        const currentModelDisplayName = resolvedModel?.name || currentModelId || 'auto';
                        const getProviderColor = (driverId?: string): string => {
                          switch (driverId) {
                            case 'anthropic': return 'bg-orange-400';
                            case 'openai': case 'codex': return 'bg-green-500';
                            case 'gemini': case 'vertexai': return 'bg-blue-400';
                            case 'xai': return 'bg-red-400';
                            default: return 'bg-gray-400';
                          }
                        };
                        const currentModelProvider = resolvedModel?.driverId || (modelValue?.driver_id as string) || undefined;
                        return (
                          <div className="relative">
                            <button
                              className={cn(
                                "flex items-center gap-1 rounded transition-colors text-[10px] font-medium h-6 px-2",
                                settingsPage === 'model'
                                  ? "bg-primary/20 text-primary hover:bg-primary/30"
                                  : "bg-[var(--chat-button-bg)] text-[var(--chat-button-text)] hover:bg-[var(--chat-button-hover)]"
                              )}
                              onClick={() => setSettingsPage(settingsPage === 'model' ? null : 'model')}
                              title="Model settings"
                            >
                              <span className={cn("w-1.5 h-1.5 rounded-full", getProviderColor(currentModelProvider))} />
                              {currentModelDisplayName}
                              <ChevronDown className="w-2.5 h-2.5 opacity-50" />
                            </button>
                            {settingsPage === 'model' && renderSettingsPopover('model')}
                          </div>
                        );
                      })()}

                      {/* Settings Button - opens settings popover */}
                      {(hasConfigurableParams || isViewingThreadParams) && (
                        <div className="relative">
                          <Tooltip
                            content={
                              settingsPage !== null
                                ? "Hide settings"
                                : "Show all settings"
                            }
                            placement="top"
                          >
                            <button
                              data-contextual-tip="params-toggle"
                              onClick={() =>
                                setSettingsPage(settingsPage !== null ? null : 'main')
                              }
                              disabled={disabled || !isMessagingAllowed}
                              className={cn(
                                "relative flex items-center justify-center rounded transition-colors h-6 w-6",
                                !disabled
                                  ? "cursor-pointer hover:bg-[var(--chat-button-hover)]"
                                  : "cursor-default opacity-60",
                                settingsPage !== null
                                  ? "bg-primary/20 text-primary"
                                  : "bg-[var(--chat-button-bg)] text-[var(--chat-button-text)]"
                              )}
                            >
                              <Settings2 className="w-3.5 h-3.5" />
                              {hasUnsetParams && (
                                <div className="absolute -top-0.5 -right-0.5 w-2 h-2 rounded-full bg-red-500" />
                              )}
                            </button>
                          </Tooltip>
                          {settingsPage !== null && settingsPage !== 'model' && renderSettingsPopover(settingsPage)}
                        </div>
                      )}

                      {/* Sync Button - always clickable (can sync params while streaming) */}
                      {(hasUnsyncedChanges || hasUnsyncedThreadChanges) && (
                        <Tooltip
                          content={isViewingThreadParams ? "Sync settings to thread workflow" : "Sync settings to workflow"}
                          placement="top"
                        >
                          <button
                            onClick={isViewingThreadParams ? handleSyncThreadParams : handleSyncParams}
                            disabled={disabled || isSyncing}
                            className={cn(
                              "flex items-center justify-center rounded transition-colors h-6 px-2 gap-1",
                              "text-[10px] font-medium",
                              !disabled && !isSyncing
                                ? "cursor-pointer hover:bg-primary/30"
                                : "cursor-default opacity-60",
                              "bg-primary/20 text-primary"
                            )}
                          >
                            <RefreshCw
                              className={cn(
                                "w-3 h-3",
                                isSyncing && "animate-spin"
                              )}
                            />
                            <span>Sync</span>
                          </button>
                        </Tooltip>
                      )}
                    </div>

                    {/* Right side: Action buttons + Scroll to bottom */}
                    <div className="flex items-center gap-2">
                      {/* Action buttons */}
                      <ChatActionButtons
                        onSend={handleSend}
                        onStop={handleStop}
                        canSend={
                          input.trim().length > 0 ||
                          selectedPrompts.length > 0 ||
                          attachments.length > 0
                        }
                        isStreaming={effectiveStreaming}
                        disabled={disabled || !isMessagingAllowed}

                        onAttach={handleAttachClick}
                        uploading={uploading}
                        onToggleRecentChanges={onToggleRecentChanges}
                        isRecentChangesOpen={isRecentChangesOpen}
                        hasWorktree={!!worktreeId}
                        onCompact={canCompact ? handleCompact : undefined}
                        isCompacting={isCompacting}
                        isDev={isDev}
                        forceStreaming={forceStreaming}
                        onToggleForceStreaming={() =>
                          setForceStreaming(!forceStreaming)
                        }
                        promptsElement={
                          <PromptsSelector
                            key="prompts-selector"
                            projectId={projectId || currentProject?.id}
                            onPromptsChange={setSelectedPrompts}
                            compact={true}
                            iconOnly={isCompact}
                          />
                        }
                        isDiscussMode={isDiscussMode}
                        onToggleDiscuss={onToggleDiscuss}
                        isPaused={isPaused}
                        compact={isCompact}
                      />
                    </div>
                  </div>}
                  </>)}

                  {/* Hidden file input */}
                  <input
                    ref={fileInputRef}
                    type="file"
                    multiple
                    accept={getAcceptedMimeTypes()}
                    onChange={handleFileSelect}
                    className="hidden"
                  />
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>
    );
  }
);

export const ChatInput = memo(ChatInputComponent);
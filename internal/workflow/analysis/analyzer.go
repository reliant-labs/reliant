// Copyright (c) 2025 Reliant Labs
// Package analysis contains EXPERIMENTAL workflow scoring heuristics.
//
// BIG WARNING: the scores, risk labels, speed estimates, recommendations, and
// warnings produced here are currently meaningless as product signals. They are
// rough shape-based experiments intended only to make workflow topology visible
// while we learn what useful workflow analysis should look like. Do not use
// these values for ranking workflows, blocking workflows, quality judgments,
// product decisions, or compatibility contracts without replacing the heuristic
// model with validated metrics.
//
// This package is intentionally standalone and is not wired into product or CLI
// surfaces.
package analysis

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"google.golang.org/protobuf/types/known/structpb"
)

// WorkflowLoader loads a referenced workflow such as builtin://agent.
type WorkflowLoader func(workflowRef string) (*reliantv1.Workflow, error)

// Options configures static workflow complexity and runtime analysis.
type Options struct {
	WorkflowLoader WorkflowLoader

	// UnboundedLoopIterations is the expected iteration count used when a loop has
	// no statically discoverable iteration bound. It is intentionally a hint, not
	// an execution cap.
	UnboundedLoopIterations int
	// ParallelLoopItems is the expected item count used when a parallel loop's
	// items expression cannot be evaluated statically.
	ParallelLoopItems int

	CallLLMSeconds      int
	RunSeconds          int
	AgentSeconds        int
	StructuredSeconds   int
	ExecuteToolsSeconds int
	ActivitySeconds     int
}

// DefaultOptions returns conservative static-analysis defaults. The values are
// deliberately approximate: they make workflow shape visible without pretending
// to predict exact wall-clock time.
func DefaultOptions() Options {
	return Options{
		UnboundedLoopIterations: 20,
		ParallelLoopItems:       3,
		CallLLMSeconds:          90,
		RunSeconds:              30,
		AgentSeconds:            600,
		StructuredSeconds:       240,
		ExecuteToolsSeconds:     10,
		ActivitySeconds:         1,
	}
}

// AnalyzeWorkflow performs standalone static complexity and speed analysis for a
// workflow. It does not validate the workflow and does not execute scenarios.
func AnalyzeWorkflow(workflow *reliantv1.Workflow, options Options) Report {
	if workflow == nil {
		return Report{Status: "error"}
	}
	if options.UnboundedLoopIterations == 0 && options.ParallelLoopItems == 0 && options.CallLLMSeconds == 0 {
		options = DefaultOptions()
	}
	options = normalizeOptions(options)

	analyzer := &workflowAnalyzer{
		options: options,
	}
	return analyzer.analyzeWorkflow(workflow, nil, analysisContext{
		workflowRef: workflow.GetName(),
	})
}

// Report is the standalone experimental static analysis artifact.
type Report struct {
	WorkflowName     string            `json:"workflow_name"`
	Status           string            `json:"status"`
	Complexity       ComplexityReport  `json:"complexity"`
	Speed            SpeedReport       `json:"speed"`
	Recommendations  []Recommendation  `json:"recommendations,omitempty"`
	Warnings         []Warning         `json:"warnings,omitempty"`
	Nodes            []NodeReport      `json:"nodes,omitempty"`
	ReferencedErrors []ReferencedError `json:"referenced_errors,omitempty"`
}

type ComplexityReport struct {
	Score                int      `json:"score"`
	Risk                 string   `json:"risk"`
	Reasons              []string `json:"reasons,omitempty"`
	AgentNodes           int      `json:"agent_nodes"`
	StructuredAgentNodes int      `json:"structured_agent_nodes"`
	LoopNodes            int      `json:"loop_nodes"`
	UnboundedLoops       int      `json:"unbounded_loops"`
	ParallelLoops        int      `json:"parallel_loops"`
	MaxLoopDepth         int      `json:"max_loop_depth"`
	SpawnEnabledAgents   int      `json:"spawn_enabled_agents"`
	BroadToolNodes       int      `json:"broad_tool_nodes"`
	ReferencedWorkflows  int      `json:"referenced_workflows"`
}

type SpeedReport struct {
	EstimatedSerialSeconds       int      `json:"estimated_serial_seconds"`
	EstimatedCriticalPathSeconds int      `json:"estimated_critical_path_seconds"`
	ParallelismRatio             float64  `json:"parallelism_ratio"`
	CriticalPath                 []string `json:"critical_path,omitempty"`
	CriticalPathAgentNodes       int      `json:"critical_path_agent_nodes"`
	SequentialAgentNodes         int      `json:"sequential_agent_nodes"`
}

type Recommendation struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Path     string `json:"path,omitempty"`
	Message  string `json:"message"`
}

type Warning struct {
	Code    string `json:"code"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

type ReferencedError struct {
	Path    string `json:"path"`
	Ref     string `json:"ref"`
	Message string `json:"message"`
}

type NodeReport struct {
	Path                string   `json:"path"`
	ID                  string   `json:"id"`
	Type                string   `json:"type"`
	ComplexityScore     int      `json:"complexity_score"`
	EstimatedSeconds    int      `json:"estimated_seconds"`
	EstimatedSerialWork int      `json:"estimated_serial_work"`
	AgentLike           bool     `json:"agent_like,omitempty"`
	StructuredAgentLike bool     `json:"structured_agent_like,omitempty"`
	Loop                bool     `json:"loop,omitempty"`
	UnboundedLoop       bool     `json:"unbounded_loop,omitempty"`
	ParallelLoop        bool     `json:"parallel_loop,omitempty"`
	SpawnEnabled        bool     `json:"spawn_enabled,omitempty"`
	BroadTools          bool     `json:"broad_tools,omitempty"`
	ReferencedWorkflow  string   `json:"referenced_workflow,omitempty"`
	Reasons             []string `json:"reasons,omitempty"`
}

type workflowAnalyzer struct {
	options Options
}

type analysisContext struct {
	workflowRef string
	pathPrefix  string
	depth       int
	loopDepth   int
	visitedRefs map[string]bool
}

type workflowMetrics struct {
	report         Report
	reachableNodes map[string]bool
	nodeMetrics    map[string]nodeMetrics
	outgoing       map[string][]string
	criticalPath   []string
}

type nodeMetrics struct {
	path                string
	id                  string
	nodeType            string
	complexity          int
	criticalSeconds     int
	serialSeconds       int
	agentLike           bool
	structuredAgentLike bool
	loop                bool
	unboundedLoop       bool
	parallelLoop        bool
	spawnEnabled        bool
	broadTools          bool
	referencedWorkflow  string
	reasons             []string

	childAgentNodes           int
	childStructuredAgentNodes int
	childLoopNodes            int
	childUnboundedLoops       int
	childParallelLoops        int
	childSpawnEnabledAgents   int
	childBroadToolNodes       int
	childReferencedWorkflows  int
	childMaxLoopDepth         int
}

func normalizeOptions(options Options) Options {
	defaults := DefaultOptions()
	if options.UnboundedLoopIterations <= 0 {
		options.UnboundedLoopIterations = defaults.UnboundedLoopIterations
	}
	if options.ParallelLoopItems <= 0 {
		options.ParallelLoopItems = defaults.ParallelLoopItems
	}
	if options.CallLLMSeconds <= 0 {
		options.CallLLMSeconds = defaults.CallLLMSeconds
	}
	if options.RunSeconds <= 0 {
		options.RunSeconds = defaults.RunSeconds
	}
	if options.AgentSeconds <= 0 {
		options.AgentSeconds = defaults.AgentSeconds
	}
	if options.StructuredSeconds <= 0 {
		options.StructuredSeconds = defaults.StructuredSeconds
	}
	if options.ExecuteToolsSeconds <= 0 {
		options.ExecuteToolsSeconds = defaults.ExecuteToolsSeconds
	}
	if options.ActivitySeconds <= 0 {
		options.ActivitySeconds = defaults.ActivitySeconds
	}
	return options
}

func (a *workflowAnalyzer) analyzeWorkflow(workflow *reliantv1.Workflow, inputOverrides map[string]interface{}, context analysisContext) Report {
	if workflow == nil {
		return Report{Status: "error"}
	}
	if context.visitedRefs == nil {
		context.visitedRefs = make(map[string]bool)
	}
	workflowInputs := collectInputValues(workflow.GetInputs(), inputOverrides)

	metrics := a.collectWorkflowMetrics(workflow, workflowInputs, context)
	report := metrics.report
	report.WorkflowName = workflow.GetName()
	report.Speed.CriticalPath = metrics.criticalPath
	report.Speed.CriticalPathAgentNodes = countAgentNodesOnPath(metrics.criticalPath, metrics.nodeMetrics)
	report.Speed.SequentialAgentNodes = report.Speed.CriticalPathAgentNodes
	report.Speed.ParallelismRatio = parallelismRatio(report.Speed.EstimatedSerialSeconds, report.Speed.EstimatedCriticalPathSeconds)
	report.Complexity.Risk = riskForScore(report.Complexity.Score)
	report.Complexity.Reasons = complexityReasons(report.Complexity, report.Speed)
	report.Recommendations = append(report.Recommendations, a.workflowRecommendations(workflow, workflowInputs, metrics)...)
	report.Status = statusForReport(report)
	return report
}

func (a *workflowAnalyzer) collectWorkflowMetrics(workflow *reliantv1.Workflow, workflowInputs map[string]interface{}, context analysisContext) workflowMetrics {
	nodeByID := make(map[string]*reliantv1.Node, len(workflow.GetNodes()))
	for _, node := range workflow.GetNodes() {
		nodeByID[node.GetId()] = node
	}
	outgoing := buildOutgoing(workflow)
	reachable := reachableNodes(workflow, outgoing)

	report := Report{}
	nodeReports := make([]NodeReport, 0, len(workflow.GetNodes()))
	nodeMetricByID := make(map[string]nodeMetrics, len(workflow.GetNodes()))
	for _, node := range workflow.GetNodes() {
		if len(reachable) > 0 && !reachable[node.GetId()] {
			continue
		}
		metrics := a.analyzeNode(node, workflowInputs, context)
		nodeMetricByID[node.GetId()] = metrics
		report.Complexity.Score += metrics.complexity
		report.Speed.EstimatedSerialSeconds += metrics.serialSeconds
		nodeReports = append(nodeReports, metrics.toReport())
		if directAgentNode(metrics) {
			report.Complexity.AgentNodes++
		}
		if directStructuredAgentNode(metrics) {
			report.Complexity.StructuredAgentNodes++
		}
		if metrics.loop {
			report.Complexity.LoopNodes++
		}
		if metrics.unboundedLoop {
			report.Complexity.UnboundedLoops++
			report.Warnings = append(report.Warnings, Warning{
				Code:    "unbounded_loop",
				Path:    metrics.path,
				Message: "Loop has no statically discoverable iteration bound; runtime estimate uses a heuristic iteration count.",
			})
		}
		if metrics.parallelLoop {
			report.Complexity.ParallelLoops++
		}
		if metrics.spawnEnabled {
			report.Complexity.SpawnEnabledAgents++
		} else {
			report.Complexity.SpawnEnabledAgents += metrics.childSpawnEnabledAgents
		}
		if metrics.broadTools {
			report.Complexity.BroadToolNodes++
		} else {
			report.Complexity.BroadToolNodes += metrics.childBroadToolNodes
		}
		if metrics.referencedWorkflow != "" {
			report.Complexity.ReferencedWorkflows++
		}
		report.Complexity.AgentNodes += metrics.childAgentNodes
		report.Complexity.StructuredAgentNodes += metrics.childStructuredAgentNodes
		report.Complexity.LoopNodes += metrics.childLoopNodes
		report.Complexity.UnboundedLoops += metrics.childUnboundedLoops
		report.Complexity.ParallelLoops += metrics.childParallelLoops
		report.Complexity.ReferencedWorkflows += metrics.childReferencedWorkflows
		report.Complexity.MaxLoopDepth = max(report.Complexity.MaxLoopDepth, context.loopDepth+boolInt(metrics.loop)+metrics.childMaxLoopDepth)
	}
	sort.Slice(nodeReports, func(i, j int) bool { return nodeReports[i].Path < nodeReports[j].Path })
	report.Nodes = nodeReports

	criticalSeconds, criticalPath := criticalPath(workflow, outgoing, nodeMetricByID)
	report.Speed.EstimatedCriticalPathSeconds = criticalSeconds

	return workflowMetrics{
		report:         report,
		reachableNodes: reachable,
		nodeMetrics:    nodeMetricByID,
		outgoing:       outgoing,
		criticalPath:   criticalPath,
	}
}

func (a *workflowAnalyzer) analyzeNode(node *reliantv1.Node, workflowInputs map[string]interface{}, context analysisContext) nodeMetrics {
	path := joinPath(context.pathPrefix, node.GetId())
	metrics := nodeMetrics{
		path:            path,
		id:              node.GetId(),
		nodeType:        node.GetType(),
		complexity:      baseComplexity(node.GetType()),
		criticalSeconds: a.baseSeconds(node.GetType()),
		serialSeconds:   a.baseSeconds(node.GetType()),
	}

	switch node.GetType() {
	case model.NodeTypeCallLLM:
		metrics.agentLike = true
		metrics.complexity += 12
		callArgs := node.GetCallLlm()
		if callLLMHasResponseTool(node) {
			metrics.structuredAgentLike = true
			metrics.complexity += 6
			metrics.reasons = append(metrics.reasons, "structured LLM response tool")
		}
		if callArgs != nil && callArgs.GetToolsConfig() != nil {
			filter := a.resolveToolFilter(callArgs.GetToolsConfig().GetFilter(), workflowInputs)
			if broadToolAccess(filter) {
				metrics.broadTools = true
				metrics.complexity += 12
				metrics.reasons = append(metrics.reasons, "broad tool access")
			}
			if spawnEnabled(callArgs.GetToolsConfig().GetSpawn(), workflowInputs) {
				metrics.spawnEnabled = true
				metrics.complexity += 20
				metrics.reasons = append(metrics.reasons, "spawn-enabled LLM")
			}
		}
	case model.NodeTypeWorkflow:
		return a.analyzeWorkflowNode(node, workflowInputs, context, metrics)
	case model.NodeTypeLoop:
		return a.analyzeLoopNode(node, workflowInputs, context, metrics)
	case model.NodeTypeRouter:
		metrics.agentLike = true
		metrics.complexity += 10
		metrics.reasons = append(metrics.reasons, "LLM-driven router")
	}

	return metrics
}

func (a *workflowAnalyzer) analyzeWorkflowNode(node *reliantv1.Node, workflowInputs map[string]interface{}, context analysisContext, metrics nodeMetrics) nodeMetrics {
	args := node.GetWorkflow()
	if args == nil {
		return metrics
	}
	ref := model.CelStringRaw(args.GetRef())
	metrics.referencedWorkflow = ref
	if args.GetInline() != nil {
		childReport := a.analyzeWorkflow(args.GetInline(), structPBMapToInterface(args.GetArgs()), childContext(context, node.GetId(), "inline", false))
		metrics = mergeChildReport(metrics, childReport, 1)
		return metrics
	}
	if ref == "" {
		return metrics
	}

	metrics.agentLike = isAgentRef(ref)
	metrics.structuredAgentLike = isStructuredAgentRef(ref)
	if metrics.agentLike {
		metrics.complexity += 40
		metrics.criticalSeconds = a.options.AgentSeconds
		metrics.serialSeconds = a.options.AgentSeconds
		metrics.reasons = append(metrics.reasons, "agent workflow reference")
	}
	if metrics.structuredAgentLike {
		metrics.complexity += 25
		metrics.criticalSeconds = a.options.StructuredSeconds
		metrics.serialSeconds = a.options.StructuredSeconds
		metrics.reasons = append(metrics.reasons, "structured-agent workflow reference")
	}

	if a.options.WorkflowLoader == nil || containsTemplate(ref) {
		return metrics
	}
	if context.visitedRefs[ref] {
		metrics.reasons = append(metrics.reasons, "recursive workflow reference skipped")
		return metrics
	}
	childWorkflow, err := a.options.WorkflowLoader(ref)
	if err != nil {
		metrics.reasons = append(metrics.reasons, "referenced workflow could not be loaded")
		return metrics
	}
	childVisited := cloneVisited(context.visitedRefs)
	childVisited[ref] = true
	childCtx := childContext(context, node.GetId(), ref, false)
	childCtx.visitedRefs = childVisited
	childReport := a.analyzeWorkflow(childWorkflow, structPBMapToInterface(args.GetArgs()), childCtx)
	metrics = mergeChildReport(metrics, childReport, 1)
	return metrics
}

func (a *workflowAnalyzer) analyzeLoopNode(node *reliantv1.Node, workflowInputs map[string]interface{}, context analysisContext, metrics nodeMetrics) nodeMetrics {
	args := node.GetLoop()
	if args == nil {
		return metrics
	}
	metrics.loop = true
	metrics.complexity += 8 + context.loopDepth*8
	parallel := resolveCelBool(args.GetParallel(), workflowInputs)
	metrics.parallelLoop = parallel

	iterationEstimate, bounded := estimateLoopIterations(args, workflowInputs, a.options)
	if !bounded && !parallel {
		metrics.unboundedLoop = true
		metrics.complexity += 40
		metrics.reasons = append(metrics.reasons, "unbounded sequential loop")
	}
	if parallel {
		metrics.reasons = append(metrics.reasons, fmt.Sprintf("parallel loop estimated at %d item(s)", iterationEstimate))
	} else {
		metrics.reasons = append(metrics.reasons, fmt.Sprintf("sequential loop estimated at %d iteration(s)", iterationEstimate))
	}

	childReport := Report{}
	if args.GetInline() != nil {
		childReport = a.analyzeWorkflow(args.GetInline(), structPBMapToInterface(args.GetArgs()), childContext(context, node.GetId(), "inline", true))
	} else if ref := model.CelStringRaw(args.GetRef()); ref != "" {
		metrics.referencedWorkflow = ref
		if a.options.WorkflowLoader != nil && !containsTemplate(ref) && !context.visitedRefs[ref] {
			childWorkflow, err := a.options.WorkflowLoader(ref)
			if err == nil {
				childVisited := cloneVisited(context.visitedRefs)
				childVisited[ref] = true
				childCtx := childContext(context, node.GetId(), ref, true)
				childCtx.visitedRefs = childVisited
				childReport = a.analyzeWorkflow(childWorkflow, structPBMapToInterface(args.GetArgs()), childCtx)
			} else {
				metrics.reasons = append(metrics.reasons, "loop body workflow could not be loaded")
			}
		}
		if childReport.WorkflowName == "" {
			childReport = fallbackWorkflowReport(ref, a.options)
		}
	}

	if childReport.WorkflowName != "" || childReport.Complexity.Score > 0 || childReport.Speed.EstimatedCriticalPathSeconds > 0 {
		multiplier := iterationEstimate
		metrics.complexity += childReport.Complexity.Score * multiplier
		metrics.serialSeconds += childReport.Speed.EstimatedSerialSeconds * multiplier
		if parallel {
			metrics.criticalSeconds += childReport.Speed.EstimatedCriticalPathSeconds
		} else {
			metrics.criticalSeconds += childReport.Speed.EstimatedCriticalPathSeconds * multiplier
		}
		metrics.agentLike = childReport.Complexity.AgentNodes > 0
		metrics.structuredAgentLike = childReport.Complexity.StructuredAgentNodes > 0
		metrics.spawnEnabled = childReport.Complexity.SpawnEnabledAgents > 0
		metrics.broadTools = childReport.Complexity.BroadToolNodes > 0
		metrics.childAgentNodes += childReport.Complexity.AgentNodes
		metrics.childStructuredAgentNodes += childReport.Complexity.StructuredAgentNodes
		metrics.childLoopNodes += childReport.Complexity.LoopNodes
		metrics.childUnboundedLoops += childReport.Complexity.UnboundedLoops
		metrics.childParallelLoops += childReport.Complexity.ParallelLoops
		metrics.childSpawnEnabledAgents += childReport.Complexity.SpawnEnabledAgents
		metrics.childBroadToolNodes += childReport.Complexity.BroadToolNodes
		metrics.childReferencedWorkflows += childReport.Complexity.ReferencedWorkflows
		metrics.childMaxLoopDepth = max(metrics.childMaxLoopDepth, childReport.Complexity.MaxLoopDepth)
		if childReport.Complexity.UnboundedLoops > 0 {
			metrics.unboundedLoop = true
		}
	}

	return metrics
}

func (a *workflowAnalyzer) baseSeconds(nodeType string) int {
	switch nodeType {
	case model.NodeTypeCallLLM, model.NodeTypeRouter:
		return a.options.CallLLMSeconds
	case model.NodeTypeExecuteTools:
		return a.options.ExecuteToolsSeconds
	case model.NodeTypeRun:
		return a.options.RunSeconds
	case model.NodeTypeJoin:
		return 0
	default:
		return a.options.ActivitySeconds
	}
}

func baseComplexity(nodeType string) int {
	switch nodeType {
	case model.NodeTypeCallLLM:
		return 15
	case model.NodeTypeExecuteTools:
		return 3
	case model.NodeTypeRun:
		return 3
	case model.NodeTypeWorkflow:
		return 5
	case model.NodeTypeLoop:
		return 8
	case model.NodeTypeRouter:
		return 12
	case model.NodeTypeAskQuestion, model.NodeTypeApproval:
		return 2
	case model.NodeTypeJoin, model.NodeTypeSaveMessage, model.NodeTypeCompact, model.NodeTypeCreateWorktree:
		return 1
	default:
		return 1
	}
}

func mergeChildReport(metrics nodeMetrics, childReport Report, overhead int) nodeMetrics {
	metrics.complexity += childReport.Complexity.Score + overhead
	metrics.criticalSeconds = childReport.Speed.EstimatedCriticalPathSeconds + overhead
	metrics.serialSeconds = childReport.Speed.EstimatedSerialSeconds + overhead
	metrics.agentLike = metrics.agentLike || childReport.Complexity.AgentNodes > 0
	metrics.structuredAgentLike = metrics.structuredAgentLike || childReport.Complexity.StructuredAgentNodes > 0
	metrics.spawnEnabled = metrics.spawnEnabled || childReport.Complexity.SpawnEnabledAgents > 0
	metrics.broadTools = metrics.broadTools || childReport.Complexity.BroadToolNodes > 0
	metrics.unboundedLoop = metrics.unboundedLoop || childReport.Complexity.UnboundedLoops > 0
	metrics.childAgentNodes += childReport.Complexity.AgentNodes
	metrics.childStructuredAgentNodes += childReport.Complexity.StructuredAgentNodes
	metrics.childLoopNodes += childReport.Complexity.LoopNodes
	metrics.childUnboundedLoops += childReport.Complexity.UnboundedLoops
	metrics.childParallelLoops += childReport.Complexity.ParallelLoops
	metrics.childSpawnEnabledAgents += childReport.Complexity.SpawnEnabledAgents
	metrics.childBroadToolNodes += childReport.Complexity.BroadToolNodes
	metrics.childReferencedWorkflows += childReport.Complexity.ReferencedWorkflows
	metrics.childMaxLoopDepth = max(metrics.childMaxLoopDepth, childReport.Complexity.MaxLoopDepth)
	return metrics
}

func fallbackWorkflowReport(ref string, options Options) Report {
	seconds := options.ActivitySeconds
	score := 5
	agentNodes := 0
	structuredNodes := 0
	if isAgentRef(ref) {
		seconds = options.AgentSeconds
		score = 80
		agentNodes = 1
	}
	if isStructuredAgentRef(ref) {
		seconds = options.StructuredSeconds
		score = 60
		structuredNodes = 1
	}
	return Report{
		WorkflowName: ref,
		Complexity: ComplexityReport{
			Score:                score,
			AgentNodes:           agentNodes,
			StructuredAgentNodes: structuredNodes,
		},
		Speed: SpeedReport{
			EstimatedSerialSeconds:       seconds,
			EstimatedCriticalPathSeconds: seconds,
		},
	}
}

func directAgentNode(metrics nodeMetrics) bool {
	if !metrics.agentLike {
		return false
	}
	if metrics.nodeType == model.NodeTypeCallLLM || metrics.nodeType == model.NodeTypeRouter {
		return true
	}
	return metrics.nodeType == model.NodeTypeWorkflow && isAgentRef(metrics.referencedWorkflow)
}

func directStructuredAgentNode(metrics nodeMetrics) bool {
	if !metrics.structuredAgentLike {
		return false
	}
	if metrics.nodeType == model.NodeTypeCallLLM {
		return true
	}
	return metrics.nodeType == model.NodeTypeWorkflow && isStructuredAgentRef(metrics.referencedWorkflow)
}

func (metrics nodeMetrics) toReport() NodeReport {
	return NodeReport{
		Path:                metrics.path,
		ID:                  metrics.id,
		Type:                metrics.nodeType,
		ComplexityScore:     metrics.complexity,
		EstimatedSeconds:    metrics.criticalSeconds,
		EstimatedSerialWork: metrics.serialSeconds,
		AgentLike:           metrics.agentLike,
		StructuredAgentLike: metrics.structuredAgentLike,
		Loop:                metrics.loop,
		UnboundedLoop:       metrics.unboundedLoop,
		ParallelLoop:        metrics.parallelLoop,
		SpawnEnabled:        metrics.spawnEnabled,
		BroadTools:          metrics.broadTools,
		ReferencedWorkflow:  metrics.referencedWorkflow,
		Reasons:             metrics.reasons,
	}
}

func buildOutgoing(workflow *reliantv1.Workflow) map[string][]string {
	outgoing := make(map[string][]string)
	for _, edge := range workflow.GetEdges() {
		if edge == nil || edge.GetFrom() == "" {
			continue
		}
		targetSet := map[string]bool{}
		for _, target := range edge.GetDefault() {
			if target != "" {
				targetSet[target] = true
			}
		}
		for _, edgeCase := range edge.GetCases() {
			for _, target := range edgeCase.GetTo() {
				if target != "" {
					targetSet[target] = true
				}
			}
		}
		for target := range targetSet {
			outgoing[edge.GetFrom()] = append(outgoing[edge.GetFrom()], target)
		}
		sort.Strings(outgoing[edge.GetFrom()])
	}
	return outgoing
}

func reachableNodes(workflow *reliantv1.Workflow, outgoing map[string][]string) map[string]bool {
	entries := workflow.GetEntry()
	if len(entries) == 0 && len(workflow.GetNodes()) > 0 {
		entries = []string{workflow.GetNodes()[0].GetId()}
	}
	reachable := make(map[string]bool)
	var visit func(string)
	visit = func(nodeID string) {
		if nodeID == "" || reachable[nodeID] {
			return
		}
		reachable[nodeID] = true
		for _, next := range outgoing[nodeID] {
			visit(next)
		}
	}
	for _, entry := range entries {
		visit(entry)
	}
	return reachable
}

func criticalPath(workflow *reliantv1.Workflow, outgoing map[string][]string, nodeMetrics map[string]nodeMetrics) (int, []string) {
	entries := workflow.GetEntry()
	if len(entries) == 0 && len(workflow.GetNodes()) > 0 {
		entries = []string{workflow.GetNodes()[0].GetId()}
	}
	memoSeconds := map[string]int{}
	memoPath := map[string][]string{}
	visiting := map[string]bool{}
	var visit func(string) (int, []string)
	visit = func(nodeID string) (int, []string) {
		if seconds, ok := memoSeconds[nodeID]; ok {
			return seconds, append([]string(nil), memoPath[nodeID]...)
		}
		if visiting[nodeID] {
			return 0, nil
		}
		visiting[nodeID] = true
		metrics := nodeMetrics[nodeID]
		bestSeconds := 0
		var bestPath []string
		for _, next := range outgoing[nodeID] {
			nextSeconds, nextPath := visit(next)
			if nextSeconds > bestSeconds {
				bestSeconds = nextSeconds
				bestPath = nextPath
			}
		}
		visiting[nodeID] = false
		total := metrics.criticalSeconds + bestSeconds
		path := append([]string{metrics.path}, bestPath...)
		memoSeconds[nodeID] = total
		memoPath[nodeID] = path
		return total, append([]string(nil), path...)
	}

	bestSeconds := 0
	var bestPath []string
	for _, entry := range entries {
		seconds, path := visit(entry)
		if seconds > bestSeconds {
			bestSeconds = seconds
			bestPath = path
		}
	}
	return bestSeconds, bestPath
}

func estimateLoopIterations(args *reliantv1.LoopArgs, workflowInputs map[string]interface{}, options Options) (int, bool) {
	if args == nil {
		return 1, true
	}
	if resolveCelBool(args.GetParallel(), workflowInputs) {
		return estimateParallelItems(args, workflowInputs, options), true
	}
	whileExpr := model.DirectCelExpr(args.GetWhile())
	if whileExpr == "" {
		return 1, true
	}
	if value, ok := firstRegexInt(whileExpr, `iter\.iteration\s*<\s*(\d+)`); ok {
		return max(value, 1), true
	}
	if inputName, ok := firstRegexGroup(whileExpr, `iter\.iteration\s*<\s*inputs\.([A-Za-z_][A-Za-z0-9_]*)`); ok {
		if value, ok := intFromInterface(workflowInputs[inputName]); ok && value > 0 {
			return value, true
		}
	}
	return options.UnboundedLoopIterations, false
}

func estimateParallelItems(args *reliantv1.LoopArgs, workflowInputs map[string]interface{}, options Options) int {
	itemsExpr := model.CelStringRaw(args.GetItems())
	itemsExpr = strings.TrimSpace(itemsExpr)
	if itemsExpr == "" {
		return options.ParallelLoopItems
	}
	if strings.HasPrefix(itemsExpr, "inputs.") {
		if list, ok := workflowInputs[strings.TrimPrefix(itemsExpr, "inputs.")].([]interface{}); ok && len(list) > 0 {
			return len(list)
		}
		if list, ok := workflowInputs[strings.TrimPrefix(itemsExpr, "inputs.")].([]string); ok && len(list) > 0 {
			return len(list)
		}
	}
	if strings.HasPrefix(itemsExpr, "[") && strings.HasSuffix(itemsExpr, "]") {
		inside := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(itemsExpr, "["), "]"))
		if inside == "" {
			return 0
		}
		return strings.Count(inside, ",") + 1
	}
	return options.ParallelLoopItems
}

func firstRegexInt(input, pattern string) (int, bool) {
	group, ok := firstRegexGroup(input, pattern)
	if !ok {
		return 0, false
	}
	value, err := strconv.Atoi(group)
	return value, err == nil
}

func firstRegexGroup(input, pattern string) (string, bool) {
	compiled := regexp.MustCompile(pattern)
	matches := compiled.FindStringSubmatch(input)
	if len(matches) < 2 {
		return "", false
	}
	return matches[1], true
}

func collectInputValues(inputs map[string]*reliantv1.Input, overrides map[string]interface{}) map[string]interface{} {
	values := make(map[string]interface{}, len(inputs)+len(overrides))
	for name, input := range inputs {
		if value, ok := defaultInputValue(input); ok {
			values[name] = value
		}
	}
	for name, value := range overrides {
		values[name] = value
	}
	return values
}

func defaultInputValue(input *reliantv1.Input) (interface{}, bool) {
	if input == nil {
		return nil, false
	}
	switch input.GetType() {
	case "integer":
		config := input.GetIntegerInput()
		if config != nil && config.Default != nil {
			return config.GetDefault(), true
		}
	case "boolean":
		config := input.GetBooleanInput()
		if config != nil && config.Default != nil {
			return config.GetDefault(), true
		}
	case "string":
		config := input.GetStringInput()
		if config != nil && config.Default != nil {
			return config.GetDefault(), true
		}
	case "tools":
		config := input.GetToolsInput()
		if config != nil && config.Default != nil {
			return config.GetDefault().AsInterface(), true
		}
	case "enum":
		config := input.GetEnumInput()
		if config != nil && config.Default != nil {
			return config.GetDefault().AsInterface(), true
		}
	case "array":
		config := input.GetArrayInput()
		if config != nil && config.Default != nil {
			return config.GetDefault().AsInterface(), true
		}
	case "object", "any", "model", "preset":
		if value := defaultStructValue(input); value != nil {
			return value, true
		}
	}
	return nil, false
}

func defaultStructValue(input *reliantv1.Input) interface{} {
	switch input.GetType() {
	case "object":
		if config := input.GetObjectInput(); config != nil && config.GetDefault() != nil {
			return config.GetDefault().AsInterface()
		}
	case "any":
		if config := input.GetAnyInput(); config != nil && config.GetDefault() != nil {
			return config.GetDefault().AsInterface()
		}
	case "model":
		if config := input.GetModelInput(); config != nil && config.GetDefault() != nil {
			return config.GetDefault()
		}
	case "preset":
		if config := input.GetPresetInput(); config != nil && config.GetDefault() != nil {
			return config.GetDefault().AsInterface()
		}
	}
	return nil
}

func structPBMapToInterface(values map[string]*structpb.Value) map[string]interface{} {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]interface{}, len(values))
	for key, value := range values {
		if value != nil {
			result[key] = value.AsInterface()
		}
	}
	return result
}

func (a *workflowAnalyzer) resolveToolFilter(filter *reliantv1.CelStringList, workflowInputs map[string]interface{}) []string {
	if values := model.CelStringListValue(filter); len(values) > 0 {
		return values
	}
	expr := strings.TrimSpace(model.CelStringListExpr(filter))
	if strings.HasPrefix(expr, "inputs.") {
		return stringSliceFromInterface(workflowInputs[strings.TrimPrefix(expr, "inputs.")])
	}
	return nil
}

func spawnEnabled(spawn *reliantv1.CelStringList, workflowInputs map[string]interface{}) bool {
	if values := model.CelStringListValue(spawn); len(values) > 0 {
		return true
	}
	expr := strings.TrimSpace(model.CelStringListExpr(spawn))
	if expr == "" || expr == "[]" {
		return false
	}
	if strings.HasPrefix(expr, "inputs.") {
		return len(stringSliceFromInterface(workflowInputs[strings.TrimPrefix(expr, "inputs.")])) > 0
	}
	return true
}

func stringSliceFromInterface(value interface{}) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []interface{}:
		values := make([]string, 0, len(typed))
		for _, item := range typed {
			if itemString, ok := item.(string); ok {
				values = append(values, itemString)
			}
		}
		return values
	case string:
		if typed == "" {
			return nil
		}
		return []string{typed}
	default:
		return nil
	}
}

func broadToolAccess(filter []string) bool {
	if len(filter) == 0 {
		return false
	}
	if len(filter) > 8 {
		return true
	}
	for _, tool := range filter {
		switch tool {
		case "*", "tag:default", "tag:all":
			return true
		}
	}
	return false
}

func callLLMHasResponseTool(node *reliantv1.Node) bool {
	args := node.GetCallLlm()
	return args != nil && args.GetResponseTool() != nil
}

func resolveCelBool(value *reliantv1.CelBool, workflowInputs map[string]interface{}) bool {
	if model.CelBoolIsExpr(value) {
		expr := strings.TrimSpace(model.CelBoolExpr(value))
		if strings.HasPrefix(expr, "inputs.") {
			boolValue, _ := boolFromInterface(workflowInputs[strings.TrimPrefix(expr, "inputs.")])
			return boolValue
		}
		return false
	}
	return model.CelBoolValue(value)
}

func boolFromInterface(value interface{}) (bool, bool) {
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		parsed, err := strconv.ParseBool(typed)
		return parsed, err == nil
	default:
		return false, false
	}
}

func intFromInterface(value interface{}) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		return int(parsed), err == nil
	case string:
		parsed, err := strconv.Atoi(typed)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func isAgentRef(ref string) bool {
	name := strings.TrimPrefix(ref, "builtin://")
	return name == "agent" || strings.Contains(name, "agent") || name == "get-it-right" || name == "implement-review"
}

func isStructuredAgentRef(ref string) bool {
	name := strings.TrimPrefix(ref, "builtin://")
	return name == "structured-agent" || strings.Contains(name, "review")
}

func containsTemplate(value string) bool {
	return strings.Contains(value, "{{") || strings.Contains(value, "inputs.")
}

func cloneVisited(input map[string]bool) map[string]bool {
	clone := make(map[string]bool, len(input))
	for key, value := range input {
		clone[key] = value
	}
	return clone
}

func childContext(parent analysisContext, nodeID, workflowRef string, enteringLoop bool) analysisContext {
	pathElement := nodeID
	if workflowRef != "" && workflowRef != "inline" {
		pathElement = nodeID + "[" + workflowRef + "]"
	}
	child := analysisContext{
		workflowRef: workflowRef,
		pathPrefix:  joinPath(parent.pathPrefix, pathElement),
		depth:       parent.depth + 1,
		loopDepth:   parent.loopDepth,
		visitedRefs: cloneVisited(parent.visitedRefs),
	}
	if enteringLoop {
		child.loopDepth++
	}
	return child
}

func joinPath(parts ...string) string {
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			cleaned = append(cleaned, part)
		}
	}
	return strings.Join(cleaned, ".")
}

func (a *workflowAnalyzer) workflowRecommendations(workflow *reliantv1.Workflow, workflowInputs map[string]interface{}, metrics workflowMetrics) []Recommendation {
	var recommendations []Recommendation
	if metrics.report.Complexity.UnboundedLoops > 0 {
		recommendations = append(recommendations, Recommendation{
			Code:     "add_natural_checkpoints",
			Severity: "warning",
			Message:  "Workflow contains unbounded loops. Prefer natural checkpoints, narrower tools, or structured completion signals for long-running agents.",
		})
	}
	criticalPathAgentNodes := countAgentNodesOnPath(metrics.criticalPath, metrics.nodeMetrics)
	if criticalPathAgentNodes >= 3 {
		recommendations = append(recommendations, Recommendation{
			Code:     "parallelize_sequential_agents",
			Severity: "info",
			Message:  fmt.Sprintf("Critical path contains %d agent-like nodes. Look for independent phases that can use fork-join or parallel loops.", criticalPathAgentNodes),
		})
	}
	if metrics.report.Complexity.Score >= 500 && !defaultAskEnabled(workflowInputs) {
		recommendations = append(recommendations, Recommendation{
			Code:     "enable_ask_for_high_complexity",
			Severity: "warning",
			Message:  "High-complexity workflow has ask disabled or unspecified. Enable user checkpoints for long autonomous runs.",
		})
	}
	recommendations = append(recommendations, sequentialAgentRecommendations(workflow, metrics)...)
	return recommendations
}

func sequentialAgentRecommendations(workflow *reliantv1.Workflow, metrics workflowMetrics) []Recommendation {
	var recommendations []Recommendation
	for _, edge := range workflow.GetEdges() {
		sourceMetrics, sourceOK := metrics.nodeMetrics[edge.GetFrom()]
		if !sourceOK || !sourceMetrics.agentLike {
			continue
		}
		for _, targetID := range edgeTargets(edge) {
			targetMetrics, targetOK := metrics.nodeMetrics[targetID]
			if !targetOK || !targetMetrics.agentLike {
				continue
			}
			targetNode := model.FindNode(workflow, targetID)
			if nodeReferencesSource(targetNode, edge.GetFrom()) {
				continue
			}
			recommendations = append(recommendations, Recommendation{
				Code:     "consider_parallel_edge",
				Severity: "info",
				Path:     sourceMetrics.path + " -> " + targetMetrics.path,
				Message:  fmt.Sprintf("%q flows directly into agent-like node %q without an obvious output reference. If their file scopes are isolated, consider fork-join parallelization.", edge.GetFrom(), targetID),
			})
		}
	}
	return recommendations
}

func edgeTargets(edge *reliantv1.Edge) []string {
	set := map[string]bool{}
	for _, target := range edge.GetDefault() {
		if target != "" {
			set[target] = true
		}
	}
	for _, edgeCase := range edge.GetCases() {
		for _, target := range edgeCase.GetTo() {
			if target != "" {
				set[target] = true
			}
		}
	}
	targets := make([]string, 0, len(set))
	for target := range set {
		targets = append(targets, target)
	}
	sort.Strings(targets)
	return targets
}

func nodeReferencesSource(node *reliantv1.Node, sourceID string) bool {
	if node == nil {
		return false
	}
	args, _ := model.NodeArgsAsMap(node)
	encoded, _ := json.Marshal(args)
	needle := "nodes." + sourceID + "."
	return strings.Contains(string(encoded), needle) || strings.Contains(model.ConditionExpr(node), needle)
}

func defaultAskEnabled(workflowInputs map[string]interface{}) bool {
	value, ok := workflowInputs["ask"]
	if !ok {
		return false
	}
	boolValue, ok := boolFromInterface(value)
	return ok && boolValue
}

func countAgentNodesOnPath(path []string, nodeMetrics map[string]nodeMetrics) int {
	count := 0
	for _, pathElement := range path {
		for _, metrics := range nodeMetrics {
			if metrics.path == pathElement && metrics.agentLike {
				count++
			}
		}
	}
	return count
}

func parallelismRatio(serialSeconds, criticalSeconds int) float64 {
	if criticalSeconds <= 0 {
		return 1
	}
	ratio := float64(serialSeconds) / float64(criticalSeconds)
	return math.Round(ratio*100) / 100
}

func riskForScore(score int) string {
	switch {
	case score >= 1000:
		return "very_high"
	case score >= 500:
		return "high"
	case score >= 200:
		return "medium"
	default:
		return "low"
	}
}

func complexityReasons(complexity ComplexityReport, speed SpeedReport) []string {
	var reasons []string
	if complexity.UnboundedLoops > 0 {
		reasons = append(reasons, fmt.Sprintf("%d unbounded loop(s)", complexity.UnboundedLoops))
	}
	if speed.CriticalPathAgentNodes >= 3 {
		reasons = append(reasons, fmt.Sprintf("%d agent-like nodes on critical path", speed.CriticalPathAgentNodes))
	}
	if complexity.SpawnEnabledAgents > 0 {
		reasons = append(reasons, fmt.Sprintf("%d spawn-enabled agent node(s)", complexity.SpawnEnabledAgents))
	}
	if complexity.BroadToolNodes > 0 {
		reasons = append(reasons, fmt.Sprintf("%d node(s) with broad tool access", complexity.BroadToolNodes))
	}
	if complexity.MaxLoopDepth > 1 {
		reasons = append(reasons, fmt.Sprintf("nested loop depth %d", complexity.MaxLoopDepth))
	}
	return reasons
}

func statusForReport(report Report) string {
	if len(report.ReferencedErrors) > 0 {
		return "warning"
	}
	if report.Complexity.Risk == "high" || report.Complexity.Risk == "very_high" || len(report.Warnings) > 0 {
		return "warning"
	}
	return "pass"
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

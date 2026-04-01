import { useState, useRef, useCallback, useEffect } from "react";
import { MessageSquare, Bug, Lightbulb, Send, Paperclip, X, Check, AlertCircle, Upload, ChevronDown } from "lucide-react";
import { Button } from "../ui/Button";
import { Textarea } from "../ui/Textarea";
import { cn } from "../../lib/utils";
import {
  submitFeedback,
  getSystemInfo,
  getSystemInfoAsync,
  FEEDBACK_TYPES,
  FEEDBACK_TYPE_LABELS,
  FEEDBACK_TYPE_DESCRIPTIONS,
  FEEDBACK_TYPE_PLACEHOLDERS,
} from "../../lib/feedback";
import type { FeedbackType, SystemInfo } from "../../lib/feedback";

const FEEDBACK_TYPE_ICONS: Record<FeedbackType, typeof Bug> = {
  bug: Bug,
  feature: Lightbulb,
  general: MessageSquare,
};

const FEEDBACK_TYPE_COLORS: Record<FeedbackType, string> = {
  bug: "#ef4444",      // red
  feature: "#8b5cf6",  // purple
  general: "#3b82f6",  // blue
};

const MAX_ATTACHMENTS = 5;
const MAX_FILE_SIZE = 10 * 1024 * 1024; // 10MB

export function FeedbackSettings() {
  // Form state
  const [feedbackType, setFeedbackType] = useState<FeedbackType>("general");
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [attachments, setAttachments] = useState<File[]>([]);
  
  // UI state
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [submitResult, setSubmitResult] = useState<{
    success: boolean;
    message: string;
  } | null>(null);
  const [isDragging, setIsDragging] = useState(false);
  const [showSystemInfo, setShowSystemInfo] = useState(false);
  
  const fileInputRef = useRef<HTMLInputElement>(null);
  const [systemInfo, setSystemInfo] = useState<SystemInfo>(getSystemInfo);
  
  // Fetch full system info including app version
  useEffect(() => {
    getSystemInfoAsync().then(setSystemInfo);
  }, []);

  // Reset form to initial state
  const resetForm = useCallback(() => {
    setTitle("");
    setDescription("");
    setAttachments([]);
    setSubmitResult(null);
  }, []);

  // Handle file selection
  const handleFileSelect = useCallback((files: FileList | null) => {
    if (!files) return;
    
    const newFiles: File[] = [];
    const errors: string[] = [];
    
    Array.from(files).forEach((file) => {
      if (attachments.length + newFiles.length >= MAX_ATTACHMENTS) {
        errors.push(`Maximum ${MAX_ATTACHMENTS} attachments allowed`);
        return;
      }
      
      if (file.size > MAX_FILE_SIZE) {
        errors.push(`${file.name} exceeds 10MB limit`);
        return;
      }
      
      // Check for duplicates
      if (attachments.some((a) => a.name === file.name && a.size === file.size)) {
        errors.push(`${file.name} is already attached`);
        return;
      }
      
      newFiles.push(file);
    });
    
    if (newFiles.length > 0) {
      setAttachments((prev) => [...prev, ...newFiles]);
    }
    
    if (errors.length > 0) {
      setSubmitResult({ success: false, message: errors[0] });
      setTimeout(() => setSubmitResult(null), 3000);
    }
  }, [attachments]);

  // Remove attachment
  const removeAttachment = useCallback((index: number) => {
    setAttachments((prev) => prev.filter((_, i) => i !== index));
  }, []);

  // Drag and drop handlers
  const handleDragOver = useCallback((e: React.DragEvent) => {
    e.preventDefault();
    setIsDragging(true);
  }, []);

  const handleDragLeave = useCallback((e: React.DragEvent) => {
    e.preventDefault();
    setIsDragging(false);
  }, []);

  const handleDrop = useCallback((e: React.DragEvent) => {
    e.preventDefault();
    setIsDragging(false);
    handleFileSelect(e.dataTransfer.files);
  }, [handleFileSelect]);

  // Submit feedback
  const handleSubmit = useCallback(async () => {
    if (!title.trim() || !description.trim()) {
      setSubmitResult({ success: false, message: "Please fill in all required fields" });
      return;
    }
    
    setIsSubmitting(true);
    setSubmitResult(null);
    
    const result = await submitFeedback({
      type: feedbackType,
      title: title.trim(),
      description: description.trim(),
      attachments: attachments.length > 0 ? attachments : undefined,
    });
    
    setIsSubmitting(false);
    
    if (result.success) {
      setSubmitResult({ success: true, message: "Thank you for your feedback!" });
      resetForm();
    } else {
      setSubmitResult({ 
        success: false, 
        message: result.error ?? "Failed to submit feedback. Please try again." 
      });
    }
  }, [feedbackType, title, description, attachments, resetForm]);

  const placeholders = FEEDBACK_TYPE_PLACEHOLDERS[feedbackType];
  const selectedColor = FEEDBACK_TYPE_COLORS[feedbackType];

  return (
    <div className="space-y-8 max-w-2xl">
      {/* Header */}
      <div>
        <h2 className="text-xl font-semibold mb-1">Send Feedback</h2>
        <p className="text-sm text-muted-foreground">
          Help us improve Reliant by sharing your feedback, reporting bugs, or suggesting features.
        </p>
      </div>

      {/* Feedback Type Selector */}
      <div className="space-y-3">
        <label className="block text-sm font-medium">
          What type of feedback?
        </label>
        <div className="grid grid-cols-3 gap-4">
          {FEEDBACK_TYPES.map((type) => {
            const Icon = FEEDBACK_TYPE_ICONS[type];
            const isSelected = feedbackType === type;
            const color = FEEDBACK_TYPE_COLORS[type];
            
            return (
              <button
                key={type}
                onClick={() => setFeedbackType(type)}
                className={cn(
                  "relative p-4 rounded-xl border-2 text-left transition-all duration-200",
                  "hover:scale-[1.02] active:scale-[0.98]",
                  !isSelected && "border-border bg-card hover:bg-accent/50"
                )}
                style={isSelected ? {
                  borderColor: color,
                  backgroundColor: `${color}15`,
                } : undefined}
              >
                {/* Selection indicator */}
                {isSelected && (
                  <div 
                    className="absolute top-2 right-2 w-5 h-5 rounded-full flex items-center justify-center"
                    style={{ backgroundColor: color }}
                  >
                    <Check className="w-3 h-3 text-white" />
                  </div>
                )}
                
                <div className="flex items-center gap-2 mb-2">
                  <div 
                    className="w-8 h-8 rounded-lg flex items-center justify-center"
                    style={{ 
                      backgroundColor: isSelected ? color : 'hsl(var(--muted))',
                      color: isSelected ? 'white' : 'hsl(var(--muted-foreground))'
                    }}
                  >
                    <Icon className="w-4 h-4" />
                  </div>
                </div>
                <div 
                  className="font-medium text-sm mb-1"
                  style={{ color: isSelected ? color : undefined }}
                >
                  {FEEDBACK_TYPE_LABELS[type]}
                </div>
                <p className="text-xs text-muted-foreground leading-relaxed">
                  {FEEDBACK_TYPE_DESCRIPTIONS[type]}
                </p>
              </button>
            );
          })}
        </div>
      </div>

      {/* Title */}
      <div className="space-y-2">
        <label className="block text-sm font-medium">
          Title <span className="text-muted-foreground font-normal">*</span>
        </label>
        <input
          type="text"
          placeholder={placeholders.title}
          value={title}
          onChange={(e) => setTitle(e.target.value)}
          maxLength={200}
          className={cn(
            "w-full px-4 py-3 rounded-lg border bg-background text-sm",
            "placeholder:text-muted-foreground/60",
            "focus:outline-none focus:ring-2 focus:ring-offset-0 transition-shadow",
            "border-border"
          )}
          style={{
            // @ts-expect-error - CSS custom property
            '--tw-ring-color': `${selectedColor}40`,
            borderColor: title ? selectedColor : undefined,
          }}
        />
      </div>

      {/* Description */}
      <div className="space-y-2">
        <label className="block text-sm font-medium">
          Description <span className="text-muted-foreground font-normal">*</span>
        </label>
        <Textarea
          placeholder={placeholders.description}
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          rows={5}
          maxLength={5000}
          className="resize-none"
          style={{
            borderColor: description ? selectedColor : undefined,
          }}
        />
        <div className="flex justify-end">
          <span className="text-xs text-muted-foreground">
            {description.length.toLocaleString()}/5,000
          </span>
        </div>
      </div>

      {/* Attachments */}
      <div className="space-y-3">
        <label className="block text-sm font-medium">
          Attachments <span className="text-muted-foreground font-normal">(optional)</span>
        </label>
        
        {/* Drop zone */}
        <div
          onDragOver={handleDragOver}
          onDragLeave={handleDragLeave}
          onDrop={handleDrop}
          onClick={() => fileInputRef.current?.click()}
          className={cn(
            "border-2 border-dashed rounded-xl p-8 text-center cursor-pointer transition-all",
            isDragging
              ? "border-primary bg-primary/5 scale-[1.02]"
              : "border-border hover:border-muted-foreground/50 hover:bg-accent/30"
          )}
        >
          <input
            ref={fileInputRef}
            type="file"
            multiple
            accept="image/*,.txt,.json,.pdf,.csv,.zip"
            onChange={(e) => handleFileSelect(e.target.files)}
            className="hidden"
          />
          <div className={cn(
            "w-12 h-12 mx-auto mb-3 rounded-full flex items-center justify-center",
            isDragging ? "bg-primary/10" : "bg-muted"
          )}>
            <Upload className={cn(
              "w-6 h-6",
              isDragging ? "text-primary" : "text-muted-foreground"
            )} />
          </div>
          <p className="text-sm text-muted-foreground mb-1">
            {isDragging ? (
              <span className="text-primary font-medium">Drop files here</span>
            ) : (
              <>
                Drag and drop or <span className="text-primary font-medium">browse</span>
              </>
            )}
          </p>
          <p className="text-xs text-muted-foreground/70">
            PNG, JPG, PDF, TXT up to 10MB (max {MAX_ATTACHMENTS})
          </p>
        </div>

        {/* Attachment list */}
        {attachments.length > 0 && (
          <div className="space-y-2">
            {attachments.map((file, index) => (
              <div
                key={`${file.name}-${index}`}
                className="flex items-center justify-between p-3 rounded-lg bg-muted/50 border border-border"
              >
                <div className="flex items-center gap-3 min-w-0">
                  <div className="w-8 h-8 rounded bg-background flex items-center justify-center">
                    <Paperclip className="w-4 h-4 text-muted-foreground" />
                  </div>
                  <div className="min-w-0">
                    <p className="text-sm font-medium truncate">{file.name}</p>
                    <p className="text-xs text-muted-foreground">
                      {(file.size / 1024).toFixed(1)} KB
                    </p>
                  </div>
                </div>
                <button
                  onClick={() => removeAttachment(index)}
                  className="p-2 rounded-lg hover:bg-destructive/10 text-muted-foreground hover:text-destructive transition-colors"
                >
                  <X className="w-4 h-4" />
                </button>
              </div>
            ))}
          </div>
        )}
      </div>

      {/* System Info (collapsible) */}
      <div className="border border-border rounded-lg overflow-hidden">
        <button
          onClick={() => setShowSystemInfo(!showSystemInfo)}
          className="w-full px-4 py-3 flex items-center justify-between text-sm text-muted-foreground hover:bg-accent/50 transition-colors"
        >
          <span>System information (auto-captured)</span>
          <ChevronDown className={cn(
            "w-4 h-4 transition-transform",
            showSystemInfo && "rotate-180"
          )} />
        </button>
        {showSystemInfo && (
          <div className="px-4 py-3 border-t border-border bg-muted/30 text-xs font-mono space-y-1.5">
            <div className="flex gap-2">
              <span className="text-muted-foreground w-24">Version:</span>
              <span>{systemInfo.appVersion ?? "N/A"}</span>
            </div>
            <div className="flex gap-2">
              <span className="text-muted-foreground w-24">OS:</span>
              <span>{systemInfo.osInfo ?? "N/A"}</span>
            </div>
            <div className="flex gap-2">
              <span className="text-muted-foreground w-24">Browser:</span>
              <span className="truncate">{systemInfo.userAgent}</span>
            </div>
          </div>
        )}
      </div>

      {/* Submit Result */}
      {submitResult && (
        <div
          className={cn(
            "flex items-center gap-3 p-4 rounded-lg",
            submitResult.success
              ? "bg-green-500/10 border border-green-500/30"
              : "bg-destructive/10 border border-destructive/30"
          )}
        >
          {submitResult.success ? (
            <Check className="w-5 h-5 text-green-500" />
          ) : (
            <AlertCircle className="w-5 h-5 text-destructive" />
          )}
          <span className={cn(
            "text-sm font-medium",
            submitResult.success ? "text-green-500" : "text-destructive"
          )}>
            {submitResult.message}
          </span>
        </div>
      )}

      {/* Submit Button */}
      <div className="flex items-center justify-between pt-2">
        <p className="text-xs text-muted-foreground max-w-sm">
          Your feedback is stored securely. We may follow up via email if you're signed in.
        </p>
        <Button
          variant="primary"
          size="lg"
          onClick={handleSubmit}
          loading={isSubmitting}
          disabled={!title.trim() || !description.trim()}
          leftIcon={<Send className="w-4 h-4" />}
          style={{
            backgroundColor: selectedColor,
            borderColor: selectedColor,
          }}
        >
          Submit Feedback
        </Button>
      </div>
    </div>
  );
}

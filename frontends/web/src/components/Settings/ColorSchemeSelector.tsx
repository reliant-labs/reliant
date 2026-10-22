import { useState, useEffect } from "react";
import { Check, Lightbulb } from "lucide-react";
import { cn } from "../../lib/utils";
import { settingsSync, SETTINGS_KEYS } from "../../services/settingsSync";

type ColorScheme = "purple" | "blue" | "neutral" | "teal" | "slate" | "forest" | "pink" | "orange" | "red" | "black";

interface ColorSchemeOption {
  id: ColorScheme;
  name: string;
  description: string;
  preview: {
    primary: string;
    accent: string;
    background: string;
  };
}

const COLOR_SCHEMES: ColorSchemeOption[] = [
  {
    id: "blue",
    name: "Professional Blue",
    description: "Modern, refined",
    preview: {
      primary: "#3E88F7",
      accent: "#E5F0FF",
      background: "#FAFAFA",
    },
  },
  {
    id: "neutral",
    name: "Refined Neutral",
    description: "Warm mocha tones, timeless and calm",
    preview: {
      primary: "#5C4A3D",
      accent: "#F7F5F3",
      background: "#FDFCFB",
    },
  },
  {
    id: "slate",
    name: "Moonlit Slate",
    description: "Cool sophistication for dashboards",
    preview: {
      primary: "#4A5568",
      accent: "#EDF2F7",
      background: "#F8FAFC",
    },
  },
  {
    id: "teal",
    name: "Modern Teal",
    description: "Vibrant energy for creative tools",
    preview: {
      primary: "#14B8A6",
      accent: "#E6FFFA",
      background: "#F0FDFA",
    },
  },
  {
    id: "forest",
    name: "Serene Forest",
    description: "Focused productivity with calming greens",
    preview: {
      primary: "#2F9E6D",
      accent: "#ECFDF5",
      background: "#F0FDF4",
    },
  },
  {
    id: "purple",
    name: "Purple (Classic)",
    description: "Original creative theme",
    preview: {
      primary: "#A855F7",
      accent: "#F3E8FF",
      background: "#FAF5FF",
    },
  },
  {
    id: "pink",
    name: "Vibrant Pink",
    description: "Energetic and creative magenta tones",
    preview: {
      primary: "#EC4899",
      accent: "#FCE7F3",
      background: "#FDF2F8",
    },
  },
  {
    id: "orange",
    name: "Energetic Orange",
    description: "Warm and vibrant for productivity",
    preview: {
      primary: "#F97316",
      accent: "#FFEDD5",
      background: "#FFF7ED",
    },
  },
  {
    id: "red",
    name: "Bold Red",
    description: "Confident and attention-grabbing",
    preview: {
      primary: "#EF4444",
      accent: "#FEE2E2",
      background: "#FEF2F2",
    },
  },
  {
    id: "black",
    name: "Pure Black",
    description: "True black for OLED screens",
    preview: {
      primary: "#FFFFFF",
      accent: "#1A1A1A",
      background: "#000000",
    },
  },
];

export function ColorSchemeSelector() {
  const [selectedScheme, setSelectedScheme] = useState<ColorScheme>("black");
  const [isLoaded, setIsLoaded] = useState(false);

  // Load settings from settingsSync on mount (wait for initialization)
  useEffect(() => {
    const loadSettings = async () => {
      // Wait for settingsSync to be initialized
      let attempts = 0;
      while (!settingsSync.isInitialized() && attempts < 50) {
        await new Promise(resolve => setTimeout(resolve, 100));
        attempts++;
      }
      
      // Now read settings from settingsSync (which has loaded from database)
      const stored = settingsSync.getSetting(SETTINGS_KEYS.COLOR_SCHEME, "black") as ColorScheme;
      setSelectedScheme(stored || "black");
      setIsLoaded(true);
    };
    
    loadSettings();
  }, []);

  useEffect(() => {
    if (!isLoaded) return;
    
    // Apply the color scheme
    const applyScheme = (scheme: ColorScheme) => {
      const root = document.documentElement;

      // Map scheme to data attribute
      const schemeMap: Record<ColorScheme, string> = {
        blue: "professional-blue",
        neutral: "refined-neutral",
        teal: "modern-teal",
        slate: "slate",
        forest: "forest",
        purple: "purple-classic",
        pink: "vibrant-pink",
        orange: "energetic-orange",
        red: "bold-red",
        black: "pure-black",
      };

      root.setAttribute("data-color-scheme", schemeMap[scheme]);

      // Sync to database
      settingsSync.setSetting(SETTINGS_KEYS.COLOR_SCHEME, scheme).catch((error) => {
        console.error("Failed to save color scheme:", error);
      });

      // Trigger theme update
      window.dispatchEvent(new CustomEvent("color-scheme-updated", { detail: { scheme } }));
    };

    applyScheme(selectedScheme);
  }, [selectedScheme, isLoaded]);

  // Don't render until settings are loaded
  if (!isLoaded) {
    return null;
  }

  return (
    <div className="space-y-4">
      <div>
        <h3 className="text-sm font-semibold mb-1">Color Scheme</h3>
        <p className="text-xs text-muted-foreground">
          Choose the accent color for your interface
        </p>
      </div>

      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-3">
        {COLOR_SCHEMES.map((scheme) => {
          const isSelected = selectedScheme === scheme.id;

          return (
            <button
              key={scheme.id}
              onClick={() => setSelectedScheme(scheme.id)}
              className={cn(
                "relative p-4 rounded-lg border-2 transition-all text-left",
                "elevation-1 hover:elevation-2",
                isSelected
                  ? "shadow-lg"
                  : "border-border/40 hover:border-primary/40 bg-background hover:bg-muted/30"
              )}
              style={isSelected ? {
                borderColor: 'hsl(var(--primary))',
                backgroundColor: 'hsl(var(--primary) / 0.1)'
              } : undefined}
            >
              {/* Color Preview */}
              <div className="flex items-center gap-2 mb-3">
                <div
                  className="w-8 h-8 rounded-md elevation-1"
                  style={{ backgroundColor: scheme.preview.primary }}
                />
                <div
                  className="w-6 h-6 rounded-md elevation-1"
                  style={{ backgroundColor: scheme.preview.accent }}
                />
                <div
                  className="w-4 h-4 rounded-md border border-border/40"
                  style={{ backgroundColor: scheme.preview.background }}
                />
              </div>

              {/* Scheme Info */}
              <div className="space-y-1">
                <div className="flex items-center justify-between">
                  <h4 className="text-sm font-semibold">{scheme.name}</h4>
                  {isSelected && (
                    <Check 
                      className="w-4 h-4 flex-shrink-0" 
                      style={{ color: 'hsl(var(--primary))' }}
                    />
                  )}
                </div>
                <p className="text-xs text-muted-foreground leading-relaxed">
                  {scheme.description}
                </p>
              </div>

              {/* Selected Indicator */}
              {isSelected && (
                <div 
                  className="absolute inset-0 rounded-lg ring-2 ring-offset-2 ring-offset-background pointer-events-none" 
                  style={{ 
                    '--tw-ring-color': 'hsl(var(--primary) / 0.6)' 
                  } as React.CSSProperties}
                />
              )}
            </button>
          );
        })}
      </div>

      <div className="mt-4 p-3 elevation-1 rounded-lg border border-border/40 bg-muted/30">
        <p className="flex items-start gap-2 text-xs text-muted-foreground">
          <Lightbulb className="mt-0.5 h-3.5 w-3.5 flex-shrink-0 text-primary" aria-hidden />
          <span><strong>Tip:</strong> The color scheme affects buttons, links, active states, and focus indicators throughout the app.</span>
        </p>
      </div>
    </div>
  );
}
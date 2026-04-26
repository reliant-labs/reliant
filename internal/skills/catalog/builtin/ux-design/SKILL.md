---
name: ux-design
description: UI/UX design methodology — trend research, accessible component architecture, responsive design, and performance-optimized interfaces
---

# UX Design Methodology

## Phase 1: Research & Analysis

### Trend Research

If asked for a specific style, you MUST research that style.

Before implementing, you can research:
- **Current Industry Trends**: What leading products in the space are doing
- **Platform Guidelines**: Latest iOS HIG, Material Design 3, Windows Fluent updates
- **Design Communities**: Current discussions on Dribbble, Behance, Awwwards
- **Emerging Patterns**: New interaction paradigms gaining adoption
- **User Expectations**: How preferences have evolved in the target demographic

Current trends to consider:
- Neumorphism evolution
- AI-driven personalization interfaces
- Bold typography and variable fonts
- Dark mode as default with system preference detection
- Micro-interactions and scroll-triggered animations
- Y2K revival and retro-futuristic aesthetics
- 3D elements and immersive depth
- Minimalist brutalism
- Organic shapes and nature-inspired design
- Accessibility-first design patterns

### User Journey Analysis
Map and optimize:
- Entry points and onboarding flows
- Core task completion paths
- Error states and recovery
- Cross-platform consistency
- Accessibility paths for assistive technologies

## Phase 2: Design Implementation

### Style Modes (select based on research)
1. **Minimalist Modern** — Whitespace, typographic clarity, subtle animations
2. **Experimental/Playful** — Gradients, bold animations, creative layouts
3. **Professional/Enterprise** — Material Design 3, familiar patterns, data-dense
4. **Glass/3D** — Glassmorphism, depth, immersive elements
5. **Retro Revival** — Y2K aesthetics, nostalgic elements, bold colors
6. **Organic/Natural** — Soft edges, nature-inspired, calming

### Component Architecture

**IMPORTANT**: UX Needs to look good and be fast. Prefer next js for SSR, or even plain html/javascript if the scope is small and simple. For more complex apps you can use React, or react native for mobile.

Generate React/Tailwind components with:
```tsx
// Example: Modern accessible component with trend awareness
export default function Card({ variant = 'glass', ...props }) {
  const styles = {
    glass: 'backdrop-blur-xl bg-white/10 border border-white/20',
    minimal: 'bg-white shadow-sm',
    brutalist: 'bg-black text-white border-4 border-black',
    organic: 'bg-gradient-to-br from-green-50 to-blue-50 rounded-3xl'
  };

  return (
    <div
      className={`p-6 rounded-2xl transition-all ${styles[variant]}`}
      role="article"
      {...props}
    >
      {/* Content with proper semantic HTML */}
    </div>
  );
}
```

### Accessibility Standards
Implement WCAG 2.1 AA compliance:
- Semantic HTML elements
- ARIA attributes where needed
- Keyboard navigation support
- Focus management and indicators
- Color contrast ratios (4.5:1 minimum)
- Screen reader compatibility
- Reduced motion options

### Responsive Design
Mobile-first with modern CSS:
```css
/* Container queries for component-level responsiveness */
.component {
  container-type: inline-size;
}

@container (min-width: 768px) {
  .component-child {
    /* Tablet+ styles */
  }
}

/* Fluid typography with clamp() */
:root {
  --fluid-text: clamp(1rem, 2vw + 0.5rem, 1.5rem);
}
```

## Phase 3: Performance Optimization

### Core Web Vitals
Target metrics:
- LCP < 2.5s
- FID < 100ms
- CLS < 0.1
- INP < 200ms

### Loading Strategies
- Lazy loading for images and components
- Code splitting for routes
- Optimistic UI updates
- Skeleton screens during loading
- Progressive enhancement

## Phase 4: Testing & Validation

**IMPORTANT**: You MUST validate the site loads with no javascript errors. You MUST navigate to all pages, and click on various buttons, and user journeys. Use playwright if you need.

## Workflow Instructions

1. **Research First**: Always research current trends and patterns before implementing
2. **Ask Clarifying Questions**: Brand identity, target audience, style preferences
3. **Select Style Mode**: Choose based on research and requirements
4. **Generate Components**: React/Tailwind with accessibility baked in
5. **Provide Alternatives**: Show 2-3 variations when appropriate
6. **Justify Decisions**: Explain using UX principles and trend research

## Output Format

Always provide:
1. Research summary of relevant trends
2. Component code (React/Tailwind)
3. Accessibility annotations
4. Responsive breakpoints
5. Alternative variations

**IMPORTANT**: NEVER use emoji's unless explicitly requested.

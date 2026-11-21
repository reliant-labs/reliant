// The tracer provider the HyperDX SDK registers at init, as far as the
// runtime's tests need it: the SDK's addAction is a span on its
// `custom-action` tracer, so a span ended there is handed to `onAction` as
// addAction(name, attributes) would be. Every other tracer records nothing.
// A test's SDK mock calls registerSdkProvider from its init. Not part of the
// package.
import { trace } from "@opentelemetry/api";

type Attributes = Record<string, unknown>;

export function registerSdkProvider(
  onAction: (name: string, attributes: Attributes, at: unknown) => void,
): void {
  const tracers = new Map<string, unknown>();
  trace.disable();
  trace.setGlobalTracerProvider({
    getTracer(name: string, version?: string) {
      const key = `${name}@${version ?? ""}`;
      if (!tracers.has(key)) {
        tracers.set(key, {
          startSpan(
            spanName: string,
            options?: { startTime?: unknown; attributes?: Attributes },
          ) {
            const attributes: Attributes = { ...options?.attributes };
            return {
              attributes,
              setAttribute(k: string, v: unknown) {
                attributes[k] = v;
              },
              setAttributes(more: Attributes) {
                Object.assign(attributes, more);
              },
              end() {
                if (name === "custom-action") {
                  onAction(spanName, attributes, options?.startTime);
                }
              },
            };
          },
        });
      }
      return tracers.get(key);
    },
  } as never);
}

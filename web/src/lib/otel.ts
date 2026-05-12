import { WebTracerProvider, BatchSpanProcessor } from '@opentelemetry/sdk-trace-web';
import { OTLPTraceExporter } from '@opentelemetry/exporter-trace-otlp-http';
import { resourceFromAttributes } from '@opentelemetry/resources';
import { ATTR_SERVICE_NAME, ATTR_SERVICE_VERSION } from '@opentelemetry/semantic-conventions';
import { ZoneContextManager } from '@opentelemetry/context-zone';
import { W3CTraceContextPropagator } from '@opentelemetry/core';
import { registerInstrumentations } from '@opentelemetry/instrumentation';
import { getWebAutoInstrumentations } from '@opentelemetry/auto-instrumentations-web';
import { trace, propagation } from '@opentelemetry/api';

let initialized = false;

export function initOTelTracing() {
  if (initialized) return;

  // Only enable if OTLP endpoint is configured
  const otlpEndpoint = (import.meta as any).env?.VITE_OTEL_EXPORTER_OTLP_ENDPOINT;
  if (!otlpEndpoint) {
    console.log('[OTel] No VITE_OTEL_EXPORTER_OTLP_ENDPOINT configured, tracing disabled');
    return;
  }

  const resource = resourceFromAttributes({
    [ATTR_SERVICE_NAME]: 'reliant-frontend',
    [ATTR_SERVICE_VERSION]: (import.meta as any).env?.VITE_VERSION || 'dev',
  });

  const exporter = new OTLPTraceExporter({
    url: `${otlpEndpoint}/v1/traces`,
  });

  const provider = new WebTracerProvider({
    resource,
    spanProcessors: [new BatchSpanProcessor(exporter)],
  });

  // Register W3C TraceContext propagator for cross-service trace correlation
  propagation.setGlobalPropagator(new W3CTraceContextPropagator());

  // Use ZoneContextManager for async context propagation in browser
  provider.register({
    contextManager: new ZoneContextManager(),
  });

  // Auto-instrument fetch, XMLHttpRequest, document load
  registerInstrumentations({
    instrumentations: [
      getWebAutoInstrumentations({
        '@opentelemetry/instrumentation-fetch': {
          propagateTraceHeaderCorsUrls: [/.*/],
        },
        '@opentelemetry/instrumentation-xml-http-request': {
          propagateTraceHeaderCorsUrls: [/.*/],
        },
      }),
    ],
  });

  initialized = true;
  console.log('[OTel] Browser tracing initialized', { endpoint: otlpEndpoint });
}

export function getTracer(name: string = 'reliant-frontend') {
  return trace.getTracer(name);
}
import { test, expect } from '@playwright/test';

// API base URL - defaults to localhost:8080, can be overridden via env
const API_BASE_URL = process.env.API_BASE_URL || 'http://localhost:8080';

/**
 * API Integration Tests
 *
 * These tests require a running backend server.
 * Skipped in CI unless API_BASE_URL is explicitly set.
 */
test.describe('API Integration Tests', () => {
  test('Health check endpoint', async ({ request }) => {
    // Skip in CI unless backend URL is explicitly configured
    test.skip(
      !!process.env.CI && !process.env.API_BASE_URL,
      'Skipping API tests in CI - no backend available'
    );

    try {
      const response = await request.get(`${API_BASE_URL}/api/health`, {
        timeout: 5000,
      });

      expect(response.ok()).toBe(true);
      const data = await response.json();
      expect(data.status).toBe('healthy');
    } catch (error: unknown) {
      // If connection refused, skip the test (backend not running)
      if (error instanceof Error && error.message.includes('ECONNREFUSED')) {
        test.skip(true, 'Backend server not available');
      }
      throw error;
    }
  });
});
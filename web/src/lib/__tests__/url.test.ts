/**
 * Tests for URL detection
 */

import { describe, it, expect } from 'vitest';
import { isHttpUrl } from '../url';

describe('isHttpUrl', () => {
  it('should accept http and https URLs', () => {
    expect(isHttpUrl('https://example.com')).toBe(true);
    expect(isHttpUrl('http://example.com')).toBe(true);
  });

  it('should accept URLs with path, query and fragment', () => {
    expect(isHttpUrl('https://example.com/a/b?c=1&d=2#frag')).toBe(true);
  });

  it('should accept localhost with a port when the scheme is explicit', () => {
    expect(isHttpUrl('http://localhost:3000/api')).toBe(true);
  });

  it('should tolerate surrounding whitespace', () => {
    expect(isHttpUrl('  https://example.com  ')).toBe(true);
  });

  it('should reject scheme-less hosts', () => {
    expect(isHttpUrl('example.com')).toBe(false);
    expect(isHttpUrl('www.example.com')).toBe(false);
    expect(isHttpUrl('localhost:3000')).toBe(false);
  });

  it('should reject non-http schemes', () => {
    expect(isHttpUrl('file:///etc/hosts')).toBe(false);
    expect(isHttpUrl('mailto:a@b.com')).toBe(false);
    expect(isHttpUrl('javascript:alert(1)')).toBe(false);
  });

  it('should reject prose that merely contains a URL', () => {
    expect(isHttpUrl('see https://example.com for details')).toBe(false);
  });

  it('should reject a scheme with no host', () => {
    expect(isHttpUrl('https://')).toBe(false);
  });

  it('should reject empty and non-string input', () => {
    expect(isHttpUrl('')).toBe(false);
    expect(isHttpUrl('   ')).toBe(false);
    expect(isHttpUrl(null as unknown as string)).toBe(false);
    expect(isHttpUrl(undefined as unknown as string)).toBe(false);
  });

  it('should reject file paths, which isFilePath owns', () => {
    expect(isHttpUrl('/src/components/App.tsx')).toBe(false);
    expect(isHttpUrl('./config/settings.json')).toBe(false);
  });
});

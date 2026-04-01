/**
 * Tests for file path detection and parsing
 */

import { describe, it, expect } from 'vitest';
import { parseFilePath, detectFilePaths, isFilePath, normalizeFilePath } from '../filePath';

describe('parseFilePath', () => {
  it('should parse absolute path', () => {
    const result = parseFilePath('/path/to/file.ts');
    expect(result).not.toBeNull();
    expect(result?.path).toBe('/path/to/file.ts');
    expect(result?.isAbsolute).toBe(true);
    expect(result?.line).toBeUndefined();
  });

  it('should parse path with line number', () => {
    const result = parseFilePath('/path/to/file.ts:123');
    expect(result).not.toBeNull();
    expect(result?.path).toBe('/path/to/file.ts');
    expect(result?.line).toBe(123);
    expect(result?.column).toBeUndefined();
  });

  it('should parse path with line and column', () => {
    const result = parseFilePath('/path/to/file.ts:123:45');
    expect(result).not.toBeNull();
    expect(result?.path).toBe('/path/to/file.ts');
    expect(result?.line).toBe(123);
    expect(result?.column).toBe(45);
  });

  it('should parse relative path', () => {
    const result = parseFilePath('./src/file.tsx');
    expect(result).not.toBeNull();
    expect(result?.path).toBe('./src/file.tsx');
    expect(result?.isAbsolute).toBe(false);
  });

  it('should return null for invalid extension', () => {
    const result = parseFilePath('/path/to/file.xyz');
    expect(result).toBeNull();
  });

  it('should return null for non-string input', () => {
    const result = parseFilePath(null as any);
    expect(result).toBeNull();
  });
});

describe('detectFilePaths', () => {
  it('should detect absolute paths in text', () => {
    const text = 'Please check /src/components/App.tsx for errors';
    const results = detectFilePaths(text);
    expect(results).toHaveLength(1);
    expect(results[0].parsed.path).toBe('/src/components/App.tsx');
  });

  it('should detect relative paths in text', () => {
    const text = 'Edit the file ./config/settings.json';
    const results = detectFilePaths(text);
    expect(results).toHaveLength(1);
    expect(results[0].parsed.path).toBe('./config/settings.json');
  });

  it('should detect paths with line numbers', () => {
    const text = 'Error at /src/App.tsx:45';
    const results = detectFilePaths(text);
    expect(results).toHaveLength(1);
    expect(results[0].parsed.path).toBe('/src/App.tsx');
    expect(results[0].parsed.line).toBe(45);
  });

  it('should detect multiple paths in text', () => {
    const text = 'Compare /src/file1.ts with ./src/file2.ts';
    const results = detectFilePaths(text);
    expect(results).toHaveLength(2);
    expect(results[0].parsed.path).toBe('/src/file1.ts');
    expect(results[1].parsed.path).toBe('./src/file2.ts');
  });

  it('should return empty array for text without paths', () => {
    const text = 'This is just regular text with no file paths';
    const results = detectFilePaths(text);
    expect(results).toHaveLength(0);
  });
});

describe('isFilePath', () => {
  it('should return true for valid file paths', () => {
    expect(isFilePath('/path/to/file.ts')).toBe(true);
    expect(isFilePath('./file.tsx')).toBe(true);
    expect(isFilePath('config.json')).toBe(true);
  });

  it('should return false for invalid paths', () => {
    expect(isFilePath('not-a-file')).toBe(false);
    expect(isFilePath('')).toBe(false);
    expect(isFilePath(null as any)).toBe(false);
  });
});

describe('normalizeFilePath', () => {
  it('should remove leading ./', () => {
    expect(normalizeFilePath('./src/file.ts')).toBe('/src/file.ts');
  });

  it('should add leading / for non-absolute paths', () => {
    expect(normalizeFilePath('src/file.ts')).toBe('/src/file.ts');
  });

  it('should preserve absolute paths', () => {
    expect(normalizeFilePath('/src/file.ts')).toBe('/src/file.ts');
  });
});

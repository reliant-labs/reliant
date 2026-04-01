import { parseUnifiedDiff, parseDiff, compareContent } from '../diffParser';

describe('diffParser', () => {
  describe('parseUnifiedDiff', () => {
    it('should parse a basic git diff', () => {
      const diff = `--- a/test.txt
+++ b/test.txt
@@ -1,3 +1,3 @@
 line 1
-line 2
+line 2 modified
 line 3`;

      const result = parseUnifiedDiff(diff);
      expect(result.oldFileName).toBe('test.txt');
      expect(result.newFileName).toBe('test.txt');
      expect(result.lines).toHaveLength(4);
      expect(result.lines[0].type).toBe('context');
      expect(result.lines[1].type).toBe('removed');
      expect(result.lines[2].type).toBe('added');
      expect(result.lines[3].type).toBe('context');
    });

    it('should detect new files', () => {
      const diff = `--- /dev/null
+++ b/new.txt
@@ -0,0 +1,2 @@
+line 1
+line 2`;

      const result = parseUnifiedDiff(diff);
      expect(result.isNewFile).toBe(true);
      expect(result.lines).toHaveLength(2);
      expect(result.lines[0].type).toBe('added');
      expect(result.lines[1].type).toBe('added');
    });

    it('should detect deleted files', () => {
      const diff = `--- a/old.txt
+++ /dev/null
@@ -1,2 +0,0 @@
-line 1
-line 2`;

      const result = parseUnifiedDiff(diff);
      expect(result.isDeletedFile).toBe(true);
      expect(result.lines).toHaveLength(2);
      expect(result.lines[0].type).toBe('removed');
      expect(result.lines[1].type).toBe('removed');
    });
  });

  describe('compareContent', () => {
    it('should compare two strings line by line', () => {
      const original = 'line 1\nline 2\nline 3';
      const modified = 'line 1\nline 2 modified\nline 3';

      const result = compareContent(original, modified);
      // 4 elements: unchanged(line 1), removed(line 2), added(line 2 modified), unchanged(line 3)
      expect(result).toHaveLength(4);
      expect(result[0].type).toBe('unchanged');
      expect(result[1].type).toBe('removed');
      expect(result[2].type).toBe('added');
      expect(result[3].type).toBe('unchanged');
    });

    it('should handle added lines', () => {
      const original = 'line 1\nline 2';
      const modified = 'line 1\nline 2\nline 3';

      const result = compareContent(original, modified);
      expect(result[2].type).toBe('added');
      expect(result[2].content).toBe('line 3');
    });

    it('should handle removed lines', () => {
      const original = 'line 1\nline 2\nline 3';
      const modified = 'line 1\nline 3';

      const result = compareContent(original, modified);
      expect(result[1].type).toBe('removed');
      expect(result[1].content).toBe('line 2');
    });
  });

  describe('parseDiff', () => {
    it('should auto-detect git diff format', () => {
      const diff = `--- a/test.txt
+++ b/test.txt
@@ -1,1 +1,1 @@
-old
+new`;

      const result = parseDiff(diff);
      expect(result).toHaveLength(2);
      expect(result[0].type).toBe('removed');
      expect(result[1].type).toBe('added');
    });

    it('should handle simple content comparison', () => {
      const result = parseDiff('line 1', 'line 2');
      expect(result).toHaveLength(2);
      expect(result[0].type).toBe('removed');
      expect(result[1].type).toBe('added');
    });

    it('should treat single string as new file', () => {
      const result = parseDiff('line 1\nline 2');
      expect(result).toHaveLength(2);
      expect(result[0].type).toBe('added');
      expect(result[1].type).toBe('added');
    });
  });
});

#!/bin/bash
# Capture all pprof endpoints for debugging stuck/locked processes
# Usage: ./scripts/capture-pprof.sh [port] [output_dir]

# Source dynamic port if available
if [ -f .dev-ports.sh ]; then
    source .dev-ports.sh
fi
PORT="${1:-${PPROF_PORT:-6060}}"
TIMESTAMP=$(date +%Y%m%d_%H%M%S)
OUTPUT_DIR="${2:-./pprof_capture_$TIMESTAMP}"

mkdir -p "$OUTPUT_DIR"

echo "Capturing pprof data from localhost:$PORT to $OUTPUT_DIR"

# Goroutines - full stack traces (most useful for deadlocks/leaks)
echo "-> goroutines (debug=2, full stacks)..."
curl -s "http://localhost:$PORT/debug/pprof/goroutine?debug=2" > "$OUTPUT_DIR/goroutines_full.txt"

# Goroutines - summary format (easier to parse)
echo "-> goroutines (debug=1, summary)..."
curl -s "http://localhost:$PORT/debug/pprof/goroutine?debug=1" > "$OUTPUT_DIR/goroutines_summary.txt"

# Goroutines - binary format (for go tool pprof)
echo "-> goroutines (binary)..."
curl -s "http://localhost:$PORT/debug/pprof/goroutine" > "$OUTPUT_DIR/goroutine.pb.gz"

# Heap - memory allocations
echo "-> heap..."
curl -s "http://localhost:$PORT/debug/pprof/heap" > "$OUTPUT_DIR/heap.pb.gz"

# Heap - debug format
echo "-> heap (debug)..."
curl -s "http://localhost:$PORT/debug/pprof/heap?debug=1" > "$OUTPUT_DIR/heap_debug.txt"

# Allocs - all past memory allocations
echo "-> allocs..."
curl -s "http://localhost:$PORT/debug/pprof/allocs" > "$OUTPUT_DIR/allocs.pb.gz"

# Block - goroutines blocked on synchronization primitives
echo "-> block (blocking profile)..."
curl -s "http://localhost:$PORT/debug/pprof/block" > "$OUTPUT_DIR/block.pb.gz"

# Mutex - mutex contention
echo "-> mutex (contention)..."
curl -s "http://localhost:$PORT/debug/pprof/mutex" > "$OUTPUT_DIR/mutex.pb.gz"

# Threadcreate - stack traces that led to OS thread creation
echo "-> threadcreate..."
curl -s "http://localhost:$PORT/debug/pprof/threadcreate" > "$OUTPUT_DIR/threadcreate.pb.gz"

# CPU profile - 5 second sample (may hang if system is stuck, do last)
echo "-> cpu (5 second profile, may take a moment)..."
curl -s "http://localhost:$PORT/debug/pprof/profile?seconds=5" > "$OUTPUT_DIR/cpu.pb.gz" &
CPU_PID=$!

# While CPU profile runs, get some other info
echo "-> cmdline..."
curl -s "http://localhost:$PORT/debug/pprof/cmdline" > "$OUTPUT_DIR/cmdline.txt"

echo "-> symbol..."
curl -s "http://localhost:$PORT/debug/pprof/symbol" > "$OUTPUT_DIR/symbol.txt"

# Trace - execution trace (2 seconds)
echo "-> trace (2 second trace)..."
curl -s "http://localhost:$PORT/debug/pprof/trace?seconds=2" > "$OUTPUT_DIR/trace.out" &
TRACE_PID=$!

# Wait for background jobs
wait $CPU_PID 2>/dev/null
wait $TRACE_PID 2>/dev/null

# Quick analysis
echo ""
echo "=== Quick Analysis ==="
echo ""

echo "Goroutine count:"
grep -c "^goroutine " "$OUTPUT_DIR/goroutines_full.txt" 2>/dev/null || echo "N/A"

echo ""
echo "Goroutine states:"
grep -E "^goroutine [0-9]+ \[" "$OUTPUT_DIR/goroutines_full.txt" 2>/dev/null | \
    sed 's/goroutine [0-9]* //' | sort | uniq -c | sort -rn | head -10

echo ""
echo "Top goroutine creators:"
grep "created by" "$OUTPUT_DIR/goroutines_full.txt" 2>/dev/null | \
    sort | uniq -c | sort -rn | head -10

echo ""
echo "=== Capture complete ==="
echo "Output directory: $OUTPUT_DIR"
echo ""
echo "To analyze:"
echo "  go tool pprof $OUTPUT_DIR/goroutine.pb.gz"
echo "  go tool pprof $OUTPUT_DIR/heap.pb.gz"
echo "  go tool pprof $OUTPUT_DIR/cpu.pb.gz"
echo "  go tool trace $OUTPUT_DIR/trace.out"

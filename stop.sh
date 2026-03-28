#!/bin/bash
# Stop script for GoMatchingDemo

echo "[$(date '+%Y-%m-%d %H:%M:%S')] Checking for running instances..."
PID=$(ps aux | grep GoMatchingDemo | grep -v grep | awk '{print $2}')
if [ -n "$PID" ]; then
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] Killing running instance (PID: $PID)"
    kill -9 $PID
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] Server stopped"
else
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] No running instance found"
fi
#!/bin/bash
# Start script for GoMatchingDemo

# Kill any running instance
echo "[$(date '+%Y-%m-%d %H:%M:%S')] Checking for running instances..."
PID=$(ps aux | grep GoMatchingDemo | grep -v grep | awk '{print $2}')
if [ -n "$PID" ]; then
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] Killing running instance (PID: $PID)"
    kill -9 $PID
    sleep 1
fi

# Build
echo "[$(date '+%Y-%m-%d %H:%M:%S')] Building..."
go build -o GoMatchingDemo . || exit 1

# Start and log to ./log.txt
echo "[$(date '+%Y-%m-%d %H:%M:%S')] Starting server..."
nohup ./GoMatchingDemo >> ./log.txt 2>&1 &

echo "[$(date '+%Y-%m-%d %H:%M:%S')] Server started, logging to ./log.txt"
echo "[$(date '+%Y-%m-%d %H:%M:%S')] View logs with: tail -f ./log.txt"

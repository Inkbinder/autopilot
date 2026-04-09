#!/usr/bin/env bash
set -euo pipefail

trap 'exit 0' TERM INT

while IFS= read -r line; do
  case "$line" in
    *'"method":"initialize"'*)
      echo '{"id":1,"result":{"ok":true}}'
      ;;
    *'"method":"newSession"'*)
      echo '{"id":2,"result":{"sessionId":"smoke-session"}}'
      ;;
    *'"method":"prompt"'*)
      echo '{"method":"thread/tokenUsage/updated","params":{"total_token_usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15},"message":"smoke prompt started"}}'
      while :; do
        sleep 1
      done
      ;;
  esac
done
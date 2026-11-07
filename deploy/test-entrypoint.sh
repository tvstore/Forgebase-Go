#!/bin/bash
# Tests of deploy/entrypoint.sh with a fake binary (no Docker, no database): bash deploy/test-entrypoint.sh
# The fake `forgebase-app` behaves as FAKE_SERVE / FAKE_WORKER say: `clean` (exit 0 on TERM), `slow` (exit 0
# one second after TERM), `failterm` (exit 1 at once on TERM), `crash:SECONDS:STATUS` (exit by itself). Every TERM it receives is counted.
set -u
dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT
here=$(cd "$(dirname "$0")" && pwd)
sed "s#^app=.*#app=$dir/fake#" "$here/entrypoint.sh" > "$dir/entrypoint.sh"
cat > "$dir/fake" <<'FAKE'
#!/bin/bash
cmd=$1; shift
echo "$cmd $*" >> "$LOG.calls"
echo "$cmd FORGEBASE_LISTEN=$FORGEBASE_LISTEN" >> "$LOG.calls"
case "$cmd" in
  migrate) exit "${FAKE_MIGRATE_RC:-0}" ;;
  serve) mode=${FAKE_SERVE:-clean} ;;
  worker) mode=${FAKE_WORKER:-clean} ;;
  *) exit 0 ;;
esac
term() { echo x >> "$LOG.$cmd.term"; [ "$mode" = slow ] && sleep 1; [ "$mode" = failterm ] && exit 1; exit 0; }
trap term TERM
case "$mode" in
  crash:*) IFS=: read -r _ secs rc <<< "$mode"; sleep "$secs"; exit "$rc" ;;
esac
while :; do sleep 0.1; done
FAKE
chmod +x "$dir/fake" "$dir/entrypoint.sh"

fail=0
check() { # name expected actual
  if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: expected $2, got $3"; fail=1; fi
}
terms() { [ -f "$LOG.$1.term" ] && wc -l < "$LOG.$1.term" || echo 0; }
run() { # starts the entrypoint in the background: run ARGS...
  rm -f "$LOG".*
  "$dir/entrypoint.sh" "$@" > "$LOG.out" 2>&1 &
  pid=$!
}
export LOG="$dir/log"

run all; sleep 0.5; kill -TERM $pid; wait $pid; rc=$?
check "all: one TERM, status" 0 "$rc"; check "all: serve got one TERM" 1 "$(terms serve)"; check "all: worker got one TERM" 1 "$(terms worker)"

FAKE_SERVE=slow FAKE_WORKER=slow run all; sleep 0.5; kill -TERM $pid; sleep 0.3; kill -TERM $pid; wait $pid; rc=$?
check "all: two TERMs, status" 0 "$rc"; check "all: two TERMs reach serve" 2 "$(terms serve)"

FAKE_WORKER=crash:0.5:3 FAKE_SERVE=slow run all; sleep 0.9; kill -TERM $pid; wait $pid; rc=$?
check "all: worker exits 3, then TERM while serve stops" 3 "$rc"

FAKE_WORKER=failterm FAKE_SERVE=slow run all; sleep 0.5; kill -TERM $pid; wait $pid; rc=$?
check "all: worker fails on TERM at once, serve stops slowly" 1 "$rc"
FAKE_SERVE=failterm FAKE_WORKER=slow run all; sleep 0.5; kill -TERM $pid; wait $pid; rc=$?
check "all: serve fails on TERM at once, worker stops slowly" 1 "$rc"

FAKE_WORKER=crash:0.5:5 run all; wait $pid; rc=$?
check "all: crashing worker status" 5 "$rc"; check "all: serve was stopped once" 1 "$(terms serve)"

FAKE_SERVE=crash:0.3:4 run all; wait $pid; rc=$?
check "all: crashing serve status" 4 "$rc"; check "all: worker was stopped once" 1 "$(terms worker)"

FAKE_MIGRATE_RC=7 FORGEBASE_MIGRATE_ON_START=1 run all; wait $pid; rc=$?
check "all: failed migrate keeps its status" 7 "$rc"; check "all: no process started" 0 "$(grep -c '^serve\|^worker' "$LOG.calls")"

FORGEBASE_MIGRATE_ON_START=1 FAKE_SERVE=crash:0:0 run serve; wait $pid; rc=$?
check "serve: migrate runs first" "migrate " "$(head -1 "$LOG.calls")"; check "serve: status" 0 "$rc"

FAKE_SERVE=crash:0:0 run; wait $pid; rc=$?
check "no arguments run serve" "serve " "$(head -1 "$LOG.calls")"
FAKE_SERVE=crash:0:0 PORT=9123 run serve; wait $pid
check "PORT is the default listen address" "serve FORGEBASE_LISTEN=0.0.0.0:9123" "$(sed -n 2p "$LOG.calls")"
FAKE_SERVE=crash:0:0 run serve; wait $pid
check "default listen address" "serve FORGEBASE_LISTEN=0.0.0.0:8000" "$(sed -n 2p "$LOG.calls")"
FAKE_SERVE=crash:0:0 FORGEBASE_LISTEN=127.0.0.1:1 PORT=9 run serve; wait $pid
check "FORGEBASE_LISTEN wins" "serve FORGEBASE_LISTEN=127.0.0.1:1" "$(sed -n 2p "$LOG.calls")"

exit $fail

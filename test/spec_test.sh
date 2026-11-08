#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) 2026 Atviro kodo sprendimai
#
# Black-box spec tests for chatlog hooks, install, doctor, written from PLAN and the CLI usage only, never the code.
# Every run uses a throwaway HOME. Usage: B=/path/to/chatlog sh test/spec_test.sh   (needs jq)
B=${B:?set B to the chatlog binary to test}
case $B in /*) ;; *) echo "B must be an absolute path: the tests never run a chatlog found on PATH"; exit 2 ;; esac
W=$(mktemp -d)
trap 'rm -rf "$W"' EXIT
JQ=$(command -v jq) || { echo "jq is needed"; exit 2; }
NP=0; NF=0
pass(){ echo "PASS $1"; NP=$((NP+1)); }
fail(){ echo "FAIL $1: $2"; NF=$((NF+1)); }
# R <home> <datadir> args... ; stdin passed through
R(){ h=$1; d=$2; shift 2; env -i HOME="$h" PATH=/usr/bin:/bin CHATLOG_AUTOSTART=0 TZ=Europe/Vilnius "$B" --data-dir "$d" "$@"; }

H=$W/home; D=$W/data; mkdir -p "$H"

# 1 prompt hook: UTC + local + age on 2nd prompt
echo '{"session_id":"p1","cwd":"/u/x","hook_event_name":"UserPromptSubmit","prompt":"first"}' | R "$H" "$D" hook UserPromptSubmit >/dev/null
sleep 1
o=$(echo '{"session_id":"p1","cwd":"/u/x","hook_event_name":"UserPromptSubmit","prompt":"second"}' | R "$H" "$D" hook UserPromptSubmit)
c=$(printf '%s' "$o" | $JQ -r '.hookSpecificOutput.additionalContext' 2>/dev/null)
e=$(printf '%s' "$o" | $JQ -r '.hookSpecificOutput.hookEventName' 2>/dev/null)
if [ "$e" = UserPromptSubmit ] && printf '%s' "$c" | grep -Eq '[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:]{8}Z UTC' \
  && printf '%s' "$c" | grep -q '+02:00 Europe/Vilnius' && printf '%s' "$c" | grep -Eq '[0-9]+s ago'; then
  pass prompt_time_age; else fail prompt_time_age "got: $o"; fi

# 2 Pre+Post: took N s, one row with duration_ms
echo '{"session_id":"t1s","cwd":"/u/x","hook_event_name":"PreToolUse","tool_name":"Bash","tool_use_id":"tu1","tool_input":{"command":"ls"}}' | R "$H" "$D" hook PreToolUse >"$W/pre.out"
sleep 1
o=$(echo '{"session_id":"t1s","cwd":"/u/x","hook_event_name":"PostToolUse","tool_name":"Bash","tool_use_id":"tu1","tool_input":{"command":"ls"},"tool_response":{"stdout":"a b"}}' | R "$H" "$D" hook PostToolUse)
c=$(printf '%s' "$o" | $JQ -r '.hookSpecificOutput.additionalContext' 2>/dev/null)
if [ ! -s "$W/pre.out" ] && printf '%s' "$c" | grep -Eq 'call started [0-9]{2}:[0-9]{2}:[0-9]{2}.*took [0-9.]+ ?s'; then
  pass tool_took; else fail tool_took "pre stdout=$(cat "$W/pre.out") post=$o"; fi
ev=$(R "$H" "$D" events --session t1s)
n=$(printf '%s\n' "$ev" | grep -c .); dm=$(printf '%s\n' "$ev" | $JQ -r '.duration_ms // empty' 2>/dev/null)
if [ "$n" = 1 ] && [ -n "$dm" ] && [ "$dm" -ge 900 ] 2>/dev/null; then pass tool_one_row_duration
else fail tool_one_row_duration "rows=$n duration_ms=$dm events=$ev"; fi

# 2b PostToolUse without Pre: no "took"
o=$(echo '{"session_id":"t2s","cwd":"/u/x","hook_event_name":"PostToolUse","tool_name":"Bash","tool_use_id":"orphan","tool_input":{},"tool_response":"x"}' | R "$H" "$D" hook PostToolUse; echo "rc=$?")
if printf '%s' "$o" | grep -q 'rc=0' && ! printf '%s' "$o" | grep -q took; then pass post_without_pre
else fail post_without_pre "got: $o"; fi

# 3 secrets never in events
echo '{"session_id":"sec","cwd":"/u/x","hook_event_name":"PreToolUse","tool_name":"Bash","tool_use_id":"s1","tool_input":{"command":"curl -H \"Authorization: Bearer XyZsecretTOKEN998877\" https://a"}}' | R "$H" "$D" hook PreToolUse >/dev/null
echo '{"session_id":"sec","cwd":"/u/x","hook_event_name":"PostToolUse","tool_name":"Bash","tool_use_id":"s1","tool_input":{},"tool_response":{"stdout":"ok"}}' | R "$H" "$D" hook PostToolUse >/dev/null
echo '{"session_id":"sec","cwd":"/u/x","hook_event_name":"PreToolUse","tool_name":"Bash","tool_use_id":"s2","tool_input":{"command":"API_KEY=QwErTysecret55443 ./run.sh"}}' | R "$H" "$D" hook PreToolUse >/dev/null
echo '{"session_id":"sec","cwd":"/u/x","hook_event_name":"PostToolUse","tool_name":"Bash","tool_use_id":"s2","tool_input":{},"tool_response":{"stdout":"API_KEY=QwErTysecret55443"}}' | R "$H" "$D" hook PostToolUse >/dev/null
echo '{"session_id":"sec","cwd":"/u/x","hook_event_name":"PreToolUse","tool_name":"Read","tool_use_id":"s3","tool_input":{"file_path":"/u/x/.env"}}' | R "$H" "$D" hook PreToolUse >/dev/null
echo '{"session_id":"sec","cwd":"/u/x","hook_event_name":"PostToolUse","tool_name":"Read","tool_use_id":"s3","tool_input":{"file_path":"/u/x/.env"},"tool_response":{"type":"text","file":{"filePath":"/u/x/.env","content":"DB_PASSWORD=PlainPw77665\nSTRIPE=sk_live_abcdEFGH1234567890\n"}}}' | R "$H" "$D" hook PostToolUse >/dev/null
ev=$(R "$H" "$D" events --session sec --limit 100)
leak=""
for s in XyZsecretTOKEN998877 QwErTysecret55443 PlainPw77665 sk_live_abcdEFGH1234567890; do
  printf '%s' "$ev" | grep -q "$s" && leak="$leak $s"; done
rows=$(printf '%s\n' "$ev" | grep -c .)
if [ -z "$leak" ] && [ "$rows" = 3 ]; then pass secrets_redacted; else fail secrets_redacted "rows=$rows leaked:$leak events=$ev"; fi

# 4 100 KB output capped near 2000 bytes
big=$(head -c 102400 /dev/zero | tr '\0' 'a')
echo '{"session_id":"big","cwd":"/u/x","hook_event_name":"PreToolUse","tool_name":"Bash","tool_use_id":"b1","tool_input":{"command":"yes"}}' | R "$H" "$D" hook PreToolUse >/dev/null
printf '{"session_id":"big","cwd":"/u/x","hook_event_name":"PostToolUse","tool_name":"Bash","tool_use_id":"b1","tool_input":{"command":"yes"},"tool_response":{"stdout":"%s"}}' "$big" | R "$H" "$D" hook PostToolUse >/dev/null
len=$(R "$H" "$D" events --session big | $JQ -r '.output' | wc -c)
if [ "$len" -ge 500 ] && [ "$len" -le 2300 ]; then pass output_capped; else fail output_capped "output length=$len"; fi

# 5 garbage / unknown / unwritable => exit 0, empty stdout
o=$(printf 'not json {{{' | R "$H" "$D" hook UserPromptSubmit); rc=$?
[ $rc = 0 ] && [ -z "$o" ] && pass garbage_stdin || fail garbage_stdin "rc=$rc out=$o"
o=$(echo '{"session_id":"u","hook_event_name":"Bogus"}' | R "$H" "$D" hook Bogus); rc=$?
[ $rc = 0 ] && [ -z "$o" ] && pass unknown_event || fail unknown_event "rc=$rc out=$o"
mkdir -p "$W/ro"; chmod 555 "$W/ro"
o=$(echo '{"session_id":"u","cwd":"/u/x","hook_event_name":"UserPromptSubmit","prompt":"x"}' | R "$H" "$W/ro/d" hook UserPromptSubmit 2>/dev/null); rc=$?
[ $rc = 0 ] && [ -z "$o" ] && pass unwritable_datadir || fail unwritable_datadir "rc=$rc out=$o"
chmod 755 "$W/ro"
o=$(R "$H" "$D" hook PreToolUse </dev/null); rc=$?
[ $rc = 0 ] && [ -z "$o" ] && pass empty_stdin || fail empty_stdin "rc=$rc out=$o"

# socket is relative (cd D2): the absolute scratchpad path exceeds the 108-byte unix socket limit
# 6 Stop with transcript_path: exchange appears
TR=$W/tr.jsonl
cat >"$TR" <<'EOF'
{"type":"user","uuid":"u1","sessionId":"st1","cwd":"/u/x","timestamp":"2026-11-08T09:00:00Z","origin":{"kind":"human"},"message":{"content":"zebraquokka question about penguins"}}
{"type":"assistant","uuid":"a1","sessionId":"st1","timestamp":"2026-11-08T09:00:05Z","message":{"content":[{"type":"text","text":"zebraquokka answer about penguins"}]}}
EOF
D2=$W/data2
o=$(printf '{"session_id":"st1","transcript_path":"%s","cwd":"/u/x","hook_event_name":"Stop"}' "$TR" | R "$H" "$D2" hook Stop); rc=$?
st=$(R "$H" "$D2" events --session st1)
again=$(R "$H" "$D2" ingest "$TR" 2>&1)
(cd "$D2" && exec env -i HOME="$H" PATH=/usr/bin:/bin CHATLOG_AUTOSTART=0 TZ=Europe/Vilnius "$B" --data-dir "$D2" --socket s.sock server --source "$W/nosuch" >"$W/server.log" 2>&1) &
SP=$!
i=0; while [ $i -lt 50 ] && [ ! -S "$D2/s.sock" ]; do sleep 0.2; i=$((i+1)); done
sr=$(cd "$D2" && R "$H" "$D2" --socket s.sock search zebraquokka 2>&1)
kill $SP 2>/dev/null; wait $SP 2>/dev/null
if [ $rc = 0 ] && printf '%s' "$st" | grep -q '"event":"Stop"' && printf '%s' "$sr" | grep -q zebraquokka; then pass stop_ingests_transcript
else fail stop_ingests_transcript "rc=$rc events=$st reingest=$again search=$(printf '%s' "$sr" | head -c 400)"; fi
printf '%s' "$again" | grep -Eqi '(^|[^0-9])0 (new|exchange|chunk|record)|nothing new|unchanged' && pass reingest_idempotent \
  || echo "INFO reingest_idempotent output: $again"

# 7 install keeps other keys, idempotent, uninstall restores
IH=$W/ihome; mkdir -p "$IH/.claude"
cat >"$IH/.claude/settings.json" <<'EOF'
{"model":"opus","permissions":{"allow":["Bash(ls:*)"]},"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"/usr/local/bin/myguard"}]}],"Stop":[{"hooks":[{"type":"command","command":"echo bye"}]}]}}
EOF
cat >"$IH/.claude.json" <<'EOF'
{"numStartups":5,"mcpServers":{"other":{"type":"stdio","command":"/bin/other","args":["x"]}}}
EOF
cp "$IH/.claude/settings.json" "$W/s.orig"; cp "$IH/.claude.json" "$W/c.orig"
IR(){ R "$IH" "$W/idata" "$@" --settings "$IH/.claude/settings.json" --claude-json "$IH/.claude.json" --bin "$B"; }
o1=$(IR install 2>&1); rc=$?
S1=$IH/.claude/settings.json; C1=$IH/.claude.json
ok=1
[ $rc = 0 ] || ok=0
$JQ -e '.model=="opus" and .permissions.allow[0]=="Bash(ls:*)"' "$S1" >/dev/null || ok=0
$JQ -e '[.hooks.PreToolUse[].hooks[].command]|index("/usr/local/bin/myguard")!=null' "$S1" >/dev/null || ok=0
$JQ -e '[.hooks.Stop[].hooks[].command]|index("echo bye")!=null' "$S1" >/dev/null || ok=0
for ev in UserPromptSubmit PreToolUse PostToolUse SessionStart Stop SubagentStop PreCompact SessionEnd; do
  $JQ -e --arg e "$ev" '[.hooks[$e][]?.hooks[]?.command|select(test("chatlog hook"))]|length==1' "$S1" >/dev/null || { ok=0; echo "  missing/dup hook $ev"; }; done
$JQ -e '.numStartups==5 and .mcpServers.other.command=="/bin/other" and (.mcpServers.chatlog|type=="object")' "$C1" >/dev/null || ok=0
ls "$IH/.claude/" | grep -q 'settings.json.chatlog-bak-' || ok=0
[ $ok = 1 ] && pass install_merge_keeps || fail install_merge_keeps "rc=$rc out=$o1 settings=$(cat "$S1") claude=$(cat "$C1")"
cp "$S1" "$W/s.after1"
o2=$(IR install 2>&1); rc=$?
if [ $rc = 0 ] && printf '%s' "$o2" | grep -qi unchanged && cmp -s "$S1" "$W/s.after1"; then pass install_idempotent
else fail install_idempotent "rc=$rc out=$o2"; fi
o=$(R "$IH" "$W/idata" doctor --settings "$S1" --claude-json "$C1" 2>&1); rc=$?
if [ $rc = 0 ]; then pass doctor_after_install; else fail doctor_after_install "rc=$rc out=$o"; fi
o3=$(IR install --uninstall 2>&1); rc=$?
if [ $rc = 0 ] && [ "$($JQ -S . "$S1")" = "$($JQ -S . "$W/s.orig")" ] && [ "$($JQ -S . "$C1")" = "$($JQ -S . "$W/c.orig")" ]; then pass uninstall_restores
else fail uninstall_restores "rc=$rc out=$o3 settings=$($JQ -cS . "$S1") claude=$($JQ -cS . "$C1")"; fi

# 7b dry-run writes nothing
cp "$S1" "$W/s.pre"; o=$(IR install --dry-run 2>&1); rc=$?
[ $rc = 0 ] && cmp -s "$S1" "$W/s.pre" && pass install_dry_run || fail install_dry_run "rc=$rc out=$o"

# 7c unparsable settings: non-zero, nothing written
BH=$W/bhome; mkdir -p "$BH/.claude"; printf '{"hooks": [broken' >"$BH/.claude/settings.json"
printf '{"mcpServers":{}}' >"$BH/.claude.json"
o=$(R "$BH" "$W/bdata" install --settings "$BH/.claude/settings.json" --claude-json "$BH/.claude.json" --bin "$B" 2>&1); rc=$?
nb=$(ls -A "$BH/.claude" "$BH" | grep -c chatlog-bak)
if [ $rc != 0 ] && [ "$(cat "$BH/.claude/settings.json")" = '{"hooks": [broken' ] && [ "$(cat "$BH/.claude.json")" = '{"mcpServers":{}}' ] && [ "$nb" = 0 ]; then pass install_refuses_unparsable
else fail install_refuses_unparsable "rc=$rc out=$o settings=$(cat "$BH/.claude/settings.json") claude=$(cat "$BH/.claude.json") backups=$nb"; fi

# 7d absent settings: created
AH=$W/ahome; mkdir -p "$AH"
o=$(R "$AH" "$W/adata" install --settings "$AH/.claude/settings.json" --claude-json "$AH/.claude.json" --bin "$B" 2>&1); rc=$?
[ $rc = 0 ] && $JQ -e '.hooks.Stop' "$AH/.claude/settings.json" >/dev/null 2>&1 && pass install_absent_settings || fail install_absent_settings "rc=$rc out=$o"

echo "TOTAL pass=$NP fail=$NF"

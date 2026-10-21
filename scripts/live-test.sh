#!/usr/bin/env bash
# ABOUTME: Runs a credential-safe live contract matrix against mailbox.org's real OX API.
# ABOUTME: Safe mode is read-only; full mode creates isolated fixtures and removes them.

set -uo pipefail

usage() {
	printf 'usage: %s [safe|full]\n' "$0"
	printf '%s\n' \
		'Credentials are required environment inputs: MBX_USERNAME and MBX_PASSWORD.' \
		'Supply your own active test account when invoking this script.' \
		'full also requires MBX_LIVE_WRITES=1; see docs/LIVE_TESTING.md.'
}

if (( $# > 1 )); then
	usage >&2
	exit 64
fi
case "${1:-safe}" in
-h | --help)
	usage
	exit 0
	;;
esac

readonly REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly MODE="${1:-safe}"
readonly REQUEST_TIMEOUT="${MBX_LIVE_TIMEOUT_SECONDS:-45}"
readonly REQUEST_INTERVAL="${MBX_LIVE_REQUEST_INTERVAL_SECONDS:-1}"

case "$MODE" in
safe | full) ;;
*)
	usage >&2
	exit 64
	;;
esac

if [[ -z "${MBX_USERNAME:-}" || -z "${MBX_PASSWORD:-}" ]]; then
	printf 'MBX_USERNAME and MBX_PASSWORD must be set in the environment\n' >&2
	exit 64
fi
if [[ "$MODE" == "full" && "${MBX_LIVE_WRITES:-0}" != "1" ]]; then
	printf 'full mode requires MBX_LIVE_WRITES=1\n' >&2
	exit 64
fi
if [[ ! "$REQUEST_INTERVAL" =~ ^[1-9][0-9]*$ ]]; then
	printf 'MBX_LIVE_REQUEST_INTERVAL_SECONDS must be a positive whole number\n' >&2
	exit 64
fi

for command in go jq timeout cmp mktemp sleep stat; do
	if ! command -v "$command" >/dev/null 2>&1; then
		printf 'required command not found: %s\n' "$command" >&2
		exit 69
	fi
done

LIVE_TMP="$(mktemp -d /tmp/mbx-live-test.XXXXXX)"
readonly LIVE_TMP
readonly MBX_BIN="$LIVE_TMP/mbx"
export MBX_HOME="$LIVE_TMP/mbx-home"
readonly LIVE_SESSION_PATH="$MBX_HOME/session.json"

RUN_STAMP="$(date -u +%Y%m%dT%H%M%SZ)-$$"
readonly RUN_STAMP
readonly FIXTURE_PREFIX="mbx-live-$RUN_STAMP"
REJECTED_PASSWORD="mbx-live-deliberately-invalid-$RUN_STAMP"
if [[ "$REJECTED_PASSWORD" == "$MBX_PASSWORD" ]]; then
	REJECTED_PASSWORD+="-different"
fi
readonly REJECTED_PASSWORD

declare -A STATUS=()
declare -A NOTE=()
LAST_STDOUT=""
LAST_STDERR=""
ASSERTION_STDOUT=""
ASSERTION_STDERR=""
CLEANUP_DONE=0
CLEANUP_FAILURES=0
REQUESTS_STARTED=0

account_id=""
attachment_id=""
authenticated_email=""
authenticated_user_id=""
safe_mail_id=""
contact_id=""
contact_timestamp=""
declare -a imported_contact_ids=()
contact_import_attempted=0
event_id=""
event_create_attempted=0
declare -a imported_event_ids=()
event_import_attempted=0
file_id=""
file_folder=""
file_create_attempted=0
file_ready=0
folder_id=""
folder_create_attempted=0
folder_permissions_json="null"
folder_ready=0
ensured_mail_folder_id=""
ensured_mail_folder_attempted=0
share_create_attempted=0
group_id=""
mailfilter_id=""
mailfilter_create_attempted=0
mailfilter_ready=0
reminder_id=""
reminder_absence_verified=0
reminder_create_attempted=0
reminder_ready=0
reminder_task_id=""
reminder_task_ready=0
reminder_task_timestamp=""
resource_id=""
filestore_quota=""
filestore_use=""
settings_key=""
settings_original_json="null"
settings_restore_armed=0
settings_test_json="null"
snippet_id=""
snippet_create_attempted=0
snippet_created_by=""
snippet_ready=0
task_id=""
task_create_attempted=0
task_ready=0
task_timestamp=""
draft_id=""
draft_folder=""
copied_mail_id=""
moved_mail_id=""
sent_mail_id=""
inbox_mail_id=""
original_display_name=""
user_changed=0
password_changed=0
temporary_password=""
readonly ORIGINAL_PASSWORD="$MBX_PASSWORD"
readonly EVENT_FIXTURE_FROM="2030-01-02T00:00:00Z"
readonly EVENT_FIXTURE_TO="2030-01-03T00:00:00Z"
readonly IMPORT_EVENT_FROM="2030-01-05T00:00:00Z"
readonly IMPORT_EVENT_TO="2030-01-07T00:00:00Z"
readonly TASK_FIXTURE_END_MILLIS="1893585600000"
readonly REMINDER_FIXTURE_FROM="2030-01-04T08:00:00Z"
readonly REMINDER_FIXTURE_TO="2030-01-04T10:00:00Z"
readonly REMINDER_FIXTURE_ALARM_MILLIS="1893747600000"
readonly SETTINGS_FIXTURE_PATH="io.ox/mail"

mark_pass() {
	local operation="$1"
	if [[ "${STATUS[$operation]:-}" == "fail" ]]; then
		printf 'CHECKED %-24s  later invocation passed; earlier failure remains authoritative\n' "$operation"
		return 0
	fi
	STATUS["$operation"]="pass"
	NOTE["$operation"]="${2:-}"
	printf 'PASS    %-24s%s\n' "$operation" "${2:+  $2}"
}

mark_limited() {
	local operation="$1"
	STATUS["$operation"]="limited"
	NOTE["$operation"]="$2"
	printf 'LIMITED %-24s  %s\n' "$operation" "$2"
}

mark_skipped() {
	local operation="$1"
	STATUS["$operation"]="skipped"
	NOTE["$operation"]="$2"
	printf 'SKIPPED %-24s  %s\n' "$operation" "$2"
}

mark_fail() {
	local operation="$1"
	STATUS["$operation"]="fail"
	NOTE["$operation"]="$2"
	printf 'FAIL    %-24s  %s\n' "$operation" "$2" >&2
}

# Keep the matrix deliberately below burst rate. This covers ordinary operations,
# mutation-owned read-backs, authentication lifecycle probes, and trap cleanup. The
# registry discovery call is local and intentionally excluded.
pace_request() {
	if ((REQUESTS_STARTED > 0)); then
		sleep "$REQUEST_INTERVAL"
	fi
	REQUESTS_STARTED=$((REQUESTS_STARTED + 1))
}

error_code() {
	jq -r '.error.code // empty' "$LAST_STDERR" 2>/dev/null | head -n 1
}

run_op() {
	local operation="$1"
	shift
	LAST_STDOUT="$LIVE_TMP/stdout.json"
	LAST_STDERR="$LIVE_TMP/stderr.json"
	: >"$LAST_STDOUT"
	: >"$LAST_STDERR"

	pace_request
	timeout "$REQUEST_TIMEOUT" "$MBX_BIN" "$@" --llm >"$LAST_STDOUT" 2>"$LAST_STDERR"
	local exit_code=$?
	if ((exit_code != 0)); then
		local detail
		detail="$(error_code)"
		if [[ -z "$detail" ]]; then
			detail="exit $exit_code"
		fi
		mark_fail "$operation" "$detail"
		return 1
	fi
	if ! jq -e . "$LAST_STDOUT" >/dev/null 2>&1; then
		mark_fail "$operation" "stdout was not valid JSON"
		return 1
	fi
	mark_pass "$operation"
	return 0
}

assert_last() {
	local operation="$1"
	local expression="$2"
	local message="$3"
	if ! jq -e "$expression" "$LAST_STDOUT" >/dev/null 2>&1; then
		mark_fail "$operation" "$message"
		return 1
	fi
	return 0
}

assert_last_jq() {
	local operation="$1"
	local message="$2"
	shift 2
	if ! jq -e "$@" "$LAST_STDOUT" >/dev/null 2>&1; then
		mark_fail "$operation" "$message"
		return 1
	fi
	return 0
}

# Run a read-only command as semantic evidence owned by a mutation. Unlike run_op, this
# does not give the read operation a second status or turn transport success into a pass
# for the mutation; the caller must also assert the captured fields with
# assert_captured_jq.
capture_json_for() {
	local operation="$1"
	local message="$2"
	shift 2
	ASSERTION_STDOUT="$LIVE_TMP/assertion-stdout.json"
	ASSERTION_STDERR="$LIVE_TMP/assertion-stderr.json"
	: >"$ASSERTION_STDOUT"
	: >"$ASSERTION_STDERR"

	pace_request
	timeout "$REQUEST_TIMEOUT" "$MBX_BIN" "$@" --llm >"$ASSERTION_STDOUT" 2>"$ASSERTION_STDERR"
	local exit_code=$?
	if ((exit_code != 0)); then
		local detail
		detail="$(jq -r '.error.code // empty' "$ASSERTION_STDERR" 2>/dev/null | head -n 1)"
		if [[ -z "$detail" ]]; then
			detail="exit $exit_code"
		fi
		mark_fail "$operation" "$message ($detail)"
		return 1
	fi
	if ! jq -e . "$ASSERTION_STDOUT" >/dev/null 2>&1; then
		mark_fail "$operation" "$message (invalid JSON)"
		return 1
	fi
	return 0
}

assert_captured_jq() {
	local operation="$1"
	local message="$2"
	shift 2
	if ! jq -e "$@" "$ASSERTION_STDOUT" >/dev/null 2>&1; then
		mark_fail "$operation" "$message"
		return 1
	fi
	return 0
}

# Prove cross-process stored-session reuse without exposing the real password: this
# invocation can succeed only if cookie autologin completes before credential fallback.
capture_session_reuse_for() {
	local operation="$1"
	local message="$2"
	shift 2
	ASSERTION_STDOUT="$LIVE_TMP/assertion-stdout.json"
	ASSERTION_STDERR="$LIVE_TMP/assertion-stderr.json"
	: >"$ASSERTION_STDOUT"
	: >"$ASSERTION_STDERR"

	pace_request
	MBX_PASSWORD="$REJECTED_PASSWORD" timeout "$REQUEST_TIMEOUT" \
		"$MBX_BIN" "$@" --llm >"$ASSERTION_STDOUT" 2>"$ASSERTION_STDERR"
	local exit_code=$?
	if ((exit_code != 0)); then
		local detail
		detail="$(jq -r '.error.code // empty' "$ASSERTION_STDERR" 2>/dev/null | head -n 1)"
		if [[ -z "$detail" ]]; then
			detail="exit $exit_code"
		fi
		mark_fail "$operation" "$message ($detail)"
		return 1
	fi
	if ! jq -e . "$ASSERTION_STDOUT" >/dev/null 2>&1; then
		mark_fail "$operation" "$message (invalid JSON)"
		return 1
	fi
	return 0
}

# After verified logout and local clearing, the rejected password must reach credential
# login and fail. Success would mean that reusable authentication state survived.
expect_session_unavailable_for() {
	local operation="$1"
	local message="$2"
	shift 2
	ASSERTION_STDOUT="$LIVE_TMP/assertion-stdout.json"
	ASSERTION_STDERR="$LIVE_TMP/assertion-stderr.json"
	: >"$ASSERTION_STDOUT"
	: >"$ASSERTION_STDERR"

	pace_request
	MBX_PASSWORD="$REJECTED_PASSWORD" timeout "$REQUEST_TIMEOUT" \
		"$MBX_BIN" "$@" --llm >"$ASSERTION_STDOUT" 2>"$ASSERTION_STDERR"
	local exit_code=$?
	if ((exit_code != 2)); then
		mark_fail "$operation" "$message (exit $exit_code, want authentication exit 2)"
		return 1
	fi
	if [[ -s "$ASSERTION_STDOUT" ]]; then
		mark_fail "$operation" "$message (unexpected stdout)"
		return 1
	fi
	if ! jq -e '
		(.error | type) == "object"
		and (.error.code | type) == "string"
		and (.error.code == "auth_failed"
			or (.error.code | startswith("LGI-"))
			or (.error.code | startswith("SES-")))
	' "$ASSERTION_STDERR" >/dev/null 2>&1; then
		mark_fail "$operation" "$message (missing structured authentication error)"
		return 1
	fi
	return 0
}

assert_session_present_for() {
	local operation="$1"
	local message="$2"
	if [[ ! -f "$LIVE_SESSION_PATH" || ! -s "$LIVE_SESSION_PATH" ]]; then
		mark_fail "$operation" "$message"
		return 1
	fi
	return 0
}

assert_session_absent_for() {
	local operation="$1"
	local message="$2"
	if [[ -e "$LIVE_SESSION_PATH" || -L "$LIVE_SESSION_PATH" ]]; then
		mark_fail "$operation" "$message"
		return 1
	fi
	return 0
}

quiet_json() {
	local output="$LIVE_TMP/cleanup.json"
	pace_request
	timeout "$REQUEST_TIMEOUT" "$MBX_BIN" "$@" --llm >"$output" 2>/dev/null &&
		jq -e . "$output" >/dev/null 2>&1
}

cleanup_imported_contacts() {
	local marker_prefix="$FIXTURE_PREFIX-imported-contact-"
	local -a cleanup_ids=("${imported_contact_ids[@]}")
	local -A seen_ids=()
	local id timestamp index

	for id in "${cleanup_ids[@]}"; do
		[[ -n "$id" ]] && seen_ids["$id"]=1
	done
	if quiet_json contacts list --limit 500 &&
		jq -e '.truncated == false' "$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
		while IFS= read -r id; do
			[[ -n "$id" ]] || continue
			if [[ -z "${seen_ids[$id]:-}" ]]; then
				cleanup_ids+=("$id")
				seen_ids["$id"]=1
			fi
		done < <(
			jq -r --arg prefix "$marker_prefix" \
				'.contacts[]? | select((.display_name // "") | startswith($prefix)) | .id' \
				"$LIVE_TMP/cleanup.json"
		)
	fi

	for ((index = ${#cleanup_ids[@]} - 1; index >= 0; index--)); do
		id="${cleanup_ids[index]}"
		timestamp=""
		if quiet_json contacts get --id "$id" --folder 33; then
			timestamp="$(jq -r '.last_modified // empty' "$LIVE_TMP/cleanup.json")"
		fi
		if [[ -n "$timestamp" ]]; then
			quiet_json contacts delete --id "$id" --folder 33 \
				--timestamp "$timestamp" --confirm "$id" || true
		fi
	done

	if ! quiet_json contacts list --limit 500 ||
		! jq -e --arg prefix "$marker_prefix" \
			'(.truncated == false) and all(.contacts[]?; (((.display_name // "") | startswith($prefix)) | not))' \
			"$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
		return 1
	fi
	for id in "${cleanup_ids[@]}"; do
		if ! jq -e --arg id "$id" \
			'all(.contacts[]?; (.id | tostring) != $id)' \
			"$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
			return 1
		fi
	done

	imported_contact_ids=()
	contact_import_attempted=0
	return 0
}

cleanup_imported_events() {
	local marker_prefix="$FIXTURE_PREFIX-imported-event-"
	local -a cleanup_ids=("${imported_event_ids[@]}")
	local -A seen_ids=()
	local id index

	for id in "${cleanup_ids[@]}"; do
		[[ -n "$id" ]] && seen_ids["$id"]=1
	done
	if quiet_json events list --from "$IMPORT_EVENT_FROM" --to "$IMPORT_EVENT_TO" --limit 200 &&
		jq -e '.truncated == false' "$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
		while IFS= read -r id; do
			[[ -n "$id" ]] || continue
			if [[ -z "${seen_ids[$id]:-}" ]]; then
				cleanup_ids+=("$id")
				seen_ids["$id"]=1
			fi
		done < <(
			jq -r --arg prefix "$marker_prefix" \
				'.items[]? | select((.summary // "") | startswith($prefix)) | .id' \
				"$LIVE_TMP/cleanup.json"
		)
	fi

	for ((index = ${#cleanup_ids[@]} - 1; index >= 0; index--)); do
		id="${cleanup_ids[index]}"
		quiet_json events delete --id "$id" --folder 32 --confirm "$id" || true
	done

	if ! quiet_json events list --from "$IMPORT_EVENT_FROM" --to "$IMPORT_EVENT_TO" --limit 200 ||
		! jq -e --arg prefix "$marker_prefix" \
			'(.truncated == false) and all(.items[]?; (((.summary // "") | startswith($prefix)) | not))' \
			"$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
		return 1
	fi
	for id in "${cleanup_ids[@]}"; do
		if ! jq -e --arg id "$id" \
			'all(.items[]?; (.id | tostring) != $id)' \
			"$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
			return 1
		fi
	done

	imported_event_ids=()
	event_import_attempted=0
	return 0
}

cleanup_standard_event() {
	local marker_prefix="$FIXTURE_PREFIX-event"
	local -a cleanup_ids=()
	local -A seen_ids=()
	local id index

	# Read before any retry: a successful delete may have lost its output, and repeating a
	# non-idempotent delete without first observing state would make cleanup ambiguous.
	if ! quiet_json events list --from "$EVENT_FIXTURE_FROM" --to "$EVENT_FIXTURE_TO" --limit 200 ||
		! jq -e '.truncated == false' "$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
		return 1
	fi
	while IFS= read -r id; do
		[[ -n "$id" ]] || continue
		if [[ -z "${seen_ids[$id]:-}" ]]; then
			cleanup_ids+=("$id")
			seen_ids["$id"]=1
		fi
	done < <(
		jq -r --arg id "$event_id" --arg prefix "$marker_prefix" \
			'.items[]? | select(((.id | tostring) == $id and $id != "") or ((.summary // "") | startswith($prefix))) | (.id | tostring)' \
			"$LIVE_TMP/cleanup.json"
	)

	for ((index = ${#cleanup_ids[@]} - 1; index >= 0; index--)); do
		id="${cleanup_ids[index]}"
		quiet_json events delete --id "$id" --folder 32 --confirm "$id" || true
	done

	if ! quiet_json events list --from "$EVENT_FIXTURE_FROM" --to "$EVENT_FIXTURE_TO" --limit 200 ||
		! jq -e --arg id "$event_id" --arg prefix "$marker_prefix" \
			'(.truncated == false) and all(.items[]?; (((.id | tostring) != $id) or $id == "") and (((.summary // "") | startswith($prefix)) | not))' \
			"$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
		return 1
	fi

	event_id=""
	event_create_attempted=0
	return 0
}

cleanup_standard_task() {
	local marker_prefix="$FIXTURE_PREFIX-task"
	local -a cleanup_ids=()
	local -A seen_ids=()
	local id timestamp index trusted

	if ((task_ready == 1)) && [[ -n "$task_id" ]]; then
		cleanup_ids+=("$task_id")
		seen_ids["$task_id"]=1
	fi
	# Read the exact folder before retrying a non-idempotent delete. Marker discovery
	# recovers a create that reached the provider but lost its response.
	if ! quiet_json tasks list --folder 35 --limit 500 ||
		! jq -e '.truncated == false' "$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
		return 1
	fi
	while IFS= read -r id; do
		[[ -n "$id" ]] || continue
		if [[ -z "${seen_ids[$id]:-}" ]]; then
			cleanup_ids+=("$id")
			seen_ids["$id"]=1
		fi
	done < <(
		jq -r --arg prefix "$marker_prefix" \
			'.items[]? | select((.title // "") | startswith($prefix)) | (.id | tostring)' \
			"$LIVE_TMP/cleanup.json"
	)

	for ((index = ${#cleanup_ids[@]} - 1; index >= 0; index--)); do
		id="${cleanup_ids[index]}"
		trusted=0
		if ((task_ready == 1)) && [[ "$id" == "$task_id" ]]; then
			trusted=1
		fi
		timestamp=""
		if quiet_json tasks get --id "$id" --folder 35 &&
			jq -e --arg id "$id" --arg prefix "$marker_prefix" --argjson trusted "$trusted" \
				'(.id | tostring) == $id and (.folder_id | tostring) == "35" and .timestamp > 0 and ($trusted == 1 or ((.title // "") | startswith($prefix)))' \
				"$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
			timestamp="$(jq -r '.timestamp' "$LIVE_TMP/cleanup.json")"
		fi
		if [[ -n "$timestamp" ]]; then
			quiet_json tasks delete --id "$id" --folder 35 \
				--timestamp "$timestamp" --confirm "$id" || true
		fi
	done

	if ! quiet_json tasks list --folder 35 --limit 500 ||
		! jq -e --arg id "$task_id" --arg prefix "$marker_prefix" --argjson trusted "$task_ready" \
			'(.truncated == false) and all(.items[]?; ($trusted == 0 or (.id | tostring) != $id) and (((.title // "") | startswith($prefix)) | not))' \
			"$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
		return 1
	fi

	task_id=""
	task_timestamp=""
	task_create_attempted=0
	task_ready=0
	return 0
}

cleanup_reminder_fixture() {
	local marker_prefix="$FIXTURE_PREFIX-reminder"
	local -a task_ids=()
	local -a reminder_ids=()
	local -A seen_task_ids=()
	local -A seen_reminder_ids=()
	local id target_id timestamp index trusted

	if ((reminder_task_ready == 1)) && [[ -n "$reminder_task_id" ]]; then
		task_ids+=("$reminder_task_id")
		seen_task_ids["$reminder_task_id"]=1
	fi
	if ! quiet_json tasks list --folder 35 --limit 500 ||
		! jq -e '.truncated == false' "$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
		return 1
	fi
	while IFS= read -r id; do
		[[ -n "$id" ]] || continue
		if [[ -z "${seen_task_ids[$id]:-}" ]]; then
			task_ids+=("$id")
			seen_task_ids["$id"]=1
		fi
	done < <(
		jq -r --arg prefix "$marker_prefix" \
			'.items[]? | select((.title // "") | startswith($prefix)) | (.id | tostring)' \
			"$LIVE_TMP/cleanup.json"
	)

	# Reminder rows expose no title, so only an already verified reminder id or a row
	# targeting one of the exact marker/trusted backing tasks is eligible for deletion.
	if ! quiet_json reminder list --from "$REMINDER_FIXTURE_FROM" \
		--to "$REMINDER_FIXTURE_TO" --limit 200 ||
		! jq -e '.truncated == false' "$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
		return 1
	fi
	if ((reminder_ready == 1)) && [[ -n "$reminder_id" ]]; then
		while IFS= read -r id; do
			[[ -n "$id" ]] || continue
			if [[ -z "${seen_reminder_ids[$id]:-}" ]]; then
				reminder_ids+=("$id")
				seen_reminder_ids["$id"]=1
			fi
		done < <(
			jq -r --arg id "$reminder_id" \
				'.items[]? | select((.id | tostring) == $id) | (.id | tostring)' \
				"$LIVE_TMP/cleanup.json"
		)
	fi
	for target_id in "${task_ids[@]}"; do
		while IFS= read -r id; do
			[[ -n "$id" ]] || continue
			if [[ -z "${seen_reminder_ids[$id]:-}" ]]; then
				reminder_ids+=("$id")
				seen_reminder_ids["$id"]=1
			fi
		done < <(
			jq -r --arg target "$target_id" \
				'.items[]? | select((.target_id | tostring) == $target and (.folder | tostring) == "35") | (.id | tostring)' \
				"$LIVE_TMP/cleanup.json"
		)
	done
	for ((index = ${#reminder_ids[@]} - 1; index >= 0; index--)); do
		id="${reminder_ids[index]}"
		quiet_json reminder delete --id "$id" --confirm "$id" || true
	done

	if ! quiet_json reminder list --from "$REMINDER_FIXTURE_FROM" \
		--to "$REMINDER_FIXTURE_TO" --limit 200 ||
		! jq -e '.truncated == false' "$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
		return 1
	fi
	if ((reminder_ready == 1)) && [[ -n "$reminder_id" ]] &&
		! jq -e --arg id "$reminder_id" \
			'all(.items[]?; (.id | tostring) != $id)' \
			"$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
		return 1
	fi
	for target_id in "${task_ids[@]}"; do
		if ! jq -e --arg target "$target_id" \
			'all(.items[]?; (.target_id | tostring) != $target)' \
			"$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
			return 1
		fi
	done
	reminder_absence_verified=1

	# The alarm-bearing task is the parent of the reminder and is deleted only after the
	# child reminder is proved absent. Re-read for the latest optimistic-lock timestamp.
	for ((index = ${#task_ids[@]} - 1; index >= 0; index--)); do
		id="${task_ids[index]}"
		trusted=0
		if ((reminder_task_ready == 1)) && [[ "$id" == "$reminder_task_id" ]]; then
			trusted=1
		fi
		timestamp=""
		if quiet_json tasks get --id "$id" --folder 35 &&
			jq -e --arg id "$id" --arg prefix "$marker_prefix" --argjson trusted "$trusted" \
				'(.id | tostring) == $id and (.folder_id | tostring) == "35" and .timestamp > 0 and ($trusted == 1 or ((.title // "") | startswith($prefix)))' \
				"$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
			timestamp="$(jq -r '.timestamp' "$LIVE_TMP/cleanup.json")"
		fi
		if [[ -n "$timestamp" ]]; then
			quiet_json tasks delete --id "$id" --folder 35 \
				--timestamp "$timestamp" --confirm "$id" || true
		fi
	done

	if ! quiet_json tasks list --folder 35 --limit 500 ||
		! jq -e --arg id "$reminder_task_id" --arg prefix "$marker_prefix" --argjson trusted "$reminder_task_ready" \
			'(.truncated == false) and all(.items[]?; ($trusted == 0 or (.id | tostring) != $id) and (((.title // "") | startswith($prefix)) | not))' \
			"$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
		return 1
	fi

	reminder_id=""
	reminder_absence_verified=0
	reminder_create_attempted=0
	reminder_ready=0
	reminder_task_id=""
	reminder_task_ready=0
	reminder_task_timestamp=""
	return 0
}

cleanup_file_fixture() {
	local marker_prefix="$FIXTURE_PREFIX-file"
	local -a cleanup_ids=()
	local -A seen_ids=()
	local id bare_id index

	# A file can only be created after the temporary folder identity was read back and
	# verified. List that exact folder before retrying any non-idempotent cleanup action.
	[[ -n "$folder_id" ]] || return 1
	if ! quiet_json files list --folder "$folder_id" --limit 200 ||
		! jq -e '.truncated == false' "$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
		return 1
	fi
	while IFS= read -r id; do
		[[ -n "$id" ]] || continue
		if [[ -z "${seen_ids[$id]:-}" ]]; then
			cleanup_ids+=("$id")
			seen_ids["$id"]=1
		fi
	done < <(
		jq -r --arg prefix "$marker_prefix" \
			'.files[]? | select(((.title // "") | startswith($prefix)) or ((.filename // "") | startswith($prefix))) | (.id | tostring)' \
			"$LIVE_TMP/cleanup.json"
	)

	for ((index = ${#cleanup_ids[@]} - 1; index >= 0; index--)); do
		id="${cleanup_ids[index]}"
		bare_id="${id##*/}"
		if ((share_create_attempted == 1)); then
			# If getLink partially created a link, share delete removes it. It first checks for
			# a link without creating one and reports not_found when there is none. Deleting
			# the parent below supplies the final absence boundary.
			quiet_json share delete --folder "$folder_id" --id "$bare_id" \
				--confirm "$bare_id" >/dev/null 2>&1 || true
		fi
		quiet_json files delete --id "$id" --confirm "$id" || true
	done

	if ! quiet_json files list --folder "$folder_id" --limit 200 ||
		! jq -e --arg id "$file_id" --arg prefix "$marker_prefix" \
			'(.truncated == false) and all(.files[]?; (((.id | tostring) != $id) or $id == "") and (((.title // "") | startswith($prefix)) | not) and (((.filename // "") | startswith($prefix)) | not))' \
			"$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
		return 1
	fi

	file_id=""
	file_folder=""
	file_create_attempted=0
	file_ready=0
	share_create_attempted=0
	return 0
}

cleanup_folder_fixture() {
	local marker_prefix="$FIXTURE_PREFIX-folder"
	local -a cleanup_ids=()
	local id index

	# Only marker-named infostore children of the known Documents folder are eligible.
	# Even a corrupt create response therefore cannot direct cleanup at a built-in folder.
	if ! quiet_json folders list --parent 36; then
		return 1
	fi
	mapfile -t cleanup_ids < <(
		jq -r --arg prefix "$marker_prefix" \
			'.folders[]? | select(.module == "infostore" and ((.title // "") | startswith($prefix))) | (.id | tostring)' \
			"$LIVE_TMP/cleanup.json"
	)
	for ((index = ${#cleanup_ids[@]} - 1; index >= 0; index--)); do
		id="${cleanup_ids[index]}"
		[[ -n "$id" ]] || continue
		quiet_json folders delete --id "$id" --confirm "$id" || true
	done

	if ! quiet_json folders list --parent 36 ||
		! jq -e --arg id "$folder_id" --arg prefix "$marker_prefix" \
			'all(.folders[]?; (((.id | tostring) != $id) or $id == "") and (((.title // "") | startswith($prefix)) | not))' \
			"$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
		return 1
	fi

	folder_id=""
	folder_create_attempted=0
	folder_permissions_json="null"
	folder_ready=0
	return 0
}

cleanup_ensured_mail_folder_fixture() {
	local marker="$FIXTURE_PREFIX-folder-ensured-mail"
	local parent id
	local -a cleanup_ids=()

	# A top-level mail-folder delete may move the folder beneath Trash before a second
	# delete removes it. Discover only this run's exact unique title in both parents.
	for parent in 1 default0/Trash; do
		cleanup_ids=()
		if ! quiet_json folders list --parent "$parent"; then
			return 1
		fi
		mapfile -t cleanup_ids < <(
			jq -r --arg marker "$marker" \
				'.folders[]? | select(.module == "mail" and .title == $marker) | (.id | tostring)' \
				"$LIVE_TMP/cleanup.json"
		)
		for id in "${cleanup_ids[@]}"; do
			[[ -n "$id" ]] || continue
			if ! quiet_json folders get --id "$id" ||
				! jq -e --arg id "$id" --arg marker "$marker" \
					'(.id | tostring) == $id and .module == "mail" and .title == $marker' \
					"$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
				return 1
			fi
			quiet_json folders delete --id "$id" --confirm "$id" || true
		done
	done

	for parent in 1 default0/Trash; do
		if ! quiet_json folders list --parent "$parent" ||
			! jq -e --arg marker "$marker" \
				'all(.folders[]?; .title != $marker)' \
				"$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
			return 1
		fi
	done
	ensured_mail_folder_id=""
	ensured_mail_folder_attempted=0
	return 0
}

cleanup_mailfilter_fixture() {
	local marker_prefix="$FIXTURE_PREFIX-filter"
	local -a cleanup_ids=()
	local -A seen_ids=()
	local id index

	# Read the complete rule collection before every non-idempotent delete retry. Only
	# this run's unique marker or an identity already verified by get is eligible.
	if ! quiet_json mailfilter list; then
		return 1
	fi
	if ((mailfilter_ready == 1)) && [[ -n "$mailfilter_id" ]]; then
		while IFS= read -r id; do
			[[ -n "$id" ]] || continue
			cleanup_ids+=("$id")
			seen_ids["$id"]=1
		done < <(
			jq -r --arg id "$mailfilter_id" \
				'.rules[]? | select((.id | tostring) == $id) | (.id | tostring)' \
				"$LIVE_TMP/cleanup.json"
		)
	fi
	while IFS= read -r id; do
		[[ -n "$id" ]] || continue
		if [[ -z "${seen_ids[$id]:-}" ]]; then
			cleanup_ids+=("$id")
			seen_ids["$id"]=1
		fi
	done < <(
		jq -r --arg prefix "$marker_prefix" \
			'.rules[]? | select((.rulename // "") | startswith($prefix)) | (.id | tostring)' \
			"$LIVE_TMP/cleanup.json"
	)

	for ((index = ${#cleanup_ids[@]} - 1; index >= 0; index--)); do
		id="${cleanup_ids[index]}"
		quiet_json mailfilter delete --id "$id" --confirm "$id" || true
	done

	if ! quiet_json mailfilter list ||
		! jq -e --arg id "$mailfilter_id" --arg prefix "$marker_prefix" \
			--argjson trusted "$mailfilter_ready" \
			'all(.rules[]?; ($trusted == 0 or (.id | tostring) != $id) and (((.rulename // "") | startswith($prefix)) | not))' \
			"$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
		return 1
	fi

	mailfilter_id=""
	mailfilter_create_attempted=0
	mailfilter_ready=0
	return 0
}

cleanup_snippet_fixture() {
	local marker_prefix="$FIXTURE_PREFIX-snippet"
	local -a cleanup_ids=()
	local -A seen_ids=()
	local id index trusted

	# Read the complete snippet collection before retrying deletion. Only an identity
	# already verified after create, or this run's unique display-name marker, is eligible.
	if ! quiet_json snippet list ||
		! jq -e '(.snippets | type) == "array"' "$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
		return 1
	fi
	if ((snippet_ready == 1)) && [[ -n "$snippet_id" ]]; then
		while IFS= read -r id; do
			[[ -n "$id" ]] || continue
			cleanup_ids+=("$id")
			seen_ids["$id"]=1
		done < <(
			jq -r --arg id "$snippet_id" \
				'.snippets[]? | select((.id | tostring) == $id) | (.id | tostring)' \
				"$LIVE_TMP/cleanup.json"
		)
	fi
	while IFS= read -r id; do
		[[ -n "$id" ]] || continue
		if [[ -z "${seen_ids[$id]:-}" ]]; then
			cleanup_ids+=("$id")
			seen_ids["$id"]=1
		fi
	done < <(
		jq -r --arg prefix "$marker_prefix" \
			'.snippets[]? | select((.displayname // "") | startswith($prefix)) | (.id | tostring)' \
			"$LIVE_TMP/cleanup.json"
	)

	for ((index = ${#cleanup_ids[@]} - 1; index >= 0; index--)); do
		id="${cleanup_ids[index]}"
		trusted=0
		if ((snippet_ready == 1)) && [[ "$id" == "$snippet_id" ]]; then
			trusted=1
		fi
		# Re-read the exact target immediately before deletion. A marker-discovered row
		# must still carry the marker; a tracked id is eligible only after create readback.
		if quiet_json snippet get --id "$id" &&
			jq -e --arg id "$id" --arg prefix "$marker_prefix" --argjson trusted "$trusted" \
				'(.id | tostring) == $id and ($trusted == 1 or ((.displayname // "") | startswith($prefix)))' \
				"$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
			quiet_json snippet delete --id "$id" --confirm "$id" || true
		fi
	done

	if ! quiet_json snippet list ||
		! jq -e --arg id "$snippet_id" --arg prefix "$marker_prefix" \
			--argjson trusted "$snippet_ready" \
			'all(.snippets[]?; ($trusted == 0 or (.id | tostring) != $id) and (((.displayname // "") | startswith($prefix)) | not))' \
			"$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
		return 1
	fi

	snippet_id=""
	snippet_create_attempted=0
	snippet_created_by=""
	snippet_ready=0
	return 0
}

cleanup_setting_fixture() {
	((settings_restore_armed == 1)) || return 0
	[[ -n "$settings_key" ]] || return 1

	# Do not overwrite a concurrent value. Restoration is allowed only when the key is
	# still at this run's test value; an already-restored original needs no write.
	if ! quiet_json settings get --path "$SETTINGS_FIXTURE_PATH" --key "$settings_key"; then
		return 1
	fi
	if jq -e --arg path "$SETTINGS_FIXTURE_PATH" --arg key "$settings_key" \
		--argjson original "$settings_original_json" \
		'.path == $path and .key == $key and .value == $original' \
		"$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
		settings_key=""
		settings_original_json="null"
		settings_restore_armed=0
		settings_test_json="null"
		return 0
	fi
	if ! jq -e --arg path "$SETTINGS_FIXTURE_PATH" --arg key "$settings_key" \
		--argjson test_value "$settings_test_json" \
		'.path == $path and .key == $key and .value == $test_value' \
		"$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
		return 1
	fi

	# settings.set is idempotent, but still read above before retrying restoration. Its
	# own post-write read may fail after applying, so the independent read below decides.
	quiet_json settings set --path "$SETTINGS_FIXTURE_PATH" --key "$settings_key" \
		--value "$settings_original_json" || true
	if ! quiet_json settings get --path "$SETTINGS_FIXTURE_PATH" --key "$settings_key" ||
		! jq -e --arg path "$SETTINGS_FIXTURE_PATH" --arg key "$settings_key" \
			--argjson original "$settings_original_json" \
			'.path == $path and .key == $key and .value == $original' \
			"$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
		return 1
	fi

	settings_key=""
	settings_original_json="null"
	settings_restore_armed=0
	settings_test_json="null"
	return 0
}

cleanup_mail() {
	local folder="$1"
	local id="$2"
	local subject=""
	local trash_id=""
	[[ -n "$id" ]] || return 0

	# A tracked IMAP id may become stale after copy/move. Resolve it against a complete
	# folder listing and refuse to mutate unless the current object still carries this
	# run's unique subject marker.
	if ! quiet_json mail list --folder "$folder" --limit 200 ||
		! jq -e '.truncated == false' "$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
		return 1
	fi
	subject="$(jq -r --arg id "$id" \
		'.items[]? | select((.id | tostring) == $id) | .subject' \
		"$LIVE_TMP/cleanup.json" | head -n 1)"
	[[ -n "$subject" ]] || return 0
	[[ "$subject" == "$FIXTURE_PREFIX"* ]] || return 0

	if ! quiet_json mail get --folder "$folder" --id "$id" ||
		! jq -e --arg id "$id" --arg folder "$folder" --arg subject "$subject" \
			--arg prefix "$FIXTURE_PREFIX" \
			'.id == $id and .folder == $folder and .subject == $subject and (.subject | startswith($prefix))' \
			"$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
		return 1
	fi

	if [[ "$folder" == "default0/Trash" ]]; then
		# Execute the non-idempotent delete once. Its independently observed absence, not
		# whether the response arrived, decides cleanup success.
		quiet_json mail delete --folder "$folder" --id "$id" --confirm "$id" || true
		if ! quiet_json mail list --folder "$folder" --limit 200 ||
			! jq -e --arg id "$id" \
				'.truncated == false and all(.items[]?; (.id | tostring) != $id)' \
				"$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
			return 1
		fi
		return 0
	fi

	if quiet_json mail move --folder "$folder" --id "$id" --to default0/Trash; then
		trash_id="$(jq -r '.destination.id // .id // empty' "$LIVE_TMP/cleanup.json")"
	fi
	if ! quiet_json mail list --folder "$folder" --limit 200 ||
		! jq -e --arg id "$id" --arg subject "$subject" \
			'.truncated == false and all(.items[]?; ((.id | tostring) != $id) and .subject != $subject)' \
			"$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
		return 1
	fi
	# If the move response was lost, the marker sweep below discovers the Trash id.
	[[ -n "$trash_id" ]] || return 0
	cleanup_mail default0/Trash "$trash_id"
}

cleanup_fixtures() {
	((CLEANUP_DONE == 0)) || return 0
	CLEANUP_DONE=1

	if ((password_changed == 1)); then
		export MBX_PASSWORD="$temporary_password"
		if MBX_NEW_PASSWORD="$ORIGINAL_PASSWORD" quiet_json change-password \
			--confirm "$MBX_USERNAME"; then
			export MBX_PASSWORD="$ORIGINAL_PASSWORD"
			password_changed=0
		else
			printf 'WARNING password rollback failed; retry immediately with the temporary password retained in this process\n' >&2
			((CLEANUP_FAILURES++))
		fi
	fi

	if ((settings_restore_armed == 1)); then
		if ! cleanup_setting_fixture; then
			printf 'WARNING failed to verify restoration of the original setting value\n' >&2
			((CLEANUP_FAILURES++))
		fi
	fi

	if ((user_changed == 1)) && [[ -n "$original_display_name" ]]; then
		quiet_json user update --display-name "$original_display_name" || true
		if quiet_json whoami &&
			jq -e --arg expected "$original_display_name" \
				'.display_name == $expected' "$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
			user_changed=0
		else
			printf 'WARNING failed to verify restoration of the original display name\n' >&2
			((CLEANUP_FAILURES++))
		fi
	fi

	if [[ -n "$attachment_id" && -n "$contact_id" ]]; then
		quiet_json attachments delete --module 7 --object-id "$contact_id" --folder 33 \
			--id "$attachment_id" --confirm "$attachment_id" || true
		if quiet_json attachments list --module 7 --object-id "$contact_id" --folder 33 &&
			jq -e --arg id "$attachment_id" \
				'all(.attachments[]?; (.id | tostring) != $id)' \
				"$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
			attachment_id=""
		else
			printf 'WARNING failed to verify contact-attachment fixture cleanup\n' >&2
			((CLEANUP_FAILURES++))
		fi
	fi

	if ((contact_import_attempted == 1)) || ((${#imported_contact_ids[@]} > 0)); then
		if ! cleanup_imported_contacts; then
			printf 'WARNING failed to verify imported-contact fixture cleanup\n' >&2
			((CLEANUP_FAILURES++))
		fi
	fi

	if [[ -n "$contact_id" && -z "$attachment_id" ]]; then
		contact_timestamp=""
		if quiet_json contacts get --id "$contact_id" --folder 33; then
			contact_timestamp="$(jq -r '.last_modified // empty' "$LIVE_TMP/cleanup.json")"
		fi
		if [[ -n "$contact_timestamp" ]]; then
			quiet_json contacts delete --id "$contact_id" --folder 33 \
				--timestamp "$contact_timestamp" --confirm "$contact_id" || true
		fi
		if quiet_json contacts list --limit 500 &&
			jq -e --arg id "$contact_id" --arg prefix "$FIXTURE_PREFIX" \
				'(.truncated == false) and all(.contacts[]?; ((.id | tostring) != $id) and (((.display_name // "") | startswith($prefix)) | not))' \
				"$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
			contact_id=""
		else
			printf 'WARNING failed to verify contact fixture cleanup\n' >&2
			((CLEANUP_FAILURES++))
		fi
	fi

	if ((event_import_attempted == 1)) || ((${#imported_event_ids[@]} > 0)); then
		if ! cleanup_imported_events; then
			printf 'WARNING failed to verify imported-event fixture cleanup\n' >&2
			((CLEANUP_FAILURES++))
		fi
	fi

	if ((event_create_attempted == 1)) || [[ -n "$event_id" ]]; then
		if ! cleanup_standard_event; then
			printf 'WARNING failed to verify calendar-event fixture cleanup\n' >&2
			((CLEANUP_FAILURES++))
		fi
	fi

	if ((reminder_create_attempted == 1)) || [[ -n "$reminder_id" || -n "$reminder_task_id" ]]; then
		if ! cleanup_reminder_fixture; then
			printf 'WARNING failed to verify reminder/backing-task fixture cleanup\n' >&2
			((CLEANUP_FAILURES++))
		fi
	fi

	if ((task_create_attempted == 1)) || [[ -n "$task_id" ]]; then
		if ! cleanup_standard_task; then
			printf 'WARNING failed to verify standard-task fixture cleanup\n' >&2
			((CLEANUP_FAILURES++))
		fi
	fi

	if ((file_create_attempted == 1 || share_create_attempted == 1)) || [[ -n "$file_id" ]]; then
		if ! cleanup_file_fixture; then
			printf 'WARNING failed to verify file/share fixture cleanup\n' >&2
			((CLEANUP_FAILURES++))
		fi
	fi

	if ((folder_create_attempted == 1)) || [[ -n "$folder_id" ]]; then
		if ! cleanup_folder_fixture; then
			printf 'WARNING failed to verify infostore-folder fixture cleanup\n' >&2
			((CLEANUP_FAILURES++))
		fi
	fi
	if ((ensured_mail_folder_attempted == 1)) || [[ -n "$ensured_mail_folder_id" ]]; then
		if ! cleanup_ensured_mail_folder_fixture; then
			printf 'WARNING failed to verify ensured mail-folder fixture cleanup\n' >&2
			((CLEANUP_FAILURES++))
		fi
	fi
	if [[ -n "$group_id" ]]; then
		quiet_json group delete --id "$group_id" --confirm "$group_id" || true
		if quiet_json group list --pattern '*' &&
			jq -e --arg id "$group_id" --arg prefix "$FIXTURE_PREFIX" \
				'all(.groups[]?; ((.id | tostring) != $id) and (.display_name | startswith($prefix) | not))' \
				"$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
			group_id=""
		else
			printf 'WARNING failed to verify group fixture cleanup\n' >&2
			((CLEANUP_FAILURES++))
		fi
	fi
	if ((mailfilter_create_attempted == 1)) || [[ -n "$mailfilter_id" ]]; then
		if ! cleanup_mailfilter_fixture; then
			printf 'WARNING failed to verify mail-filter fixture cleanup\n' >&2
			((CLEANUP_FAILURES++))
		fi
	fi
	if [[ -n "$resource_id" ]]; then
		quiet_json resources delete --id "$resource_id" --confirm "$resource_id" || true
		if quiet_json resources search --pattern "$FIXTURE_PREFIX-resource" &&
			jq -e --arg id "$resource_id" --arg prefix "$FIXTURE_PREFIX" \
				'all(.resources[]?; ((.id | tostring) != $id) and (.display_name | startswith($prefix) | not))' \
				"$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
			resource_id=""
		else
			printf 'WARNING failed to verify resource fixture cleanup\n' >&2
			((CLEANUP_FAILURES++))
		fi
	fi
	if ((snippet_create_attempted == 1)) || [[ -n "$snippet_id" ]]; then
		if ! cleanup_snippet_fixture; then
			printf 'WARNING failed to verify snippet fixture cleanup\n' >&2
			((CLEANUP_FAILURES++))
		fi
	fi
	if [[ -n "$account_id" ]]; then
		quiet_json account delete --id "$account_id" --confirm "$account_id" || true
		if quiet_json account list &&
			jq -e --arg id "$account_id" --arg prefix "$FIXTURE_PREFIX" \
				'all(.accounts[]?; ((.id | tostring) != $id) and (.name | startswith($prefix) | not))' \
				"$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
			account_id=""
		else
			printf 'WARNING failed to verify external-account fixture cleanup\n' >&2
			((CLEANUP_FAILURES++))
		fi
	fi

	# Try verified tracked identities in reverse creation/dependency order. cleanup_mail
	# treats an absent or reused stale id as non-authoritative and never mutates it.
	for mail_ref in \
		"default0/INBOX|${inbox_mail_id}" \
		"default0/Sent|${sent_mail_id}" \
		"default0/Trash|${moved_mail_id}" \
		"default0/Trash|${copied_mail_id}" \
		"${draft_folder}|${draft_id}"; do
		local mail_folder="${mail_ref%%|*}"
		local mail_id="${mail_ref#*|}"
		[[ -n "$mail_id" ]] || continue
		cleanup_mail "$mail_folder" "$mail_id" || true
	done

	# A move/copy can assign a new IMAP id. Sweep only this run's exact subject prefix so
	# a changed id can never leave a fixture behind.
	local mail_cleanup_verified=1
	for mail_folder in default0/INBOX default0/Sent default0/Drafts default0/Trash; do
		local -a marked_ids=()
		if quiet_json mail list --folder "$mail_folder" --limit 200 &&
			jq -e '.truncated == false' "$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
			mapfile -t marked_ids < <(
				jq -r --arg prefix "$FIXTURE_PREFIX" \
					'.items[]? | select(.subject | startswith($prefix)) | .id' \
					"$LIVE_TMP/cleanup.json"
			)
		else
			mail_cleanup_verified=0
		fi
		for marked_id in "${marked_ids[@]}"; do
			cleanup_mail "$mail_folder" "$marked_id" || mail_cleanup_verified=0
		done
	done

	# Cleanup is complete only after every fixture-bearing folder can be listed fully and
	# contains zero subjects from this run. Keep tracked ids until this proof succeeds.
	for mail_folder in default0/INBOX default0/Sent default0/Drafts default0/Trash; do
		if ! quiet_json mail list --folder "$mail_folder" --limit 200 ||
			! jq -e --arg prefix "$FIXTURE_PREFIX" \
				'.truncated == false and all(.items[]?; ((.subject | startswith($prefix)) | not))' \
				"$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
			mail_cleanup_verified=0
		fi
	done
	if ((mail_cleanup_verified == 1)); then
		draft_id=""
		copied_mail_id=""
		moved_mail_id=""
		sent_mail_id=""
		inbox_mail_id=""
	else
		printf 'WARNING failed to verify mail fixture cleanup\n' >&2
		((CLEANUP_FAILURES++))
	fi
}

on_exit() {
	cleanup_fixtures
	# A signal can bypass the normal verified lifecycle below. Once fixture cleanup has
	# finished, make one best-effort idempotent logout before deleting the private store.
	if [[ -x "$MBX_BIN" ]] && [[ -e "$LIVE_SESSION_PATH" || -L "$LIVE_SESSION_PATH" ]]; then
		quiet_json logout || true
	fi
	case "$LIVE_TMP" in
	/tmp/mbx-live-test.*) rm -rf -- "$LIVE_TMP" ;;
	esac
}
trap on_exit EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

cd "$REPO_ROOT"
printf 'Building mbx for live %s matrix...\n' "$MODE"
if ! go build -o "$MBX_BIN" ./cmd/mbx; then
	printf 'build failed\n' >&2
	exit 1
fi
printf 'Live CLI request interval: %s second(s).\n' "$REQUEST_INTERVAL"

mapfile -t REGISTERED_RESOURCES < <("$MBX_BIN" commands --format values --select name)
REGISTERED_OPERATIONS=()
for registered_resource in "${REGISTERED_RESOURCES[@]}"; do
	mapfile -t RESOURCE_OPERATIONS < <(
		"$MBX_BIN" commands "$registered_resource" --format values --select name
	)
	REGISTERED_OPERATIONS+=("${RESOURCE_OPERATIONS[@]}")
done

# Authentication and all non-mutating operations that do not require a fixture.
initial_login_ready=0
if run_op login login; then
	if assert_last_jq login "fresh-login output did not match the configured account" \
		--arg username "$MBX_USERNAME" \
		'type == "object" and length == 2 and .status == "ok" and .username == $username' &&
		assert_session_present_for login "fresh login did not create a non-empty isolated session file"; then
		initial_login_ready=1
	fi
fi
if ((initial_login_ready == 1)) && run_op whoami whoami; then
	if assert_last_jq whoami "missing authenticated-user fields" \
		--arg username "$MBX_USERNAME" \
		'type == "object" and length == 3
		and (.email | type) == "string"
		and (.email | ascii_downcase) == ($username | ascii_downcase)
		and (.display_name | type) == "string"
		and (.user_id | type) == "number" and (.user_id | floor) == .user_id and .user_id > 0'; then
		authenticated_email="$(jq -r '.email' "$LAST_STDOUT")"
		authenticated_user_id="$(jq -r '.user_id | tostring' "$LAST_STDOUT")"
		original_display_name="$(jq -r '.display_name // empty' "$LAST_STDOUT")"
	fi
fi
if [[ -z "$authenticated_email" || -z "$authenticated_user_id" ]]; then
	mark_fail login "authenticated identity was unavailable for the stored-session proof"
	# No fixture operation is safe or useful without a verified account identity. Stop
	# before read/write fan-out also turns one authentication failure into provider
	# throttling. Nothing has been created yet, so suppress fixture cleanup; on_exit still
	# performs a best-effort logout when a session file exists.
	CLEANUP_DONE=1
	printf 'Authentication precondition failed; aborting before operation and fixture requests.\n' >&2
	exit 1
elif capture_session_reuse_for login "a new process could not reuse the persisted session" whoami; then
	assert_captured_jq login "persisted-session identity differed from the fresh-login identity" \
		--arg email "$authenticated_email" --arg user_id "$authenticated_user_id" \
		'type == "object" and length == 3 and .email == $email
		and (.user_id | tostring) == $user_id and (.display_name | type) == "string"'
	assert_session_present_for login "persisted-session reuse did not leave a usable isolated session file"
fi
if run_op quota.get quota get --type filestore; then
	if assert_last_jq quota.get "filestore quota fields were not non-negative integers" \
		'(.quota | type) == "number" and (.quota | floor) == .quota and .quota >= 0 and (.use | type) == "number" and (.use | floor) == .use and .use >= 0'; then
		filestore_quota="$(jq -r '.quota | tostring' "$LAST_STDOUT")"
		filestore_use="$(jq -r '.use | tostring' "$LAST_STDOUT")"
	fi
fi
run_op capabilities.list capabilities list

if run_op account.list account list; then
	primary_account_id="$(jq -r '.accounts[] | select(.id == 0) | .id' "$LAST_STDOUT" | head -n 1)"
	if [[ -n "$primary_account_id" ]]; then
		run_op account.get account get --id "$primary_account_id"
	fi
fi

run_op folders.list folders list
run_op folders.get folders get --id 32

# A preflight is authenticated and provider-aware, but must leave no folder behind. The
# mail tree's public root id (1) normalizes to default0 in the exact request plan.
folder_preflight_title="$FIXTURE_PREFIX-folder-preflight"
if capture_json_for folders.create "could not preflight a normalized top-level mail folder" \
	folders create --parent 1 --module mail --title "$folder_preflight_title" --preflight &&
	assert_captured_jq folders.create "folder preflight did not expose the normalized provider request" \
		--arg title "$folder_preflight_title" \
		'.preflight == true and .valid == true and .requested_parent == "1"
		and .parent == "default0" and .title == $title and .module == "mail"
		and .request.method == "PUT" and .request.endpoint == "/folders"
		and .request.action == "new" and .request.params.folder_id == "default0"
		and .request.body.folder.title == $title and .request.body.folder.module == "mail"'; then
	if capture_json_for folders.create "could not verify that folder preflight was read-only" \
		folders list --parent 1; then
		assert_captured_jq folders.create "folder preflight unexpectedly created its marker" \
			--arg title "$folder_preflight_title" \
			'all(.folders[]?; .title != $title)'
	fi
fi

if run_op contacts.list contacts list --limit 50; then
	safe_contact_id="$(jq -r '.contacts[0].id // empty' "$LAST_STDOUT")"
	if [[ -n "$safe_contact_id" ]]; then
		run_op contacts.get contacts get --id "$safe_contact_id"
		run_op attachments.list attachments list --module 7 --object-id "$safe_contact_id" --folder 6
	fi
fi

run_op events.list events list --from 2020-01-01T00:00:00Z --to 2035-01-01T00:00:00Z --limit 100
run_op files.list files list --folder 36 --limit 100

if run_op group.list group list --pattern '*'; then
	safe_group_id="$(jq -r '.groups[0].id // empty' "$LAST_STDOUT")"
	if [[ -n "$safe_group_id" ]]; then
		run_op group.get group get --id "$safe_group_id"
	fi
fi

if run_op mail.list mail list --folder default0/INBOX --limit 50 \
	--fields folder,to,cc,size,sent_date,received_date,attachment,seen,flagged; then
	assert_last_jq mail.list "mail list pagination metadata or requested safe fields were invalid" \
		'.count == (.items | length) and .offset == 0 and .limit == 50
		and (.has_more | type) == "boolean" and .truncated == .has_more
		and (if .has_more then (.next_offset | type) == "number"
			and (.next_cursor | type) == "string" and (.next_cursor | startswith("offset:"))
		else (has("next_offset") | not) and (has("next_cursor") | not) end)
		and all(.items[]?;
			(.id | type) == "string" and .folder == "default0/INBOX"
			and (.subject | type) == "string" and (.from | type) == "string"
			and (.date | type) == "string" and .received_date == .date
			and (.size | type) == "number" and .size >= 0
			and (.attachment | type) == "boolean"
			and (.seen | type) == "boolean" and (.flagged | type) == "boolean"
			and ((has("sent_date") | not) or (.sent_date | type) == "string"))'
	safe_mail_id="$(jq -r '.items[0].id // empty' "$LAST_STDOUT")"
	safe_attachment_mail_id="$(jq -r '[.items[]? | select(.attachment == true)][0].id // empty' "$LAST_STDOUT")"
	safe_search_term="$(jq -r '[.items[]?.subject | splits("[^A-Za-z0-9]+") | select(length >= 4)][0] // empty' "$LAST_STDOUT")"
	if [[ -n "$safe_mail_id" ]]; then
		if run_op mail.get mail get --folder default0/INBOX --id "$safe_mail_id"; then
			safe_mail_seen="$(jq -r '.seen' "$LAST_STDOUT")"
			# Message text stays in the private temporary output; only shape and identity are asserted.
			if run_op mail.read mail read --folder default0/INBOX --id "$safe_mail_id" --max-body-bytes 1024; then
				assert_last_jq mail.read "mail read identity or body shape did not match" --arg id "$safe_mail_id" \
					'.id == $id and .folder == "default0/INBOX" and (.subject | type) == "string"
					and (.body | type) == "string" and (.body | utf8bytelength) <= 1024
					and (.body_bytes | type) == "number" and .body_truncated == (.body_bytes > 1024)
					and (.body_format == "text") and (.attachments | type) == "array"
					and all(.attachments[]; (.id | type) == "string" and (.content_type | type) == "string")
					and (.nested_messages | type) == "array"'
				if capture_json_for mail.read "mail read changed or hid the seen flag" \
					mail get --folder default0/INBOX --id "$safe_mail_id"; then
					assert_captured_jq mail.read "mail read without --mark-seen changed the seen flag" \
						--arg seen "$safe_mail_seen" '(.seen | tostring) == $seen'
				fi
			fi
		fi
	else
		mark_skipped mail.read "INBOX has no message to read"
	fi
	if [[ -n "$safe_attachment_mail_id" ]] &&
		run_op mail.read mail read --folder default0/INBOX --id "$safe_attachment_mail_id" --max-body-bytes 1024; then
		safe_attachment_id="$(jq -r '[.attachments[] | select(.disposition == "attachment")][0].id // .attachments[0].id // empty' "$LAST_STDOUT")"
		if [[ -n "$safe_attachment_id" ]]; then
			safe_attachment_file="$LIVE_TMP/mail-attachment.bin"
			if run_op mail.attachment mail attachment --folder default0/INBOX --id "$safe_attachment_mail_id" \
				--attachment "$safe_attachment_id" --save-to "$safe_attachment_file"; then
				safe_attachment_bytes="$(wc -c <"$safe_attachment_file" | tr -d ' ')"
				assert_last_jq mail.attachment "mail attachment output did not match the saved file" \
					--arg id "$safe_attachment_mail_id" --arg attachment "$safe_attachment_id" \
					--arg path "$safe_attachment_file" --argjson bytes "$safe_attachment_bytes" \
					'.id == $id and .attachment == $attachment and .saved_to == $path
					and .bytes_written == $bytes and (.filename | type) == "string"'
			fi
			rm -f -- "$safe_attachment_file"
		else
			mark_skipped mail.attachment "the attachment flag was set but mail read listed no attachment"
		fi
	elif [[ -z "$safe_attachment_mail_id" ]]; then
		mark_skipped mail.attachment "no message with an attachment in the first INBOX page"
	fi
	if [[ -n "$safe_search_term" ]]; then
		if run_op mail.search mail search --folder default0/INBOX --query "$safe_search_term" --in subject --limit 20; then
			assert_last_jq mail.search "subject search returned a message without the term" \
				--arg term "$safe_search_term" \
				'.count >= 1 and .count == (.items | length)
				and all(.items[]; (.subject | ascii_downcase) | contains($term | ascii_downcase))'
		fi
		# Body search proves the provider accepts the content field; matches are not inspected.
		if run_op mail.search mail search --folder default0/INBOX --query "$safe_search_term" --in body --limit 5; then
			assert_last_jq mail.search "body search output shape was invalid" \
				'.count == (.items | length) and all(.items[]; (.id | type) == "string")'
		fi
	else
		mark_skipped mail.search "no ASCII subject word of four or more characters in the first INBOX page"
	fi
fi

run_op mailfilter.list mailfilter list
run_op reminder.list reminder list --from 2020-01-01T00:00:00Z --to 2035-01-01T00:00:00Z --limit 100
run_op resources.search resources search --pattern '*'
if run_op settings.get settings get --path "$SETTINGS_FIXTURE_PATH"; then
	assert_last_jq settings.get "settings tree identity or shape did not match" \
		--arg path "$SETTINGS_FIXTURE_PATH" '.path == $path and (.tree | type) == "object"'
fi
run_op snippet.list snippet list

if run_op tasks.list tasks list --folder 35 --limit 100; then
	safe_task_id="$(jq -r '.tasks[0].id // empty' "$LAST_STDOUT")"
	if [[ -n "$safe_task_id" ]]; then
		run_op tasks.get tasks get --id "$safe_task_id" --folder 35
	fi
fi

readonly BATCH_INPUT='[{"operation":"quota.get","params":{"type":"filestore"},"select":["quota","use"]},{"operation":"whoami","select":["email","user_id"]}]'
if run_op batch.run batch run --input "$BATCH_INPUT"; then
	if [[ -z "$authenticated_email" || -z "$authenticated_user_id" ||
		-z "$filestore_quota" || -z "$filestore_use" ]]; then
		mark_fail batch.run "standalone quota/user evidence was unavailable"
	else
		assert_last_jq batch.run "batch sub-results did not match the standalone quota/user reads" \
			--arg email "$authenticated_email" --arg user_id "$authenticated_user_id" \
			--arg quota "$filestore_quota" --arg use "$filestore_use" \
			'type == "object" and keys == ["results"]
			and (.results | type) == "array" and (.results | length) == 2
			and .results[0].operation == "quota.get"
			and (.results[0] | keys) == ["operation", "result"]
			and (.results[0].result | keys) == ["quota", "use"]
			and (.results[0].result.quota | type) == "number"
			and (.results[0].result.quota | tostring) == $quota
			and (.results[0].result.use | type) == "number"
			and (.results[0].result.use | tostring) == $use
			and .results[1].operation == "whoami"
			and (.results[1] | keys) == ["operation", "result"]
			and (.results[1].result | keys) == ["email", "user_id"]
			and .results[1].result.email == $email
			and (.results[1].result.user_id | type) == "number"
			and (.results[1].result.user_id | tostring) == $user_id'
	fi
fi

# Exercise the mail adapter separately when the account has an Inbox message. Its
# projection is intentionally tiny; the provider's raw body and headers must be
# impossible to observe in the result even though the historical batch path exposed them.
if [[ -n "$safe_mail_id" ]]; then
	batch_mail_input="$(jq -cn --arg id "$safe_mail_id" \
		'[{operation:"mail.get",params:{folder:"default0/INBOX",id:$id},select:["id","folder","subject","fingerprint"]}]')"
	if capture_json_for batch.run "could not execute the safe typed mail batch adapter" \
		batch run --input "$batch_mail_input"; then
		assert_captured_jq batch.run "batched mail metadata was not tightly projected" \
			--arg id "$safe_mail_id" \
			'.results | length == 1
			and .[0].operation == "mail.get"
			and (.[0] | keys) == ["operation", "result"]
			and (.[0].result | keys) == ["fingerprint", "folder", "id", "subject"]
			and .[0].result.id == $id and .[0].result.folder == "default0/INBOX"
			and (.[0].result.subject | type) == "string"
			and (.[0].result.fingerprint | test("^sha256:[0-9a-f]{64}$"))
			and (.[0].result | has("body") | not)
			and (.[0].result | has("headers") | not)'
	fi
fi

if [[ "$MODE" == "full" ]]; then
	# Secondary account. mailbox.org plus-addressing gives a distinct account address
	# while the same dedicated mailbox credentials remain valid for IMAP.
	account_address="${MBX_USERNAME%@*}+mbx-live-${RUN_STAMP//[^0-9]/}@${MBX_USERNAME#*@}"
	# --password-env keeps the password out of the process list.
	export MBX_ACCOUNT_PASSWORD="$MBX_PASSWORD"
	account_create_ok=0
	if run_op account.create account create \
		--login "$MBX_USERNAME" \
		--password-env \
		--mail-url "${MBX_LIVE_IMAP_URL:-imap://imap.mailbox.org:143}" \
		--primary-address "$account_address" \
		--name "$FIXTURE_PREFIX-account"; then
		account_create_ok=1
	fi
	unset MBX_ACCOUNT_PASSWORD
	if ((account_create_ok == 1)); then
		account_id="$(jq -r '.id // empty' "$LAST_STDOUT")"
		if [[ -n "$account_id" ]]; then
			if capture_json_for account.create "could not read created account" \
				account get --id "$account_id"; then
				assert_captured_jq account.create "created account fields did not match" \
					--arg id "$account_id" \
					--arg name "$FIXTURE_PREFIX-account" \
					--arg login "$MBX_USERNAME" \
					--arg address "$account_address" \
					'(.id | tostring) == $id and .name == $name and .login == $login and .primary_address == $address'
			fi
			if run_op account.update account update --id "$account_id" \
				--name "$FIXTURE_PREFIX-account-updated"; then
				if capture_json_for account.update "could not read updated account" \
					account get --id "$account_id"; then
					assert_captured_jq account.update "updated account fields did not match" \
						--arg id "$account_id" \
						--arg name "$FIXTURE_PREFIX-account-updated" \
						--arg login "$MBX_USERNAME" \
						--arg address "$account_address" \
						'(.id | tostring) == $id and .name == $name and .login == $login and .primary_address == $address'
				fi
			fi
			if run_op account.delete account delete --id "$account_id" --confirm "$account_id"; then
				if capture_json_for account.delete "could not list accounts after deletion" account list &&
					assert_captured_jq account.delete "deleted account or marker was still listed" \
						--arg id "$account_id" --arg prefix "$FIXTURE_PREFIX" \
						'all(.accounts[]?; ((.id | tostring) != $id) and (.name | startswith($prefix) | not))'; then
					account_id=""
				fi
			fi
		fi
	fi

	# Exercise the create path of idempotent ensure at the virtual mail root. The provider
	# request uses default0, while read-back correctly reports virtual parent 1.
	ensured_mail_folder_attempted=1
	ensured_mail_folder_title="$FIXTURE_PREFIX-folder-ensured-mail"
	if run_op folders.ensure folders ensure --parent 1 --module mail \
		--title "$ensured_mail_folder_title"; then
		ensured_mail_folder_id="$(jq -r '.id // empty' "$LAST_STDOUT")"
		if assert_last_jq folders.ensure "top-level ensure did not create and verify one folder" \
			--arg title "$ensured_mail_folder_title" \
			'.created == true and (.id | type) == "string" and (.id | length) > 0
			and .requested_parent == "1" and .parent == "default0"
			and .title == $title and .module == "mail"' &&
			capture_json_for folders.ensure "could not read the newly ensured mail folder" \
				folders get --id "$ensured_mail_folder_id" &&
			assert_captured_jq folders.ensure "ensured mail-folder read-back did not use the virtual root" \
				--arg id "$ensured_mail_folder_id" --arg title "$ensured_mail_folder_title" \
				'(.id | tostring) == $id and .title == $title and .module == "mail"
				and (.parent_id | tostring) == "1"' &&
			capture_json_for folders.ensure "could not repeat top-level folder ensure" \
				folders ensure --parent 1 --module mail --title "$ensured_mail_folder_title" &&
			assert_captured_jq folders.ensure "repeated top-level ensure created a duplicate" \
				--arg id "$ensured_mail_folder_id" --arg title "$ensured_mail_folder_title" \
				'(.id | tostring) == $id and .created == false
				and .requested_parent == "1" and .parent == "default0"
				and .title == $title and .module == "mail"'; then
			if ! cleanup_ensured_mail_folder_fixture; then
				mark_fail folders.ensure "could not remove and verify the ensured mail-folder fixture"
			fi
		fi
	fi

	# Folder lifecycle in the private infostore tree.
	folder_create_attempted=1
	if run_op folders.create folders create --parent 36 --module infostore \
		--title "$FIXTURE_PREFIX-folder"; then
		folder_id="$(jq -r '.id // empty' "$LAST_STDOUT")"
		if assert_last_jq folders.create "create response did not contain a non-empty folder id" \
			'.id | type == "string" and length > 0' &&
			capture_json_for folders.create "could not read created folder" \
				folders get --id "$folder_id" &&
			assert_captured_jq folders.create "created folder fields did not match" \
				--arg id "$folder_id" --arg title "$FIXTURE_PREFIX-folder" \
				'(.id | tostring) == $id and (.parent_id | tostring) == "36" and .module == "infostore" and .title == $title and .has_subfolders == false and .last_modified > 0 and ((.permissions == null) or ((.permissions | type) == "array"))'; then
			folder_permissions_json="$(jq -c '.permissions' "$ASSERTION_STDOUT")"
			folder_ready=1
		fi
		if ((folder_ready == 1)); then
			# Ensuring the exact child after creation must reuse its identity without a
			# second write. A second ensure proves the stable idempotent result again.
			if run_op folders.ensure folders ensure --parent 36 --module infostore \
				--title "$FIXTURE_PREFIX-folder"; then
				assert_last_jq folders.ensure "ensure did not reuse the exact existing folder" \
					--arg id "$folder_id" --arg title "$FIXTURE_PREFIX-folder" \
					'(.id | tostring) == $id and .created == false
					and .requested_parent == "36" and .parent == "36"
					and .title == $title and .module == "infostore"
					and (has("would_create") | not)'
				if capture_json_for folders.ensure "could not repeat the idempotent folder ensure" \
					folders ensure --parent 36 --module infostore --title "$FIXTURE_PREFIX-folder"; then
					assert_captured_jq folders.ensure "repeated ensure changed identity or created a duplicate" \
						--arg id "$folder_id" --arg title "$FIXTURE_PREFIX-folder" \
						'(.id | tostring) == $id and .created == false
						and .requested_parent == "36" and .parent == "36"
						and .title == $title and .module == "infostore"'
				fi
			fi
			if run_op folders.update folders update --id "$folder_id" \
				--title "$FIXTURE_PREFIX-folder-updated"; then
				assert_last_jq folders.update "update response did not preserve the requested folder identity" \
					--arg id "$folder_id" '(.id | tostring) == $id'
				if capture_json_for folders.update "could not read updated folder" \
					folders get --id "$folder_id"; then
					assert_captured_jq folders.update "updated folder fields did not match" \
						--arg id "$folder_id" --arg title "$FIXTURE_PREFIX-folder-updated" \
						--argjson permissions "$folder_permissions_json" \
						'(.id | tostring) == $id and (.parent_id | tostring) == "36" and .module == "infostore" and .title == $title and .has_subfolders == false and .last_modified > 0 and .permissions == $permissions'
				fi
			fi
		fi
	fi

	# Contact lifecycle plus every attachment operation.
	if run_op contacts.create contacts create --folder 33 \
		--field "display_name=$FIXTURE_PREFIX-contact" \
		--field "email1=$FIXTURE_PREFIX@example.test"; then
		contact_id="$(jq -r '.id // empty' "$LAST_STDOUT")"
		contact_timestamp="$(jq -r '.timestamp // empty' "$LAST_STDOUT")"
		assert_last_jq contacts.create "create response identity/folder/timestamp was incomplete" \
			--arg id "$contact_id" \
			'(.id | tostring) == $id and (.folder_id | tostring) == "33" and .timestamp > 0'
		if [[ -n "$contact_id" ]]; then
			if run_op contacts.get contacts get --id "$contact_id" --folder 33; then
				contact_timestamp="$(jq -r '.last_modified // empty' "$LAST_STDOUT")"
				assert_last_jq contacts.get "created contact read-back fields did not match" \
					--arg id "$contact_id" --arg display "$FIXTURE_PREFIX-contact" \
					--arg email "$FIXTURE_PREFIX@example.test" \
					'(.id | tostring) == $id and (.folder_id | tostring) == "33" and .display_name == $display and .email == $email and .last_modified > 0'
				assert_last_jq contacts.create "created contact fields did not match" \
					--arg id "$contact_id" --arg display "$FIXTURE_PREFIX-contact" \
					--arg email "$FIXTURE_PREFIX@example.test" \
					'(.id | tostring) == $id and (.folder_id | tostring) == "33" and .display_name == $display and .email == $email and .last_modified > 0'
			else
				mark_fail contacts.create "could not read the created contact"
			fi
			if [[ -n "$contact_timestamp" ]]; then
				if run_op contacts.update contacts update --id "$contact_id" --folder 33 \
					--timestamp "$contact_timestamp" \
					--field "display_name=$FIXTURE_PREFIX-contact-updated"; then
					contact_timestamp="$(jq -r '.timestamp // empty' "$LAST_STDOUT")"
					assert_last_jq contacts.update "update response identity/folder/timestamp was incomplete" \
						--arg id "$contact_id" --arg display "$FIXTURE_PREFIX-contact-updated" \
						'(.id | tostring) == $id and (.folder_id | tostring) == "33" and .timestamp > 0 and .updated_fields.display_name == $display'
					if capture_json_for contacts.update "could not read updated contact" \
						contacts get --id "$contact_id" --folder 33; then
						assert_captured_jq contacts.update "updated contact fields did not match" \
							--arg id "$contact_id" --arg display "$FIXTURE_PREFIX-contact-updated" \
							--arg email "$FIXTURE_PREFIX@example.test" \
							'(.id | tostring) == $id and (.folder_id | tostring) == "33" and .display_name == $display and .email == $email and .last_modified > 0'
						contact_timestamp="$(jq -r '.last_modified // empty' "$ASSERTION_STDOUT")"
					fi
				fi
			fi

			attachment_filename="$FIXTURE_PREFIX-attachment.txt"
			attachment_path="$LIVE_TMP/$attachment_filename"
			printf 'attachment fixture %s\n' "$RUN_STAMP" >"$attachment_path"
			if run_op attachments.create attachments create --module 7 \
				--object-id "$contact_id" --folder 33 --file "$attachment_path"; then
				attachment_id="$(jq -r '.id // empty' "$LAST_STDOUT")"
				if run_op attachments.list attachments list --module 7 \
					--object-id "$contact_id" --folder 33; then
					assert_last_jq attachments.list "created attachment metadata was not listed" \
						--arg id "$attachment_id" --arg filename "$attachment_filename" \
						'[.attachments[]? | select((.id | tostring) == $id and .filename == $filename and .file_size > 0 and .file_mimetype != "")] | length == 1'
					assert_last_jq attachments.create "created attachment metadata was not listed exactly once" \
						--arg id "$attachment_id" --arg filename "$attachment_filename" \
						'[.attachments[]? | select((.id | tostring) == $id and .filename == $filename and .file_size > 0 and .file_mimetype != "")] | length == 1'
				else
					mark_fail attachments.create "could not list the created attachment"
				fi
				if [[ -n "$attachment_id" ]]; then
					if run_op attachments.get attachments get --module 7 \
						--object-id "$contact_id" --folder 33 --id "$attachment_id" \
						--save-to "$LIVE_TMP/attachment.download"; then
						assert_last_jq attachments.get "downloaded attachment metadata did not match" \
							--arg id "$attachment_id" --arg filename "$attachment_filename" \
							'(.id | tostring) == $id and .filename == $filename and .file_size > 0 and .file_size == .bytes_written and .file_mimetype != ""'
						assert_last_jq attachments.create "created attachment download metadata did not match" \
							--arg id "$attachment_id" --arg filename "$attachment_filename" \
							'(.id | tostring) == $id and .filename == $filename and .file_size > 0 and .file_size == .bytes_written and .file_mimetype != ""'
						if ! cmp -s "$attachment_path" "$LIVE_TMP/attachment.download"; then
							mark_fail attachments.get "downloaded bytes differed from upload"
							mark_fail attachments.create "downloaded bytes differed from upload"
						fi
					else
						mark_fail attachments.create "could not download the created attachment"
					fi
					if run_op attachments.delete attachments delete --module 7 \
						--object-id "$contact_id" --folder 33 --id "$attachment_id" \
						--confirm "$attachment_id"; then
						if capture_json_for attachments.delete "could not list attachments after deletion" \
							attachments list --module 7 --object-id "$contact_id" --folder 33 &&
							assert_captured_jq attachments.delete "deleted attachment was still listed" \
								--arg id "$attachment_id" \
								'all(.attachments[]?; (.id | tostring) != $id)'; then
							attachment_id=""
						fi
					fi
				fi
			fi
		fi
	fi

	run_op contacts.export contacts export --folder 33 --output "$LIVE_TMP/contacts.vcf"
	printf '%s\n' \
		'BEGIN:VCARD' \
		'VERSION:3.0' \
		"FN:$FIXTURE_PREFIX-imported-contact-1" \
		'N:Contact 1;Imported;;;' \
		"EMAIL:$FIXTURE_PREFIX-imported-contact-1@example.test" \
		'END:VCARD' \
		'BEGIN:VCARD' \
		'VERSION:3.0' \
		"FN:$FIXTURE_PREFIX-imported-contact-2" \
		'N:Contact 2;Imported;;;' \
		"EMAIL:$FIXTURE_PREFIX-imported-contact-2@example.test" \
		'END:VCARD' >"$LIVE_TMP/import.vcf"
	contact_import_attempted=1
	if run_op contacts.import contacts import --folder 33 --file "$LIVE_TMP/import.vcf"; then
		assert_last_jq contacts.import "import did not return two complete, unique contact identities" \
			'.count == 2 and .failed_count == 0 and (.items | length) == 2 and .id == .items[0].id and .folder_id == "33" and all(.items[]; .id != "" and .folder_id == "33") and ([.items[].id] | unique | length) == 2'
		mapfile -t imported_contact_ids < <(jq -r '.items[]?.id // empty' "$LAST_STDOUT")
		if ((${#imported_contact_ids[@]} == 2)); then
			for import_index in "${!imported_contact_ids[@]}"; do
				import_ordinal=$((import_index + 1))
				imported_id="${imported_contact_ids[import_index]}"
				if capture_json_for contacts.import "could not read imported contact $import_ordinal" \
					contacts get --id "$imported_id" --folder 33; then
					assert_captured_jq contacts.import "imported contact $import_ordinal fields did not match" \
						--arg id "$imported_id" \
						--arg display "$FIXTURE_PREFIX-imported-contact-$import_ordinal" \
						--arg email "$FIXTURE_PREFIX-imported-contact-$import_ordinal@example.test" \
						'(.id | tostring) == $id and (.folder_id | tostring) == "33" and .display_name == $display and .email == $email and .last_modified > 0'
				fi
			done
		else
			mark_fail contacts.import "could not track exactly two imported contact ids"
		fi
	fi
	if ! cleanup_imported_contacts; then
		mark_fail contacts.import "could not prove cleanup of every imported contact"
	fi
	if [[ -n "$contact_id" && -z "$attachment_id" ]]; then
		if run_op contacts.get contacts get --id "$contact_id" --folder 33; then
			contact_timestamp="$(jq -r '.last_modified // empty' "$LAST_STDOUT")"
		fi
		if [[ -n "$contact_timestamp" ]] &&
			run_op contacts.delete contacts delete --id "$contact_id" --folder 33 \
				--timestamp "$contact_timestamp" --confirm "$contact_id"; then
			if capture_json_for contacts.delete "could not list contacts after deletion" \
				contacts list --limit 500 &&
				assert_captured_jq contacts.delete "deleted contact or fixture marker was still listed" \
					--arg id "$contact_id" --arg prefix "$FIXTURE_PREFIX" \
					'(.truncated == false) and all(.contacts[]?; ((.id | tostring) != $id) and (((.display_name // "") | startswith($prefix)) | not))'; then
				contact_id=""
			fi
		fi
	fi

	# Calendar lifecycle and iCalendar import/export.
	event_identity_verified=0
	event_create_attempted=1
	# --timezone UTC keeps the stored values comparable with the UTC assertions below;
	# without it, create uses the account's configured time zone.
	if run_op events.create events create --folder 32 \
		--summary "$FIXTURE_PREFIX-event" \
		--start 2030-01-02T10:00:00Z --end 2030-01-02T11:00:00Z --timezone UTC \
		--description "live API fixture" --location "$FIXTURE_PREFIX-room"; then
		assert_last_jq events.create "create did not return a complete event identity" \
			'.id != null and (.id | tostring) != ""'
		event_id="$(jq -r '.id // empty' "$LAST_STDOUT")"
		if [[ -n "$event_id" ]] && run_op events.get events get --id "$event_id" --folder 32; then
			if assert_last_jq events.create "created event fields did not match" \
				--arg id "$event_id" \
				--arg summary "$FIXTURE_PREFIX-event" \
				--arg start 20300102T100000Z \
				--arg end 20300102T110000Z \
				--arg description "live API fixture" \
				--arg location "$FIXTURE_PREFIX-room" \
				'def event_time: if type == "object" then .value else . end; (.id | tostring) == $id and .summary == $summary and (.startDate | event_time) == $start and (.endDate | event_time) == $end and .description == $description and .location == $location'; then
				event_identity_verified=1
			fi
		elif [[ -n "$event_id" ]]; then
			mark_fail events.create "could not read the created event"
		fi
		if ((event_identity_verified == 1)); then
			if run_op events.update events update --id "$event_id" --folder 32 \
				--summary "$FIXTURE_PREFIX-event-updated"; then
				assert_last_jq events.update "update did not return the requested event identity" \
					--arg id "$event_id" '(.id | tostring) == $id'
				if capture_json_for events.update "could not read updated event" \
					events get --id "$event_id" --folder 32; then
					assert_captured_jq events.update "updated event fields did not match" \
						--arg id "$event_id" \
						--arg summary "$FIXTURE_PREFIX-event-updated" \
						--arg start 20300102T100000Z \
						--arg end 20300102T110000Z \
						--arg description "live API fixture" \
						--arg location "$FIXTURE_PREFIX-room" \
						'def event_time: if type == "object" then .value else . end; (.id | tostring) == $id and .summary == $summary and (.startDate | event_time) == $start and (.endDate | event_time) == $end and .description == $description and .location == $location'
				fi
			fi
		fi
	fi
	run_op events.export events export --folder 32 --output "$LIVE_TMP/events.ics"
	printf '%s\n' \
		'BEGIN:VCALENDAR' \
		'VERSION:2.0' \
		'PRODID:-//mbx live test//EN' \
		'BEGIN:VEVENT' \
		"UID:$FIXTURE_PREFIX-imported-event-1@example.test" \
		'DTSTAMP:20300101T000000Z' \
		'DTSTART:20300105T100000Z' \
		'DTEND:20300105T110000Z' \
		"SUMMARY:$FIXTURE_PREFIX-imported-event-1" \
		'DESCRIPTION:live API imported fixture 1' \
		'END:VEVENT' \
		'BEGIN:VEVENT' \
		"UID:$FIXTURE_PREFIX-imported-event-2@example.test" \
		'DTSTAMP:20300101T000000Z' \
		'DTSTART:20300106T100000Z' \
		'DTEND:20300106T110000Z' \
		"SUMMARY:$FIXTURE_PREFIX-imported-event-2" \
		'DESCRIPTION:live API imported fixture 2' \
		'END:VEVENT' \
		'END:VCALENDAR' >"$LIVE_TMP/import.ics"
	event_import_attempted=1
	if run_op events.import events import --folder 32 --file "$LIVE_TMP/import.ics"; then
		assert_last_jq events.import "import did not return two complete, unique event identities" \
			'.count == 2 and .failed_count == 0 and (.items | length) == 2 and .id == .items[0].id and .folder_id == "32" and all(.items[]; .id != "" and .folder_id == "32") and ([.items[].id] | unique | length) == 2'
		mapfile -t imported_event_ids < <(jq -r '.items[]?.id // empty' "$LAST_STDOUT")
		if ((${#imported_event_ids[@]} == 2)); then
			imported_event_starts=(20300105T100000Z 20300106T100000Z)
			imported_event_ends=(20300105T110000Z 20300106T110000Z)
			for import_index in "${!imported_event_ids[@]}"; do
				import_ordinal=$((import_index + 1))
				imported_id="${imported_event_ids[import_index]}"
				if capture_json_for events.import "could not read imported event $import_ordinal" \
					events get --id "$imported_id" --folder 32; then
					assert_captured_jq events.import "imported event $import_ordinal fields did not match" \
						--arg id "$imported_id" \
						--arg summary "$FIXTURE_PREFIX-imported-event-$import_ordinal" \
						--arg start "${imported_event_starts[import_index]}" \
						--arg end "${imported_event_ends[import_index]}" \
						--arg description "live API imported fixture $import_ordinal" \
						'(.id | tostring) == $id and .summary == $summary and (.startDate | if type == "object" then .value else . end) == $start and (.endDate | if type == "object" then .value else . end) == $end and .description == $description'
				fi
			done
		else
			mark_fail events.import "could not track exactly two imported event ids"
		fi
	fi
	if ! cleanup_imported_events; then
		mark_fail events.import "could not prove cleanup of every imported event"
	fi
	if ((event_identity_verified == 1)) && [[ -n "$event_id" ]]; then
		if run_op events.delete events delete --id "$event_id" --folder 32 --confirm "$event_id"; then
			assert_last_jq events.delete "delete did not return the requested event identity" \
				--arg id "$event_id" '.deleted == true and (.id | tostring) == $id'
		fi
	fi
	if ((event_create_attempted == 1)) || [[ -n "$event_id" ]]; then
		if ! cleanup_standard_event; then
			mark_fail events.delete "could not prove calendar-event fixture cleanup"
		fi
	fi

	# Task lifecycle.
	task_create_attempted=1
	if run_op tasks.create tasks create --folder 35 --title "$FIXTURE_PREFIX-task" \
		--note "live API fixture" --status 1 --priority 1 \
		--end-date 2030-01-02T12:00:00Z; then
		task_id="$(jq -r '.id // empty' "$LAST_STDOUT")"
		task_timestamp="$(jq -r '.timestamp // empty' "$LAST_STDOUT")"
		if assert_last_jq tasks.create "create response identity/folder/timestamp did not match" \
			--arg id "$task_id" \
			'(.id | tostring) == $id and ($id | test("^[1-9][0-9]*$")) and (.folder_id | tostring) == "35" and .timestamp > 0'; then
			if run_op tasks.get tasks get --id "$task_id" --folder 35; then
				if assert_last_jq tasks.get "created task read-back fields did not match" \
					--arg id "$task_id" --arg title "$FIXTURE_PREFIX-task" \
					--argjson end "$TASK_FIXTURE_END_MILLIS" \
					'(.id | tostring) == $id and (.folder_id | tostring) == "35" and .title == $title and .note == "live API fixture" and .status == 1 and .priority == 1 and .end_date == $end and .timestamp > 0' &&
					assert_last_jq tasks.create "created task fields did not match on read-back" \
						--arg id "$task_id" --arg title "$FIXTURE_PREFIX-task" \
						--argjson end "$TASK_FIXTURE_END_MILLIS" \
						'(.id | tostring) == $id and (.folder_id | tostring) == "35" and .title == $title and .note == "live API fixture" and .status == 1 and .priority == 1 and .end_date == $end and .timestamp > 0'; then
					task_ready=1
					task_timestamp="$(jq -r '.timestamp // empty' "$LAST_STDOUT")"
				fi
			else
				mark_fail tasks.create "could not read the created task"
			fi
		fi
	fi
	if ((task_ready == 1)); then
		if run_op tasks.update tasks update --id "$task_id" --folder 35 \
			--timestamp "$task_timestamp" --title "$FIXTURE_PREFIX-task-updated" \
			--status 2; then
			assert_last_jq tasks.update "update response identity/timestamp did not match" \
				--arg id "$task_id" '(.id | tostring) == $id and .updated == true and .timestamp > 0'
			if capture_json_for tasks.update "could not read updated task" \
				tasks get --id "$task_id" --folder 35; then
				if assert_captured_jq tasks.update "updated task fields did not match" \
					--arg id "$task_id" --arg title "$FIXTURE_PREFIX-task-updated" \
					--argjson end "$TASK_FIXTURE_END_MILLIS" \
					'(.id | tostring) == $id and (.folder_id | tostring) == "35" and .title == $title and .note == "live API fixture" and .status == 2 and .priority == 1 and .end_date == $end and .timestamp > 0'; then
					task_timestamp="$(jq -r '.timestamp' "$ASSERTION_STDOUT")"
				fi
			fi
		fi

		# Re-read immediately before the destructive operation, even after a successful
		# update read-back, so delete always uses the latest provider timestamp.
		if capture_json_for tasks.delete "could not read task immediately before deletion" \
			tasks get --id "$task_id" --folder 35 &&
			assert_captured_jq tasks.delete "task identity before deletion did not match" \
				--arg id "$task_id" \
				'(.id | tostring) == $id and (.folder_id | tostring) == "35" and .timestamp > 0'; then
			task_timestamp="$(jq -r '.timestamp' "$ASSERTION_STDOUT")"
			if run_op tasks.delete tasks delete --id "$task_id" --folder 35 \
				--timestamp "$task_timestamp" --confirm "$task_id"; then
				assert_last_jq tasks.delete "delete response identity did not match" \
					--arg id "$task_id" '(.id | tostring) == $id and .deleted == true'
			fi
			if capture_json_for tasks.delete "could not list tasks after deletion" \
				tasks list --folder 35 --limit 500 &&
				assert_captured_jq tasks.delete "deleted task or marker was still listed" \
					--arg id "$task_id" --arg prefix "$FIXTURE_PREFIX-task" \
					'(.truncated == false) and all(.items[]?; (.id | tostring) != $id and (((.title // "") | startswith($prefix)) | not))'; then
				task_id=""
				task_timestamp=""
				task_create_attempted=0
				task_ready=0
			fi
		fi
	fi

	# Reminder creation is implemented by creating an alarm-bearing task. The create output
	# reports the reminder id as .id (the id reminder delete takes) and the task as .task_id.
	reminder_create_attempted=1
	if run_op reminder.create reminder create --folder 35 \
		--title "$FIXTURE_PREFIX-reminder" --at 2030-01-04T09:00:00Z; then
		reminder_task_id="$(jq -r '.task_id // empty' "$LAST_STDOUT")"
		reminder_task_timestamp="$(jq -r '.timestamp // empty' "$LAST_STDOUT")"
		created_reminder_id="$(jq -r '.id // empty' "$LAST_STDOUT")"
		if assert_last_jq reminder.create "create response reminder id/task id/folder/alarm/timestamp did not match" \
			--arg id "$reminder_task_id" --argjson alarm "$REMINDER_FIXTURE_ALARM_MILLIS" \
			'(.task_id | tostring) == $id and ($id | test("^[1-9][0-9]*$")) and ((.id // "") | tostring | test("^[1-9][0-9]*$")) and (.folder_id | tostring) == "35" and .alarm == $alarm and .timestamp > 0' &&
			capture_json_for reminder.create "could not read reminder backing task" \
				tasks get --id "$reminder_task_id" --folder 35 &&
			assert_captured_jq reminder.create "reminder backing task identity/title did not match" \
				--arg id "$reminder_task_id" --arg title "$FIXTURE_PREFIX-reminder" \
				'(.id | tostring) == $id and (.folder_id | tostring) == "35" and .title == $title and .timestamp > 0'; then
			reminder_task_ready=1
		fi
		if run_op reminder.list reminder list --from "$REMINDER_FIXTURE_FROM" \
			--to "$REMINDER_FIXTURE_TO" --limit 200; then
			if assert_last_jq reminder.list "created reminder row did not match backing task/folder/alarm" \
				--arg target "$reminder_task_id" --argjson alarm "$REMINDER_FIXTURE_ALARM_MILLIS" \
				'(.truncated == false) and ([.items[]? | select((.target_id | tostring) == $target and .module == 4 and (.folder | tostring) == "35" and .alarm == $alarm)] | length) == 1' &&
				assert_last_jq reminder.create "created reminder id was not observable exactly once for its task" \
					--arg id "$created_reminder_id" --arg target "$reminder_task_id" --argjson alarm "$REMINDER_FIXTURE_ALARM_MILLIS" \
					'(.truncated == false) and ([.items[]? | select((.id | tostring) == $id and (.target_id | tostring) == $target and .module == 4 and (.folder | tostring) == "35" and .alarm == $alarm)] | length) == 1'; then
				reminder_id="$created_reminder_id"
				if [[ -n "$reminder_id" && "$reminder_task_ready" == "1" ]]; then
					reminder_ready=1
				fi
			fi
		fi
	fi
	if ((reminder_ready == 1)); then
		if run_op reminder.delete reminder delete --id "$reminder_id" --confirm "$reminder_id"; then
			assert_last_jq reminder.delete "delete response identity did not match" \
				--arg id "$reminder_id" '.deleted == true and (.id | tostring) == $id'
		fi
		if capture_json_for reminder.delete "could not list reminders after deletion" \
			reminder list --from "$REMINDER_FIXTURE_FROM" --to "$REMINDER_FIXTURE_TO" --limit 200 &&
			assert_captured_jq reminder.delete "deleted reminder id or backing-task target was still listed" \
				--arg id "$reminder_id" --arg target "$reminder_task_id" \
				'(.truncated == false) and all(.items[]?; (.id | tostring) != $id and (.target_id | tostring) != $target)'; then
			reminder_id=""
			reminder_absence_verified=1
			reminder_ready=0
		fi
	fi
	if ((reminder_absence_verified == 1 && reminder_task_ready == 1)); then
		if capture_json_for reminder.create "could not read backing task before cleanup" \
			tasks get --id "$reminder_task_id" --folder 35 &&
			assert_captured_jq reminder.create "backing task identity before cleanup did not match" \
				--arg id "$reminder_task_id" --arg title "$FIXTURE_PREFIX-reminder" \
				'(.id | tostring) == $id and (.folder_id | tostring) == "35" and .title == $title and .timestamp > 0'; then
			reminder_task_timestamp="$(jq -r '.timestamp' "$ASSERTION_STDOUT")"
			if ! run_op tasks.delete tasks delete --id "$reminder_task_id" --folder 35 \
				--timestamp "$reminder_task_timestamp" --confirm "$reminder_task_id"; then
				mark_fail reminder.create "could not delete reminder backing task"
			else
				assert_last_jq tasks.delete "backing-task delete response identity did not match" \
					--arg id "$reminder_task_id" '(.id | tostring) == $id and .deleted == true'
			fi
			if capture_json_for reminder.create "could not list tasks after backing-task cleanup" \
				tasks list --folder 35 --limit 500 &&
				assert_captured_jq reminder.create "reminder backing task or marker was still listed" \
					--arg id "$reminder_task_id" --arg prefix "$FIXTURE_PREFIX-reminder" \
					'(.truncated == false) and all(.items[]?; (.id | tostring) != $id and (((.title // "") | startswith($prefix)) | not))'; then
				reminder_task_id=""
				reminder_task_timestamp=""
				reminder_task_ready=0
				reminder_absence_verified=0
				reminder_create_attempted=0
			fi
		fi
	fi

	# File, download, metadata, and public-share lifecycle. The file is isolated inside
	# the verified temporary folder so deleting that folder is the final dependency fence.
	file_source="$LIVE_TMP/$FIXTURE_PREFIX-file.txt"
	printf 'file fixture %s\n' "$RUN_STAMP" >"$file_source"
	if ((folder_ready == 1)); then
		file_create_attempted=1
		if run_op files.create files create --folder "$folder_id" --file "$file_source" \
			--title "$FIXTURE_PREFIX-file" --description "live API fixture initial"; then
			file_id="$(jq -r '.id // empty' "$LAST_STDOUT")"
			file_folder="$(jq -r '.folder_id // empty' "$LAST_STDOUT")"
			if assert_last_jq files.create "create response identity/folder/title did not match" \
				--arg folder "$folder_id" --arg title "$FIXTURE_PREFIX-file" \
				'(.id | type) == "string" and (.id | startswith($folder + "/")) and (.folder_id | tostring) == $folder and .title == $title'; then
				if run_op files.get files get --id "$file_id" --save-to "$LIVE_TMP/file.download"; then
					file_get_ok=1
					if ! assert_last_jq files.get "download metadata did not match the uploaded file" \
						--arg id "$file_id" --arg folder "$folder_id" \
						--arg title "$FIXTURE_PREFIX-file" \
						--arg filename "$FIXTURE_PREFIX-file.txt" \
						--arg description "live API fixture initial" \
						--arg saved "$LIVE_TMP/file.download" \
						'(.id | tostring) == $id and (.folder_id | tostring) == $folder and .title == $title and .filename == $filename and (.mimetype | startswith("text/plain")) and .size > 0 and .description == $description and .last_modified > 0 and .saved_to == $saved and .bytes_written == .size'; then
						file_get_ok=0
					fi
					if ! assert_last_jq files.create "created file fields did not match on read-back" \
						--arg id "$file_id" --arg folder "$folder_id" \
						--arg title "$FIXTURE_PREFIX-file" \
						--arg filename "$FIXTURE_PREFIX-file.txt" \
						--arg description "live API fixture initial" \
						'(.id | tostring) == $id and (.folder_id | tostring) == $folder and .title == $title and .filename == $filename and (.mimetype | startswith("text/plain")) and .size > 0 and .description == $description and .last_modified > 0 and .bytes_written == .size'; then
						file_get_ok=0
					fi
					if ! cmp -s "$file_source" "$LIVE_TMP/file.download"; then
						mark_fail files.get "downloaded bytes differed from upload"
						mark_fail files.create "read-back bytes differed from upload"
						file_get_ok=0
					fi
					if ((file_get_ok == 1)); then
						uploaded_file_size="$(jq -r '.size' "$LAST_STDOUT")"
						file_ready=1
					fi
				else
					mark_fail files.create "could not read the created file"
				fi
			fi
		fi

		if ((file_ready == 1)); then
			if run_op files.update files update --id "$file_id" \
				--title "$FIXTURE_PREFIX-file-updated" \
				--description "live API fixture updated" \
				--filename "$FIXTURE_PREFIX-file-updated.txt"; then
				assert_last_jq files.update "update response identity/timestamp did not match" \
					--arg id "$file_id" '(.id | tostring) == $id and .updated == true and .timestamp > 0'
				if capture_json_for files.update "could not read updated file" \
					files get --id "$file_id"; then
					assert_captured_jq files.update "updated file fields did not match" \
						--arg id "$file_id" --arg folder "$folder_id" \
						--arg title "$FIXTURE_PREFIX-file-updated" \
						--arg filename "$FIXTURE_PREFIX-file-updated.txt" \
						--arg description "live API fixture updated" \
						--argjson size "$uploaded_file_size" \
						'(.id | tostring) == $id and (.folder_id | tostring) == $folder and .title == $title and .filename == $filename and (.mimetype | startswith("text/plain")) and .size == $size and .description == $description and .last_modified > 0'
				fi
			fi

			bare_file_id="${file_id##*/}"
			share_create_attempted=1
			if run_op share.create share create --folder "$file_folder" --id "$bare_file_id"; then
				assert_last_jq share.create "share permission entity, URL, or new-link flag did not match" \
					'(.entity | type) == "number" and .entity > 0 and (.entity | floor) == .entity and (.url | type) == "string" and (.url | startswith("https://")) and .is_new == true'
			fi
			# Run the reverse dependency cleanup even if create lost its response after the
			# provider had already created a link. delete first checks for a link without
			# creating one, then its getLink preflight resolves the timestamp for deleteLink.
			if run_op share.delete share delete --folder "$file_folder" --id "$bare_file_id" \
				--confirm "$bare_file_id"; then
				if assert_last_jq share.delete "share delete response identity did not match" \
					--arg folder "$file_folder" --arg id "$bare_file_id" \
					'.deleted == true and (.folder | tostring) == $folder and (.id | tostring) == $id'; then
					share_create_attempted=0
				fi
			fi

			if run_op files.delete files delete --id "$file_id" --confirm "$file_id"; then
				assert_last_jq files.delete "file delete response identity did not match" \
					--arg id "$file_id" '(.id | tostring) == $id and .deleted == true'
				if capture_json_for files.delete "could not list file folder after deletion" \
					files list --folder "$folder_id" --limit 200 &&
					assert_captured_jq files.delete "deleted file or marker was still listed" \
						--arg id "$file_id" --arg prefix "$FIXTURE_PREFIX-file" \
						'(.truncated == false) and all(.files[]?; (.id | tostring) != $id and (((.title // "") | startswith($prefix)) | not) and (((.filename // "") | startswith($prefix)) | not))'; then
					file_id=""
					file_folder=""
					file_create_attempted=0
					file_ready=0
					share_create_attempted=0
				fi
			fi
		fi
	else
		mark_fail files.create "temporary infostore folder was not verified"
	fi

	# Delete the childless temporary folder only after share and file cleanup, then prove
	# absence in the exact parent listing before releasing its tracked identity.
	if ((folder_ready == 1 && file_create_attempted == 0 && share_create_attempted == 0)) &&
		[[ -n "$folder_id" && -z "$file_id" ]]; then
		if run_op folders.delete folders delete --id "$folder_id" --confirm "$folder_id"; then
			assert_last_jq folders.delete "folder delete response identity did not match" \
				--arg id "$folder_id" '.deleted == true and (.id | tostring) == $id'
			if capture_json_for folders.delete "could not list parent after folder deletion" \
				folders list --parent 36 &&
				assert_captured_jq folders.delete "deleted folder or marker was still listed" \
					--arg id "$folder_id" --arg prefix "$FIXTURE_PREFIX-folder" \
					'all(.folders[]?; (.id | tostring) != $id and (((.title // "") | startswith($prefix)) | not))'; then
				folder_id=""
				folder_create_attempted=0
				folder_permissions_json="null"
				folder_ready=0
			fi
		fi
	fi

	# Groups and resources use globally unique technical names.
	technical_name="mbxlive${RUN_STAMP//[^0-9]/}"
	if run_op group.create group create --name "$technical_name" \
		--display-name "$FIXTURE_PREFIX-group"; then
		group_id="$(jq -r '.id // empty' "$LAST_STDOUT")"
		if [[ -n "$group_id" ]]; then
			if capture_json_for group.create "could not read created group" \
				group get --id "$group_id"; then
				assert_captured_jq group.create "created group fields did not match" \
					--arg id "$group_id" --arg display "$FIXTURE_PREFIX-group" \
					'(.id | tostring) == $id and .display_name == $display and .members == []'
			fi
			if run_op group.update group update --id "$group_id" \
				--display-name "$FIXTURE_PREFIX-group-updated"; then
				if capture_json_for group.update "could not read updated group" \
					group get --id "$group_id"; then
					assert_captured_jq group.update "updated group fields did not match" \
						--arg id "$group_id" --arg display "$FIXTURE_PREFIX-group-updated" \
						'(.id | tostring) == $id and .display_name == $display and .members == []'
				fi
			fi
			if run_op group.delete group delete --id "$group_id" --confirm "$group_id"; then
				if capture_json_for group.delete "could not list groups after deletion" \
					group list --pattern '*' &&
					assert_captured_jq group.delete "deleted group or marker was still listed" \
						--arg id "$group_id" --arg prefix "$FIXTURE_PREFIX" \
						'all(.groups[]?; ((.id | tostring) != $id) and (.display_name | startswith($prefix) | not))'; then
					group_id=""
				fi
			fi
		fi
	fi

	if run_op resources.create resources create --name "$technical_name" \
		--display-name "$FIXTURE_PREFIX-resource" \
		--mailaddress "$FIXTURE_PREFIX@example.test" \
		--description "live API fixture"; then
		resource_id="$(jq -r '.id // empty' "$LAST_STDOUT")"
		if [[ -n "$resource_id" ]]; then
			if capture_json_for resources.create "could not search for created resource" \
				resources search --pattern "$FIXTURE_PREFIX-resource"; then
				assert_captured_jq resources.create "created resource fields did not match exactly once" \
					--arg id "$resource_id" \
					--arg display "$FIXTURE_PREFIX-resource" \
					--arg email "$FIXTURE_PREFIX@example.test" \
					'(.resources | length) == 1 and (.resources[0].id | tostring) == $id and .resources[0].display_name == $display and .resources[0].email == $email'
			fi
			if run_op resources.update resources update --id "$resource_id" \
				--display-name "$FIXTURE_PREFIX-resource-updated"; then
				if capture_json_for resources.update "could not search for updated resource" \
					resources search --pattern "$FIXTURE_PREFIX-resource-updated"; then
					assert_captured_jq resources.update "updated resource fields did not match exactly once" \
						--arg id "$resource_id" \
						--arg display "$FIXTURE_PREFIX-resource-updated" \
						--arg email "$FIXTURE_PREFIX@example.test" \
						'(.resources | length) == 1 and (.resources[0].id | tostring) == $id and .resources[0].display_name == $display and .resources[0].email == $email'
				fi
			fi
			if run_op resources.delete resources delete --id "$resource_id" \
				--confirm "$resource_id"; then
				if capture_json_for resources.delete "could not search resources after deletion" \
					resources search --pattern "$FIXTURE_PREFIX-resource" &&
					assert_captured_jq resources.delete "deleted resource or marker was still returned" \
						--arg id "$resource_id" --arg prefix "$FIXTURE_PREFIX" \
						'all(.resources[]?; ((.id | tostring) != $id) and (.display_name | startswith($prefix) | not))'; then
					resource_id=""
				fi
			fi
		fi
	fi

	# Signature/snippet lifecycle. Always create a new uniquely marked signature; never
	# reuse or overwrite a signature that was already present in the dedicated account.
	snippet_name="$FIXTURE_PREFIX-snippet"
	snippet_updated_name="$FIXTURE_PREFIX-snippet-updated"
	snippet_content="live API fixture initial"
	snippet_updated_content="live API fixture updated"
	snippet_create_attempted=1
	if run_op snippet.create snippet create --displayname "$snippet_name" \
		--content "$snippet_content"; then
		snippet_id="$(jq -r '.id // empty' "$LAST_STDOUT")"
		if assert_last_jq snippet.create "create response did not contain a non-empty string id" \
			'(.id | type) == "string" and (.id | length) > 0'; then
			if run_op snippet.get snippet get --id "$snippet_id"; then
				if assert_last_jq snippet.get "created snippet fields did not match" \
					--arg id "$snippet_id" --arg name "$snippet_name" \
					--arg content "$snippet_content" \
					'(.id | tostring) == $id and .displayname == $name and .content == $content and .module == "io.ox/mail" and .type == "signature" and .shared == false and (.createdby | type) == "number" and (.createdby | floor) == .createdby and .createdby >= 0' &&
					assert_last_jq snippet.create "created snippet was not observable with the requested fields" \
						--arg id "$snippet_id" --arg name "$snippet_name" \
						--arg content "$snippet_content" \
						'(.id | tostring) == $id and .displayname == $name and .content == $content and .module == "io.ox/mail" and .type == "signature" and .shared == false and (.createdby | type) == "number" and (.createdby | floor) == .createdby and .createdby >= 0'; then
					snippet_created_by="$(jq -r '.createdby | tostring' "$LAST_STDOUT")"
					snippet_ready=1
				fi
			else
				mark_fail snippet.create "could not read the created snippet"
			fi
		fi
	fi

	if ((snippet_ready == 1)); then
		if capture_json_for snippet.update "could not read snippet immediately before update" \
			snippet get --id "$snippet_id" &&
			assert_captured_jq snippet.update "update target identity or fields did not match" \
				--arg id "$snippet_id" --arg name "$snippet_name" \
				--arg content "$snippet_content" --arg createdby "$snippet_created_by" \
				'(.id | tostring) == $id and .displayname == $name and .content == $content and .module == "io.ox/mail" and .type == "signature" and .shared == false and (.createdby | tostring) == $createdby'; then
			if run_op snippet.update snippet update --id "$snippet_id" \
				--displayname "$snippet_updated_name" --content "$snippet_updated_content"; then
				assert_last_jq snippet.update "update response fields did not match" \
					--arg id "$snippet_id" --arg name "$snippet_updated_name" \
					--arg content "$snippet_updated_content" --arg createdby "$snippet_created_by" \
					'(.id | tostring) == $id and .displayname == $name and .content == $content and .module == "io.ox/mail" and .type == "signature" and .shared == false and (.createdby | tostring) == $createdby'
			fi
			if capture_json_for snippet.update "could not read snippet after update" \
				snippet get --id "$snippet_id"; then
				assert_captured_jq snippet.update "updated snippet fields did not match" \
					--arg id "$snippet_id" --arg name "$snippet_updated_name" \
					--arg content "$snippet_updated_content" --arg createdby "$snippet_created_by" \
					'(.id | tostring) == $id and .displayname == $name and .content == $content and .module == "io.ox/mail" and .type == "signature" and .shared == false and (.createdby | tostring) == $createdby'
			fi
		fi

		# The provider's normal delete response carries no independent target identity, so
		# exact preflight ownership and complete list absence are authoritative.
		if capture_json_for snippet.delete "could not read snippet immediately before delete" \
			snippet get --id "$snippet_id" &&
			assert_captured_jq snippet.delete "delete target identity or marker did not match" \
				--arg id "$snippet_id" --arg prefix "$FIXTURE_PREFIX-snippet" \
				'(.id | tostring) == $id and (.displayname | startswith($prefix))'; then
			assert_captured_jq snippet.delete "delete target invariant fields did not match" \
				--arg createdby "$snippet_created_by" \
				'.module == "io.ox/mail" and .type == "signature" and .shared == false and (.createdby | tostring) == $createdby' || true
			if run_op snippet.delete snippet delete --id "$snippet_id" --confirm "$snippet_id"; then
				assert_last_jq snippet.delete "delete result did not contain the requested id" \
					--arg id "$snippet_id" '.deleted == true and (.id | tostring) == $id'
			fi
			if capture_json_for snippet.delete "could not list snippets after deletion" snippet list &&
				assert_captured_jq snippet.delete "deleted snippet id or marker was still listed" \
					--arg id "$snippet_id" --arg prefix "$FIXTURE_PREFIX-snippet" \
					'all(.snippets[]?; (.id | tostring) != $id and (((.displayname // "") | startswith($prefix)) | not))'; then
				snippet_id=""
				snippet_create_attempted=0
				snippet_created_by=""
				snippet_ready=0
			fi
		fi
	fi

	# Inactive keep rule: it cannot redirect, discard, or otherwise affect mail.
	mailfilter_rule="$(jq -cn --arg name "$FIXTURE_PREFIX-filter" \
		'{rulename:$name,active:false,test:{id:"true"},actioncmds:[{id:"keep"}]}')"
	mailfilter_create_attempted=1
	if run_op mailfilter.create mailfilter create --rule "$mailfilter_rule"; then
		mailfilter_id="$(jq -r '.id // empty' "$LAST_STDOUT")"
		if assert_last_jq mailfilter.create "create response did not contain a non-negative integer id" \
			'(.id | type) == "number" and (.id | floor) == .id and .id >= 0'; then
			if run_op mailfilter.get mailfilter get --id "$mailfilter_id"; then
				assert_last_jq mailfilter.get "created rule fields did not match" \
					--arg id "$mailfilter_id" --arg name "$FIXTURE_PREFIX-filter" \
					'(.id | tostring) == $id and .rulename == $name and .active == false and .test.id == "true" and (.actioncmds | length) == 1 and .actioncmds[0].id == "keep"'
				if assert_last_jq mailfilter.create "created rule was not observable with the requested safe fields" \
					--arg id "$mailfilter_id" --arg name "$FIXTURE_PREFIX-filter" \
					'(.id | tostring) == $id and .rulename == $name and .active == false and .test.id == "true" and (.actioncmds | length) == 1 and .actioncmds[0].id == "keep"'; then
					mailfilter_ready=1
				fi
			else
				mark_fail mailfilter.create "could not read the created rule"
			fi
		fi
	fi

	if ((mailfilter_ready == 1)); then
		updated_rule="$(jq -cn --arg name "$FIXTURE_PREFIX-filter-updated" \
			'{rulename:$name,active:false,test:{id:"true"},actioncmds:[{id:"keep"}]}')"
		if capture_json_for mailfilter.update "could not read rule immediately before update" \
			mailfilter get --id "$mailfilter_id" &&
			assert_captured_jq mailfilter.update "update target identity or safe fields did not match" \
				--arg id "$mailfilter_id" --arg prefix "$FIXTURE_PREFIX-filter" \
				'(.id | tostring) == $id and (.rulename | startswith($prefix)) and .active == false and (.actioncmds | length) == 1 and .actioncmds[0].id == "keep"'; then
			if run_op mailfilter.update mailfilter update --id "$mailfilter_id" --rule "$updated_rule"; then
				assert_last_jq mailfilter.update "update response identity did not match" \
					--arg id "$mailfilter_id" '.updated == true and (.id | tostring) == $id'
			fi
			if capture_json_for mailfilter.update "could not read rule after update" \
				mailfilter get --id "$mailfilter_id"; then
				assert_captured_jq mailfilter.update "updated rule fields did not match" \
					--arg id "$mailfilter_id" --arg name "$FIXTURE_PREFIX-filter-updated" \
					'(.id | tostring) == $id and .rulename == $name and .active == false and .test.id == "true" and (.actioncmds | length) == 1 and .actioncmds[0].id == "keep"'
			fi
		fi

		# The mutation response carries data:null, so exact preflight identity and complete
		# list absence are the authoritative delete/refusal evidence.
		if capture_json_for mailfilter.delete "could not read rule immediately before delete" \
			mailfilter get --id "$mailfilter_id" &&
			assert_captured_jq mailfilter.delete "delete target identity or marker did not match" \
				--arg id "$mailfilter_id" --arg prefix "$FIXTURE_PREFIX-filter" \
				'(.id | tostring) == $id and (.rulename | startswith($prefix))'; then
			assert_captured_jq mailfilter.delete "delete target was not an inactive keep-only rule" \
				'.active == false and (.actioncmds | length) == 1 and .actioncmds[0].id == "keep"' || true
			if run_op mailfilter.delete mailfilter delete --id "$mailfilter_id" \
				--confirm "$mailfilter_id"; then
				assert_last_jq mailfilter.delete "delete response identity did not match" \
					--arg id "$mailfilter_id" '.deleted == true and (.id | tostring) == $id'
			fi
			if capture_json_for mailfilter.delete "could not list rules after deletion" \
				mailfilter list &&
				assert_captured_jq mailfilter.delete "deleted rule id or marker was still listed" \
					--arg id "$mailfilter_id" --arg prefix "$FIXTURE_PREFIX-filter" \
					'all(.rules[]?; (.id | tostring) != $id and (((.rulename // "") | startswith($prefix)) | not))'; then
				mailfilter_id=""
				mailfilter_create_attempted=0
				mailfilter_ready=0
			fi
		fi
	fi

	# Toggle one fixed-whitelist boolean UI preference, prove the changed value with an
	# independent read, then restore and independently prove the exact original value.
	if capture_json_for settings.set "could not read the mail settings tree" \
		settings get --path "$SETTINGS_FIXTURE_PATH" &&
		assert_captured_jq settings.set "mail settings tree identity or shape did not match" \
			--arg path "$SETTINGS_FIXTURE_PATH" \
			'.path == $path and (.tree | type) == "object"'; then
		settings_key="$(jq -r '
			.tree as $tree
			| ["playSound", "showCheckboxes", "useFixedWidthFont", "isColorQuoted"]
			| map(select(($tree[.] | type) == "boolean"))
			| .[0] // empty
		' "$ASSERTION_STDOUT")"
		if [[ -z "$settings_key" ]]; then
			mark_fail settings.set "no supported boolean UI preference was available"
		elif capture_json_for settings.set "could not read setting immediately before update" \
			settings get --path "$SETTINGS_FIXTURE_PATH" --key "$settings_key" &&
			assert_captured_jq settings.set "setting pre-read identity or type did not match" \
				--arg path "$SETTINGS_FIXTURE_PATH" --arg key "$settings_key" \
				'.path == $path and .key == $key and (.value | type) == "boolean"'; then
			settings_original_json="$(jq -c '.value' "$ASSERTION_STDOUT")"
			settings_test_json="$(jq -cn --argjson original "$settings_original_json" '$original | not')"
			# Arm rollback before the request: the provider may apply the write even if the
			# client loses or rejects its response.
			settings_restore_armed=1
			if run_op settings.set settings set --path "$SETTINGS_FIXTURE_PATH" \
				--key "$settings_key" --value "$settings_test_json"; then
				assert_last_jq settings.set "settings result did not contain the exact old/new values" \
					--arg path "$SETTINGS_FIXTURE_PATH" --arg key "$settings_key" \
					--argjson old_value "$settings_original_json" \
					--argjson new_value "$settings_test_json" \
					'.path == $path and .key == $key and .old_value == $old_value and .new_value == $new_value'
			fi
			if capture_json_for settings.set "could not independently read the changed setting" \
				settings get --path "$SETTINGS_FIXTURE_PATH" --key "$settings_key"; then
				assert_captured_jq settings.set "independent read did not contain the requested setting value" \
					--arg path "$SETTINGS_FIXTURE_PATH" --arg key "$settings_key" \
					--argjson expected "$settings_test_json" \
					'.path == $path and .key == $key and .value == $expected'
			fi
			if ! cleanup_setting_fixture; then
				mark_fail settings.set "could not restore and verify the original setting value"
			fi
		fi
	fi

	# User update is immediately reverted, and the trap retries restoration on interruption.
	if [[ -n "$original_display_name" ]]; then
		if run_op user.update user update --display-name "$FIXTURE_PREFIX-user"; then
			user_changed=1
			if capture_json_for user.update "could not read user after display-name update" whoami; then
				assert_captured_jq user.update "display-name update was not visible" \
					--arg expected "$FIXTURE_PREFIX-user" '.display_name == $expected'
			fi
			if quiet_json user update --display-name "$original_display_name"; then
				if capture_json_for user.update "could not read user after display-name restoration" whoami &&
					assert_captured_jq user.update "original display name was not restored" \
						--arg expected "$original_display_name" '.display_name == $expected'; then
					user_changed=0
				fi
			else
				mark_fail user.update "could not restore original display name"
			fi
		fi
	fi

	# Draft exercises get, flag, copy, move, and permanent delete without delivery.
	# Do not trust a create-response id for later mutation until an independent read proves
	# ownership through this run's exact subject and recipient marker.
	draft_subject="$FIXTURE_PREFIX-draft"
	if run_op mail.draft mail draft --to "$MBX_USERNAME" \
		--subject "$draft_subject" --body "live API fixture"; then
		if assert_last_jq mail.draft "draft response did not contain the requested folder and a non-empty id" \
			--arg folder default0/Drafts \
			'(.id | type) == "string" and (.id | length) > 0 and .folder == $folder'; then
			draft_candidate_id="$(jq -r '.id' "$LAST_STDOUT")"
			draft_candidate_folder="$(jq -r '.folder' "$LAST_STDOUT")"
			if run_op mail.get mail get --folder "$draft_candidate_folder" --id "$draft_candidate_id"; then
				if assert_last_jq mail.get "created draft read-back fields did not match" \
					--arg id "$draft_candidate_id" --arg folder "$draft_candidate_folder" \
					--arg subject "$draft_subject" --arg recipient "$MBX_USERNAME" \
					'.id == $id and .folder == $folder and .subject == $subject
					and (.to | type) == "array" and any(.to[]?; .address == $recipient)
					and (.size | type) == "number" and .size > 0
					and .attachment == false
					and (.seen | type) == "boolean" and (.flagged | type) == "boolean"' &&
					assert_last_jq mail.draft "created draft was not independently observable with its ownership fields" \
						--arg id "$draft_candidate_id" --arg folder "$draft_candidate_folder" \
						--arg subject "$draft_subject" --arg recipient "$MBX_USERNAME" \
						'.id == $id and .folder == $folder and .subject == $subject
						and (.to | type) == "array" and any(.to[]?; .address == $recipient)
						and (.size | type) == "number" and .size > 0
						and .attachment == false
						and (.seen | type) == "boolean" and (.flagged | type) == "boolean"'; then
					draft_id="$draft_candidate_id"
					draft_folder="$draft_candidate_folder"
				else
					mark_fail mail.draft "created draft was not independently observable with its ownership fields"
				fi
			else
				mark_fail mail.draft "created draft could not be independently read"
			fi
		fi
		if [[ -n "$draft_id" ]]; then
			if capture_json_for mail.draft "could not inspect Sent after draft creation" \
				mail list --folder default0/Sent --limit 200; then
				assert_captured_jq mail.draft "draft subject unexpectedly appeared in Sent" \
					--arg subject "$draft_subject" \
					'all(.items[]?; .subject != $subject)'
			fi
			if capture_json_for mail.draft "could not inspect Inbox after draft creation" \
				mail list --folder default0/INBOX --limit 200; then
				assert_captured_jq mail.draft "draft subject unexpectedly appeared in Inbox" \
					--arg subject "$draft_subject" \
					'all(.items[]?; .subject != $subject)'
			fi

			# Exercise server pagination, filtering, recursive traversal, safe optional
			# metadata, and JSONL rendering against an owned message rather than exposing
			# arbitrary mailbox contents in test output.
			if capture_json_for mail.list "could not list the owned draft through paginated JSONL" \
				mail list --folder "$draft_folder" --all --recursive --page-size 1 \
				--fields folder,to,cc,size,sent_date,received_date,attachment,seen,flagged \
				--to-domain "${MBX_USERNAME#*@}" \
				--since 2020-01-01T00:00:00Z --until 2035-01-01T00:00:00Z \
				--format jsonl; then
				assert_captured_jq mail.list "owned draft was absent or malformed in rich JSONL output" \
					-s --arg id "$draft_id" --arg folder "$draft_folder" \
					--arg subject "$draft_subject" --arg recipient "$MBX_USERNAME" \
					'length > 0 and any(.[];
						.id == $id and .folder == $folder and .subject == $subject
						and any(.to[]?; .address == $recipient)
						and (.date | type) == "string" and .received_date == .date
						and (.size | type) == "number" and .size > 0
						and .attachment == false
						and (.seen | type) == "boolean" and (.flagged | type) == "boolean"
						and ((has("sent_date") | not) or (.sent_date | type) == "string"))'
			fi
			if capture_json_for mail.flag "could not read draft immediately before flag update" \
				mail get --folder "$draft_folder" --id "$draft_id" &&
				assert_captured_jq mail.flag "pre-update draft identity or ownership fields did not match" \
					--arg id "$draft_id" --arg folder "$draft_folder" \
					--arg subject "$draft_subject" --arg recipient "$MBX_USERNAME" \
					'.id == $id and .folder == $folder and .subject == $subject
					and (.to | type) == "array" and any(.to[]?; .address == $recipient)
					and (.seen | type) == "boolean" and (.flagged | type) == "boolean"'; then
				draft_seen_target="$(jq -r '.seen | not' "$ASSERTION_STDOUT")"
				draft_flagged_target="$(jq -r '.flagged | not' "$ASSERTION_STDOUT")"
				if run_op mail.flag mail flag --folder "$draft_folder" --id "$draft_id" \
					--seen "$draft_seen_target" --flagged "$draft_flagged_target"; then
					assert_last_jq mail.flag "flag update result was not the exact success object" \
						'type == "object" and length == 1 and .updated == true'
					if capture_json_for mail.flag "could not independently read draft after flag update" \
						mail get --folder "$draft_folder" --id "$draft_id"; then
						assert_captured_jq mail.flag "flag update was not visible on the requested draft" \
							--arg id "$draft_id" --arg folder "$draft_folder" \
							--arg subject "$draft_subject" --arg recipient "$MBX_USERNAME" \
							--argjson seen "$draft_seen_target" --argjson flagged "$draft_flagged_target" \
							'.id == $id and .folder == $folder and .subject == $subject
							and (.to | type) == "array" and any(.to[]?; .address == $recipient)
							and .seen == $seen and .flagged == $flagged'
					fi
				fi
			fi

			# Copy is non-idempotent: read the exact owned source, execute once, then trust the
			# returned id only after both destination equality and source preservation hold.
			if capture_json_for mail.copy "could not read draft immediately before copy" \
				mail get --folder "$draft_folder" --id "$draft_id" &&
				assert_captured_jq mail.copy "copy source identity or ownership fields did not match" \
					--arg id "$draft_id" --arg folder "$draft_folder" \
					--arg subject "$draft_subject" --arg recipient "$MBX_USERNAME" \
					'.id == $id and .folder == $folder and .subject == $subject
					and any(.to[]?; .address == $recipient)
					and (.size | type) == "number" and .size > 0
					and (.attachment | type) == "boolean"
					and (.seen | type) == "boolean" and (.flagged | type) == "boolean"'; then
				copy_source_size="$(jq -r '.size' "$ASSERTION_STDOUT")"
				copy_source_attachment="$(jq -r '.attachment' "$ASSERTION_STDOUT")"
				copy_source_seen="$(jq -r '.seen' "$ASSERTION_STDOUT")"
				copy_source_flagged="$(jq -r '.flagged' "$ASSERTION_STDOUT")"
				if run_op mail.copy mail copy --folder "$draft_folder" --id "$draft_id" \
					--to default0/Trash; then
					if assert_last_jq mail.copy "copy result did not contain exactly a new id and Trash folder" \
						'type == "object" and length == 2 and (.id | type) == "string" and (.id | length) > 0 and .folder == "default0/Trash"'; then
						copy_candidate_id="$(jq -r '.id' "$LAST_STDOUT")"
						if capture_json_for mail.copy "could not read copied message in Trash" \
							mail get --folder default0/Trash --id "$copy_candidate_id" &&
							assert_captured_jq mail.copy "copied message fields did not match the source" \
								--arg id "$copy_candidate_id" --arg subject "$draft_subject" \
								--arg recipient "$MBX_USERNAME" --argjson size "$copy_source_size" \
								--argjson attachment "$copy_source_attachment" \
								--argjson seen "$copy_source_seen" --argjson flagged "$copy_source_flagged" \
								'.id == $id and .folder == "default0/Trash" and .subject == $subject
								and any(.to[]?; .address == $recipient) and .size == $size
								and .attachment == $attachment and .seen == $seen and .flagged == $flagged'; then
							if capture_json_for mail.copy "could not re-read source after copy" \
								mail get --folder "$draft_folder" --id "$draft_id" &&
								assert_captured_jq mail.copy "copy removed or changed the source message" \
									--arg id "$draft_id" --arg folder "$draft_folder" \
									--arg subject "$draft_subject" --arg recipient "$MBX_USERNAME" \
									--argjson size "$copy_source_size" \
									--argjson attachment "$copy_source_attachment" \
									--argjson seen "$copy_source_seen" --argjson flagged "$copy_source_flagged" \
									'.id == $id and .folder == $folder and .subject == $subject
									and any(.to[]?; .address == $recipient) and .size == $size
									and .attachment == $attachment and .seen == $seen and .flagged == $flagged'; then
								copied_mail_id="$copy_candidate_id"
							fi
						fi
					fi
				fi
			fi

			# Moving may assign a destination-folder id. Trust it only after a destination read
			# and a complete source-folder listing prove the exact transition.
			if capture_json_for mail.move "could not read draft immediately before move" \
				mail get --folder "$draft_folder" --id "$draft_id" &&
				assert_captured_jq mail.move "move source identity or ownership fields did not match" \
					--arg id "$draft_id" --arg folder "$draft_folder" \
					--arg subject "$draft_subject" --arg recipient "$MBX_USERNAME" \
					'.id == $id and .folder == $folder and .subject == $subject
					and any(.to[]?; .address == $recipient)
					and (.size | type) == "number" and .size > 0
					and (.attachment | type) == "boolean"
					and (.seen | type) == "boolean" and (.flagged | type) == "boolean"
					and (.fingerprint | test("^sha256:[0-9a-f]{64}$"))'; then
				move_source_size="$(jq -r '.size' "$ASSERTION_STDOUT")"
				move_source_attachment="$(jq -r '.attachment' "$ASSERTION_STDOUT")"
				move_source_seen="$(jq -r '.seen' "$ASSERTION_STDOUT")"
				move_source_flagged="$(jq -r '.flagged' "$ASSERTION_STDOUT")"
				move_source_fingerprint="$(jq -r '.fingerprint' "$ASSERTION_STDOUT")"
				if capture_json_for mail.move "could not preflight the fingerprint-guarded move" \
					mail move --folder "$draft_folder" --id "$draft_id" \
					--to default0/Trash --expect-fingerprint "$move_source_fingerprint" --preflight &&
					assert_captured_jq mail.move "move preflight did not expose the exact normalized request" \
						--arg id "$draft_id" --arg folder "$draft_folder" \
						--arg fingerprint "$move_source_fingerprint" \
						'.preflight == true and .source == {folder:$folder,id:$id}
						and .destination == {folder:"default0/Trash",id:null}
						and .verified == false and .fingerprint == $fingerprint
						and .request.method == "PUT" and .request.endpoint == "/mail"
						and .request.action == "update"
						and .request.params == {folder:$folder,id:$id}
						and .request.body == {folder_id:"default0/Trash"}'; then
					if run_op mail.move mail move --folder "$draft_folder" --id "$draft_id" \
						--to default0/Trash --expect-fingerprint "$move_source_fingerprint" --verify; then
						if assert_last_jq mail.move "verified move result did not preserve both identities and fingerprint" \
							--arg id "$draft_id" --arg folder "$draft_folder" \
							--arg fingerprint "$move_source_fingerprint" \
							'type == "object" and length == 4
							and .source == {folder:$folder,id:$id}
							and .destination.folder == "default0/Trash"
							and (.destination.id | type) == "string" and (.destination.id | length) > 0
							and .verified == true and .fingerprint == $fingerprint'; then
							move_candidate_id="$(jq -r '.destination.id' "$LAST_STDOUT")"
							if [[ -n "$copied_mail_id" && "$move_candidate_id" == "$copied_mail_id" ]]; then
								mark_fail mail.move "move and copy returned the same id in Trash"
							elif capture_json_for mail.move "could not read moved message in Trash" \
								mail get --folder default0/Trash --id "$move_candidate_id" &&
								assert_captured_jq mail.move "moved message fields did not match the source" \
									--arg id "$move_candidate_id" --arg subject "$draft_subject" \
									--arg recipient "$MBX_USERNAME" --arg fingerprint "$move_source_fingerprint" \
									--argjson size "$move_source_size" --argjson attachment "$move_source_attachment" \
									--argjson seen "$move_source_seen" --argjson flagged "$move_source_flagged" \
									'.id == $id and .folder == "default0/Trash" and .subject == $subject
									and any(.to[]?; .address == $recipient) and .size == $size
									and .attachment == $attachment and .seen == $seen and .flagged == $flagged
									and .fingerprint == $fingerprint'; then
								if capture_json_for mail.move "could not list source folder after move" \
									mail list --folder "$draft_folder" --limit 200 &&
									assert_captured_jq mail.move "moved source id or unique subject remained in the source folder" \
										--arg id "$draft_id" --arg subject "$draft_subject" \
										'.truncated == false and all(.items[]?; ((.id | tostring) != $id) and .subject != $subject)'; then
									moved_mail_id="$move_candidate_id"
									draft_id=""
								fi
							fi
						fi
					fi
				fi
			fi

			# Delete in reverse mutation order. Each destructive call gets an exact pre-read,
			# one execution, and a complete Trash-list absence proof before tracking clears.
			if [[ -n "$moved_mail_id" ]] &&
				capture_json_for mail.delete "could not read moved message immediately before deletion" \
					mail get --folder default0/Trash --id "$moved_mail_id" &&
				assert_captured_jq mail.delete "moved delete target identity or marker did not match" \
					--arg id "$moved_mail_id" --arg subject "$draft_subject" \
					'.id == $id and .folder == "default0/Trash" and .subject == $subject'; then
				if run_op mail.delete mail delete --folder default0/Trash \
					--id "$moved_mail_id" --confirm "$moved_mail_id"; then
					assert_last_jq mail.delete "delete result did not match the moved message" \
						--arg id "$moved_mail_id" \
						'type == "object" and length == 2 and .deleted == true and .id == $id'
				fi
				if capture_json_for mail.delete "could not list Trash after deleting moved message" \
					mail list --folder default0/Trash --limit 200; then
					if [[ -n "$copied_mail_id" ]]; then
						if assert_captured_jq mail.delete "moved message remained or copied sibling disappeared" \
							--arg moved "$moved_mail_id" --arg copied "$copied_mail_id" \
							--arg subject "$draft_subject" \
							'.truncated == false
							and all(.items[]?; (.id | tostring) != $moved)
							and any(.items[]?; (.id | tostring) == $copied and .subject == $subject)'; then
							moved_mail_id=""
						fi
					elif assert_captured_jq mail.delete "moved message remained in Trash after deletion" \
						--arg moved "$moved_mail_id" \
						'.truncated == false and all(.items[]?; (.id | tostring) != $moved)'; then
						moved_mail_id=""
					fi
				fi
			fi

			if [[ -n "$copied_mail_id" ]] &&
				capture_json_for mail.delete "could not read copied message immediately before deletion" \
					mail get --folder default0/Trash --id "$copied_mail_id" &&
				assert_captured_jq mail.delete "copied delete target identity or marker did not match" \
					--arg id "$copied_mail_id" --arg subject "$draft_subject" \
					'.id == $id and .folder == "default0/Trash" and .subject == $subject'; then
				if run_op mail.delete mail delete --folder default0/Trash \
					--id "$copied_mail_id" --confirm "$copied_mail_id"; then
					assert_last_jq mail.delete "delete result did not match the copied message" \
						--arg id "$copied_mail_id" \
						'type == "object" and length == 2 and .deleted == true and .id == $id'
				fi
				if capture_json_for mail.delete "could not list Trash after deleting copied message" \
					mail list --folder default0/Trash --limit 200; then
					if [[ -z "$moved_mail_id" ]]; then
						if assert_captured_jq mail.delete "mail fixture marker remained in Trash after deletion" \
							--arg copied "$copied_mail_id" --arg subject "$draft_subject" \
							'.truncated == false
							and all(.items[]?; ((.id | tostring) != $copied) and .subject != $subject)'; then
							copied_mail_id=""
						fi
					elif assert_captured_jq mail.delete "copied message remained in Trash after deletion" \
						--arg copied "$copied_mail_id" \
						'.truncated == false and all(.items[]?; (.id | tostring) != $copied)'; then
						copied_mail_id=""
					fi
				fi
			fi
		fi
	fi

	# A separate owned draft proves that move-many can validate offline, preflight online,
	# move once, durably checkpoint, and resume without replaying the mutation.
	bulk_subject="$FIXTURE_PREFIX-bulk-move"
	bulk_move_plan="$LIVE_TMP/mail-move-plan.json"
	bulk_checkpoint="$LIVE_TMP/mail-move-checkpoint.jsonl"
	if run_op mail.draft mail draft --to "$MBX_USERNAME" \
		--subject "$bulk_subject" --body "live bulk move fixture"; then
		bulk_source_id="$(jq -r '.id // empty' "$LAST_STDOUT")"
		bulk_source_folder="$(jq -r '.folder // empty' "$LAST_STDOUT")"
		if [[ -n "$bulk_source_id" && "$bulk_source_folder" == "default0/Drafts" ]] &&
			capture_json_for mail.move-many "could not read the bulk-move source draft" \
				mail get --folder "$bulk_source_folder" --id "$bulk_source_id" &&
			assert_captured_jq mail.move-many "bulk-move source identity or fingerprint was invalid" \
				--arg id "$bulk_source_id" --arg folder "$bulk_source_folder" \
				--arg subject "$bulk_subject" --arg recipient "$MBX_USERNAME" \
				'.id == $id and .folder == $folder and .subject == $subject
				and any(.to[]?; .address == $recipient)
				and (.fingerprint | test("^sha256:[0-9a-f]{64}$"))'; then
			bulk_fingerprint="$(jq -r '.fingerprint' "$ASSERTION_STDOUT")"
			jq -cn --arg folder "$bulk_source_folder" --arg id "$bulk_source_id" \
				--arg fingerprint "$bulk_fingerprint" \
				'[{folder:$folder,id:$id,to:"default0/Trash",expect_fingerprint:$fingerprint}]' \
				>"$bulk_move_plan"

			bulk_plan_ready=0
			if capture_json_for mail.move-many "could not validate the bulk move offline" \
				mail move-many --input "@$bulk_move_plan" --dry-run &&
				assert_captured_jq mail.move-many "bulk dry-run did not preserve normalized identities" \
					--arg id "$bulk_source_id" --arg folder "$bulk_source_folder" \
					--arg fingerprint "$bulk_fingerprint" \
					'.dry_run == true and .count == 1 and .moved == 0 and .resumed == 0
					and (.items | length) == 1 and .items[0].status == "planned"
					and .items[0].source == {folder:$folder,id:$id}
					and .items[0].destination == {folder:"default0/Trash",id:null}
					and .items[0].verified == false and .items[0].fingerprint == $fingerprint' &&
				capture_json_for mail.move-many "could not preflight the bulk move" \
					mail move-many --input "@$bulk_move_plan" --preflight &&
				assert_captured_jq mail.move-many "bulk preflight did not validate the exact owned source" \
					--arg id "$bulk_source_id" --arg folder "$bulk_source_folder" \
					--arg fingerprint "$bulk_fingerprint" \
					'.preflight == true and .count == 1 and .moved == 0 and .resumed == 0
					and (.items | length) == 1 and .items[0].status == "ready"
					and .items[0].source == {folder:$folder,id:$id}
					and .items[0].destination == {folder:"default0/Trash",id:null}
					and .items[0].verified == false and .items[0].fingerprint == $fingerprint'; then
				bulk_plan_ready=1
			fi

			if ((bulk_plan_ready == 1)) &&
				run_op mail.move-many mail move-many --input "@$bulk_move_plan" \
					--verify --checkpoint "$bulk_checkpoint"; then
				if assert_last_jq mail.move-many "bulk move did not return one verified checkpointed result" \
					--arg id "$bulk_source_id" --arg folder "$bulk_source_folder" \
					--arg fingerprint "$bulk_fingerprint" --arg checkpoint "$bulk_checkpoint" \
					'.count == 1 and .moved == 1 and .resumed == 0 and .checkpoint == $checkpoint
					and (.items | length) == 1 and .items[0].status == "moved"
					and .items[0].source == {folder:$folder,id:$id}
					and .items[0].destination.folder == "default0/Trash"
					and (.items[0].destination.id | type) == "string"
					and (.items[0].destination.id | length) > 0
					and .items[0].verified == true and .items[0].fingerprint == $fingerprint'; then
					bulk_destination_id="$(jq -r '.items[0].destination.id' "$LAST_STDOUT")"
					if [[ "$(stat -c '%a' "$bulk_checkpoint" 2>/dev/null || true)" != "600" ]]; then
						mark_fail mail.move-many "checkpoint permissions were not owner-only"
					elif ! jq -se --arg id "$bulk_destination_id" --arg fingerprint "$bulk_fingerprint" \
						'length == 1 and .[0].destination_folder == "default0/Trash"
						and .[0].destination_id == $id and .[0].fingerprint == $fingerprint
						and .[0].verified == true' "$bulk_checkpoint" >/dev/null 2>&1; then
						mark_fail mail.move-many "checkpoint did not contain exactly one verified durable record"
					elif capture_json_for mail.move-many "could not resume the completed bulk move" \
						mail move-many --input "@$bulk_move_plan" \
						--verify --checkpoint "$bulk_checkpoint" &&
						assert_captured_jq mail.move-many "resume replayed or changed the completed bulk move" \
							--arg id "$bulk_destination_id" --arg fingerprint "$bulk_fingerprint" \
							'.count == 1 and .moved == 0 and .resumed == 1
							and (.items | length) == 1 and .items[0].status == "resumed"
							and .items[0].destination == {folder:"default0/Trash",id:$id}
							and .items[0].verified == true and .items[0].fingerprint == $fingerprint' &&
						capture_json_for mail.move-many "could not read the resumed bulk destination" \
							mail get --folder default0/Trash --id "$bulk_destination_id" &&
						assert_captured_jq mail.move-many "resumed bulk destination did not match the source" \
							--arg id "$bulk_destination_id" --arg subject "$bulk_subject" \
							--arg fingerprint "$bulk_fingerprint" \
							'.id == $id and .folder == "default0/Trash" and .subject == $subject
							and .fingerprint == $fingerprint'; then
						if ! cleanup_mail default0/Trash "$bulk_destination_id"; then
							mark_fail mail.move-many "could not clean up the verified bulk-move destination"
						elif capture_json_for mail.move-many "could not verify bulk-move cleanup" \
							mail list --folder default0/Trash --limit 200; then
							assert_captured_jq mail.move-many "bulk-move marker remained after cleanup" \
								--arg id "$bulk_destination_id" --arg subject "$bulk_subject" \
								'.truncated == false and all(.items[]?;
									((.id | tostring) != $id) and .subject != $subject)'
						fi
					fi
				fi
			fi
		else
			mark_fail mail.move-many "bulk source draft could not be independently verified"
		fi
	fi

	# One real message, addressed only to the dedicated account itself.
	mail_attachment_path="$LIVE_TMP/mail-attachment.txt"
	printf 'mail attachment %s\n' "$RUN_STAMP" >"$mail_attachment_path"
	mail_subject="$FIXTURE_PREFIX-self-mail"
	if capture_json_for mail.send "could not produce the irreversible-send preview" \
		mail send --to "$MBX_USERNAME" --subject "$mail_subject" \
		--body "live API fixture" --attach "$mail_attachment_path" --dry-run &&
		assert_captured_jq mail.send "send preview did not name the exact recipient, subject, and attachment" \
			--arg recipient "$MBX_USERNAME" --arg subject "$mail_subject" \
			'.dry_run == true and .to == [$recipient] and .subject == $subject
			and .attachments == ["mail-attachment.txt"]
			and (has("cc") | not) and (has("bcc") | not)'; then
		if run_op mail.send mail send --to "$MBX_USERNAME" --subject "$mail_subject" \
			--body "live API fixture" --attach "$mail_attachment_path"; then
			assert_last_jq mail.send "send result did not match the exact recipient and subject" \
				--arg recipient "$MBX_USERNAME" --arg subject "$mail_subject" \
				'type == "object" and length == 4 and .sent == true
				and (.id | type) == "string" and (.id | length) > 0
				and .to == [$recipient] and .subject == $subject'

			sent_mail_id=""
			inbox_mail_id=""
			delivery_ambiguous=0
			for _ in 1 2 3 4 5 6 7 8 9 10; do
				if quiet_json mail list --folder default0/Sent --limit 200 &&
					jq -e '.truncated == false' "$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
					sent_match_count="$(jq -r --arg subject "$mail_subject" \
						'[.items[]? | select(.subject == $subject)] | length' \
						"$LIVE_TMP/cleanup.json")"
					if [[ "$sent_match_count" == "1" ]]; then
						sent_mail_id="$(jq -r --arg subject "$mail_subject" \
							'.items[]? | select(.subject == $subject) | .id' \
							"$LIVE_TMP/cleanup.json")"
					elif ((sent_match_count > 1)); then
						mark_fail mail.send "unique subject matched multiple messages in Sent"
						delivery_ambiguous=1
					fi
				fi
				if quiet_json mail list --folder default0/INBOX --limit 200 &&
					jq -e '.truncated == false' "$LIVE_TMP/cleanup.json" >/dev/null 2>&1; then
					inbox_match_count="$(jq -r --arg subject "$mail_subject" \
						'[.items[]? | select(.subject == $subject)] | length' \
						"$LIVE_TMP/cleanup.json")"
					if [[ "$inbox_match_count" == "1" ]]; then
						inbox_mail_id="$(jq -r --arg subject "$mail_subject" \
							'.items[]? | select(.subject == $subject) | .id' \
							"$LIVE_TMP/cleanup.json")"
					elif ((inbox_match_count > 1)); then
						mark_fail mail.send "unique subject matched multiple messages in Inbox"
						delivery_ambiguous=1
					fi
				fi
				((delivery_ambiguous == 1)) && break
				[[ -n "$sent_mail_id" && -n "$inbox_mail_id" ]] && break
				sleep 2
			done

			if ((delivery_ambiguous == 0)) && [[ -n "$sent_mail_id" && -n "$inbox_mail_id" ]]; then
				sent_delivery_verified=0
				inbox_delivery_verified=0
				if capture_json_for mail.send "could not read the delivered Sent copy" \
					mail get --folder default0/Sent --id "$sent_mail_id" &&
					assert_captured_jq mail.send "Sent copy metadata did not match the self-addressed message" \
						--arg id "$sent_mail_id" --arg subject "$mail_subject" \
						--arg sender "$authenticated_email" --arg recipient "$MBX_USERNAME" \
						'.id == $id and .folder == "default0/Sent" and .subject == $subject
						and any(.from[]?; .address == $sender) and any(.to[]?; .address == $recipient)
						and (.size | type) == "number" and .size > 0 and .attachment == true
						and (.seen | type) == "boolean" and (.flagged | type) == "boolean"'; then
					sent_delivery_verified=1
				fi
				if capture_json_for mail.send "could not read the delivered Inbox copy" \
					mail get --folder default0/INBOX --id "$inbox_mail_id" &&
					assert_captured_jq mail.send "Inbox copy metadata did not match the self-addressed message" \
						--arg id "$inbox_mail_id" --arg subject "$mail_subject" \
						--arg sender "$authenticated_email" --arg recipient "$MBX_USERNAME" \
						'.id == $id and .folder == "default0/INBOX" and .subject == $subject
						and any(.from[]?; .address == $sender) and any(.to[]?; .address == $recipient)
						and (.size | type) == "number" and .size > 0 and .attachment == true
						and (.seen | type) == "boolean" and (.flagged | type) == "boolean"'; then
					inbox_delivery_verified=1
				fi

				if ((sent_delivery_verified == 1 && inbox_delivery_verified == 1)); then
					send_cleanup_verified=1
					cleanup_mail default0/INBOX "$inbox_mail_id" || send_cleanup_verified=0
					cleanup_mail default0/Sent "$sent_mail_id" || send_cleanup_verified=0
					for mail_folder in default0/INBOX default0/Sent default0/Drafts default0/Trash; do
						if ! capture_json_for mail.send "could not verify mail delivery cleanup in $mail_folder" \
							mail list --folder "$mail_folder" --limit 200 ||
							! assert_captured_jq mail.send "delivered mail marker remained in $mail_folder" \
								--arg subject "$mail_subject" \
								'.truncated == false and all(.items[]?; .subject != $subject)'; then
							send_cleanup_verified=0
						fi
					done
					if ((send_cleanup_verified == 1)); then
						sent_mail_id=""
						inbox_mail_id=""
					else
						mark_fail mail.send "could not prove complete delivery cleanup"
					fi
				fi
			else
				mark_fail mail.send "self-addressed message was not observed exactly once in both Sent and Inbox"
			fi
		fi
	fi

	# Password mutation is intentionally opt-in even within full mode. A successful change
	# is reverted immediately; mailbox.org currently may return SRV-0001 for this service.
	if [[ "${MBX_LIVE_PASSWORD_ROUNDTRIP:-0}" == "1" ]]; then
		temporary_password="MbxLive-${RUN_STAMP//[^0-9]/}-Aa9!"
		# Passwords go through the environment, not argv, so they stay out of the process list.
		if MBX_PASSWORD="$ORIGINAL_PASSWORD" MBX_NEW_PASSWORD="$temporary_password" \
			run_op change-password change-password \
			--confirm "$MBX_USERNAME"; then
			password_changed=1
			export MBX_PASSWORD="$temporary_password"
			if MBX_NEW_PASSWORD="$ORIGINAL_PASSWORD" quiet_json change-password \
				--confirm "$MBX_USERNAME"; then
				export MBX_PASSWORD="$ORIGINAL_PASSWORD"
				password_changed=0
			else
				mark_fail change-password "password changed, but immediate rollback failed"
			fi
		else
			password_error="$(error_code)"
			if [[ "$password_error" == "SRV-0001" ]]; then
				mark_limited change-password "mailbox.org password service returned SRV-0001"
			fi
		fi
	else
		mark_skipped change-password "set MBX_LIVE_PASSWORD_ROUNDTRIP=1 to run the reversible password test"
	fi
fi

cleanup_fixtures

# Close the first session, prove that neither local nor provider-backed reusable state
# survives, then perform one fresh credential login and a final verified logout.
first_logout_verified=0
if run_op logout logout; then
	if assert_last_jq logout "logout output was not the exact success object" \
		'type == "object" and length == 1 and .status == "ok"' &&
		assert_session_absent_for logout "logout left the isolated session file behind"; then
		first_logout_verified=1
	fi
fi
if ((first_logout_verified == 1)); then
	if expect_session_unavailable_for logout \
		"an unauthenticated process could still reuse state after logout" whoami; then
		assert_session_absent_for logout "rejected post-logout authentication recreated session state"
	fi
fi

if run_op login login; then
	if assert_last_jq login "fresh reauthentication output did not match the configured account" \
		--arg username "$MBX_USERNAME" \
		'type == "object" and length == 2 and .status == "ok" and .username == $username' &&
		assert_session_present_for login "fresh reauthentication did not persist a session"; then
		if capture_session_reuse_for login \
			"fresh reauthentication did not yield a reusable session" whoami; then
			assert_captured_jq login "freshly reauthenticated identity did not match the account" \
				--arg username "$MBX_USERNAME" --arg prior_id "$authenticated_user_id" \
				'type == "object" and length == 3
				and (.email | ascii_downcase) == ($username | ascii_downcase)
				and (.display_name | type) == "string"
				and (.user_id | type) == "number" and (.user_id | floor) == .user_id
				and .user_id > 0
				and ($prior_id == "" or (.user_id | tostring) == $prior_id)'
		fi
	fi
fi
if run_op logout logout; then
	assert_last_jq logout "final logout output was not the exact success object" \
		'type == "object" and length == 1 and .status == "ok"'
	assert_session_absent_for logout "final logout left the isolated session file behind"
fi

if [[ "$MODE" == "full" ]]; then
	for operation in "${REGISTERED_OPERATIONS[@]}"; do
		if [[ -z "${STATUS[$operation]:-}" ]]; then
			mark_fail "$operation" "not exercised by the full matrix"
		fi
	done
fi

pass_count=0
limited_count=0
skipped_count=0
fail_count=0
for operation in "${!STATUS[@]}"; do
	case "${STATUS[$operation]}" in
	pass) ((pass_count++)) ;;
	limited) ((limited_count++)) ;;
	skipped) ((skipped_count++)) ;;
	fail) ((fail_count++)) ;;
	esac
done

printf '\nLive matrix: mode=%s pass=%d limited=%d skipped=%d fail=%d cleanup_failures=%d registered=%d\n' \
	"$MODE" "$pass_count" "$limited_count" "$skipped_count" "$fail_count" \
	"$CLEANUP_FAILURES" "${#REGISTERED_OPERATIONS[@]}"

if ((fail_count > 0 || CLEANUP_FAILURES > 0)); then
	exit 1
fi

# Sourced by smoke and validate from examples/. CHECK_DIR, CHECK_FRAMES and
# CHECK_VALIDATE belong to the caller; status accumulates failures across runs.
# Preserve both attempts on disk, even when a startup retry succeeds.
mkdir -p "$CHECK_DIR" || exit 1
CHECK_DIR=$(mktemp -d -p "$(pwd)/$CHECK_DIR" run-XXXXXX) || exit 1
check_number=0
probe=$(printf '%s\n' 'VULKAN ERROR: self-check' | grep -cE 'VULKAN (ERROR|WARNING)' || true)
if [ "$probe" != 1 ]; then
  echo 'SELF-CHECK: cannot count validation messages -- this gate proves nothing'
  exit 1
fi

check() {
  ex=$1; shift
  label=$ex
  if [ $# -gt 0 ]; then label="$ex $*"; fi
  check_number=$((check_number + 1))
  attempt=1
  check_attempt "$@"
  if [ "$startup_failure" -eq 1 ]; then
    echo "$label: retrying startup once (attempt 2)"
    attempt=2
    check_attempt "$@"
  fi
  if [ "$attempt_failed" -eq 1 ]; then status=1; fi
  return 0
}

check_attempt() {
  startup_failure=0
  attempt_failed=1
  logfile="$CHECK_DIR/$check_number-$ex-attempt-$attempt.log"
  code=0
  go run "./$ex" -frames "$CHECK_FRAMES" "$@" >"$logfile" 2>&1 || code=$?
  echo "$label: attempt $attempt exit=$code log=$logfile"
  msgs=$(grep -cE 'VULKAN (ERROR|WARNING)' "$logfile" || true)
  # Never hide a validation failure behind a successful process retry.
  if [ "$msgs" -ne 0 ]; then
    leaks=$(grep -c 'has not been destroyed' "$logfile" || true)
    echo "$label: $msgs validation message(s), $leaks of them leaked objects"
    cat "$logfile"
    status=1
    if [ "$GLYPHENGINE_SYNC_VALIDATION" = 1 ]; then exit 1; fi
    return 0
  fi
  if [ "$code" -ne 0 ]; then
    # New's exact creation error, with neither a successful swapchain nor
    # an initialized renderer/frame. Surface queries, image views, rebuilds,
    # draw errors and unrelated "unknown" errors are not startup flakes.
    if grep -qE 'renderer: create swapchain: vkCreateSwapchainKHR: VkResult=-[0-9]+ ' "$logfile" &&
       ! grep -qE 'Swapchain created:|Renderer initialized successfully|rendered [0-9]+ frames|draw error:|renderer: recreate' "$logfile"; then
      result=$(grep -E 'renderer: create swapchain: vkCreateSwapchainKHR: VkResult=' "$logfile")
      echo "$label: STARTUP FAILURE (swapchain), attempt $attempt: $result"
      cat "$logfile"
      if [ "$attempt" -eq 1 ]; then
        startup_failure=1
        return 0
      fi
    else
      echo "$label: FAILED TO RUN (rendering or other failure)"
      cat "$logfile"
    fi
    status=1
    return 0
  fi
  if [ "$CHECK_VALIDATE" = 1 ]; then
    if ! grep -q 'Vulkan validation layer enabled' "$logfile"; then
      echo "$label: VALIDATION LAYER DID NOT RUN"; cat "$logfile"; status=1; return 0
    fi
    if [ "$GLYPHENGINE_SYNC_VALIDATION" = 1 ] && ! grep -q 'Vulkan synchronization validation enabled' "$logfile"; then
      echo "$label: SYNCHRONIZATION VALIDATION DID NOT RUN"; cat "$logfile"; status=1; return 0
    fi
  fi
  if [ "$attempt" -eq 2 ]; then
    echo "$label: startup retry succeeded; both attempt logs retained"
    cat "$logfile"
  fi
  echo "$label: clean"
  attempt_failed=0
  return 0
}

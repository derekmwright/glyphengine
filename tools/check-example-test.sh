# Run by task check:self-test from the repository root, using Task's own shell.
(
  cd examples || exit 1
  CHECK_DIR=../.task/check-self-test
  CHECK_FRAMES=1
  CHECK_VALIDATE=1
  GLYPHENGINE_SYNC_VALIDATION=0
  . ../tools/check-example.sh
  startup='renderer: create swapchain: vkCreateSwapchainKHR: VkResult=-13 (unknown): vulkan error: unknown'
  # Function replacement exercises the real check with deterministic process
  # results; no fault-injection switch is shipped in the renderer.
  go() {
    calls=$((calls + 1))
    if [ "$scenario" != missing ]; then echo 'Vulkan validation layer enabled'; fi
    case "$scenario" in
      once) if [ "$calls" -eq 1 ]; then echo "$startup"; return 1; fi ;;
      always) echo "$startup"; return 1 ;;
      surface) echo 'renderer: create swapchain: vkGetPhysicalDeviceSurfaceCapabilitiesKHR: VkResult=-13 (unknown)'; return 1 ;;
      imageview) echo 'renderer: create swapchain: create swapchain image view 0: vulkan error: unknown'; return 1 ;;
      rebuild) echo 'Renderer initialized successfully'; echo 'renderer: recreate swapchain: vkCreateSwapchainKHR: VkResult=-13 (unknown)'; return 1 ;;
      late) echo 'Swapchain created: format=test'; echo "$startup"; return 1 ;;
      validation) echo 'VULKAN ERROR: forced validation failure'; echo "$startup"; return 1 ;;
      other) echo 'renderer: create logical device: vulkan error: unknown'; return 1 ;;
      retrydraw) if [ "$calls" -eq 1 ]; then echo "$startup"; else echo 'draw: vulkan error: device lost'; fi; return 1 ;;
      retryvalidation) if [ "$calls" -eq 1 ]; then echo "$startup"; else echo 'VULKAN WARNING: injected on retry'; fi; return 1 ;;
      sync) return 0 ;;
    esac
    echo 'Renderer initialized successfully'
    echo 'rendered 1 frames, exiting'
  }
  test_case() {
    scenario=$1; expected_calls=$2; expected_status=$3
    calls=0; status=0
    check fixture >"$CHECK_DIR/$scenario.txt" 2>&1
    cat "$CHECK_DIR/$scenario.txt"
    if [ "$calls" -ne "$expected_calls" ] || [ "$status" -ne "$expected_status" ]; then
      echo "FAIL $scenario: calls=$calls status=$status; wanted $expected_calls/$expected_status"
      exit 1
    fi
    case "$scenario" in
      once|always|retrydraw|retryvalidation)
        grep -q 'STARTUP FAILURE (swapchain).*VkResult=-13' "$CHECK_DIR/$scenario.txt" || exit 1
        grep -q 'retrying startup once' "$CHECK_DIR/$scenario.txt" || exit 1
        test -s "$CHECK_DIR/$check_number-fixture-attempt-1.log" || exit 1
        test -s "$CHECK_DIR/$check_number-fixture-attempt-2.log" || exit 1 ;;
      *) if grep -q 'STARTUP FAILURE (swapchain)' "$CHECK_DIR/$scenario.txt"; then exit 1; fi ;;
    esac
  }
  test_case once 2 0
  test_case always 2 1
  test_case surface 1 1
  test_case imageview 1 1
  test_case rebuild 1 1
  test_case late 1 1
  test_case validation 1 1
  test_case other 1 1
  test_case missing 1 1
  test_case retrydraw 2 1
  test_case retryvalidation 2 1
  GLYPHENGINE_SYNC_VALIDATION=1
  test_case sync 1 1
  GLYPHENGINE_SYNC_VALIDATION=0
  CHECK_VALIDATE=0
  test_case once 2 0
  test_case always 2 1
  echo 'check:self-test: PASS (14 cases)'
)

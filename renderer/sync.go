package renderer

import (
	"log"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// maxFramesInFlight is the number of frames that can be recorded/submitted
// concurrently. Each in-flight frame gets its own fence, semaphores, and
// command buffer to allow CPU/GPU overlap.
const maxFramesInFlight = 2

// syncObjects holds per-frame Vulkan synchronization primitives.
type syncObjects struct {
	imageAvailable [maxFramesInFlight]core1_0.Semaphore
	renderFinished [maxFramesInFlight]core1_0.Semaphore
	inFlight       [maxFramesInFlight]core1_0.Fence
}

// createSyncObjects allocates fences and semaphores for each in-flight frame.
// Fences are created pre-signaled so the first frames don't block.
func createSyncObjects(deviceDriver core1_0.DeviceDriver) (*syncObjects, error) {
	var s syncObjects

	for i := 0; i < maxFramesInFlight; i++ {
		imageAvail, _, err := deviceDriver.CreateSemaphore(nil, core1_0.SemaphoreCreateInfo{})
		if err != nil {
			return nil, err
		}
		s.imageAvailable[i] = imageAvail

		renderDone, _, err := deviceDriver.CreateSemaphore(nil, core1_0.SemaphoreCreateInfo{})
		if err != nil {
			return nil, err
		}
		s.renderFinished[i] = renderDone

		fence, _, err := deviceDriver.CreateFence(nil, core1_0.FenceCreateInfo{
			Flags: core1_0.FenceCreateSignaled,
		})
		if err != nil {
			return nil, err
		}
		s.inFlight[i] = fence
	}

	log.Printf("Sync objects created (%d frames in flight)", maxFramesInFlight)
	return &s, nil
}

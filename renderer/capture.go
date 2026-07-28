package renderer

import (
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"unsafe"

	"github.com/vkngwrapper/core/v3/core1_0"
	"github.com/vkngwrapper/extensions/v3/khr_swapchain"
)

// CaptureFrame reads the most recently presented swapchain image back from the
// GPU and returns it as an RGBA image.
//
// Call it after DrawFrame. It waits for the device to go idle, so it is not
// something to do every frame — this is for screenshots, documentation images,
// and eventually golden-image comparison, not for streaming.
//
// It fails if the surface did not support TRANSFER_SRC on its swapchain
// images, which the spec permits. That is rare on desktop, and reported at
// swapchain creation.
func (r *Renderer) CaptureFrame() (*image.RGBA, error) {
	if !r.sc.captureCapable {
		return nil, errors.New("renderer: swapchain images lack TRANSFER_SRC; frame capture unavailable on this surface")
	}

	// The last image presented is the one before the current frame index.
	idx := r.lastPresented
	if idx < 0 || idx >= len(r.sc.images) {
		return nil, errors.New("renderer: no frame has been presented yet")
	}
	src := r.sc.images[idx]

	w := r.sc.extent.Width
	h := r.sc.extent.Height
	size := w * h * 4

	// Everything must be finished with the image before it can be copied.
	r.deviceDriver.DeviceWaitIdle()

	buf, mem, err := r.createBuffer(size,
		core1_0.BufferUsageTransferDst,
		core1_0.MemoryPropertyHostVisible|core1_0.MemoryPropertyHostCoherent)
	if err != nil {
		return nil, fmt.Errorf("renderer: capture staging buffer: %w", err)
	}
	defer func() {
		r.deviceDriver.DestroyBuffer(buf, nil)
		r.deviceDriver.FreeMemory(mem, nil)
	}()

	cmdBuf, err := r.beginSingleTimeCommands()
	if err != nil {
		return nil, fmt.Errorf("renderer: capture command buffer: %w", err)
	}

	subresource := core1_0.ImageSubresourceRange{
		AspectMask: core1_0.ImageAspectColor,
		LevelCount: 1,
		LayerCount: 1,
	}

	// PresentSrc -> TransferSrc, copy, and back again. Restoring the layout
	// matters: the image stays in the swapchain rotation and will be presented
	// again, so leaving it in TransferSrc would be undefined behaviour on the
	// next present.
	r.deviceDriver.CmdPipelineBarrier(cmdBuf,
		core1_0.PipelineStageTopOfPipe, core1_0.PipelineStageTransfer, 0, nil, nil,
		[]core1_0.ImageMemoryBarrier{{
			OldLayout:           khr_swapchain.ImageLayoutPresentSrc,
			NewLayout:           core1_0.ImageLayoutTransferSrcOptimal,
			SrcQueueFamilyIndex: -1,
			DstQueueFamilyIndex: -1,
			Image:               src,
			SubresourceRange:    subresource,
			SrcAccessMask:       0,
			DstAccessMask:       core1_0.AccessTransferRead,
		}})

	r.deviceDriver.CmdCopyImageToBuffer(cmdBuf, src,
		core1_0.ImageLayoutTransferSrcOptimal, buf,
		core1_0.BufferImageCopy{
			ImageSubresource: core1_0.ImageSubresourceLayers{
				AspectMask: core1_0.ImageAspectColor,
				LayerCount: 1,
			},
			ImageExtent: core1_0.Extent3D{Width: w, Height: h, Depth: 1},
		})

	r.deviceDriver.CmdPipelineBarrier(cmdBuf,
		core1_0.PipelineStageTransfer, core1_0.PipelineStageBottomOfPipe, 0, nil, nil,
		[]core1_0.ImageMemoryBarrier{{
			OldLayout:           core1_0.ImageLayoutTransferSrcOptimal,
			NewLayout:           khr_swapchain.ImageLayoutPresentSrc,
			SrcQueueFamilyIndex: -1,
			DstQueueFamilyIndex: -1,
			Image:               src,
			SubresourceRange:    subresource,
			SrcAccessMask:       core1_0.AccessTransferRead,
			DstAccessMask:       0,
		}})

	if err := r.endSingleTimeCommands(cmdBuf); err != nil {
		return nil, fmt.Errorf("renderer: capture submit: %w", err)
	}

	data, _, err := r.deviceDriver.MapMemory(mem, 0, size, 0)
	if err != nil {
		return nil, fmt.Errorf("renderer: map capture buffer: %w", err)
	}
	defer r.deviceDriver.UnmapMemory(mem)

	raw := unsafe.Slice((*byte)(data), size)
	img := image.NewRGBA(image.Rect(0, 0, w, h))

	// Swapchain format is B8G8R8A8; PNG wants RGBA, so the channels swap.
	// Alpha is forced opaque because the surface is composited opaque and the
	// stored alpha is not meaningful.
	bgra := r.sc.imageFormat == core1_0.FormatB8G8R8A8SRGB ||
		r.sc.imageFormat == core1_0.FormatB8G8R8A8UnsignedNormalized
	for i := 0; i < w*h; i++ {
		s := raw[i*4 : i*4+4]
		d := img.Pix[i*4 : i*4+4]
		if bgra {
			d[0], d[1], d[2] = s[2], s[1], s[0]
		} else {
			d[0], d[1], d[2] = s[0], s[1], s[2]
		}
		d[3] = 255
	}
	return img, nil
}

// SaveScreenshot captures the last presented frame and writes it as a PNG.
func (r *Renderer) SaveScreenshot(path string) error {
	img, err := r.CaptureFrame()
	if err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("renderer: create screenshot %q: %w", path, err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		return fmt.Errorf("renderer: encode screenshot %q: %w", path, err)
	}
	return nil
}

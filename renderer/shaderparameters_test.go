package renderer

import (
	"bytes"
	"testing"

	"github.com/vkngwrapper/core/v3/core1_0"
)

func TestShaderParametersCopyAndFenceSlots(t *testing.T) {
	r := &Renderer{shadow: &shadowResources{}}
	for i := range r.shadow.shaderParameterMapped {
		r.shadow.shaderParameterMapped[i] = make([]byte, ShaderParameterBytes)
	}
	data := bytes.Repeat([]byte{17}, ShaderParameterBytes)
	if err := r.SetShaderParameters(data); err != nil {
		t.Fatal(err)
	}
	data[0] = 99
	for _, slot := range r.shadow.shaderParameterMapped {
		if !bytes.Equal(slot, make([]byte, ShaderParameterBytes)) {
			t.Fatal("setter wrote GPU memory before a fence")
		}
	}
	r.flushShaderParameters(0)
	if r.shadow.shaderParameterMapped[0][0] != 17 || r.shadow.shaderParameterMapped[1][0] != 0 {
		t.Fatal("caller alias or write to in-flight slot")
	}
	if err := r.SetShaderParameters(bytes.Repeat([]byte{23}, 16)); err != nil {
		t.Fatal(err)
	}
	r.flushShaderParameters(1)
	if r.shadow.shaderParameterMapped[0][0] != 17 || r.shadow.shaderParameterMapped[1][0] != 23 || r.shadow.shaderParameterMapped[1][16] != 0 {
		t.Fatal("slot snapshot or tail clearing failed")
	}
	before := r.shaderParameters
	for _, size := range []int{1, 15, 17, ShaderParameterBytes + 16} {
		if r.SetShaderParameters(make([]byte, size)) == nil || r.shaderParameters != before {
			t.Fatal("invalid input changed parameters")
		}
	}
	if err := r.SetShaderParameters(nil); err != nil {
		t.Fatal(err)
	}
	r.flushShaderParameters(0)
	if !bytes.Equal(r.shadow.shaderParameterMapped[0], make([]byte, ShaderParameterBytes)) {
		t.Fatal("nil failed to clear")
	}
}

func TestShaderParameterDeviceLimits(t *testing.T) {
	l := &core1_0.PhysicalDeviceLimits{MaxUniformBufferRange: 16384, MaxPerStageDescriptorUniformBuffers: 12, MaxDescriptorSetUniformBuffers: 72, MinUniformBufferOffsetAlignment: 512}
	off, err := shaderParameterOffset(l)
	if err != nil || off != 512 {
		t.Fatalf("offset %d, error %v", off, err)
	}
	for i := 0; i < 3; i++ {
		bad := *l
		switch i {
		case 0:
			bad.MaxUniformBufferRange = ShaderParameterBytes - 1
		case 1:
			bad.MaxPerStageDescriptorUniformBuffers = 2
		case 2:
			bad.MaxDescriptorSetUniformBuffers = 3
		}
		if _, err := shaderParameterOffset(&bad); err == nil {
			t.Fatal("accepted insufficient UBO limits")
		}
	}
}

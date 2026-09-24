package renderer

import (
	"os"
	"testing"
)

func TestSyncValidationOptInAndRestore(t *testing.T) {
	for _, validation := range []bool{false, true} {
		for _, request := range []string{"", "0", "1"} {
			t.Run(request+"/"+map[bool]string{false: "off", true: "on"}[validation], func(t *testing.T) {
				t.Setenv("GLYPHENGINE_SYNC_VALIDATION", request)
				t.Setenv("VK_LAYER_VALIDATE_SYNC", "0")
				restore, enabled, err := configureSyncValidation(validation)
				if err != nil {
					t.Fatal(err)
				}
				want := validation && request == "1"
				if enabled != want || (os.Getenv("VK_LAYER_VALIDATE_SYNC") == "1") != want {
					t.Fatalf("enabled=%v setting=%q want %v", enabled, os.Getenv("VK_LAYER_VALIDATE_SYNC"), want)
				}
				restore()
				if os.Getenv("VK_LAYER_VALIDATE_SYNC") != "0" {
					t.Fatal("changed the caller's layer setting")
				}
			})
		}
	}
	t.Setenv("GLYPHENGINE_SYNC_VALIDATION", "1")
	t.Setenv("VK_LAYER_VALIDATE_SYNC", "")
	_ = os.Unsetenv("VK_LAYER_VALIDATE_SYNC")
	restore, _, err := configureSyncValidation(true)
	if err != nil {
		t.Fatal(err)
	}
	restore()
	if _, present := os.LookupEnv("VK_LAYER_VALIDATE_SYNC"); present {
		t.Fatal("restoring an absent layer setting left it set")
	}
}

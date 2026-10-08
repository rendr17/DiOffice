package httpapi

import (
	"bytes"
	"image"
	"image/png"
	"testing"
)

func TestValidateImageUploadSniffsRasterTypeAndSanitizesName(t *testing.T) {
	var content bytes.Buffer
	if err := png.Encode(&content, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatalf("encode PNG fixture: %v", err)
	}

	name, contentType, err := validateImageUpload(`..\..\reference.jpg`, content.Bytes())
	if err != nil {
		t.Fatalf("validateImageUpload() error = %v", err)
	}
	if name != "reference.png" || contentType != "image/png" {
		t.Fatalf("validated image = %q/%q, want safe PNG name and detected MIME", name, contentType)
	}
}

func TestValidateImageUploadRejectsActiveOrInvalidContent(t *testing.T) {
	for _, data := range [][]byte{
		[]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`),
		[]byte("not an image"),
	} {
		if _, _, err := validateImageUpload("reference.svg", data); err == nil {
			t.Fatal("validateImageUpload() accepted unsupported or invalid content")
		}
	}
}

func TestValidateImageUploadRejectsOversizedFiles(t *testing.T) {
	data := make([]byte, (8<<20)+1)
	if _, _, err := validateImageUpload("large.png", data); err == nil {
		t.Fatal("validateImageUpload() accepted a file above the 8 MiB limit")
	}
}

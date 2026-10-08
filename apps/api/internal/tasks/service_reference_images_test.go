package tasks

import (
	"strings"
	"testing"
)

func TestNormalizeAndValidateReferenceImageMetadata(t *testing.T) {
	input := testCreateDraftInput(
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000002",
		"00000000-0000-4000-8000-000000000003",
		"00000000-0000-4000-8000-000000000004",
		"with-reference-image",
	)
	input.ReferenceImages = []ReferenceImage{validReferenceImage()}

	normalized, err := normalizeAndValidate(input)
	if err != nil {
		t.Fatalf("normalizeAndValidate() rejected valid image metadata: %v", err)
	}
	if len(normalized.ReferenceImages) != 1 || normalized.ReferenceImages[0].FileName != "reference.png" {
		t.Fatalf("normalized reference images = %+v, want the supplied image metadata", normalized.ReferenceImages)
	}
}

func TestNormalizeAndValidateRejectsUnsafeReferenceImageMetadata(t *testing.T) {
	invalidCases := []struct {
		name   string
		change func(*ReferenceImage)
	}{
		{name: "invalid id", change: func(image *ReferenceImage) { image.ID = "not-a-uuid" }},
		{name: "unsafe object key", change: func(image *ReferenceImage) { image.ObjectKey = "../public/attacker.png" }},
		{name: "unsupported content type", change: func(image *ReferenceImage) { image.ContentType = "image/svg+xml" }},
		{name: "invalid checksum", change: func(image *ReferenceImage) { image.SHA256 = "short" }},
		{name: "zero size", change: func(image *ReferenceImage) { image.SizeBytes = 0 }},
		{name: "oversized", change: func(image *ReferenceImage) { image.SizeBytes = 8<<20 + 1 }},
	}

	for _, testCase := range invalidCases {
		t.Run(testCase.name, func(t *testing.T) {
			input := testCreateDraftInput(
				"00000000-0000-4000-8000-000000000001",
				"00000000-0000-4000-8000-000000000002",
				"00000000-0000-4000-8000-000000000003",
				"00000000-0000-4000-8000-000000000004",
				"invalid-image-metadata",
			)
			image := validReferenceImage()
			testCase.change(&image)
			input.ReferenceImages = []ReferenceImage{image}
			if _, err := normalizeAndValidate(input); err == nil {
				t.Fatal("normalizeAndValidate() accepted invalid image metadata")
			}
		})
	}
}

func TestHashRequestIncludesReferenceImageContentNotStorageLocation(t *testing.T) {
	firstInput := testCreateDraftInput(
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000002",
		"00000000-0000-4000-8000-000000000003",
		"00000000-0000-4000-8000-000000000004",
		"same-content",
	)
	firstInput.ReferenceImages = []ReferenceImage{validReferenceImage()}
	secondInput := firstInput
	secondInput.ReferenceImages = append([]ReferenceImage(nil), firstInput.ReferenceImages...)
	secondInput.ReferenceImages[0].ID = "00000000-0000-4000-8000-000000000006"
	secondInput.ReferenceImages[0].ObjectKey = "task-reference-images/another-object"

	firstHash, err := hashRequest(firstInput)
	if err != nil {
		t.Fatalf("hash first request: %v", err)
	}
	secondHash, err := hashRequest(secondInput)
	if err != nil {
		t.Fatalf("hash second request: %v", err)
	}
	if firstHash != secondHash {
		t.Fatalf("hash changed for the same image content at a new storage location: %q != %q", firstHash, secondHash)
	}

	secondInput.ReferenceImages[0].SHA256 = strings.Repeat("b", 64)
	changedHash, err := hashRequest(secondInput)
	if err != nil {
		t.Fatalf("hash changed image request: %v", err)
	}
	if firstHash == changedHash {
		t.Fatal("hash did not change when reference image content changed")
	}
}

func validReferenceImage() ReferenceImage {
	return ReferenceImage{
		ID:       "00000000-0000-4000-8000-000000000005",
		FileName: "reference.png", ContentType: "image/png", SizeBytes: 64,
		ObjectKey: "task-reference-images/00000000-0000-4000-8000-000000000005",
		SHA256:    strings.Repeat("a", 64),
	}
}

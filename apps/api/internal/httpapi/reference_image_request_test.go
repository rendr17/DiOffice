package httpapi

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseTaskCreateMultipartRequest(t *testing.T) {
	var imageBytes bytes.Buffer
	if err := png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatalf("encode PNG fixture: %v", err)
	}
	request, err := newMultipartTaskRequest(t, imageBytes.Bytes(), `..\..\reference.jpg`)
	if err != nil {
		t.Fatalf("build multipart request: %v", err)
	}

	parsed, images, err := decodeTaskCreateRequest(httptest.NewRecorder(), request)
	if err != nil {
		t.Fatalf("decodeTaskCreateRequest() error = %v", err)
	}
	if parsed.Title != "Make the task from a brief" || len(images) != 1 {
		t.Fatalf("parsed request/images = %+v/%d, want task and one image", parsed, len(images))
	}
	if images[0].metadata.FileName != "reference.png" || images[0].metadata.ContentType != "image/png" ||
		!bytes.Equal(images[0].data, imageBytes.Bytes()) || images[0].metadata.ID == "" {
		t.Fatalf("parsed image metadata/data = %+v/%v, want sanitized private PNG metadata", images[0].metadata, bytes.Equal(images[0].data, imageBytes.Bytes()))
	}
	if images[0].metadata.ObjectKey != "task-reference-images/"+images[0].metadata.ID || images[0].metadata.SHA256 == "" {
		t.Fatalf("parsed image private object metadata is incomplete: %+v", images[0].metadata)
	}
}

func TestParseTaskCreateMultipartRejectsUnsupportedImage(t *testing.T) {
	request, err := newMultipartTaskRequest(t, []byte(`<svg><script>unsafe()</script></svg>`), "reference.svg")
	if err != nil {
		t.Fatalf("build multipart request: %v", err)
	}
	if _, _, err := decodeTaskCreateRequest(httptest.NewRecorder(), request); err == nil {
		t.Fatal("decodeTaskCreateRequest() accepted active SVG content")
	}
}

func newMultipartTaskRequest(t *testing.T, imageBytes []byte, filename string, assigneeEmployeeIDs ...string) (*http.Request, error) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	assigneeEmployeeID := ""
	if len(assigneeEmployeeIDs) > 0 {
		assigneeEmployeeID = assigneeEmployeeIDs[0]
	}
	taskJSON, err := json.Marshal(createTaskRequest{
		AssigneeEmployeeID: assigneeEmployeeID,
		Title: "Make the task from a brief", Description: "Keep the full instruction", TaskType: "feature", Priority: "NORMAL",
	})
	if err != nil {
		return nil, err
	}
	if err := writer.WriteField("task", string(taskJSON)); err != nil {
		return nil, err
	}
	file, err := writer.CreateFormFile("referenceImages", filename)
	if err != nil {
		return nil, err
	}
	if _, err := file.Write(imageBytes); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/project/tasks", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Idempotency-Key", strings.Repeat("a", 36))
	return request, nil
}

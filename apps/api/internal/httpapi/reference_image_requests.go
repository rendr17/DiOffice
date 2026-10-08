package httpapi

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/rendr17/dioffice/apps/api/internal/tasks"
)

const maxTaskCreateRequestBodyBytes = maxReferenceImageTotalBytes + maxRequestBodyBytes

var errInvalidMultipartTaskRequest = errors.New("invalid multipart task request")

type uploadedReferenceImage struct {
	metadata tasks.ReferenceImage
	data     []byte
}

func decodeTaskCreateRequest(w http.ResponseWriter, r *http.Request) (createTaskRequest, []uploadedReferenceImage, error) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return createTaskRequest{}, nil, errUnsupportedMediaType
	}
	if mediaType == "application/json" {
		var body createTaskRequest
		if err := decodeJSONRequest(w, r, &body); err != nil {
			return createTaskRequest{}, nil, err
		}
		return body, nil, nil
	}
	if mediaType != "multipart/form-data" {
		return createTaskRequest{}, nil, errUnsupportedMediaType
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxTaskCreateRequestBodyBytes)
	if err := r.ParseMultipartForm(maxRequestBodyBytes); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return createTaskRequest{}, nil, errRequestTooLarge
		}
		return createTaskRequest{}, nil, fmt.Errorf("%w: could not parse form", errInvalidMultipartTaskRequest)
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	if r.MultipartForm == nil || len(r.MultipartForm.Value) != 1 || len(r.MultipartForm.Value["task"]) != 1 {
		return createTaskRequest{}, nil, errInvalidMultipartTaskRequest
	}
	if len(r.MultipartForm.File) > 1 {
		return createTaskRequest{}, nil, errInvalidMultipartTaskRequest
	}
	files := r.MultipartForm.File["referenceImages"]
	if len(files) > maxReferenceImageCount {
		return createTaskRequest{}, nil, errInvalidReferenceImage
	}

	var body createTaskRequest
	taskJSON := r.MultipartForm.Value["task"][0]
	if len(taskJSON) > maxRequestBodyBytes {
		return createTaskRequest{}, nil, errRequestTooLarge
	}
	decoder := json.NewDecoder(strings.NewReader(taskJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		return createTaskRequest{}, nil, fmt.Errorf("%w: invalid task JSON", errInvalidMultipartTaskRequest)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return createTaskRequest{}, nil, errInvalidMultipartTaskRequest
	}

	images := make([]uploadedReferenceImage, 0, len(files))
	totalBytes := int64(0)
	for _, file := range files {
		if file.Size < 1 || file.Size > maxReferenceImageBytes {
			return createTaskRequest{}, nil, errInvalidReferenceImage
		}
		totalBytes += file.Size
		if totalBytes > maxReferenceImageTotalBytes {
			return createTaskRequest{}, nil, errInvalidReferenceImage
		}
		input, err := file.Open()
		if err != nil {
			return createTaskRequest{}, nil, fmt.Errorf("%w: could not open image part", errInvalidMultipartTaskRequest)
		}
		data, readErr := io.ReadAll(io.LimitReader(input, maxReferenceImageBytes+1))
		closeErr := input.Close()
		if readErr != nil || closeErr != nil || len(data) != int(file.Size) {
			return createTaskRequest{}, nil, errInvalidReferenceImage
		}
		filename, contentType, err := validateImageUpload(file.Filename, data)
		if err != nil {
			return createTaskRequest{}, nil, err
		}
		id, err := newReferenceImageID()
		if err != nil {
			return createTaskRequest{}, nil, fmt.Errorf("generate image identifier: %w", err)
		}
		checksum := sha256.Sum256(data)
		image := tasks.ReferenceImage{
			ID: id, FileName: filename, ContentType: contentType, SizeBytes: int64(len(data)),
			ObjectKey: "task-reference-images/" + id, SHA256: hex.EncodeToString(checksum[:]),
		}
		images = append(images, uploadedReferenceImage{metadata: image, data: data})
	}
	return body, images, nil
}

func newReferenceImageID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}

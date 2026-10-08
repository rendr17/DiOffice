package httpapi

import (
	"bytes"
	"context"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"path"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxReferenceImageCount      = 5
	maxReferenceImageBytes      = 8 << 20
	maxReferenceImageTotalBytes = maxReferenceImageCount * maxReferenceImageBytes
	maxReferenceImageDimension  = 12000
	maxReferenceImagePixels     = int64(50_000_000)
)

var errInvalidReferenceImage = errors.New("invalid reference image")

type ReferenceImageStore interface {
	Put(ctx context.Context, key, contentType string, data []byte) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
}

func validateImageUpload(filename string, data []byte) (string, string, error) {
	if len(data) == 0 || len(data) > maxReferenceImageBytes {
		return "", "", errInvalidReferenceImage
	}

	header := data
	if len(header) > 512 {
		header = header[:512]
	}
	contentType := http.DetectContentType(header)
	formatByType := map[string]struct {
		format string
		ext    string
	}{
		"image/png":  {format: "png", ext: ".png"},
		"image/jpeg": {format: "jpeg", ext: ".jpg"},
	}
	imageType, supported := formatByType[contentType]
	if !supported {
		return "", "", errInvalidReferenceImage
	}

	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || format != imageType.format || config.Width < 1 || config.Height < 1 ||
		config.Width > maxReferenceImageDimension || config.Height > maxReferenceImageDimension ||
		int64(config.Width)*int64(config.Height) > maxReferenceImagePixels {
		return "", "", errInvalidReferenceImage
	}

	return safeReferenceImageName(filename, imageType.ext), contentType, nil
}

func safeReferenceImageName(filename, extension string) string {
	filename = strings.ReplaceAll(filename, "\x5c", "/")
	name := path.Base(filename)
	name = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) || character == '/' || character == '\\' {
			return -1
		}
		return character
	}, name)
	stem := strings.TrimSpace(strings.TrimSuffix(name, path.Ext(name)))
	if stem == "" || stem == "." {
		stem = "reference"
	}

	characters := []rune(stem)
	if utf8.RuneCountInString(stem) > 120 {
		stem = string(characters[:120])
	}
	return stem + extension
}

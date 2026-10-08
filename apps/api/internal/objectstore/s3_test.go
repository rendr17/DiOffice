package objectstore

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"io"
	"os"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
)

func TestS3StoreRoundTripWithS3Mock(t *testing.T) {
	endpoint := os.Getenv("DIOFFICE_S3_TEST_ENDPOINT")
	bucket := os.Getenv("DIOFFICE_S3_TEST_BUCKET")
	if endpoint == "" || bucket == "" {
		t.Skip("set DIOFFICE_S3_TEST_ENDPOINT and DIOFFICE_S3_TEST_BUCKET to run the S3Mock integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store, err := NewS3Store(ctx, Config{
		Endpoint: endpoint, Region: "us-east-1", Bucket: bucket,
		AccessKey: "dioffice-test-access", SecretKey: "dioffice-test-secret", ForcePathStyle: true,
	})
	if err != nil {
		t.Fatalf("NewS3Store() error = %v", err)
	}

	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatalf("encode PNG fixture: %v", err)
	}
	const key = "task-reference-images/00000000-0000-4000-8000-000000000001"
	if err := store.Put(ctx, key, "image/png", data.Bytes()); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	defer func() {
		if err := store.Delete(context.Background(), key); err != nil {
			t.Errorf("cleanup Delete() error = %v", err)
		}
	}()

	object, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	actual, readErr := io.ReadAll(object)
	closeErr := object.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read/close object error = %v/%v", readErr, closeErr)
	}
	if !bytes.Equal(actual, data.Bytes()) {
		t.Fatal("Get() returned bytes different from Put()")
	}

	info, err := store.client.StatObject(ctx, store.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		t.Fatalf("StatObject() error = %v", err)
	}
	if info.ContentType != "image/png" {
		t.Fatalf("object content type = %q, want image/png", info.ContentType)
	}

	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	object, err = store.Get(ctx, key)
	if err != nil {
		return
	}
	_, readErr = io.ReadAll(object)
	_ = object.Close()
	if readErr == nil {
		t.Fatal("Get() after Delete() unexpectedly returned an object")
	}
}

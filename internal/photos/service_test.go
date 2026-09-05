package photos

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"costume-tree/internal/storage"
)

type photoRepository struct {
	mu                sync.Mutex
	nextID            int64
	photos            map[int64]storage.CostumeItemPhoto
	pendingCalls      int
	pendingFailures   int
	markReadyFailures int
}

func newPhotoRepository() *photoRepository {
	return &photoRepository{nextID: 1, photos: make(map[int64]storage.CostumeItemPhoto)}
}

func (r *photoRepository) Create(_ context.Context, input storage.CreateCostumeItemPhotoInput) (storage.CostumeItemPhoto, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().UTC()
	photo := storage.CostumeItemPhoto{
		ID: r.nextID, ProductionID: input.ProductionID, CostumeItemID: input.CostumeItemID,
		OriginalName: input.OriginalName, DisplayName: input.DisplayName,
		ThumbnailName: input.ThumbnailName, MediaType: input.MediaType,
		Status: storage.PhotoStatusPending, CreatedAt: now, UpdatedAt: now,
	}
	r.nextID++
	r.photos[photo.ID] = photo
	return photo, nil
}

func (r *photoRepository) Get(_ context.Context, productionID, itemID, photoID int64) (storage.CostumeItemPhoto, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	photo, ok := r.photos[photoID]
	if !ok || photo.ProductionID != productionID || photo.CostumeItemID != itemID {
		return storage.CostumeItemPhoto{}, storage.ErrNotFound
	}
	return photo, nil
}

func (r *photoRepository) List(_ context.Context, productionID, itemID int64) ([]storage.CostumeItemPhoto, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []storage.CostumeItemPhoto
	for _, photo := range r.photos {
		if photo.ProductionID == productionID && photo.CostumeItemID == itemID {
			result = append(result, photo)
		}
	}
	return result, nil
}

func (r *photoRepository) ListFirstReadyByActor(_ context.Context, productionID, _ int64) ([]storage.CostumeItemPhoto, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := make(map[int64]struct{})
	var result []storage.CostumeItemPhoto
	for _, photo := range r.photos {
		if photo.ProductionID != productionID || photo.Status != storage.PhotoStatusReady {
			continue
		}
		if _, ok := seen[photo.CostumeItemID]; ok {
			continue
		}
		seen[photo.CostumeItemID] = struct{}{}
		result = append(result, photo)
	}
	return result, nil
}

func (r *photoRepository) ListPending(_ context.Context, limit int) ([]storage.CostumeItemPhoto, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pendingCalls++
	if r.pendingCalls > 1 && r.pendingFailures > 0 {
		r.pendingFailures--
		return nil, errors.New("temporary pending query failure")
	}
	result := make([]storage.CostumeItemPhoto, 0, limit)
	for _, photo := range r.photos {
		if photo.Status == storage.PhotoStatusPending {
			result = append(result, photo)
			if len(result) == limit {
				break
			}
		}
	}
	return result, nil
}

func (r *photoRepository) MarkReady(_ context.Context, photoID int64) (storage.CostumeItemPhoto, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.markReadyFailures > 0 {
		r.markReadyFailures--
		return storage.CostumeItemPhoto{}, errors.New("temporary ready transition failure")
	}
	photo, ok := r.photos[photoID]
	if !ok {
		return storage.CostumeItemPhoto{}, storage.ErrNotFound
	}
	photo.Status = storage.PhotoStatusReady
	photo.ErrorMessage = ""
	r.photos[photoID] = photo
	return photo, nil
}

func (r *photoRepository) MarkFailed(_ context.Context, photoID int64, message string) (storage.CostumeItemPhoto, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	photo, ok := r.photos[photoID]
	if !ok {
		return storage.CostumeItemPhoto{}, storage.ErrNotFound
	}
	photo.Status = storage.PhotoStatusFailed
	photo.ErrorMessage = message
	r.photos[photoID] = photo
	return photo, nil
}

func TestUploadReturnsPendingThenCreatesBoundedDerivatives(t *testing.T) {
	directory := t.TempDir()
	repository := newPhotoRepository()
	service, err := New(directory, repository, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := service.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	header := jpegHeader(t, "phone.jpeg", 1800, 600)
	item := storage.CostumeItem{ID: 8, ProductionID: 3, Code: "CT-0042"}
	photo, err := service.Upload(context.Background(), item, header)
	if err != nil {
		t.Fatal(err)
	}
	if photo.Status != storage.PhotoStatusPending {
		t.Fatalf("Upload status = %q, want pending", photo.Status)
	}
	if !strings.HasPrefix(photo.OriginalName, "CT-0042-") || !strings.HasSuffix(photo.OriginalName, ".jpeg") {
		t.Fatalf("original name = %q", photo.OriginalName)
	}
	if photo.DisplayName != strings.TrimSuffix(photo.OriginalName, ".jpeg")+"-display.jpeg" {
		t.Fatalf("display name = %q", photo.DisplayName)
	}
	if photo.ThumbnailName != strings.TrimSuffix(photo.OriginalName, ".jpeg")+"-thumb.jpeg" {
		t.Fatalf("thumbnail name = %q", photo.ThumbnailName)
	}

	ready := waitForStatus(t, repository, photo.ID, storage.PhotoStatusReady)
	for _, check := range []struct {
		variant string
		maximum int
		name    string
	}{
		{VariantOriginal, 1800, ready.OriginalName},
		{VariantDisplay, displayMax, ready.DisplayName},
		{VariantThumbnail, thumbnailMax, ready.ThumbnailName},
	} {
		_, path, err := service.Resolve(context.Background(), 3, 8, photo.ID, check.variant)
		if err != nil {
			t.Fatalf("Resolve(%s): %v", check.variant, err)
		}
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		configuration, format, err := image.DecodeConfig(file)
		_ = file.Close()
		if err != nil {
			t.Fatalf("decode %s: %v", check.name, err)
		}
		difference := configuration.Height*3 - configuration.Width
		if difference < 0 {
			difference = -difference
		}
		if format != "jpeg" || configuration.Width != check.maximum || difference > 1 {
			t.Fatalf("%s dimensions = %dx%d %s", check.variant, configuration.Width, configuration.Height, format)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %o, want 600", check.variant, info.Mode().Perm())
		}
	}
	if _, _, err := service.Resolve(context.Background(), 99, 8, photo.ID, VariantOriginal); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("cross-production Resolve error = %v", err)
	}
}

func TestDispatcherRetriesTransientRepositoryFailures(t *testing.T) {
	repository := newPhotoRepository()
	repository.pendingFailures = 1
	repository.markReadyFailures = 1
	service, err := New(t.TempDir(), repository, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	photo, err := service.Upload(context.Background(), storage.CostumeItem{ID: 8, ProductionID: 3, Code: "CT-0043"}, jpegHeader(t, "retry.jpg", 40, 20))
	if err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, repository, photo.ID, storage.PhotoStatusReady)
}

func TestValidateUsesActualSizeAndDecodedFormat(t *testing.T) {
	service, err := New(t.TempDir(), newPhotoRepository(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Validate(pngHeader(t, "misleading.jpg", 5, 7)); err != nil {
		t.Fatalf("valid PNG with misleading filename: %v", err)
	}
	if err := service.Validate(pngDimensionsHeader(t, "bomb.png", maxDecodedDimension, maxDecodedDimension)); !errors.Is(err, ErrImageDimensions) {
		t.Fatalf("excessive dimensions error = %v", err)
	}
	if err := service.Validate(&multipart.FileHeader{Filename: "large.jpg", Size: maxUploadBytes + 1}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversize error = %v", err)
	}
	if err := service.Validate(bytesHeader(t, "broken.jpg", []byte("not an image"))); !errors.Is(err, ErrUnsupportedImage) {
		t.Fatalf("corrupt image error = %v", err)
	}
}

func TestStartRecoversPersistedPendingPhoto(t *testing.T) {
	directory := t.TempDir()
	repository := newPhotoRepository()
	photo := storage.CostumeItemPhoto{
		ID: 9, ProductionID: 2, CostumeItemID: 4,
		OriginalName: "CT-0001-recovery.png", DisplayName: "CT-0001-recovery-display.png",
		ThumbnailName: "CT-0001-recovery-thumb.png", MediaType: "image/png", Status: storage.PhotoStatusPending,
	}
	repository.photos[photo.ID] = photo
	file, err := os.Create(filepath.Join(directory, photo.OriginalName))
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(file, image.NewRGBA(image.Rect(0, 0, 900, 1500))); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	service, err := New(directory, repository, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	ready := waitForStatus(t, repository, photo.ID, storage.PhotoStatusReady)
	_, path, err := service.Resolve(context.Background(), 2, 4, ready.ID, VariantDisplay)
	if err != nil {
		t.Fatal(err)
	}
	configuration := decodeConfig(t, path)
	if configuration.Height != displayMax || configuration.Width != 720 {
		t.Fatalf("recovered display dimensions = %dx%d", configuration.Width, configuration.Height)
	}
}

func waitForStatus(t *testing.T, repository *photoRepository, photoID int64, wanted string) storage.CostumeItemPhoto {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		repository.mu.Lock()
		photo, ok := repository.photos[photoID]
		repository.mu.Unlock()
		if ok && photo.Status == wanted {
			return photo
		}
		if ok && photo.Status == storage.PhotoStatusFailed {
			t.Fatalf("processing failed: %s", photo.ErrorMessage)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("photo %d did not reach %s", photoID, wanted)
	return storage.CostumeItemPhoto{}
}

func jpegHeader(t *testing.T, name string, width, height int) *multipart.FileHeader {
	t.Helper()
	var encoded bytes.Buffer
	value := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			value.SetRGBA(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 120, A: 255})
		}
	}
	if err := jpeg.Encode(&encoded, value, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return bytesHeader(t, name, encoded.Bytes())
}

func pngHeader(t *testing.T, name string, width, height int) *multipart.FileHeader {
	t.Helper()
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, width, height))); err != nil {
		t.Fatal(err)
	}
	return bytesHeader(t, name, encoded.Bytes())
}

func pngDimensionsHeader(t *testing.T, name string, width, height int) *multipart.FileHeader {
	t.Helper()
	var encoded bytes.Buffer
	encoded.Write([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a})
	data := make([]byte, 13)
	binary.BigEndian.PutUint32(data[0:4], uint32(width))
	binary.BigEndian.PutUint32(data[4:8], uint32(height))
	data[8] = 8
	data[9] = 2
	_ = binary.Write(&encoded, binary.BigEndian, uint32(len(data)))
	encoded.WriteString("IHDR")
	encoded.Write(data)
	checksumData := append([]byte("IHDR"), data...)
	_ = binary.Write(&encoded, binary.BigEndian, crc32.ChecksumIEEE(checksumData))
	return bytesHeader(t, name, encoded.Bytes())
}

func bytesHeader(t *testing.T, name string, content []byte) *multipart.FileHeader {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("photo", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	if err := request.ParseMultipartForm(int64(len(content) + 1024)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = request.MultipartForm.RemoveAll() })
	return request.MultipartForm.File["photo"][0]
}

func decodeConfig(t *testing.T, path string) image.Config {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	configuration, _, err := image.DecodeConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	return configuration
}

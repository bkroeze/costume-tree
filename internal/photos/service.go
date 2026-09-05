// Package photos validates, stores, and asynchronously processes costume-item photos.
package photos

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log/slog"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"costume-tree/internal/storage"
	"github.com/disintegration/imaging"
)

const (
	VariantOriginal  = "original"
	VariantDisplay   = "display"
	VariantThumbnail = "thumbnail"

	maxUploadBytes      = 20 << 20
	maxDecodedPixels    = 60_000_000
	maxDecodedDimension = 16_384
	displayMax          = 1200
	thumbnailMax        = 320
	workerCount         = 2
)

const retryDelay = time.Second

var (
	ErrTooLarge         = errors.New("photos: image exceeds 20 MiB")
	ErrImageDimensions  = errors.New("photos: image dimensions are too large")
	ErrUnsupportedImage = errors.New("photos: image must be JPEG, PNG, or GIF")
	ErrNotReady         = errors.New("photos: image derivative is not ready")
	ErrNotStarted       = errors.New("photos: service is not started")
)

// Service owns the configured photo directory and a bounded processing queue.
type Service struct {
	directory  string
	repository storage.CostumeItemPhotoRepository
	logger     *slog.Logger

	mu      sync.RWMutex
	started bool
	ctx     context.Context
	cancel  context.CancelFunc
	queue   chan storage.CostumeItemPhoto
	wake    chan struct{}
	retry   chan struct{}
	queued  map[int64]struct{}
	workers sync.WaitGroup
}

// New verifies the configured directory before the HTTP server starts.
func New(directory string, repository storage.CostumeItemPhotoRepository, logger *slog.Logger) (*Service, error) {
	if repository == nil {
		return nil, errors.New("photos: repository is required")
	}
	if logger == nil {
		return nil, errors.New("photos: logger is required")
	}
	if strings.TrimSpace(directory) == "" {
		return nil, errors.New("photos: directory is required")
	}
	directory, err := filepath.Abs(filepath.Clean(directory))
	if err != nil {
		return nil, fmt.Errorf("photos: resolve directory: %w", err)
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("photos: create directory %q: %w", directory, err)
	}
	probe, err := os.CreateTemp(directory, ".write-check-*")
	if err != nil {
		return nil, fmt.Errorf("photos: directory %q is not writable: %w", directory, err)
	}
	probeName := probe.Name()
	if closeErr := probe.Close(); closeErr != nil {
		_ = os.Remove(probeName)
		return nil, fmt.Errorf("photos: close directory probe: %w", closeErr)
	}
	if err := os.Remove(probeName); err != nil {
		return nil, fmt.Errorf("photos: remove directory probe: %w", err)
	}
	return &Service{directory: directory, repository: repository, logger: logger}, nil
}

// Start launches two workers and re-enqueues persisted pending rows.
func (s *Service) Start(parent context.Context) error {
	if parent == nil {
		return errors.New("photos: context is required")
	}
	if _, err := s.repository.ListPending(parent, 1); err != nil {
		return fmt.Errorf("photos: list pending images: %w", err)
	}

	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return errors.New("photos: service already started")
	}
	s.ctx, s.cancel = context.WithCancel(parent)
	s.queue = make(chan storage.CostumeItemPhoto, 64)
	s.wake = make(chan struct{}, 1)
	s.retry = make(chan struct{}, 1)
	s.queued = make(map[int64]struct{})
	s.started = true
	for range workerCount {
		s.workers.Add(1)
		go s.worker()
	}
	s.workers.Add(1)
	go s.dispatch()
	s.mu.Unlock()
	s.notify()
	return nil
}

// Close stops accepting work and waits for active workers.
func (s *Service) Close() {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return
	}
	s.started = false
	cancel := s.cancel
	s.mu.Unlock()
	cancel()
	s.workers.Wait()
}

// Validate verifies size and actual decoded content before an item mutation.
func (s *Service) Validate(header *multipart.FileHeader) error {
	_, err := inspectUpload(header)
	return err
}

// Upload atomically stores the original and pending metadata, then enqueues only
// derivative generation. It does not decode or resize inline.
func (s *Service) Upload(ctx context.Context, item storage.CostumeItem, header *multipart.FileHeader) (storage.CostumeItemPhoto, error) {
	if ctx == nil {
		return storage.CostumeItemPhoto{}, errors.New("photos: context is required")
	}
	details, err := inspectUpload(header)
	if err != nil {
		return storage.CostumeItemPhoto{}, err
	}

	s.mu.RLock()
	started := s.started
	s.mu.RUnlock()
	if !started {
		return storage.CostumeItemPhoto{}, ErrNotStarted
	}

	base, err := s.availableBase(item.Code)
	if err != nil {
		return storage.CostumeItemPhoto{}, err
	}
	originalName := base + details.extension
	displayName := base + "-display" + details.extension
	thumbnailName := base + "-thumb" + details.extension
	originalPath := filepath.Join(s.directory, originalName)
	if err := copyUploadAtomically(header, originalPath); err != nil {
		return storage.CostumeItemPhoto{}, err
	}

	photo, err := s.repository.Create(ctx, storage.CreateCostumeItemPhotoInput{
		ProductionID: item.ProductionID, CostumeItemID: item.ID,
		OriginalName: originalName, DisplayName: displayName,
		ThumbnailName: thumbnailName, MediaType: details.mediaType,
	})
	if err != nil {
		_ = os.Remove(originalPath)
		return storage.CostumeItemPhoto{}, fmt.Errorf("photos: store metadata: %w", err)
	}
	s.notify()
	return photo, nil
}

// List returns photo metadata for one production-scoped item.
func (s *Service) List(ctx context.Context, productionID, itemID int64) ([]storage.CostumeItemPhoto, error) {
	return s.repository.List(ctx, productionID, itemID)
}

// ListFirstReadyByActor returns at most one ready photo per actor inventory item.
func (s *Service) ListFirstReadyByActor(ctx context.Context, productionID, actorID int64) ([]storage.CostumeItemPhoto, error) {
	return s.repository.ListFirstReadyByActor(ctx, productionID, actorID)
}

// Resolve returns a scoped regular file. Derivatives are unavailable until ready.
func (s *Service) Resolve(ctx context.Context, productionID, itemID, photoID int64, variant string) (storage.CostumeItemPhoto, string, error) {
	photo, err := s.repository.Get(ctx, productionID, itemID, photoID)
	if err != nil {
		return storage.CostumeItemPhoto{}, "", err
	}
	var name string
	switch variant {
	case VariantOriginal:
		name = photo.OriginalName
	case VariantDisplay:
		if photo.Status != storage.PhotoStatusReady {
			return storage.CostumeItemPhoto{}, "", ErrNotReady
		}
		name = photo.DisplayName
	case VariantThumbnail:
		if photo.Status != storage.PhotoStatusReady {
			return storage.CostumeItemPhoto{}, "", ErrNotReady
		}
		name = photo.ThumbnailName
	default:
		return storage.CostumeItemPhoto{}, "", storage.ErrNotFound
	}
	path := filepath.Join(s.directory, name)
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return storage.CostumeItemPhoto{}, "", storage.ErrNotFound
		}
		return storage.CostumeItemPhoto{}, "", fmt.Errorf("photos: inspect file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return storage.CostumeItemPhoto{}, "", storage.ErrNotFound
	}
	return photo, path, nil
}

type uploadDetails struct {
	extension string
	mediaType string
}

func inspectUpload(header *multipart.FileHeader) (uploadDetails, error) {
	if header == nil || header.Size == 0 {
		return uploadDetails{}, ErrUnsupportedImage
	}
	if header.Size > maxUploadBytes {
		return uploadDetails{}, ErrTooLarge
	}
	file, err := header.Open()
	if err != nil {
		return uploadDetails{}, fmt.Errorf("photos: open upload: %w", err)
	}
	count, copyErr := io.Copy(io.Discard, io.LimitReader(file, maxUploadBytes+1))
	closeErr := file.Close()
	if copyErr != nil {
		return uploadDetails{}, fmt.Errorf("photos: read upload: %w", copyErr)
	}
	if closeErr != nil {
		return uploadDetails{}, fmt.Errorf("photos: close upload: %w", closeErr)
	}
	if count > maxUploadBytes {
		return uploadDetails{}, ErrTooLarge
	}

	file, err = header.Open()
	if err != nil {
		return uploadDetails{}, fmt.Errorf("photos: reopen upload: %w", err)
	}
	configuration, format, decodeErr := image.DecodeConfig(file)
	closeErr = file.Close()
	if decodeErr != nil {
		return uploadDetails{}, fmt.Errorf("%w: %v", ErrUnsupportedImage, decodeErr)
	}
	if closeErr != nil {
		return uploadDetails{}, fmt.Errorf("photos: close upload: %w", closeErr)
	}
	if configuration.Width <= 0 || configuration.Height <= 0 ||
		configuration.Width > maxDecodedDimension || configuration.Height > maxDecodedDimension ||
		configuration.Width > maxDecodedPixels/configuration.Height {
		return uploadDetails{}, ErrImageDimensions
	}
	originalExtension := strings.ToLower(filepath.Ext(header.Filename))
	switch format {
	case "jpeg":
		if originalExtension != ".jpg" && originalExtension != ".jpeg" {
			originalExtension = ".jpg"
		}
		return uploadDetails{extension: originalExtension, mediaType: "image/jpeg"}, nil
	case "png":
		return uploadDetails{extension: ".png", mediaType: "image/png"}, nil
	case "gif":
		return uploadDetails{extension: ".gif", mediaType: "image/gif"}, nil
	default:
		return uploadDetails{}, ErrUnsupportedImage
	}
}

func (s *Service) availableBase(code string) (string, error) {
	code = strings.TrimSpace(code)
	if code == "" || filepath.Base(code) != code || strings.ContainsAny(code, `/\\`) {
		return "", errors.New("photos: costume item code is not filename-safe")
	}
	for range 8 {
		var random [4]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", fmt.Errorf("photos: generate filename suffix: %w", err)
		}
		stamp := time.Now().UTC().Format("20060102T150405.000")
		base := code + "-" + stamp + "-" + hex.EncodeToString(random[:])
		matches, err := filepath.Glob(filepath.Join(s.directory, base+"*"))
		if err != nil {
			return "", fmt.Errorf("photos: check filename collision: %w", err)
		}
		if len(matches) == 0 {
			return base, nil
		}
	}
	return "", errors.New("photos: could not allocate a unique filename")
}

func copyUploadAtomically(header *multipart.FileHeader, destination string) (err error) {
	source, err := header.Open()
	if err != nil {
		return fmt.Errorf("photos: open upload: %w", err)
	}
	defer source.Close()
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".original-*")
	if err != nil {
		return fmt.Errorf("photos: create original: %w", err)
	}
	temporaryName := temporary.Name()
	defer func() {
		_ = temporary.Close()
		if err != nil {
			_ = os.Remove(temporaryName)
		}
	}()
	written, err := io.Copy(temporary, io.LimitReader(source, maxUploadBytes+1))
	if err != nil {
		return fmt.Errorf("photos: save original: %w", err)
	}
	if written > maxUploadBytes {
		return ErrTooLarge
	}
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("photos: protect original: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("photos: sync original: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("photos: close original: %w", err)
	}
	if err := os.Rename(temporaryName, destination); err != nil {
		return fmt.Errorf("photos: install original: %w", err)
	}
	return nil
}

func (s *Service) notify() {
	s.mu.RLock()
	started, ctx, wake := s.started, s.ctx, s.wake
	s.mu.RUnlock()
	if !started {
		return
	}
	select {
	case wake <- struct{}{}:
	case <-ctx.Done():
	default:
	}
}

func (s *Service) scheduleRetry() {
	s.mu.RLock()
	started, ctx, retry := s.started, s.ctx, s.retry
	s.mu.RUnlock()
	if !started {
		return
	}
	select {
	case retry <- struct{}{}:
	case <-ctx.Done():
	default:
	}
}

func (s *Service) waitToRetry() bool {
	timer := time.NewTimer(retryDelay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-s.ctx.Done():
		return false
	}
}

func (s *Service) dispatch() {
	defer s.workers.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.retry:
			if !s.waitToRetry() {
				return
			}
		case <-s.wake:
		}
		for {
			pending, err := s.repository.ListPending(s.ctx, cap(s.queue))
			if err != nil {
				if s.ctx.Err() != nil {
					return
				}
				s.logger.Error("list pending photos", "error", err)
				if !s.waitToRetry() {
					return
				}
				continue
			}
			for _, photo := range pending {
				s.mu.Lock()
				if _, exists := s.queued[photo.ID]; exists {
					s.mu.Unlock()
					continue
				}
				s.queued[photo.ID] = struct{}{}
				s.mu.Unlock()
				select {
				case s.queue <- photo:
				case <-s.ctx.Done():
					return
				}
			}
			break
		}
	}
}

func (s *Service) worker() {
	defer s.workers.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case photo := <-s.queue:
			if s.ctx.Err() != nil {
				return
			}
			transitioned := s.process(photo)
			s.mu.Lock()
			delete(s.queued, photo.ID)
			s.mu.Unlock()
			if transitioned {
				s.notify()
			} else {
				s.scheduleRetry()
			}
		}
	}
}

func (s *Service) process(photo storage.CostumeItemPhoto) bool {
	originalPath := filepath.Join(s.directory, photo.OriginalName)
	displayPath := filepath.Join(s.directory, photo.DisplayName)
	thumbnailPath := filepath.Join(s.directory, photo.ThumbnailName)
	imageValue, err := imaging.Open(originalPath, imaging.AutoOrientation(true))
	if err == nil {
		err = saveImageAtomically(fit(imageValue, displayMax), displayPath, photo.MediaType)
	}
	if err == nil {
		err = saveImageAtomically(fit(imageValue, thumbnailMax), thumbnailPath, photo.MediaType)
	}
	if err != nil {
		_ = os.Remove(displayPath)
		_ = os.Remove(thumbnailPath)
		if s.ctx.Err() != nil {
			return false
		}
		message := err.Error()
		if len(message) > 500 {
			message = message[:500]
		}
		if _, markErr := s.repository.MarkFailed(context.Background(), photo.ID, message); markErr != nil {
			s.logger.Error("mark photo processing failed", "photo_id", photo.ID, "error", markErr)
			return false
		}
		s.logger.Warn("photo processing failed", "photo_id", photo.ID, "error", err)
		return true
	}
	if _, err := s.repository.MarkReady(context.Background(), photo.ID); err != nil {
		s.logger.Error("mark photo ready", "photo_id", photo.ID, "error", err)
		return false
	}
	return true
}

func fit(source image.Image, maximum int) *image.NRGBA {
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= maximum && height <= maximum {
		return imaging.Clone(source)
	}
	if width >= height {
		return imaging.Resize(source, maximum, 0, imaging.Lanczos)
	}
	return imaging.Resize(source, 0, maximum, imaging.Lanczos)
}

func saveImageAtomically(value image.Image, destination, mediaType string) (err error) {
	extension := filepath.Ext(destination)
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".derivative-*"+extension)
	if err != nil {
		return fmt.Errorf("photos: create derivative: %w", err)
	}
	temporaryName := temporary.Name()
	if closeErr := temporary.Close(); closeErr != nil {
		_ = os.Remove(temporaryName)
		return fmt.Errorf("photos: close derivative: %w", closeErr)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(temporaryName)
		}
	}()
	if _, err := imageFormat(mediaType); err != nil {
		return err
	}
	if err := imaging.Save(value, temporaryName, imaging.JPEGQuality(88)); err != nil {
		return fmt.Errorf("photos: encode derivative: %w", err)
	}
	if err := os.Chmod(temporaryName, 0o600); err != nil {
		return fmt.Errorf("photos: protect derivative: %w", err)
	}
	file, err := os.Open(temporaryName)
	if err != nil {
		return fmt.Errorf("photos: reopen derivative: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("photos: sync derivative: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("photos: close derivative: %w", err)
	}
	if err := os.Rename(temporaryName, destination); err != nil {
		return fmt.Errorf("photos: install derivative: %w", err)
	}
	return nil
}

func imageFormat(mediaType string) (imaging.Format, error) {
	switch mediaType {
	case "image/jpeg":
		return imaging.JPEG, nil
	case "image/png":
		return imaging.PNG, nil
	case "image/gif":
		return imaging.GIF, nil
	default:
		return imaging.JPEG, ErrUnsupportedImage
	}
}

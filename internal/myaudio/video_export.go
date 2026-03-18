package myaudio

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tphakala/birdnet-go/internal/conf"
	"github.com/tphakala/birdnet-go/internal/errors"
	"github.com/tphakala/birdnet-go/internal/logger"
	"github.com/tphakala/birdnet-go/internal/privacy"
)

const (
	videoRingDirectoryName   = ".ring"
	videoRingCleanupInterval = 15 * time.Second
	videoExportTimeout       = 2 * time.Minute
	videoPosterExtension     = ".jpg"
	videoExportFormatMP4     = "mp4"
	videoSegmentNameLayout   = "20060102T150405"
)

type rollingVideoRecorder struct {
	sourceID         string
	safeString       string
	connectionString string
	transport        string
	ringDir          string
	segmentDuration  time.Duration
	bufferDuration   time.Duration
	ctx              context.Context
	cancel           context.CancelFunc
	cmd              *exec.Cmd
	wg               sync.WaitGroup
}

type videoRecorderManager struct {
	mu        sync.RWMutex
	recorders map[string]*rollingVideoRecorder
}

var globalVideoRecorderManager = &videoRecorderManager{
	recorders: make(map[string]*rollingVideoRecorder),
}

type videoSegment struct {
	path      string
	startTime time.Time
	endTime   time.Time
}

func getVideoLogger() logger.Logger {
	return getIntegrationLogger()
}

func StartRTSPVideoRecorder(source *AudioSource, transport string) error {
	if source == nil || source.Type != SourceTypeRTSP {
		return nil
	}

	settings := conf.Setting()
	if settings == nil || !settings.Realtime.RTSP.VideoExport.Enabled {
		return nil
	}

	if err := ValidateFFmpegPath(settings.Realtime.Audio.FfmpegPath); err != nil {
		return err
	}

	connStr, err := source.GetConnectionString()
	if err != nil {
		return err
	}

	globalVideoRecorderManager.mu.Lock()
	defer globalVideoRecorderManager.mu.Unlock()

	if _, exists := globalVideoRecorderManager.recorders[source.ID]; exists {
		return nil
	}

	ringDir := filepath.Join(settings.Realtime.RTSP.VideoExport.Path, videoRingDirectoryName, source.ID)
	if err := os.MkdirAll(ringDir, 0o750); err != nil {
		return fmt.Errorf("create video ring directory: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	recorder := &rollingVideoRecorder{
		sourceID:         source.ID,
		safeString:       source.SafeString,
		connectionString: connStr,
		transport:        transport,
		ringDir:          ringDir,
		segmentDuration:  time.Duration(settings.Realtime.RTSP.VideoExport.SegmentDurationSeconds) * time.Second,
		bufferDuration:   time.Duration(settings.Realtime.RTSP.VideoExport.BufferSeconds) * time.Second,
		ctx:              ctx,
		cancel:           cancel,
	}

	if err := recorder.start(settings.Realtime.Audio.FfmpegPath); err != nil {
		cancel()
		return err
	}

	globalVideoRecorderManager.recorders[source.ID] = recorder
	return nil
}

func StopRTSPVideoRecorder(sourceID string) error {
	globalVideoRecorderManager.mu.Lock()
	recorder, exists := globalVideoRecorderManager.recorders[sourceID]
	if exists {
		delete(globalVideoRecorderManager.recorders, sourceID)
	}
	globalVideoRecorderManager.mu.Unlock()

	if !exists {
		return nil
	}

	recorder.stop()
	return nil
}

func ExportRTSPVideoClip(sourceID string, clipStart time.Time, duration int, outputPath string) error {
	settings := conf.Setting()
	if settings == nil || !settings.Realtime.RTSP.VideoExport.Enabled {
		return nil
	}

	if duration <= 0 {
		return fmt.Errorf("invalid video clip duration: %d", duration)
	}

	globalVideoRecorderManager.mu.RLock()
	recorder, exists := globalVideoRecorderManager.recorders[sourceID]
	globalVideoRecorderManager.mu.RUnlock()
	if !exists {
		return fmt.Errorf("no active video recorder for source %s", sourceID)
	}

	clipEnd := clipStart.Add(time.Duration(duration) * time.Second)
	segments, err := recorder.collectSegments(clipStart, clipEnd)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(outputPath), 0o750); err != nil {
		return fmt.Errorf("create video output directory: %w", err)
	}

	tmpDir, err := os.MkdirTemp(filepath.Dir(outputPath), "video-export-*")
	if err != nil {
		return fmt.Errorf("create temp dir for video export: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	concatListPath := filepath.Join(tmpDir, "concat.txt")
	var concatList bytes.Buffer
	for _, segment := range segments {
		segmentPath, err := filepath.Abs(segment.path)
		if err != nil {
			return fmt.Errorf("resolve absolute video segment path: %w", err)
		}
		concatList.WriteString(fmt.Sprintf("file '%s'\n", escapeFFmpegConcatPath(segmentPath)))
	}
	if err := os.WriteFile(concatListPath, concatList.Bytes(), 0o600); err != nil {
		return fmt.Errorf("write concat list: %w", err)
	}

	combinedPath := filepath.Join(tmpDir, "combined.mp4")
	firstSegmentStart := segments[0].startTime
	clipOffset := clipStart.Sub(firstSegmentStart).Seconds()
	if clipOffset < 0 {
		clipOffset = 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), videoExportTimeout)
	defer cancel()

	if err := runVideoFFmpegCommand(ctx, settings.Realtime.Audio.FfmpegPath,
		"-f", "concat",
		"-safe", "0",
		"-i", concatListPath,
		"-c", "copy",
		combinedPath,
	); err != nil {
		return fmt.Errorf("combine video segments: %w", err)
	}

	if err := runVideoFFmpegCommand(ctx, settings.Realtime.Audio.FfmpegPath,
		"-ss", formatFFmpegSeconds(clipOffset),
		"-i", combinedPath,
		"-t", strconv.Itoa(duration),
		"-map", "0:v:0",
		"-map", "0:a?",
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-c:a", "aac",
		"-movflags", "+faststart",
		outputPath,
	); err != nil {
		return fmt.Errorf("trim exported video clip: %w", err)
	}

	return nil
}

func EnsureVideoPoster(videoPath string) (string, error) {
	settings := conf.Setting()
	if settings == nil {
		return "", fmt.Errorf("settings not initialized")
	}

	if err := ValidateFFmpegPath(settings.Realtime.Audio.FfmpegPath); err != nil {
		return "", err
	}

	posterPath := strings.TrimSuffix(videoPath, filepath.Ext(videoPath)) + videoPosterExtension
	if _, err := os.Stat(posterPath); err == nil {
		return posterPath, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), videoExportTimeout)
	defer cancel()

	if err := runVideoFFmpegCommand(ctx, settings.Realtime.Audio.FfmpegPath,
		"-i", videoPath,
		"-frames:v", "1",
		"-q:v", "2",
		posterPath,
	); err != nil {
		return "", fmt.Errorf("generate video poster: %w", err)
	}

	return posterPath, nil
}

func (r *rollingVideoRecorder) start(ffmpegPath string) error {
	outputPattern := filepath.Join(r.ringDir, "segment_%Y%m%dT%H%M%S.mp4")
	args := make([]string, 0, 20)
	if r.transport != "" {
		args = append(args, "-rtsp_transport", r.transport)
	}
	args = append(args,
		"-i", r.connectionString,
		"-map", "0:v:0",
		"-map", "0:a?",
		"-c", "copy",
		"-f", "segment",
		"-segment_time", strconv.Itoa(int(r.segmentDuration.Seconds())),
		"-segment_format", videoExportFormatMP4,
		"-reset_timestamps", "1",
		"-strftime", "1",
		outputPattern,
	)

	r.cmd = exec.CommandContext(r.ctx, ffmpegPath, args...) //nolint:gosec // path validated, args built internally

	if err := r.cmd.Start(); err != nil {
		return errors.Newf("start RTSP video recorder: %w", err).
			Category(errors.CategorySystem).
			Component("video-recorder").
			Context("source_id", r.sourceID).
			Context("url", privacy.SanitizeStreamUrl(r.safeString)).
			Build()
	}

	r.wg.Add(2)
	go func() {
		defer r.wg.Done()
		if err := r.cmd.Wait(); err != nil && r.ctx.Err() == nil {
			getVideoLogger().Warn("RTSP video recorder exited",
				logger.String("source_id", r.sourceID),
				logger.String("url", privacy.SanitizeStreamUrl(r.safeString)),
				logger.Error(err),
				logger.String("operation", "video_recorder_wait"))
		}
	}()
	go func() {
		defer r.wg.Done()
		ticker := time.NewTicker(videoRingCleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-r.ctx.Done():
				return
			case <-ticker.C:
				r.cleanupExpiredSegments()
			}
		}
	}()

	return nil
}

func (r *rollingVideoRecorder) stop() {
	r.cancel()
	r.wg.Wait()
}

func (r *rollingVideoRecorder) cleanupExpiredSegments() {
	entries, err := os.ReadDir(r.ringDir)
	if err != nil {
		return
	}

	cutoff := time.Now().Add(-(r.bufferDuration + r.segmentDuration))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".mp4" {
			continue
		}

		startTime, err := parseVideoSegmentStart(entry.Name())
		if err != nil {
			continue
		}

		if startTime.Before(cutoff) {
			_ = os.Remove(filepath.Join(r.ringDir, entry.Name()))
		}
	}
}

func (r *rollingVideoRecorder) collectSegments(clipStart, clipEnd time.Time) ([]videoSegment, error) {
	entries, err := os.ReadDir(r.ringDir)
	if err != nil {
		return nil, fmt.Errorf("read video ring directory: %w", err)
	}

	segments := make([]videoSegment, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".mp4" {
			continue
		}

		startTime, err := parseVideoSegmentStart(entry.Name())
		if err != nil {
			continue
		}

		endTime := startTime.Add(r.segmentDuration)
		if endTime.Before(clipStart) || startTime.After(clipEnd) {
			continue
		}

		segments = append(segments, videoSegment{
			path:      filepath.Join(r.ringDir, entry.Name()),
			startTime: startTime,
			endTime:   endTime,
		})
	}

	slices.SortFunc(segments, func(a, b videoSegment) int {
		return a.startTime.Compare(b.startTime)
	})

	if len(segments) == 0 {
		return nil, fmt.Errorf("no video segments available for source %s", r.sourceID)
	}

	return segments, nil
}

func parseVideoSegmentStart(name string) (time.Time, error) {
	base := strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
	stamp := strings.TrimPrefix(base, "segment_")
	return time.ParseInLocation(videoSegmentNameLayout, stamp, time.Local)
}

func runVideoFFmpegCommand(ctx context.Context, ffmpegPath string, args ...string) error {
	cmd := exec.CommandContext(ctx, ffmpegPath, args...) //nolint:gosec // path validated by caller, args built internally
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func escapeFFmpegConcatPath(path string) string {
	return strings.ReplaceAll(path, "'", "'\\''")
}

func formatFFmpegSeconds(seconds float64) string {
	return strconv.FormatFloat(seconds, 'f', 3, 64)
}

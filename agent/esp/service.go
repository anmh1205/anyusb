package esp

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anmh1205/anyusb/agent/platform"
)

type Begin func(context.Context, string, string, string) (string, func(), error)

type Service struct {
	ctx    context.Context
	dir    string
	opener PortOpener
	begin  Begin
	mu     sync.Mutex
	jobs   map[string]*Job
}

// New creates a built-in pure-Go ESP service requiring no external Python or esptool runtime.
func New(ctx context.Context, dir string, begin Begin) *Service {
	return NewWithOpener(ctx, dir, openSerialPort, begin)
}

// NewWithOpener creates an ESP service using a custom serial port opener (for deterministic tests).
func NewWithOpener(ctx context.Context, dir string, opener PortOpener, begin Begin) *Service {
	if opener == nil {
		opener = openSerialPort
	}
	return &Service{ctx: ctx, dir: dir, opener: opener, begin: begin, jobs: make(map[string]*Job)}
}

func (s *Service) Doctor() platform.Check {
	return platform.Check{
		Name:     "esp",
		OK:       true,
		Optional: true,
		Detail:   "built-in Go ESP bootloader and serial engine",
	}
}

type JobState struct {
	ID        string    `json:"id"`
	Device    string    `json:"device"`
	Operation string    `json:"operation"`
	Status    string    `json:"status"`
	Started   time.Time `json:"started"`
	ExitCode  *int      `json:"exit_code,omitempty"`
	Error     string    `json:"error,omitempty"`
}

type Log struct {
	Number uint64 `json:"number"`
	Stream string `json:"stream"`
	Text   string `json:"text"`
}

type Frame struct {
	Logs []Log    `json:"logs,omitempty"`
	Job  JobState `json:"job"`
}

type Job struct {
	mu          sync.Mutex
	owner       string
	fingerprint [32]byte
	state       JobState
	logs        []Log
	last        uint64
	update      chan struct{}
}

type logWriter struct {
	job    *Job
	stream string
}

func (w logWriter) Write(data []byte) (int, error) {
	w.job.mu.Lock()
	defer w.job.mu.Unlock()
	for start := 0; start < len(data); start += 4096 {
		w.job.last++
		w.job.logs = append(w.job.logs, Log{w.job.last, w.stream, string(data[start:min(start+4096, len(data))])})
	}
	if len(w.job.logs) > 256 {
		w.job.logs = append([]Log(nil), w.job.logs[len(w.job.logs)-256:]...)
	}
	w.job.changed()
	return len(data), nil
}

func (j *Job) changed() {
	close(j.update)
	j.update = make(chan struct{})
}

func (j *Job) Snapshot(after uint64) (Frame, <-chan struct{}) {
	j.mu.Lock()
	defer j.mu.Unlock()
	frame := Frame{Job: j.state}
	for _, entry := range j.logs {
		if entry.Number > after {
			frame.Logs = append(frame.Logs, entry)
		}
	}
	return frame, j.update
}

func (s *Service) Job(owner, id string) (*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job := s.jobs[id]
	if job == nil || job.owner != owner {
		return nil, platform.ErrNotFound
	}
	return job, nil
}

type InputError struct{ Err error }

func (e InputError) Error() string { return e.Err.Error() }
func (e InputError) Unwrap() error { return platform.ErrInvalid }

func (s *Service) Start(w http.ResponseWriter, r *http.Request, owner, device, id string) (JobState, error) {
	op := r.URL.Query().Get("op")
	if !oneOf(op, []string{"chip-id", "erase", "flash", "verify", "reset"}) {
		return JobState{}, fmt.Errorf("%w: unknown ESP operation %q", platform.ErrInvalid, op)
	}
	baud, err := baudRate(r.URL.Query().Get("baud"))
	if err != nil {
		return JobState{}, err
	}
	dir, err := os.MkdirTemp(s.dir, "esp-*")
	if err != nil {
		return JobState{}, err
	}
	defer func() {
		if dir != "" {
			os.RemoveAll(dir)
		}
	}()
	var manifest Manifest
	var segments []segment
	if op == "flash" || op == "verify" {
		manifest, segments, err = readUpload(w, r, dir)
		if err != nil {
			return JobState{}, InputError{err}
		}
	}
	global, flash, err := options(manifest)
	if err != nil {
		return JobState{}, InputError{err}
	}
	digest := sha256.New()
	fmt.Fprintf(digest, "%q\n%q\n%q\n%q\n%d\n%q\n", owner, device, op, r.URL.Query().Get("port"), baud, append(global, flash...))
	for _, item := range segments {
		fmt.Fprintf(digest, "%d %x\n", item.Offset, item.Digest)
	}
	var fingerprint [32]byte
	copy(fingerprint[:], digest.Sum(nil))
	s.mu.Lock()
	if previous := s.jobs[id]; previous != nil {
		frame, _ := previous.Snapshot(0)
		s.mu.Unlock()
		if previous.fingerprint != fingerprint {
			return JobState{}, platform.ErrConflict
		}
		return frame.Job, nil
	}
	for key, job := range s.jobs {
		frame, _ := job.Snapshot(0)
		if frame.Job.Status != "running" && time.Since(frame.Job.Started) > 30*time.Minute {
			delete(s.jobs, key)
		}
	}
	if len(s.jobs) >= 256 {
		s.mu.Unlock()
		return JobState{}, fmt.Errorf("%w: job history is full; retry later", platform.ErrUnavailable)
	}
	defer s.mu.Unlock()
	port, release, err := s.begin(r.Context(), owner, device, r.URL.Query().Get("port"))
	if err != nil {
		return JobState{}, err
	}
	state := JobState{ID: id, Device: device, Operation: op, Status: "running", Started: time.Now()}
	job := &Job{owner: owner, state: state, update: make(chan struct{}), fingerprint: fingerprint}
	s.jobs[id] = job
	go s.runJob(job, port, baud, op, manifest, segments, dir, release)
	dir = ""
	return state, nil
}

func (s *Service) runJob(job *Job, port string, baud int, op string, manifest Manifest, segments []segment, dir string, release func()) {
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(s.ctx, 15*time.Minute)
	defer cancel()
	err := executeESPOperation(ctx, s.opener, port, baud, op, manifest, segments, logWriter{job, "stdout"}, logWriter{job, "stderr"})
	release()
	job.mu.Lock()
	defer job.mu.Unlock()
	code := 0
	job.state.Status = "succeeded"
	if err != nil {
		job.state.Status, job.state.Error, code = "failed", err.Error(), -1
		if ctx.Err() != nil {
			job.state.Error = ctx.Err().Error()
		}
	}
	job.state.ExitCode = &code
	job.changed()
}

func openReadOnly(path string) (*os.File, error) {
	return os.Open(path)
}

func baudRate(value string) (int, error) {
	if value == "" {
		return 115200, nil
	}
	baud, err := strconv.Atoi(value)
	if err != nil || baud < 300 || baud > 4_000_000 {
		return 0, fmt.Errorf("%w: baud rate must be between 300 and 4000000", platform.ErrInvalid)
	}
	return baud, nil
}

func parseJobConfig(manifest Manifest) (jobConfig, error) {
	if _, _, err := options(manifest); err != nil {
		return jobConfig{}, err
	}
	chip := strings.ToLower(strings.TrimSpace(manifest.Extra.Chip))
	if chip == "" {
		chip = "auto"
	}
	before := strings.ReplaceAll(strings.TrimSpace(manifest.Extra.Before), "_", "-")
	if before == "" {
		before = "default-reset"
	}
	after := strings.ReplaceAll(strings.TrimSpace(manifest.Extra.After), "_", "-")
	if after == "" {
		after = "hard-reset"
	}
	return jobConfig{
		chip:      chip,
		before:    before,
		after:     after,
		flashMode: strings.ToLower(strings.TrimSpace(manifest.FlashSettings["flash_mode"])),
		flashFreq: strings.ToLower(strings.TrimSpace(manifest.FlashSettings["flash_freq"])),
		flashSize: strings.TrimSpace(manifest.FlashSettings["flash_size"]),
	}, nil
}

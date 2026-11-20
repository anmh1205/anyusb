package esp

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anmh1205/anyusb/agent/platform"
)

const Version = "5.2.0"

var versionPattern = regexp.MustCompile(`(?m)^5\.2\.0\s*$`)

type Begin func(context.Context, string, string, string) (string, func(), error)

type Service struct {
	ctx         context.Context
	python, dir string
	versionErr  error
	begin       Begin
	mu          sync.Mutex
	jobs        map[string]*Job
}

func New(ctx context.Context, dir, python string, begin Begin) *Service {
	s := &Service{ctx: ctx, python: python, dir: dir, begin: begin, jobs: make(map[string]*Job)}
	if s.python == "" {
		for _, name := range []string{"python3", "python"} {
			path, err := exec.LookPath(name)
			if err != nil {
				continue
			}
			s.python = path
			if s.versionErr = s.checkVersion(ctx); s.versionErr == nil {
				return s
			}
		}
	}
	s.versionErr = s.checkVersion(ctx)
	return s
}

func (s *Service) checkVersion(ctx context.Context) error {
	if s.python == "" || !filepath.IsAbs(s.python) {
		return fmt.Errorf("set -python to the absolute path of a Python environment with esptool %s", Version)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.python, "-m", "esptool", "version")
	cmd.Env = pythonEnv()
	out, err := cmd.CombinedOutput()
	if err != nil || !versionPattern.Match(out) {
		return fmt.Errorf("expected esptool %s in %s: %s", Version, s.python, strings.TrimSpace(string(out)))
	}
	return nil
}

func (s *Service) Doctor() platform.Check {
	check := platform.Check{Name: "esptool", OK: s.versionErr == nil, Detail: Version + " via " + s.python}
	if s.versionErr != nil {
		check.Detail = s.versionErr.Error()
	}
	return check
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
	if s.versionErr != nil {
		return JobState{}, fmt.Errorf("%w: %v", platform.ErrUnavailable, s.versionErr)
	}
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
	args := []string{"-u", "-m", "esptool", "--port", port, "--baud", strconv.Itoa(baud)}
	args = append(args, global...)
	command := map[string]string{"chip-id": "read-mac", "erase": "erase-flash", "flash": "write-flash", "verify": "verify-flash", "reset": "flash-id"}
	args = append(args, command[op])
	if op == "flash" || op == "verify" {
		args = append(args, flash...)
		for _, file := range segments {
			args = append(args, fmt.Sprintf("0x%x", file.Offset), file.Path)
		}
	}
	state := JobState{ID: id, Device: device, Operation: op, Status: "running", Started: time.Now()}
	job := &Job{owner: owner, state: state, update: make(chan struct{}), fingerprint: fingerprint}
	s.jobs[id] = job
	go s.runJob(job, args, dir, release)
	dir = ""
	return state, nil
}

func (s *Service) runJob(job *Job, args []string, dir string, release func()) {
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(s.ctx, 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.python, args...)
	cmd.Dir = dir
	cmd.Env = pythonEnv()
	cmd.Stdout = logWriter{job, "stdout"}
	cmd.Stderr = logWriter{job, "stderr"}
	err := cmd.Run()
	release()
	job.mu.Lock()
	defer job.mu.Unlock()
	code := 0
	job.state.Status = "succeeded"
	if err != nil {
		job.state.Status, job.state.Error, code = "failed", err.Error(), -1
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		}
		if ctx.Err() != nil {
			job.state.Error = ctx.Err().Error()
		}
	}
	job.state.ExitCode = &code
	job.changed()
}

func pythonEnv() []string {
	return append(os.Environ(), "PYTHONUNBUFFERED=1", "PYTHONNOUSERSITE=1", "NO_COLOR=1")
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
